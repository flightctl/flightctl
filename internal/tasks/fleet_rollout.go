package tasks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/rollout"
	"github.com/flightctl/flightctl/internal/service/common"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// fleetRolloutIterationTimeout is the time budget for each pagination iteration (one
// ListDevices plus processing all devices on that page). It applies to a context derived
// from the parent without inheriting the parent's deadline (see fleetRolloutIterationContext).
const fleetRolloutIterationTimeout = time.Minute

// fleetRolloutIterationContext returns a context whose deadline comes only from timeout.
// The parent's deadline does not shorten the iteration; explicit parent cancellation
// (not deadline) still ends the iteration by canceling the child.
func fleetRolloutIterationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(parent)
	iterCtx, cancelIter := context.WithTimeout(base, timeout)
	go func() {
		select {
		case <-parent.Done():
			if errors.Is(parent.Err(), context.Canceled) {
				cancelIter()
			}
		case <-iterCtx.Done():
		}
	}()
	return iterCtx, cancelIter
}

// The fleet rollout task updates all devices in a fleet to match the latest template
// version.
//
// Behavior:
// - Iterates over devices that belong to the fleet.
// - Skips devices that:
//     - Have no owner
//     - Have multiple owners
//     - Are already being rolled out
// - For each eligible device:
//     - Compares the device spec and template version with the latest desired version.
//     - Updates the device spec and annotation only if necessary.
//
// Idempotency:
// - The task checks whether the device is already up to date.
// - No updates are made if the spec and version match.
// - Retries on conflict (409) to safely handle concurrent updates.
// - Skips devices not eligible for rollout, avoiding partial or duplicate writes.
//
// This design ensures the task can be run repeatedly without side effects.

func fleetRollout(ctx context.Context, orgId uuid.UUID, event domain.Event, fleetSvc fleetservice.Service, templateversionSvc templateversionservice.Service, deviceSvc deviceservice.Service, dependencyrefSvc dependencyrefservice.Service, log logrus.FieldLogger) error {
	logic := NewFleetRolloutsLogic(log, fleetSvc, templateversionSvc, deviceSvc, dependencyrefSvc, orgId, event)
	switch event.InvolvedObject.Kind {
	case domain.FleetKind:
		err := logic.RolloutFleet(ctx)
		if err != nil {
			log.Errorf("failed rolling out fleet %s/%s: %v", orgId, event.InvolvedObject.Name, err)
		}
		return err
	case domain.DeviceKind:
		err := logic.RolloutDevice(ctx)
		if err != nil {
			log.Errorf("failed rolling out device %s/%s: %v", orgId, event.InvolvedObject.Name, err)
		}
		return err
	default:
		return fmt.Errorf("FleetRollouts called with incorrect resource kind %s", event.InvolvedObject.Kind)
	}
}

type FleetRolloutsLogic struct {
	log                logrus.FieldLogger
	fleetSvc           fleetservice.Service
	templateversionSvc templateversionservice.Service
	deviceSvc          deviceservice.Service
	dependencyrefSvc   dependencyrefservice.Service
	orgId              uuid.UUID
	event              domain.Event
	itemsPerPage       int
	owner              string
}

func NewFleetRolloutsLogic(log logrus.FieldLogger, fleetSvc fleetservice.Service, templateversionSvc templateversionservice.Service, deviceSvc deviceservice.Service, dependencyrefSvc dependencyrefservice.Service, orgId uuid.UUID, event domain.Event) FleetRolloutsLogic {
	return FleetRolloutsLogic{
		log:                log,
		fleetSvc:           fleetSvc,
		templateversionSvc: templateversionSvc,
		deviceSvc:          deviceSvc,
		dependencyrefSvc:   dependencyrefSvc,
		orgId:              orgId,
		event:              event,
		itemsPerPage:       ItemsPerPage,
	}
}

func (f *FleetRolloutsLogic) SetItemsPerPage(items int) {
	f.itemsPerPage = items
}

func (f FleetRolloutsLogic) RolloutFleet(ctx context.Context) error {
	fleet, status := f.fleetSvc.GetFleet(ctx, f.orgId, f.event.InvolvedObject.Name, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get fleet %s/%s: %s", f.orgId, f.event.InvolvedObject.Name, status.Message)
	}
	f.log.Infof("Rolling out fleet %s/%s", f.orgId, f.event.InvolvedObject.Name)

	templateVersion, status := f.templateversionSvc.GetLatestTemplateVersion(ctx, f.orgId, f.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get templateVersion: %s", status.Message)
	}

	owner := util.SetResourceOwner(domain.FleetKind, f.event.InvolvedObject.Name)
	f.owner = *owner

	listParams := domain.ListDevicesParams{
		Limit:         lo.ToPtr(int32(ItemsPerPage)),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *owner)),
	}
	annotationFilter := []string{
		domain.MatchExpression{
			Key:      domain.DeviceAnnotationTemplateVersion,
			Operator: domain.NotIn,
			Values:   &[]string{lo.FromPtr(templateVersion.Metadata.Name)},
		}.String(),
	}
	if fleet.Spec.RolloutPolicy != nil && fleet.Spec.RolloutPolicy.DeviceSelection != nil {
		annotationFilter = append(annotationFilter, domain.MatchExpression{
			Key:      domain.DeviceAnnotationSelectedForRollout,
			Operator: domain.Exists,
		}.String())
	}
	annotationSelector := selector.NewAnnotationSelectorOrDie(strings.Join(annotationFilter, ","))
	delayDeviceRender := fleet.Spec.RolloutPolicy != nil && fleet.Spec.RolloutPolicy.DisruptionBudget != nil

	failureCount := 0
	var allDeviceRefs []model.DependencyRef
	for {
		pageFailures, pageRefs, nextContinue, err := f.rolloutFleetPage(ctx, templateVersion, listParams, annotationSelector, delayDeviceRender)
		if err != nil {
			return err
		}
		failureCount += pageFailures
		allDeviceRefs = append(allDeviceRefs, pageRefs...)
		if nextContinue == nil {
			break
		}
		listParams.Continue = nextContinue
	}

	// Transactionally replace all device-level dependency refs for this fleet
	// so readers never see a partially empty set.
	fleetName := f.event.InvolvedObject.Name
	if st := f.dependencyrefSvc.ReplaceDeviceDependencyRefsByFleet(ctx, f.orgId, fleetName, allDeviceRefs); st.Code != http.StatusOK {
		f.log.Errorf("failed to replace device dependency refs for fleet %s: %s", fleetName, st.Message)
	}

	if failureCount != 0 {
		// TODO: Retry when we have a mechanism that allows it
		return fmt.Errorf("failed updating %d devices", failureCount)
	}

	return nil
}

// rolloutFleetPage performs one ListDevices call and updates every device in that page.
// nextContinue is nil when there are no further pages; otherwise it is the token for the next list.
func (f FleetRolloutsLogic) rolloutFleetPage(
	ctx context.Context,
	templateVersion *domain.TemplateVersion,
	listParams domain.ListDevicesParams,
	annotationSelector *selector.AnnotationSelector,
	delayDeviceRender bool,
) (pageFailures int, pageRefs []model.DependencyRef, nextContinue *string, err error) {
	iterCtx, cancel := fleetRolloutIterationContext(ctx, fleetRolloutIterationTimeout)
	defer cancel()

	devices, status := f.deviceSvc.ListDevices(iterCtx, f.orgId, listParams, annotationSelector)
	if status.Code != http.StatusOK {
		// TODO: Retry when we have a mechanism that allows it
		return 0, nil, nil, fmt.Errorf("failed fetching devices: %s", status.Message)
	}

	for devIndex := range devices.Items {
		device := &devices.Items[devIndex]
		refs, updateErr := f.updateDeviceToFleetTemplate(iterCtx, device, templateVersion, delayDeviceRender)
		if updateErr != nil {
			f.log.Errorf("failed to update target generation for device %s (fleet %s): %v", *device.Metadata.Name, f.event.InvolvedObject.Name, updateErr)
			pageFailures++
		}
		pageRefs = append(pageRefs, refs...)
	}

	return pageFailures, pageRefs, devices.Metadata.Continue, nil
}

// The device's owner was changed, roll out if necessary
func (f FleetRolloutsLogic) RolloutDevice(ctx context.Context) error {
	f.log.Infof("Rolling out device %s/%s", f.orgId, f.event.InvolvedObject.Name)

	device, status := f.deviceSvc.GetDevice(ctx, f.orgId, f.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get device: %s", status.Message)
	}

	if device.Metadata.Owner == nil || len(*device.Metadata.Owner) == 0 {
		return nil
	}

	if domain.IsStatusConditionTrue(device.Status.Conditions, domain.ConditionTypeDeviceMultipleOwners) {
		f.log.Errorf("Device %s has multiple owners, skipping rollout", f.event.InvolvedObject.Name)
		return nil
	}

	ownerName, isFleetOwner, err := getOwnerFleet(device)
	if err != nil {
		return fmt.Errorf("failed getting device owner: %w", err)
	}
	if !isFleetOwner {
		return nil
	}
	f.owner = *device.Metadata.Owner

	templateVersion, status := f.templateversionSvc.GetLatestTemplateVersion(ctx, f.orgId, ownerName)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get templateVersion: %s", status.Message)
	}

	fleet, status := f.fleetSvc.GetFleet(ctx, f.orgId, ownerName, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get fleet: %s", status.Message)
	}

	if err := f.syncFleetApplicationLifecycleDefault(ctx, device, fleet); err != nil {
		f.log.Errorf("failed to sync fleet application lifecycle default to device %s: %v", f.event.InvolvedObject.Name, err)
	}

	rolloutProgressStage, err := rollout.ProgressStage(fleet)
	if err != nil {
		return fmt.Errorf("failed to find rollout progress stage for fleet: %w", err)
	}
	if rolloutProgressStage == rollout.ConfiguredBatch {
		// If a rollout is in progress, then the device will be rolled out by one of the next batches
		f.log.Infof("Rollout is in progress for fleet %v/%s. Skipping device %s rollout", f.orgId, lo.FromPtr(fleet.Metadata.Name), f.event.InvolvedObject.Name)
		return nil
	}
	delayDeviceRender := fleet.Spec.RolloutPolicy != nil && fleet.Spec.RolloutPolicy.DisruptionBudget != nil
	refs, err := f.updateDeviceToFleetTemplate(ctx, device, templateVersion, delayDeviceRender)
	if err != nil {
		return err
	}
	deviceName := f.event.InvolvedObject.Name
	if st := f.dependencyrefSvc.ReplaceFleetScopedDeviceDependencyRefs(ctx, f.orgId, deviceName, refs); st.Code != http.StatusOK {
		f.log.Errorf("failed to replace dependency refs for device %s: %s", deviceName, st.Message)
	}
	return nil
}

// syncFleetApplicationLifecycleDefault bootstraps the device's local cache of the owning
// fleet's application lifecycle default so device-render can read it without a Fleet lookup
// of its own. This only ever runs once per device, the first time it is rolled out with no
// cache annotation yet, so a routine rollout can never overwrite a lifecycle action taken
// after the device joined the fleet.
func (f FleetRolloutsLogic) syncFleetApplicationLifecycleDefault(ctx context.Context, device *domain.Device, fleet *domain.Fleet) error {
	if _, alreadySynced := lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationFleetApplicationLifecycle]; alreadySynced {
		return nil
	}
	fleetRaw := lo.FromPtr(fleet.Metadata.Annotations)[domain.FleetAnnotationApplicationLifecycle]
	if fleetRaw == "" {
		return nil
	}

	deviceName := lo.FromPtr(device.Metadata.Name)
	status := f.deviceSvc.UpdateDeviceAnnotations(ctx, f.orgId, deviceName, map[string]string{domain.DeviceAnnotationFleetApplicationLifecycle: fleetRaw}, nil)
	return common.ApiStatusToErr(status)
}

func (f FleetRolloutsLogic) updateDeviceToFleetTemplate(ctx context.Context, device *domain.Device, templateVersion *domain.TemplateVersion, delayDeviceRender bool) ([]model.DependencyRef, error) {
	currentVersion := ""
	currentRenderedVersion := ""
	if device.Metadata.Annotations != nil {
		if v, ok := (*device.Metadata.Annotations)[domain.DeviceAnnotationTemplateVersion]; ok {
			currentVersion = v
		}
		if v, ok := (*device.Metadata.Annotations)[domain.DeviceAnnotationRenderedTemplateVersion]; ok {
			currentRenderedVersion = v
		}
	}
	errs := []error{}

	var osSpec *domain.DeviceOsSpec
	if templateVersion.Status.Os != nil {
		img, err := ReplaceParametersInString(templateVersion.Status.Os.Image, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in OS image: %w", err))
		} else {
			osSpec = &domain.DeviceOsSpec{Image: img}
		}
	}

	deviceConfig, depRefs, configErrs := f.getDeviceConfig(device, templateVersion)
	errs = append(errs, configErrs...)

	deviceApps, appErrs := f.getDeviceApps(device, templateVersion)
	errs = append(errs, appErrs...)

	if len(errs) > 0 {
		annotations := map[string]string{
			domain.DeviceAnnotationLastRolloutError: errors.Join(errs...).Error(),
		}
		status := f.deviceSvc.UpdateDeviceAnnotations(ctx, f.orgId, *device.Metadata.Name, annotations, nil)
		if status.Code != http.StatusOK {
			errs = append(errs, common.ApiStatusToErr(status))
		}
		return nil, fmt.Errorf("failed generating device spec for %s/%s: %w", f.orgId, *device.Metadata.Name, errors.Join(errs...))
	}

	newDeviceSpec := domain.DeviceSpec{
		Config:       deviceConfig,
		Os:           osSpec,
		Systemd:      templateVersion.Status.Systemd,
		Resources:    templateVersion.Status.Resources,
		Applications: deviceApps,
		UpdatePolicy: templateVersion.Status.UpdatePolicy,
	}

	errs = newDeviceSpec.Validate(false)
	if len(errs) > 0 {
		return nil, fmt.Errorf("failed validating device spec for %s/%s: %w", f.orgId, *device.Metadata.Name, errors.Join(errs...))
	}

	if currentVersion == *templateVersion.Metadata.Name && currentRenderedVersion == *templateVersion.Metadata.Name && domain.DeviceSpecsAreEqual(newDeviceSpec, *device.Spec) {
		f.log.Debugf("Not rolling out device %s/%s because it is already at templateVersion %s", f.orgId, *device.Metadata.Name, *templateVersion.Metadata.Name)
		return depRefs, nil
	}

	f.log.Infof("Rolling out device %s/%s to templateVersion %s", f.orgId, *device.Metadata.Name, *templateVersion.Metadata.Name)
	err := f.updateDeviceInStore(ctx, device, &newDeviceSpec, delayDeviceRender)
	if err != nil {
		return nil, fmt.Errorf("failed updating device spec: %w", err)
	}

	annotations := map[string]string{
		domain.DeviceAnnotationTemplateVersion: *templateVersion.Metadata.Name,
	}
	status := f.deviceSvc.UpdateDeviceAnnotations(ctx, f.orgId, *device.Metadata.Name, annotations, []string{domain.DeviceAnnotationLastRolloutError})
	if status.Code != http.StatusOK {
		return nil, fmt.Errorf("failed updating templateVersion annotation: %s", status.Message)
	}

	return depRefs, nil
}

// getDeviceApps evaluates the fleet template's applications against the device's labels
// (parameter substitution). The device's DeviceAnnotationApplicationLifecycle annotation is
// not overlaid here: it is applied by the device render task directly onto
// RenderedApplications, so it survives fleet template rollouts without ever being persisted
// into the device's Spec.Applications.
func (f FleetRolloutsLogic) getDeviceApps(device *domain.Device, templateVersion *domain.TemplateVersion) (*[]domain.ApplicationProviderSpec, []error) {
	if templateVersion.Status.Applications == nil {
		return nil, nil
	}

	deviceApps := []domain.ApplicationProviderSpec{}
	appErrs := []error{}
	for appIndex, appItem := range *templateVersion.Status.Applications {
		var newAppItem *domain.ApplicationProviderSpec
		var errs []error

		appType, err := appItem.GetAppType()
		if err != nil {
			appErrs = append(appErrs, fmt.Errorf("failed to get app type for app %d: %w", appIndex, err))
			continue
		}

		switch appType {
		case domain.AppTypeContainer:
			newAppItem, errs = f.replaceContainerApplicationParameters(device, appItem)
		case domain.AppTypeHelm:
			newAppItem, errs = f.replaceHelmApplicationParameters(device, appItem)
		case domain.AppTypeCompose:
			newAppItem, errs = f.replaceComposeApplicationParameters(device, appItem)
		case domain.AppTypeQuadlet:
			newAppItem, errs = f.replaceQuadletApplicationParameters(device, appItem)
		case domain.AppTypeVm:
			newAppItem, errs = f.replaceVmApplicationParameters(device, appItem)
		default:
			errs = append(errs, fmt.Errorf("unsupported app type for app %d: %s", appIndex, appType))
		}

		appErrs = append(appErrs, errs...)
		if newAppItem != nil {
			deviceApps = append(deviceApps, *newAppItem)
		}
	}

	if len(appErrs) > 0 {
		return nil, appErrs
	}

	return &deviceApps, nil
}

func replaceEnvVarsMap(device *domain.Device, envVars *map[string]string) (*map[string]string, []error) {
	if envVars == nil {
		return nil, nil
	}
	var errs []error
	origEnvVars := *envVars
	newEnvVars := make(map[string]string, len(origEnvVars))
	for k, v := range origEnvVars {
		newValue, err := ReplaceParametersInString(v, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in env var %s: %w", k, err))
			continue
		}
		newEnvVars[k] = newValue
	}
	return &newEnvVars, errs
}

func (f FleetRolloutsLogic) replaceContainerApplicationParameters(device *domain.Device, app domain.ApplicationProviderSpec) (*domain.ApplicationProviderSpec, []error) {
	containerApp, err := app.AsContainerApplication()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert to container application: %w", err)}
	}
	appName := lo.FromPtr(containerApp.Name)

	var errs []error

	containerApp.Image, err = ReplaceParametersInString(containerApp.Image, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in image for app %s: %w", appName, err))
	}

	newEnvVars, envErrs := replaceEnvVarsMap(device, containerApp.EnvVars)
	errs = append(errs, envErrs...)
	containerApp.EnvVars = newEnvVars

	if containerApp.Volumes != nil {
		newVolumes, volErrs := f.replaceVolumeParameters(device, appName, *containerApp.Volumes)
		errs = append(errs, volErrs...)
		if len(volErrs) == 0 {
			containerApp.Volumes = &newVolumes
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}

	var newItem domain.ApplicationProviderSpec
	if err := newItem.FromContainerApplication(containerApp); err != nil {
		return nil, []error{fmt.Errorf("failed converting container application: %w", err)}
	}

	return &newItem, nil
}

func (f FleetRolloutsLogic) replaceHelmApplicationParameters(device *domain.Device, app domain.ApplicationProviderSpec) (*domain.ApplicationProviderSpec, []error) {
	helmApp, err := app.AsHelmApplication()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert to helm application: %w", err)}
	}
	appName := lo.FromPtr(helmApp.Name)

	var errs []error

	helmApp.Image, err = ReplaceParametersInString(helmApp.Image, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in image for app %s: %w", appName, err))
	}

	if len(errs) > 0 {
		return nil, errs
	}

	var newItem domain.ApplicationProviderSpec
	if err := newItem.FromHelmApplication(helmApp); err != nil {
		return nil, []error{fmt.Errorf("failed converting helm application: %w", err)}
	}

	return &newItem, nil
}

func (f FleetRolloutsLogic) replaceComposeApplicationParameters(device *domain.Device, app domain.ApplicationProviderSpec) (*domain.ApplicationProviderSpec, []error) {
	composeApp, err := app.AsComposeApplication()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert to compose application: %w", err)}
	}
	appName := lo.FromPtr(composeApp.Name)

	var errs []error

	newEnvVars, envErrs := replaceEnvVarsMap(device, composeApp.EnvVars)
	errs = append(errs, envErrs...)
	composeApp.EnvVars = newEnvVars

	if composeApp.Volumes != nil {
		newVolumes, volErrs := f.replaceVolumeParameters(device, appName, *composeApp.Volumes)
		errs = append(errs, volErrs...)
		if len(volErrs) == 0 {
			composeApp.Volumes = &newVolumes
		}
	}

	providerType, err := composeApp.Type()
	if err != nil {
		return nil, []error{fmt.Errorf("failed getting provider type for compose app %s: %w", appName, err)}
	}

	switch providerType {
	case domain.ImageApplicationProviderType:
		imageSpec, err := composeApp.AsImageApplicationProviderSpec()
		if err != nil {
			return nil, []error{fmt.Errorf("failed to get image spec for compose app %s: %w", appName, err)}
		}
		imageSpec.Image, err = ReplaceParametersInString(imageSpec.Image, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in image for app %s: %w", appName, err))
		}
		if len(errs) > 0 {
			return nil, errs
		}
		if err := composeApp.FromImageApplicationProviderSpec(imageSpec); err != nil {
			return nil, []error{fmt.Errorf("failed updating image spec for compose app %s: %w", appName, err)}
		}

	case domain.InlineApplicationProviderType:
		inlineSpec, err := composeApp.AsInlineApplicationProviderSpec()
		if err != nil {
			return nil, []error{fmt.Errorf("failed to get inline spec for compose app %s: %w", appName, err)}
		}
		inlineErrs := f.replaceInlineContentParameters(device, appName, &inlineSpec)
		errs = append(errs, inlineErrs...)
		if len(errs) > 0 {
			return nil, errs
		}
		if err := composeApp.FromInlineApplicationProviderSpec(inlineSpec); err != nil {
			return nil, []error{fmt.Errorf("failed updating inline spec for compose app %s: %w", appName, err)}
		}
	}

	var newItem domain.ApplicationProviderSpec
	if err := newItem.FromComposeApplication(composeApp); err != nil {
		return nil, []error{fmt.Errorf("failed converting compose application: %w", err)}
	}

	return &newItem, nil
}

func (f FleetRolloutsLogic) replaceQuadletApplicationParameters(device *domain.Device, app domain.ApplicationProviderSpec) (*domain.ApplicationProviderSpec, []error) {
	quadletApp, err := app.AsQuadletApplication()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert to quadlet application: %w", err)}
	}
	appName := lo.FromPtr(quadletApp.Name)

	var errs []error

	newEnvVars, envErrs := replaceEnvVarsMap(device, quadletApp.EnvVars)
	errs = append(errs, envErrs...)
	quadletApp.EnvVars = newEnvVars

	if quadletApp.Volumes != nil {
		newVolumes, volErrs := f.replaceVolumeParameters(device, appName, *quadletApp.Volumes)
		errs = append(errs, volErrs...)
		if len(volErrs) == 0 {
			quadletApp.Volumes = &newVolumes
		}
	}

	providerType, err := quadletApp.Type()
	if err != nil {
		return nil, []error{fmt.Errorf("failed getting provider type for quadlet app %s: %w", appName, err)}
	}

	switch providerType {
	case domain.ImageApplicationProviderType:
		imageSpec, err := quadletApp.AsImageApplicationProviderSpec()
		if err != nil {
			return nil, []error{fmt.Errorf("failed to get image spec for quadlet app %s: %w", appName, err)}
		}
		imageSpec.Image, err = ReplaceParametersInString(imageSpec.Image, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in image for app %s: %w", appName, err))
		}
		if len(errs) > 0 {
			return nil, errs
		}
		if err := quadletApp.FromImageApplicationProviderSpec(imageSpec); err != nil {
			return nil, []error{fmt.Errorf("failed updating image spec for quadlet app %s: %w", appName, err)}
		}

	case domain.InlineApplicationProviderType:
		inlineSpec, err := quadletApp.AsInlineApplicationProviderSpec()
		if err != nil {
			return nil, []error{fmt.Errorf("failed to get inline spec for quadlet app %s: %w", appName, err)}
		}
		inlineErrs := f.replaceInlineContentParameters(device, appName, &inlineSpec)
		errs = append(errs, inlineErrs...)
		if len(errs) > 0 {
			return nil, errs
		}
		if err := quadletApp.FromInlineApplicationProviderSpec(inlineSpec); err != nil {
			return nil, []error{fmt.Errorf("failed updating inline spec for quadlet app %s: %w", appName, err)}
		}
	}

	var newItem domain.ApplicationProviderSpec
	if err := newItem.FromQuadletApplication(quadletApp); err != nil {
		return nil, []error{fmt.Errorf("failed converting quadlet application: %w", err)}
	}

	return &newItem, nil
}

func (f FleetRolloutsLogic) replaceVmApplicationParameters(device *domain.Device, app domain.ApplicationProviderSpec) (*domain.ApplicationProviderSpec, []error) {
	vmApp, err := app.AsVmApplication()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert to vm application: %w", err)}
	}
	appName := lo.FromPtr(vmApp.Name)

	providerType, err := vmApp.Type()
	if err != nil {
		return nil, []error{fmt.Errorf("failed getting provider type for vm app %s: %w", appName, err)}
	}

	switch providerType {
	case domain.ImageApplicationProviderType:
		imageSpec, err := vmApp.AsImageApplicationProviderSpec()
		if err != nil {
			return nil, []error{fmt.Errorf("failed to get image spec for vm app %s: %w", appName, err)}
		}
		imageSpec.Image, err = ReplaceParametersInString(imageSpec.Image, device)
		if err != nil {
			return nil, []error{fmt.Errorf("failed replacing parameters in image for vm app %s: %w", appName, err)}
		}
		if err := vmApp.FromImageApplicationProviderSpec(imageSpec); err != nil {
			return nil, []error{fmt.Errorf("failed updating image spec for vm app %s: %w", appName, err)}
		}

	case domain.InlineApplicationProviderType:
		inlineSpec, err := vmApp.AsInlineApplicationProviderSpec()
		if err != nil {
			return nil, []error{fmt.Errorf("failed to get inline spec for vm app %s: %w", appName, err)}
		}
		if inlineErrs := f.replaceInlineContentParameters(device, appName, &inlineSpec); len(inlineErrs) > 0 {
			return nil, inlineErrs
		}
		if err := vmApp.FromInlineApplicationProviderSpec(inlineSpec); err != nil {
			return nil, []error{fmt.Errorf("failed updating inline spec for vm app %s: %w", appName, err)}
		}
	}

	var newItem domain.ApplicationProviderSpec
	if err := newItem.FromVmApplication(vmApp); err != nil {
		return nil, []error{fmt.Errorf("failed converting vm application: %w", err)}
	}

	return &newItem, nil
}

func (f FleetRolloutsLogic) replaceInlineContentParameters(device *domain.Device, appName string, inlineSpec *domain.InlineApplicationProviderSpec) []error {
	var errs []error
	for fileIndex, file := range inlineSpec.Inline {
		var decodedBytes []byte
		var err error

		inlineSpec.Inline[fileIndex].Path, err = ReplaceParametersInString(file.Path, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in path for file %d in inline app %s: %w", fileIndex, appName, err))
		}

		content := lo.FromPtr(file.Content)
		encoding := lo.FromPtr(file.ContentEncoding)
		if encoding == domain.EncodingBase64 {
			decodedBytes, err = base64.StdEncoding.DecodeString(content)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed base64 decoding contents for file %d in inline app %s: %w", fileIndex, appName, err))
				continue
			}
		} else {
			decodedBytes = []byte(content)
		}

		contentsReplaced, err := ReplaceParametersInString(string(decodedBytes), device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in contents for file %d in inline app %s: %w", fileIndex, appName, err))
			continue
		}

		if encoding == domain.EncodingBase64 {
			contentsReplaced = base64.StdEncoding.EncodeToString([]byte(contentsReplaced))
		}
		inlineSpec.Inline[fileIndex].Content = &contentsReplaced
	}
	return errs
}

func (f FleetRolloutsLogic) replaceVolumeParameters(device *domain.Device, appName string, volumes []domain.ApplicationVolume) ([]domain.ApplicationVolume, []error) {
	var errs []error
	newVolumes := make([]domain.ApplicationVolume, 0, len(volumes))

	for volIndex, vol := range volumes {
		volType, err := vol.Type()
		if err != nil {
			errs = append(errs, fmt.Errorf("failed getting volume type for volume %d in app %s: %w", volIndex, appName, err))
			continue
		}

		newVol := domain.ApplicationVolume{
			Name:          vol.Name,
			ReclaimPolicy: vol.ReclaimPolicy,
		}

		switch volType {
		case domain.ImageApplicationVolumeProviderType:
			imgSpec, err := vol.AsImageVolumeProviderSpec()
			if err != nil {
				errs = append(errs, fmt.Errorf("failed getting image volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			imgSpec.Image.Reference, err = ReplaceParametersInString(imgSpec.Image.Reference, device)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed replacing parameters in image reference for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			if err := newVol.FromImageVolumeProviderSpec(imgSpec); err != nil {
				errs = append(errs, fmt.Errorf("failed converting image volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

		case domain.ImageMountApplicationVolumeProviderType:
			imgMountSpec, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil {
				errs = append(errs, fmt.Errorf("failed getting image mount volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			imgMountSpec.Image.Reference, err = ReplaceParametersInString(imgMountSpec.Image.Reference, device)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed replacing parameters in image reference for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			if err := newVol.FromImageMountVolumeProviderSpec(imgMountSpec); err != nil {
				errs = append(errs, fmt.Errorf("failed converting image mount volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

		case domain.MountApplicationVolumeProviderType:
			mountSpec, err := vol.AsMountVolumeProviderSpec()
			if err != nil {
				errs = append(errs, fmt.Errorf("failed getting mount volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			if err := newVol.FromMountVolumeProviderSpec(mountSpec); err != nil {
				errs = append(errs, fmt.Errorf("failed converting mount volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

		default:
			errs = append(errs, fmt.Errorf("unsupported volume type %s for volume %d in app %s", volType, volIndex, appName))
			continue
		}

		newVolumes = append(newVolumes, newVol)
	}

	return newVolumes, errs
}

func (f FleetRolloutsLogic) getDeviceConfig(device *domain.Device, templateVersion *domain.TemplateVersion) (*[]domain.ConfigProviderSpec, []model.DependencyRef, []error) {
	if templateVersion.Status.Config == nil {
		return nil, nil, nil
	}

	deviceConfig := []domain.ConfigProviderSpec{}
	depRefs := make(map[string]model.DependencyRef)
	configErrs := []error{}
	for _, configItem := range *templateVersion.Status.Config {
		var newConfigItem *domain.ConfigProviderSpec
		errs := []error{}

		configType, err := configItem.Type()
		if err != nil {
			configErrs = append(configErrs, fmt.Errorf("%w: failed getting config type: %w", ErrUnknownConfigName, err))
			continue
		}

		switch configType {
		case domain.GitConfigProviderType:
			var refs []model.DependencyRef
			newConfigItem, refs, errs = f.replaceGitConfigParameters(device, configItem)
			for _, ref := range refs {
				depRefs[ref.ResourceKey] = ref
			}
		case domain.KubernetesSecretProviderType:
			var refs []model.DependencyRef
			newConfigItem, refs, errs = f.replaceKubeSecretConfigParameters(device, configItem)
			for _, ref := range refs {
				depRefs[ref.ResourceKey] = ref
			}
		case domain.InlineConfigProviderType:
			newConfigItem, errs = f.replaceInlineConfigParameters(device, configItem)
		case domain.HttpConfigProviderType:
			var refs []model.DependencyRef
			newConfigItem, refs, errs = f.replaceHTTPConfigParameters(device, configItem)
			for _, ref := range refs {
				depRefs[ref.ResourceKey] = ref
			}
		default:
			errs = append(errs, fmt.Errorf("%w: unsupported config type %q", ErrUnknownConfigName, configType))
		}

		configErrs = append(configErrs, errs...)
		if newConfigItem != nil {
			deviceConfig = append(deviceConfig, *newConfigItem)
		}
	}

	if len(configErrs) > 0 {
		return nil, nil, configErrs
	}

	refs := make([]model.DependencyRef, 0, len(depRefs))
	for _, ref := range depRefs {
		refs = append(refs, ref)
	}
	return &deviceConfig, refs, nil
}

func (f FleetRolloutsLogic) replaceGitConfigParameters(device *domain.Device, configItem domain.ConfigProviderSpec) (*domain.ConfigProviderSpec, []model.DependencyRef, []error) {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return nil, nil, []error{fmt.Errorf("failed to convert config to git config: %w", err)}
	}

	errs := []error{}
	originalRevision := gitSpec.GitRef.TargetRevision

	gitSpec.GitRef.TargetRevision, err = ReplaceParametersInString(gitSpec.GitRef.TargetRevision, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in targetRevision in git config %s: %w", gitSpec.Name, err))
	}

	gitSpec.GitRef.Path, err = ReplaceParametersInString(gitSpec.GitRef.Path, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in path in git config %s: %w", gitSpec.Name, err))
	}

	if len(errs) > 0 {
		return nil, nil, errs
	}

	var refs []model.DependencyRef
	if isParameterized(originalRevision) {
		deviceName := lo.FromPtr(device.Metadata.Name)
		ownerFleetName, _, _ := getOwnerFleet(device)
		refs = append(refs, model.DependencyRef{
			FleetName:      &ownerFleetName,
			DeviceName:     &deviceName,
			RefType:        "git",
			ResourceKey:    fmt.Sprintf("git:%s/%s", gitSpec.GitRef.Repository, gitSpec.GitRef.TargetRevision),
			RepositoryName: &gitSpec.GitRef.Repository,
			Revision:       &gitSpec.GitRef.TargetRevision,
		})
	}

	newConfigItem := domain.ConfigProviderSpec{}
	err = newConfigItem.FromGitConfigProviderSpec(gitSpec)
	if err != nil {
		return nil, nil, []error{fmt.Errorf("failed converting git config: %w", err)}
	}

	return &newConfigItem, refs, nil
}

func (f FleetRolloutsLogic) replaceKubeSecretConfigParameters(device *domain.Device, configItem domain.ConfigProviderSpec) (*domain.ConfigProviderSpec, []model.DependencyRef, []error) {
	secretSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return nil, nil, []error{fmt.Errorf("failed to convert config to kubernetes secret config: %w", err)}
	}

	errs := []error{}
	originalNamespace := secretSpec.SecretRef.Namespace
	originalName := secretSpec.SecretRef.Name

	secretSpec.SecretRef.Name, err = ReplaceParametersInString(secretSpec.SecretRef.Name, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in name in k8s secret config %s: %w", secretSpec.Name, err))
	}

	secretSpec.SecretRef.Namespace, err = ReplaceParametersInString(secretSpec.SecretRef.Namespace, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in namespace in k8s secret config %s: %w", secretSpec.Name, err))
	}

	secretSpec.SecretRef.MountPath, err = ReplaceParametersInString(secretSpec.SecretRef.MountPath, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in mountPath in k8s secret config %s: %w", secretSpec.Name, err))
	}

	if len(errs) > 0 {
		return nil, nil, errs
	}

	var refs []model.DependencyRef
	if isParameterized(originalNamespace) || isParameterized(originalName) {
		deviceName := lo.FromPtr(device.Metadata.Name)
		ownerFleetName, _, _ := getOwnerFleet(device)
		refs = append(refs, model.DependencyRef{
			FleetName:       &ownerFleetName,
			DeviceName:      &deviceName,
			RefType:         "secret",
			ResourceKey:     fmt.Sprintf("secret:%s/%s", secretSpec.SecretRef.Namespace, secretSpec.SecretRef.Name),
			SecretName:      &secretSpec.SecretRef.Name,
			SecretNamespace: &secretSpec.SecretRef.Namespace,
		})
	}

	newConfigItem := domain.ConfigProviderSpec{}
	err = newConfigItem.FromKubernetesSecretProviderSpec(secretSpec)
	if err != nil {
		return nil, nil, []error{fmt.Errorf("failed converting secret config: %w", err)}
	}

	return &newConfigItem, refs, nil
}

func (f FleetRolloutsLogic) replaceInlineConfigParameters(device *domain.Device, configItem domain.ConfigProviderSpec) (*domain.ConfigProviderSpec, []error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert config to inline config: %w", err)}
	}

	errs := []error{}

	for fileIndex, file := range inlineSpec.Inline {
		var decodedBytes []byte
		var err error

		inlineSpec.Inline[fileIndex].Path, err = ReplaceParametersInString(file.Path, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in path for file %d in inline config %s: %w", fileIndex, inlineSpec.Name, err))
		}

		encoding := lo.FromPtr(file.ContentEncoding)
		if encoding == domain.EncodingBase64 {
			decodedBytes, err = base64.StdEncoding.DecodeString(file.Content)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed base64 decoding contents for file %d in inline config %s: %w", fileIndex, inlineSpec.Name, err))
				continue
			}
		} else {
			decodedBytes = []byte(file.Content)
		}

		contentsReplaced, err := ReplaceParametersInString(string(decodedBytes), device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in contents for file %d in inline config %s: %w", fileIndex, inlineSpec.Name, err))
			continue
		}

		if encoding == domain.EncodingBase64 {
			inlineSpec.Inline[fileIndex].Content = base64.StdEncoding.EncodeToString([]byte(contentsReplaced))
		} else {
			inlineSpec.Inline[fileIndex].Content = contentsReplaced
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}

	newConfigItem := domain.ConfigProviderSpec{}
	err = newConfigItem.FromInlineConfigProviderSpec(inlineSpec)
	if err != nil {
		return nil, []error{fmt.Errorf("failed converting inline config: %w", err)}
	}

	return &newConfigItem, nil
}

func (f FleetRolloutsLogic) replaceHTTPConfigParameters(device *domain.Device, configItem domain.ConfigProviderSpec) (*domain.ConfigProviderSpec, []model.DependencyRef, []error) {
	httpSpec, err := configItem.AsHttpConfigProviderSpec()
	if err != nil {
		return nil, nil, []error{fmt.Errorf("failed to convert config to http config: %w", err)}
	}

	errs := []error{}
	var originalSuffix string
	if httpSpec.HttpRef.Suffix != nil {
		originalSuffix = *httpSpec.HttpRef.Suffix
	}

	if httpSpec.HttpRef.Suffix != nil {
		suffix, err := ReplaceParametersInString(*httpSpec.HttpRef.Suffix, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in suffix in http config %s: %w", httpSpec.Name, err))
		}
		httpSpec.HttpRef.Suffix = &suffix
	}

	httpSpec.HttpRef.FilePath, err = ReplaceParametersInString(httpSpec.HttpRef.FilePath, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in file path in http config %s: %w", httpSpec.Name, err))
	}

	if len(errs) > 0 {
		return nil, nil, errs
	}

	var refs []model.DependencyRef
	if isParameterized(originalSuffix) {
		deviceName := lo.FromPtr(device.Metadata.Name)
		ownerFleetName, _, _ := getOwnerFleet(device)
		resolvedSuffix := ""
		if httpSpec.HttpRef.Suffix != nil {
			resolvedSuffix = *httpSpec.HttpRef.Suffix
		}
		refs = append(refs, model.DependencyRef{
			FleetName:      &ownerFleetName,
			DeviceName:     &deviceName,
			RefType:        "http",
			ResourceKey:    httpResourceKey(httpSpec.HttpRef.Repository, resolvedSuffix),
			RepositoryName: &httpSpec.HttpRef.Repository,
			HTTPSuffix:     httpSpec.HttpRef.Suffix,
		})
	}

	newConfigItem := domain.ConfigProviderSpec{}
	err = newConfigItem.FromHttpConfigProviderSpec(httpSpec)
	if err != nil {
		return nil, nil, []error{fmt.Errorf("failed converting http config: %w", err)}
	}

	return &newConfigItem, refs, nil
}

func (f FleetRolloutsLogic) updateDeviceInStore(ctx context.Context, device *domain.Device, newDeviceSpec *domain.DeviceSpec, delayDeviceRender bool) error {
	var status domain.Status
	for i := 0; i < 10; i++ {
		if device.Metadata.Owner == nil || *device.Metadata.Owner != f.owner {
			return fmt.Errorf("device owner changed, skipping rollout")
		}
		device.Spec = newDeviceSpec
		newCtx := context.WithValue(ctx, consts.DelayDeviceRenderCtxKey, delayDeviceRender)
		_, status = f.deviceSvc.ReplaceDevice(newCtx, f.orgId, *device.Metadata.Name, *device, nil, false)
		if status.Code != http.StatusOK {
			if status.Code == http.StatusConflict {
				device, status = f.deviceSvc.GetDevice(ctx, f.orgId, *device.Metadata.Name)
				if status.Code != http.StatusOK {
					return fmt.Errorf("the device changed before we could update it, and we failed to fetch it again: %s", status.Message)
				}
			} else {
				return common.ApiStatusToErr(status)
			}
		} else {
			break
		}
	}

	return common.ApiStatusToErr(status)
}

func ReplaceParametersInString(s string, device *domain.Device) (string, error) {
	t, err := template.New("t").Option("missingkey=error").Funcs(domain.GetGoTemplateFuncMap()).Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid parameter syntax: %v", err)
	}

	output, err := domain.ExecuteGoTemplateOnDevice(t, device)
	if err != nil {
		return "", fmt.Errorf("cannot apply parameters, possibly because they access invalid fields: %w", err)
	}

	return output, nil
}
