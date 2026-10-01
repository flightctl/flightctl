package labelsyncmapping

import (
	"context"
	"errors"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

const maxReconciliationAttempts = 5

// ReconciliationDeviceStore exposes the device-label operations used by the
// reconciliation and provenance flows.
type ReconciliationDeviceStore interface {
	GetLabelSnapshot(context.Context, uuid.UUID, string) (domain.DeviceLabelSnapshot, error)
	GetLabelSyncMappingIDsByKeys(context.Context, uuid.UUID, []string) (map[string][]uuid.UUID, error)
	ApplyLabels(context.Context, uuid.UUID, string, domain.DeviceLabelSnapshot, map[string]domain.DesiredDeviceLabel, *domain.Condition) (domain.DeviceLabelApplyResult, error)
}

// ErrConditionPersistence distinguishes an unrecorded result from a device
// failure. A scan must retain its page when the result cannot be persisted.
var ErrConditionPersistence = errors.New("label reconciliation condition was not persisted")

func (h *ServiceHandler) ReconcileDeviceLabels(ctx context.Context, orgID uuid.UUID, deviceName string) (ReconciliationResult, error) {
	if store.InTransaction(ctx) {
		return ReconciliationResult{}, errors.New("label-sync reconciliation cannot run inside an existing store transaction")
	}

	var result ReconciliationResult

	for attempt := 0; attempt < maxReconciliationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return ReconciliationResult{}, err
		}
		result.MappingApplyCommitted = false
		result.MappingOutcomes = nil
		mappings, err := h.store.GetDeviceMappingsSnapshot(ctx, orgID)
		if err != nil {
			return h.recordReconciliationFailure(ctx, orgID, deviceName, ReconciliationResult{}, err)
		}
		deviceSnapshot, err := h.deviceStore.GetLabelSnapshot(ctx, orgID, deviceName)
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return ReconciliationResult{DeviceDeleted: true}, nil
		}
		if err != nil {
			return result, errors.Join(ErrConditionPersistence, err)
		}
		// Decommissioning clears labels. If it races this read, ApplyLabels' resource-version
		// CAS forces a retry, which observes the decommissioning state here.
		if deviceSnapshot.Device.Spec != nil && deviceSnapshot.Device.Spec.Decommissioning != nil {
			return result, nil
		}
		snapshot := deviceReconciliationSnapshot{
			Device:          deviceSnapshot,
			Mappings:        mappings.Mappings,
			MappingRevision: mappings.Revision,
		}
		desired, outcomes, reconcileErr := desiredDeviceLabels(snapshot, h.evaluator)
		result.MappingOutcomes = outcomes
		if reconcileErr != nil {
			desired = currentDeviceLabels(deviceSnapshot)
		}
		failure := reconcileErr
		for _, outcome := range outcomes {
			failure = errors.Join(failure, outcome.Err)
		}
		condition := labelReconciliationCondition(failure)
		applied, applyErr := h.deviceStore.ApplyLabels(ctx, orgID, deviceName, deviceSnapshot, desired, &condition)
		if applyErr != nil {
			retry, retryErr := h.shouldRetryApply(ctx, orgID, mappings.Revision, applyErr)
			if retry && attempt+1 < maxReconciliationAttempts {
				continue
			}
			return h.recordReconciliationFailure(ctx, orgID, deviceName, result, errors.Join(reconcileErr, retryErr))
		}
		result.MappingApplyCommitted = reconcileErr == nil
		result.LabelsChanged = result.LabelsChanged || applied.LabelsChanged
		result.ManagedLabelsChanged = result.ManagedLabelsChanged || applied.ManagedLabelsChanged
		result.OwnershipChanged = result.OwnershipChanged || applied.OwnershipChanged
		h.emitLabelChange(ctx, orgID, deviceName, applied)
		latestMappings, err := h.store.GetDeviceMappingsSnapshot(ctx, orgID)
		if err != nil {
			result.MappingOutcomes = failedDeviceOutcomes(snapshot.Mappings, result.MappingOutcomes, err)
			return h.recordReconciliationFailure(ctx, orgID, deviceName, result, err)
		}
		if latestMappings.Revision != mappings.Revision {
			continue
		}
		return result, reconcileErr
	}
	return h.recordReconciliationFailure(ctx, orgID, deviceName, ReconciliationResult{}, flterrors.ErrResourceVersionConflict)
}

func (h *ServiceHandler) recordReconciliationFailure(ctx context.Context, orgID uuid.UUID, name string, result ReconciliationResult, failure error) (ReconciliationResult, error) {
	condition := labelReconciliationCondition(failure)
	for attempt := 0; attempt < maxReconciliationAttempts; attempt++ {
		snapshot, err := h.deviceStore.GetLabelSnapshot(ctx, orgID, name)
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return ReconciliationResult{DeviceDeleted: true}, nil
		}
		if err != nil {
			return result, errors.Join(ErrConditionPersistence, failure, err)
		}
		// A failed apply rolled back. Persist False while preserving the current
		// labels and owners under the same device resource-version check.
		applied, err := h.deviceStore.ApplyLabels(ctx, orgID, name, snapshot, currentDeviceLabels(snapshot), &condition)
		if errors.Is(err, flterrors.ErrResourceVersionConflict) || errors.Is(err, flterrors.ErrNoRowsUpdated) || isPostgresDeadlock(err) {
			continue
		}
		if err != nil {
			return result, errors.Join(ErrConditionPersistence, failure, err)
		}
		h.emitLabelChange(ctx, orgID, name, applied)
		return result, failure
	}
	return result, errors.Join(ErrConditionPersistence, failure, flterrors.ErrResourceVersionConflict)
}

func currentDeviceLabels(snapshot domain.DeviceLabelSnapshot) map[string]domain.DesiredDeviceLabel {
	labels := make(map[string]domain.DesiredDeviceLabel, len(lo.FromPtr(snapshot.Device.Metadata.Labels)))
	for key, value := range lo.FromPtr(snapshot.Device.Metadata.Labels) {
		labels[key] = domain.DesiredDeviceLabel{Value: value}
	}
	for _, owned := range snapshot.Labels {
		if label, exists := labels[owned.Key]; exists {
			label.MappingID = owned.MappingID
			labels[owned.Key] = label
		}
	}
	return labels
}

func labelReconciliationCondition(failure error) domain.Condition {
	condition := domain.Condition{Type: domain.ConditionTypeDeviceLabelsSynced, Status: domain.ConditionStatusTrue, Reason: "Reconciled", Message: "Device labels are reconciled"}
	if failure != nil {
		condition.Status, condition.Reason, condition.Message = domain.ConditionStatusFalse, "ReconciliationFailed", boundedLabelFailure(failure)
	}
	return condition
}

func (h *ServiceHandler) emitLabelChange(ctx context.Context, orgID uuid.UUID, name string, applied domain.DeviceLabelApplyResult) {
	if applied.LabelsChanged || applied.ManagedLabelsChanged {
		updates := &domain.ResourceUpdatedDetails{UpdatedFields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.Labels}}
		event := common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, domain.DeviceKind, name, updates, h.log, nil)
		if event != nil {
			h.events.CreateEvent(ctx, orgID, event)
		}
	}
}

func boundedLabelFailure(err error) string {
	const maxMessageBytes = 1024
	message := err.Error()
	if len(message) > maxMessageBytes {
		message = strings.ToValidUTF8(message[:maxMessageBytes-3], "") + "..."
	}
	return message
}

func failedDeviceOutcomes(mappings []labelsyncmappingstore.DeviceMapping, current []MappingOutcome, err error) []MappingOutcome {
	byID := make(map[uuid.UUID]MappingOutcome, len(current))
	for _, outcome := range current {
		byID[outcome.MappingID] = outcome
	}
	outcomes := make([]MappingOutcome, 0, len(mappings))
	for _, mapping := range mappings {
		if mapping.Mapping.Spec.ResourceType != domain.LabelSyncMappingDevice || mapping.Mapping.Metadata.DeletionTimestamp != nil {
			continue
		}
		outcome := byID[mapping.ID]
		outcome.MappingID = mapping.ID
		outcome.Generation = lo.FromPtr(mapping.Mapping.Metadata.Generation)
		outcome.ResourceVersion = lo.FromPtr(mapping.Mapping.Metadata.ResourceVersion)
		outcome.Err = errors.Join(outcome.Err, err)
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

func (h *ServiceHandler) shouldRetryApply(ctx context.Context, orgID uuid.UUID, revision int64, applyErr error) (bool, error) {
	if errors.Is(applyErr, flterrors.ErrResourceVersionConflict) || errors.Is(applyErr, flterrors.ErrNoRowsUpdated) {
		return true, applyErr
	}
	if isPostgresDeadlock(applyErr) {
		return true, applyErr
	}
	if !errors.Is(applyErr, flterrors.ErrResourceNotFound) {
		return false, applyErr
	}

	// A mapping may be finalized after evaluation but before the device store
	// assigns its owner ID. A changed revision makes that FK failure stale.
	latestMappings, err := h.store.GetDeviceMappingsSnapshot(ctx, orgID)
	if err != nil {
		return false, errors.Join(applyErr, err)
	}
	return latestMappings.Revision != revision, applyErr
}

func isPostgresDeadlock(err error) bool {
	type sqlStateError interface {
		SQLState() string
	}
	var stateErr sqlStateError
	return errors.As(err, &stateErr) && stateErr.SQLState() == "40P01"
}

type MappingOutcome struct {
	MappingID       uuid.UUID
	Generation      int64
	ResourceVersion string
	Err             error
}

type ReconciliationResult struct {
	DeviceDeleted         bool
	MappingApplyCommitted bool
	LabelsChanged         bool
	ManagedLabelsChanged  bool
	OwnershipChanged      bool
	MappingOutcomes       []MappingOutcome
}

type MappingScanToken struct {
	MappingID        uuid.UUID
	Generation       int64
	DeletionRevision *int64
	FailureRevision  int64
}

func (h *ServiceHandler) ListMappingScanTargets(ctx context.Context, orgID uuid.UUID) ([]MappingScanToken, error) {
	records, err := h.store.ListMappingScanTargets(ctx, orgID)
	if err != nil {
		return nil, err
	}
	tokens := make([]MappingScanToken, len(records))
	for i, record := range records {
		tokens[i] = MappingScanToken{
			MappingID:        record.MappingID,
			Generation:       record.Generation,
			DeletionRevision: record.DeletionRevision,
			FailureRevision:  record.FailureRevision,
		}
	}
	return tokens, nil
}

func (h *ServiceHandler) RecordMappingScanFailure(ctx context.Context, orgID uuid.UUID, token MappingScanToken, message string) (MappingScanToken, bool, error) {
	record, updated, err := h.store.RecordMappingScanFailure(ctx, orgID, labelsyncmappingstore.MappingScanRecord{
		MappingID:        token.MappingID,
		Generation:       token.Generation,
		DeletionRevision: token.DeletionRevision,
		FailureRevision:  token.FailureRevision,
	}, message)
	if err != nil {
		return MappingScanToken{}, false, err
	}
	return MappingScanToken{
		MappingID:        record.MappingID,
		Generation:       record.Generation,
		DeletionRevision: record.DeletionRevision,
		FailureRevision:  record.FailureRevision,
	}, updated, nil
}

func (h *ServiceHandler) CompleteMappingScan(ctx context.Context, orgID uuid.UUID, tokens []MappingScanToken) (map[uuid.UUID]bool, error) {
	records := make([]labelsyncmappingstore.MappingScanRecord, len(tokens))
	for i, token := range tokens {
		records[i] = labelsyncmappingstore.MappingScanRecord{
			MappingID:        token.MappingID,
			Generation:       token.Generation,
			DeletionRevision: token.DeletionRevision,
			FailureRevision:  token.FailureRevision,
		}
	}
	return h.store.CompleteMappingScan(ctx, orgID, records)
}

var _ Service = (*ServiceHandler)(nil)
