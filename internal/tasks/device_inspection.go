package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/helm"
	"github.com/flightctl/flightctl/internal/oci"
	inspectionservice "github.com/flightctl/flightctl/internal/service/appinspection"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	inspectionstore "github.com/flightctl/flightctl/internal/store/appinspection"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

type deviceInspection struct {
	store       *inspectionstore.Store
	wakeups     *InspectionWakeups
	coordinator *inspectionservice.Coordinator
}

// WithInspection enables durable admission and resume on the worker's renderer.
func (t *DeviceRenderLogic) WithInspection(store *inspectionstore.Store, wakeups *InspectionWakeups, coordinator *inspectionservice.Coordinator) *DeviceRenderLogic {
	clone := *t
	clone.inspection = &deviceInspection{store: store, wakeups: wakeups, coordinator: coordinator}
	return &clone
}

type inspectionAdmissionError struct{ error }

func (e *inspectionAdmissionError) Unwrap() error { return e.error }

func inspectionRetrySignal(reason domain.EventReason) bool {
	switch reason {
	case domain.EventReasonResourceUpdated, domain.EventReasonReferencedRepositoryUpdated,
		domain.EventReasonDependencyChangeDetected, domain.EventReasonApplicationLifecycleChanged:
		return true
	default:
		return false
	}
}

func inspectionOwner(device *domain.Device) inspectionstore.OwnerVersion {
	annotations := lo.FromPtr(device.Metadata.Annotations)
	return inspectionstore.OwnerVersion{
		Owner:      inspectionstore.Owner{Kind: inspectionstore.OwnerDevice, Name: lo.FromPtr(device.Metadata.Name)},
		Generation: lo.FromPtr(device.Metadata.Generation), CreatedAt: lo.FromPtr(device.Metadata.CreationTimestamp),
		TemplateIdentity: annotations[domain.DeviceAnnotationTemplateVersion],
	}
}

func (t *deviceRenderState) currentInspection(ctx context.Context, device *domain.Device) (inspectionstore.OwnerProgress, error) {
	if t.inspection == nil || t.continuation != nil {
		return inspectionstore.OwnerProgress{}, nil
	}
	return t.inspection.store.CurrentContinuation(ctx, t.orgId, inspectionOwner(device).Owner)
}

func (t *deviceRenderState) discardInspection(ctx context.Context) error {
	if t.continuation != nil {
		return t.inspection.store.DiscardContinuation(ctx, t.orgId, t.continuation.ID)
	}
	return nil
}

type inspectionSnapshot struct {
	owner inspectionstore.OwnerVersion
	apps  []domain.ApplicationProviderSpec
	key   model.InspectionFingerprint
}

func (t *deviceRenderState) inspectionSnapshot(device *domain.Device, rendered RenderedSpec) (inspectionSnapshot, error) {
	snapshot := inspectionSnapshot{owner: inspectionOwner(device)}
	if len(rendered.Applications) > 0 {
		if err := json.Unmarshal(rendered.Applications, &snapshot.apps); err != nil {
			return inspectionSnapshot{}, fmt.Errorf("decode effective applications: %w", err)
		}
	}
	var releases []helm.ReleaseContext
	for i := range snapshot.apps {
		if kind, _ := snapshot.apps[i].GetAppType(); kind == domain.AppTypeHelm {
			app, err := snapshot.apps[i].AsHelmApplication()
			if err != nil {
				return inspectionSnapshot{}, err
			}
			release, _ := helm.TargetRelease(app, device)
			releases = append(releases, release)
		}
	}
	var aliases []helm.RepositoryAlias
	var invalidAliases bool
	if len(releases) > 0 {
		var err error
		aliases, err = helm.TargetRepositoryAliases(rendered.Config)
		invalidAliases = err != nil
	}
	identity, err := json.Marshal(struct {
		Applications   json.RawMessage
		OSImage        string
		Platform       any
		Releases       []helm.ReleaseContext
		Aliases        []helm.RepositoryAlias
		InvalidAliases bool
	}{rendered.Applications, rendered.OsImage,
		oci.DeviceImagePlatform(device), releases, aliases, invalidAliases})
	if err != nil {
		return inspectionSnapshot{}, err
	}
	snapshot.key = sha256.Sum256(identity)
	return snapshot, nil
}

func (s inspectionSnapshot) matches(c model.ApplicationInspectionContinuation) bool {
	return s.owner.Generation == c.Generation && s.owner.CreatedAt.Equal(c.OwnerCreatedAt) &&
		s.owner.TemplateIdentity == c.TemplateIdentity && s.key == c.Snapshot
}

func (t *deviceRenderState) advanceInspection(ctx context.Context, device *domain.Device, rendered RenderedSpec, current inspectionstore.OwnerProgress, force bool) (*deviceservice.RenderContinuation, bool, error) {
	snapshot, err := t.inspectionSnapshot(device, rendered)
	if err != nil {
		return nil, false, err
	}
	if t.continuation != nil {
		return t.prepareInspectionResume(ctx, device, rendered, snapshot)
	}
	if len(snapshot.apps) == 0 && current.ID == uuid.Nil {
		return nil, false, nil
	}
	// Rollout ticks and duplicate non-retry events can share the current render.
	if current.ID != uuid.Nil && current.ConsumedAt == nil && !current.Invalidated && snapshot.matches(current.ApplicationInspectionContinuation) && !inspectionRetrySignal(t.event.Reason) {
		return nil, true, nil
	}
	eventID, err := uuid.Parse(lo.FromPtr(t.event.Metadata.Name))
	if err != nil {
		return nil, false, errors.New("inspection admission requires an event UUID")
	}
	return nil, true, t.admitInspection(ctx, snapshot, eventID, current.ID, force)
}

func (t *deviceRenderState) admitInspection(ctx context.Context, snapshot inspectionSnapshot, eventID, previousID uuid.UUID, force bool) error {
	admission := inspectionstore.Admission{Owner: snapshot.owner, Snapshot: snapshot.key, EventID: eventID, PreviousID: previousID, ForceRender: force}
	for i := range snapshot.apps {
		app := &snapshot.apps[i]
		mechanism := model.InspectionPullContainer
		if kind, _ := app.GetAppType(); kind == domain.AppTypeHelm {
			mechanism = model.InspectionPullHelmChart
		}
		admission.Applications = append(admission.Applications, inspectionstore.AdmissionApplication{
			Context: strconv.Itoa(i), User: inspectionApplicationUser(app), Mechanism: mechanism, References: inspectionservice.DeclaredReferences(app),
		})
	}
	admitted, err := t.inspection.store.Admit(ctx, t.orgId, admission)
	if err != nil || admitted.ContinuationID == uuid.Nil {
		return err
	}
	publishCtx, cancel := context.WithTimeout(ctx, AckTimeout)
	defer cancel()
	if len(admitted.WorkIDs) == 0 && len(snapshot.apps) == 0 {
		err = t.inspection.wakeups.publish(publishCtx, inspectionResume, t.orgId, admitted.ContinuationID)
	} else {
		err = t.inspection.wakeups.publishBatch(publishCtx, inspectionWork, t.orgId, admitted.WorkIDs)
	}
	if err != nil {
		t.log.WithError(err).Warn("inspection admission committed; recovery will republish wakeups")
	}
	return nil
}

func inspectionApplicationUser(app *domain.ApplicationProviderSpec) string {
	kind, _ := app.GetAppType()
	switch kind {
	case domain.AppTypeContainer:
		spec, _ := app.AsContainerApplication()
		return spec.RunAs.String()
	case domain.AppTypeQuadlet:
		spec, _ := app.AsQuadletApplication()
		return spec.RunAs.String()
	default:
		return ""
	}
}

// ReloadInspectionApplication reconstructs inputs through the same effective
// render path used by admission, without making a rendered or status write.
func (t *DeviceRenderLogic) ReloadInspectionApplication(ctx context.Context, orgID uuid.UUID, target inspectionstore.WorkTarget) (inspectionservice.EffectiveApplication, bool, error) {
	var app inspectionservice.EffectiveApplication
	name := lo.FromPtr(target.Continuation.DeviceName)
	if name == "" {
		return app, false, nil
	}
	state := t.newRenderState(orgID, domain.Event{InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind, Name: name}})
	device, status := t.deviceSvc.GetDevice(ctx, orgID, name)
	if status.Code == http.StatusNotFound {
		return app, false, nil
	}
	if status.Code != http.StatusOK {
		return app, false, fmt.Errorf("reload device inspection inputs: %s", status.Message)
	}
	owner := inspectionOwner(device)
	if owner.Generation != target.Continuation.Generation || !owner.CreatedAt.Equal(target.Continuation.OwnerCreatedAt) || owner.TemplateIdentity != target.Continuation.TemplateIdentity {
		return app, false, nil
	}
	state.bindVmLauncher(device)
	if err := state.bindRenderOwner(device); err != nil {
		return app, false, err
	}
	rendered, err := state.renderEffectiveSpec(ctx, device)
	if err != nil {
		return app, false, err
	}
	snapshot, err := state.inspectionSnapshot(device, rendered)
	if err != nil || !snapshot.matches(target.Continuation) {
		return app, false, err
	}
	i, err := strconv.Atoi(target.ApplicationContext)
	if err != nil || i < 0 || i >= len(snapshot.apps) {
		return app, false, nil
	}
	return inspectionservice.EffectiveApplication{Spec: snapshot.apps[i], Device: device, RenderedConfig: rendered.Config}, true, nil
}

func (t *DeviceRenderLogic) ResumeInspection(ctx context.Context, orgID, id uuid.UUID) error {
	ready, err := t.inspection.store.ReadyContinuation(ctx, orgID, id)
	if err != nil || ready.ID == uuid.Nil {
		return err
	}
	name := lo.FromPtr(ready.DeviceName)
	if name == "" {
		return nil
	}
	if ready.PendingVersion != nil {
		return deviceservice.NotifyInspectionRender(ctx, t.inspection.store, orgID, name, id, *ready.PendingVersion)
	}
	state := t.newRenderState(orgID, domain.Event{InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind, Name: name}})
	state.continuation = &ready
	return state.renderDevice(ctx)
}

func (t *deviceRenderState) prepareInspectionResume(ctx context.Context, device *domain.Device, rendered RenderedSpec, snapshot inspectionSnapshot) (*deviceservice.RenderContinuation, bool, error) {
	current := t.continuation
	if !snapshot.owner.CreatedAt.Equal(current.OwnerCreatedAt) {
		return nil, true, t.discardInspection(ctx)
	}
	refresh := func() (*deviceservice.RenderContinuation, bool, error) {
		eventID := uuid.NewSHA1(current.ID, []byte("refresh-app-inspection"))
		return nil, true, t.admitInspection(ctx, snapshot, eventID, current.ID, true)
	}
	if current.Invalidated || !current.Fresh || !snapshot.matches(current.ApplicationInspectionContinuation) {
		return refresh()
	}
	for after := ""; ; {
		page, err := t.inspection.store.ListUsages(ctx, t.orgId, snapshot.owner.Owner, inspectionWakeupBatch, after)
		if err != nil {
			return nil, false, err
		}
		for _, usage := range page.Items {
			if lo.FromPtr(usage.ContinuationID) != current.ID {
				return nil, true, nil
			}
			index, err := strconv.Atoi(usage.ApplicationContext)
			if err != nil || index < 0 || index >= len(snapshot.apps) {
				return refresh()
			}
			result, err := t.inspection.store.GetAttempt(ctx, t.orgId, usage.InspectionID)
			if errors.Is(err, flterrors.ErrResourceNotFound) {
				return nil, true, nil
			}
			if err != nil {
				return nil, false, err
			}
			app := inspectionservice.EffectiveApplication{Spec: snapshot.apps[index], Device: device, RenderedConfig: rendered.Config}
			matches, err := t.inspection.coordinator.MatchesRevision(ctx, t.orgId, result.Inspection.Input, app)
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			if (!matches && err == nil) || (err != nil && result.Inspection.State == model.InspectionComplete) {
				return refresh()
			}
			if kind, _ := app.Spec.GetAppType(); kind == domain.AppTypeHelm {
				t.recordInspectedHelm(usage.ApplicationContext, result)
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	return &deviceservice.RenderContinuation{ID: current.ID, Snapshot: snapshot.key, ResourceVersion: lo.FromPtr(device.Metadata.ResourceVersion)}, false, nil
}

func (t *deviceRenderState) recordInspectedHelm(app string, result inspectionstore.Result) {
	if t.inspectedHelm == nil {
		t.inspectedHelm = make(map[string][]string)
	}
	images := []string{}
	for _, ref := range result.References {
		if ref.Role == model.InspectionRoleWorkload && ref.Mechanism == model.InspectionPullHelmImage {
			images = append(images, ref.Reference)
		}
	}
	t.inspectedHelm[app] = images
}
