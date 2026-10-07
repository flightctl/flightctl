package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	mappingScanCheckpointConsumer = "label-sync-mapping-scan"
	mappingScanCheckpointVersion  = 1
	maxMappingScanPageSize        = config.MaxLabelMappingScanPageSize
)

// LabelMappingScanConfig bounds the amount of device work performed by one poll.
type LabelMappingScanConfig struct {
	PageSize   int
	TimeBudget time.Duration
}

type mappingScanCheckpoint struct {
	Active bool `json:"active,omitempty"`
	// Recovery contains only mappings whose captured generation finished a full
	// pass. It prevents Degraded mappings from starting another fleet scan.
	Recovery     []mappingScanProgress `json:"recovery,omitempty"`
	Version      int                   `json:"version"`
	Cursor       *string               `json:"cursor,omitempty"`
	ScanComplete bool                  `json:"scanComplete,omitempty"`
	Mappings     []mappingScanProgress `json:"mappings"`
}

type mappingScanProgress struct {
	Token  labelsyncmappingservice.MappingScanToken `json:"token"`
	Stale  bool                                     `json:"stale,omitempty"`
	Failed bool                                     `json:"failed,omitempty"`
	// FailureMessage is cleared after the campaign publishes Degraded.
	FailureMessage *string `json:"failureMessage,omitempty"`
}

// LabelMappingScanTask runs a resumable device-label mapping scan for one org.
type LabelMappingScanTask struct {
	log         logrus.FieldLogger
	reconciler  labelsyncmappingservice.Service
	deviceSvc   deviceservice.Service
	checkpoints checkpointservice.Service
	config      LabelMappingScanConfig
}

func NewLabelMappingScanTask(
	reconciler labelsyncmappingservice.Service,
	deviceSvc deviceservice.Service,
	checkpoints checkpointservice.Service,
	config LabelMappingScanConfig,
	log logrus.FieldLogger,
) (*LabelMappingScanTask, error) {
	if reconciler == nil || deviceSvc == nil || checkpoints == nil {
		return nil, errors.New("mapping scan task requires a reconciler, device service, and checkpoint service")
	}
	if config.PageSize < 1 || config.PageSize > maxMappingScanPageSize {
		return nil, fmt.Errorf("mapping scan page size must be between 1 and %d", maxMappingScanPageSize)
	}
	if config.TimeBudget <= 0 {
		return nil, errors.New("mapping scan time budget must be positive")
	}
	if log == nil {
		log = logrus.New()
	}
	return &LabelMappingScanTask{
		log:         log,
		reconciler:  reconciler,
		deviceSvc:   deviceSvc,
		checkpoints: checkpoints,
		config:      config,
	}, nil
}

// Poll advances one organization's mapping scan by whole device pages.
func (t *LabelMappingScanTask) Poll(ctx context.Context, orgID uuid.UUID) {
	if err := t.poll(ctx, orgID); err != nil {
		t.log.WithError(err).WithField("orgID", orgID).Error("Mapping scan stopped")
	}
}

func (t *LabelMappingScanTask) poll(ctx context.Context, orgID uuid.UUID) error {
	targets, err := t.reconciler.ListMappingScanTargets(ctx, orgID)
	if err != nil {
		return err
	}
	checkpoint, found, valid, err := t.loadCheckpoint(ctx, orgID)
	if err != nil {
		return err
	}
	checkpoint = mappingScanCampaignCheckpoint(targets, checkpoint, found && valid)
	startedAt := time.Now()
	for checkpoint.Active {
		if err := ctx.Err(); err != nil {
			return err
		}
		if checkpoint.ScanComplete {
			if err := t.completeCampaign(ctx, orgID, &checkpoint); err != nil {
				return err
			}
			break
		}
		devices, status := t.deviceSvc.ListDevices(ctx, orgID, domain.ListDevicesParams{Limit: lo.ToPtr(int32(t.config.PageSize)), Continue: cloneString(checkpoint.Cursor)}, nil)
		if status.Code >= http.StatusBadRequest {
			return fmt.Errorf("device listing returned %d: %s", status.Code, status.Message)
		}
		if err := t.processDevicePage(ctx, orgID, &checkpoint, devices); err != nil {
			return err
		}
		if devices.Metadata.Continue != nil && checkpoint.Cursor != nil && *devices.Metadata.Continue == *checkpoint.Cursor {
			return errors.New("device listing did not advance its cursor")
		}
		checkpoint.Cursor = cloneString(devices.Metadata.Continue)
		checkpoint.ScanComplete = devices.Metadata.Continue == nil
		if err := t.persistCheckpoint(ctx, orgID, checkpoint); err != nil {
			return err
		}
		if checkpoint.ScanComplete {
			if err := t.completeCampaign(ctx, orgID, &checkpoint); err != nil {
				return err
			}
			break
		}
		if time.Since(startedAt) >= t.config.TimeBudget {
			break
		}
	}
	return t.completeRecovery(ctx, orgID, &checkpoint)
}

func (t *LabelMappingScanTask) processDevicePage(ctx context.Context, orgID uuid.UUID, checkpoint *mappingScanCheckpoint, devices *domain.DeviceList) error {
	for _, device := range devices.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, reconcileErr := t.reconciler.ReconcileDeviceLabels(ctx, orgID, *device.Metadata.Name)
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(reconcileErr, labelsyncmappingservice.ErrConditionPersistence) {
			return reconcileErr
		}
		if result.DeviceDeleted {
			continue
		}
		recordDeviceOutcomes(checkpoint, result, reconcileErr)
	}
	return nil
}

func recordDeviceOutcomes(checkpoint *mappingScanCheckpoint, result labelsyncmappingservice.ReconciliationResult, reconcileErr error) {
	for i := range checkpoint.Mappings {
		progress := &checkpoint.Mappings[i]
		if progress.Failed || progress.Stale {
			continue
		}
		var failure error
		if !result.MappingApplyCommitted {
			failure = reconcileErr
		}
		for _, outcome := range result.MappingOutcomes {
			if outcome.MappingID == progress.Token.MappingID {
				failure = errors.Join(failure, outcome.Err)
			}
		}
		if failure == nil {
			continue
		}
		progress.Failed = true
		message := failure.Error()
		// Detailed failures remain on Devices; bound the campaign's first
		// failure message per mapping independently of the device count.
		if len(message) > 1024 {
			message = "Mapping propagation completed with device reconciliation failures"
		}
		progress.FailureMessage = &message
	}
}

func (t *LabelMappingScanTask) completeCampaign(ctx context.Context, orgID uuid.UUID, checkpoint *mappingScanCheckpoint) error {
	changed := false
	for i := range checkpoint.Mappings {
		progress := &checkpoint.Mappings[i]
		if progress.Stale || progress.FailureMessage == nil {
			continue
		}
		updated, saved, err := t.reconciler.RecordMappingScanFailure(ctx, orgID, progress.Token, *progress.FailureMessage)
		if err != nil {
			return err
		}
		if saved {
			progress.Token = cloneMappingScanToken(updated)
		} else {
			progress.Stale = true
		}
		progress.FailureMessage = nil
		changed = true
	}
	if changed {
		// Save returned failure fences before attempting clean-map completion,
		// so a completion retry never republishes the same campaign failure.
		if err := t.persistCheckpoint(ctx, orgID, *checkpoint); err != nil {
			return err
		}
	}
	tokens := []labelsyncmappingservice.MappingScanToken{}
	for _, progress := range checkpoint.Mappings {
		if !progress.Failed && !progress.Stale {
			tokens = append(tokens, progress.Token)
		}
	}
	completed := map[uuid.UUID]bool{}
	if len(tokens) > 0 {
		var err error
		completed, err = t.reconciler.CompleteMappingScan(ctx, orgID, tokens)
		if err != nil {
			return err
		}
	}
	for _, progress := range checkpoint.Mappings {
		if !progress.Stale && !completed[progress.Token.MappingID] {
			checkpoint.Recovery = append(checkpoint.Recovery, cloneMappingScanProgress(progress))
		}
	}
	checkpoint.Active, checkpoint.ScanComplete, checkpoint.Cursor = false, false, nil
	checkpoint.Mappings = []mappingScanProgress{}
	return t.persistCheckpoint(ctx, orgID, *checkpoint)
}

func (t *LabelMappingScanTask) completeRecovery(ctx context.Context, orgID uuid.UUID, checkpoint *mappingScanCheckpoint) error {
	if len(checkpoint.Recovery) == 0 {
		return nil
	}
	// Device events repair failures. Periodic work only checks whether the
	// already-completed generations can now leave Degraded; it retries no devices.
	devices, status := t.deviceSvc.ListDevicesByServiceCondition(ctx, orgID, string(domain.ConditionTypeDeviceLabelsSynced), string(domain.ConditionStatusFalse), store.ListParams{Limit: 1})
	if status.Code >= http.StatusBadRequest {
		return fmt.Errorf("recovery listing returned %d: %s", status.Code, status.Message)
	}
	if len(devices.Items) != 0 {
		return nil
	}
	tokens := make([]labelsyncmappingservice.MappingScanToken, len(checkpoint.Recovery))
	for i, progress := range checkpoint.Recovery {
		tokens[i] = progress.Token
	}
	completed, err := t.reconciler.CompleteMappingScan(ctx, orgID, tokens)
	if err != nil {
		return err
	}
	remaining := checkpoint.Recovery[:0]
	for _, progress := range checkpoint.Recovery {
		if !completed[progress.Token.MappingID] {
			remaining = append(remaining, progress)
		}
	}
	checkpoint.Recovery = remaining
	return t.persistCheckpoint(ctx, orgID, *checkpoint)
}

func (t *LabelMappingScanTask) loadCheckpoint(ctx context.Context, orgID uuid.UUID) (mappingScanCheckpoint, bool, bool, error) {
	data, status := t.checkpoints.GetCheckpoint(ctx, mappingScanCheckpointConsumer, orgID.String())
	if status.Code == http.StatusNotFound {
		return mappingScanCheckpoint{}, false, true, nil
	}
	if status.Code >= http.StatusBadRequest {
		return mappingScanCheckpoint{}, false, false, fmt.Errorf("checkpoint service returned %d: %s", status.Code, status.Message)
	}
	var checkpoint mappingScanCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil || !validMappingScanCheckpoint(checkpoint) {
		return mappingScanCheckpoint{}, true, false, nil
	}
	return checkpoint, true, true, nil
}

func validMappingScanCheckpoint(checkpoint mappingScanCheckpoint) bool {
	if checkpoint.Version != mappingScanCheckpointVersion || (checkpoint.ScanComplete && (!checkpoint.Active || checkpoint.Cursor != nil)) || (!checkpoint.Active && (checkpoint.Cursor != nil || len(checkpoint.Mappings) != 0)) {
		return false
	}
	if checkpoint.Active && len(checkpoint.Mappings) == 0 {
		return false
	}
	if _, err := store.ParseContinueString(checkpoint.Cursor); err != nil {
		return false
	}
	seen := make(map[uuid.UUID]struct{})
	for _, progress := range append(append([]mappingScanProgress{}, checkpoint.Mappings...), checkpoint.Recovery...) {
		if progress.Token.MappingID == uuid.Nil || progress.Token.Generation < 1 || progress.Token.FailureRevision < 0 {
			return false
		}
		if _, exists := seen[progress.Token.MappingID]; exists {
			return false
		}
		seen[progress.Token.MappingID] = struct{}{}
	}
	return true
}

func mappingScanCampaignCheckpoint(targets []labelsyncmappingservice.MappingScanToken, checkpoint mappingScanCheckpoint, resume bool) mappingScanCheckpoint {
	if !resume {
		checkpoint = mappingScanCheckpoint{Version: mappingScanCheckpointVersion, Mappings: []mappingScanProgress{}}
	}
	current := make(map[uuid.UUID]labelsyncmappingservice.MappingScanToken, len(targets))
	for _, target := range targets {
		current[target.MappingID] = target
	}
	recovery := checkpoint.Recovery[:0]
	for _, progress := range checkpoint.Recovery {
		if token, exists := current[progress.Token.MappingID]; exists && mappingScanTokensMatch(progress.Token, token) {
			recovery = append(recovery, progress)
			delete(current, token.MappingID)
		}
	}
	checkpoint.Recovery = recovery
	if checkpoint.Active {
		for i := range checkpoint.Mappings {
			progress := &checkpoint.Mappings[i]
			token, exists := current[progress.Token.MappingID]
			progress.Stale = progress.Stale || !exists || !mappingScanTokensMatch(progress.Token, token)
		}
		return checkpoint
	}
	checkpoint.Mappings = []mappingScanProgress{}
	for _, target := range targets {
		if _, needsCoverage := current[target.MappingID]; needsCoverage {
			checkpoint.Mappings = append(checkpoint.Mappings, mappingScanProgress{Token: cloneMappingScanToken(target)})
		}
	}
	checkpoint.Active = len(checkpoint.Mappings) != 0
	return checkpoint
}

func mappingScanTokensMatch(left, right labelsyncmappingservice.MappingScanToken) bool {
	if left.MappingID != right.MappingID || left.Generation != right.Generation || left.FailureRevision != right.FailureRevision {
		return false
	}
	if left.DeletionRevision == nil || right.DeletionRevision == nil {
		return left.DeletionRevision == nil && right.DeletionRevision == nil
	}
	return *left.DeletionRevision == *right.DeletionRevision
}

func (t *LabelMappingScanTask) persistCheckpoint(ctx context.Context, orgID uuid.UUID, checkpoint mappingScanCheckpoint) error {
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	status := t.checkpoints.SetCheckpoint(ctx, mappingScanCheckpointConsumer, orgID.String(), data)
	if status.Code >= http.StatusBadRequest {
		return fmt.Errorf("checkpoint service returned %d: %s", status.Code, status.Message)
	}
	return nil
}

func cloneMappingScanProgress(progress mappingScanProgress) mappingScanProgress {
	progress.Token = cloneMappingScanToken(progress.Token)
	progress.FailureMessage = cloneString(progress.FailureMessage)
	return progress
}

func cloneMappingScanToken(token labelsyncmappingservice.MappingScanToken) labelsyncmappingservice.MappingScanToken {
	token.DeletionRevision = cloneScanRevision(token.DeletionRevision)
	return token
}

func cloneScanRevision(value *int64) *int64 {
	if value == nil {
		return nil
	}
	return lo.ToPtr(*value)
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	return lo.ToPtr(*value)
}
