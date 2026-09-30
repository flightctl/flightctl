package labelsyncmapping

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

const maxReconciliationAttempts = 5

// ReconciliationDeviceStore exposes only the labels persistence operations
// needed by the service-led reconciliation flow.
type ReconciliationDeviceStore interface {
	GetLabelSnapshot(context.Context, uuid.UUID, string) (domain.DeviceLabelSnapshot, error)
	ApplyLabels(context.Context, uuid.UUID, string, domain.DeviceLabelSnapshot, map[string]domain.DesiredDeviceLabel) (domain.DeviceLabelApplyResult, error)
}

func (h *ServiceHandler) ReconcileDeviceLabels(ctx context.Context, orgID uuid.UUID, deviceName string) (ReconciliationResult, error) {
	if store.InTransaction(ctx) {
		return ReconciliationResult{}, errors.New("label-sync reconciliation cannot run inside an existing store transaction")
	}

	var result ReconciliationResult

	for attempt := 0; attempt < maxReconciliationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return ReconciliationResult{}, err
		}

		mappings, err := h.store.GetDeviceMappingsSnapshot(ctx, orgID)
		if err != nil {
			return ReconciliationResult{}, err
		}
		if len(mappings.Mappings) == 0 {
			return ReconciliationResult{}, nil
		}
		deviceSnapshot, err := h.deviceStore.GetLabelSnapshot(ctx, orgID, deviceName)
		if err != nil {
			if !errors.Is(err, flterrors.ErrResourceNotFound) {
				return ReconciliationResult{MappingOutcomes: failedDeviceOutcomes(mappings.Mappings, nil, err)}, err
			}
			return ReconciliationResult{}, err
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
		desired, outcomes, err := desiredDeviceLabels(snapshot, h.evaluator)
		if err != nil {
			return ReconciliationResult{MappingOutcomes: failedDeviceOutcomes(snapshot.Mappings, nil, err)}, err
		}

		applied, err := h.deviceStore.ApplyLabels(ctx, orgID, deviceName, deviceSnapshot, desired)
		if err != nil {
			retry, retryErr := h.shouldRetryApply(ctx, orgID, mappings.Revision, err)
			if retry {
				if attempt+1 == maxReconciliationAttempts {
					return ReconciliationResult{MappingOutcomes: failedDeviceOutcomes(snapshot.Mappings, outcomes, retryErr)}, retryErr
				}
				continue
			}
			return ReconciliationResult{MappingOutcomes: failedDeviceOutcomes(snapshot.Mappings, outcomes, retryErr)}, retryErr
		}
		result.LabelsChanged = result.LabelsChanged || applied.LabelsChanged
		result.ManagedLabelsChanged = result.ManagedLabelsChanged || applied.ManagedLabelsChanged
		result.OwnershipChanged = result.OwnershipChanged || applied.OwnershipChanged
		if applied.LabelsChanged || applied.ManagedLabelsChanged {
			updates := &domain.ResourceUpdatedDetails{
				UpdatedFields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.Labels},
			}
			event := common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, domain.DeviceKind, deviceName, updates, h.log, nil)
			if event != nil {
				h.events.CreateEvent(ctx, orgID, event)
			}
		}

		latestMappings, err := h.store.GetDeviceMappingsSnapshot(ctx, orgID)
		if err != nil {
			return ReconciliationResult{MappingOutcomes: failedDeviceOutcomes(snapshot.Mappings, outcomes, err)}, err
		}
		if latestMappings.Revision != mappings.Revision {
			if attempt+1 == maxReconciliationAttempts {
				return ReconciliationResult{
					MappingOutcomes: failedDeviceOutcomes(snapshot.Mappings, outcomes, flterrors.ErrResourceVersionConflict),
				}, flterrors.ErrResourceVersionConflict
			}
			continue
		}

		result.MappingOutcomes = outcomes
		return result, nil
	}
	return ReconciliationResult{}, flterrors.ErrResourceVersionConflict
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

func (h *ServiceHandler) RecordDeviceLabelReconciliationFailures(ctx context.Context, orgID uuid.UUID, outcomes []MappingOutcome) error {
	var recordErrors []error
	for _, outcome := range outcomes {
		if outcome.Err == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			recordErrors = append(recordErrors, err)
			break
		}
		_, err := h.store.RecordReconciliationFailure(ctx, orgID, labelsyncmappingstore.ReconciliationFailure{
			MappingID:       outcome.MappingID,
			Generation:      outcome.Generation,
			ResourceVersion: outcome.ResourceVersion,
			Message:         outcome.Err.Error(),
		})
		if err != nil {
			recordErrors = append(recordErrors, fmt.Errorf("record reconciliation failure for mapping %s: %w", outcome.MappingID, err))
		}
	}
	return errors.Join(recordErrors...)
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
	LabelsChanged        bool
	ManagedLabelsChanged bool
	OwnershipChanged     bool
	MappingOutcomes      []MappingOutcome
}

var _ Service = (*ServiceHandler)(nil)
