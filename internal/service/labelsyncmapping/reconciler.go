package labelsyncmapping

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
)

const maxReconciliationAttempts = 5

// ReconciliationDeviceStore exposes the device-label operations used by the
// service-led reconciliation and provenance flows.
type ReconciliationDeviceStore interface {
	GetLabelSnapshot(context.Context, uuid.UUID, string) (domain.DeviceLabelSnapshot, error)
	GetLabelSyncMappingIDsByKeys(context.Context, uuid.UUID, []string) (map[string][]uuid.UUID, error)
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
		deviceSnapshot, err := h.deviceStore.GetLabelSnapshot(ctx, orgID, deviceName)
		if err != nil {
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
			return ReconciliationResult{}, err
		}

		applied, err := h.deviceStore.ApplyLabels(ctx, orgID, deviceName, deviceSnapshot, desired)
		if err != nil {
			retry, retryErr := h.shouldRetryApply(ctx, orgID, mappings.Revision, err)
			if retry {
				if attempt+1 == maxReconciliationAttempts {
					return ReconciliationResult{}, retryErr
				}
				continue
			}
			return ReconciliationResult{}, retryErr
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
			return ReconciliationResult{}, err
		}
		if latestMappings.Revision != mappings.Revision {
			continue
		}

		result.MappingOutcomes = outcomes
		return result, nil
	}
	return ReconciliationResult{}, flterrors.ErrResourceVersionConflict
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
	MappingID uuid.UUID
	Err       error
}

type ReconciliationResult struct {
	LabelsChanged        bool
	ManagedLabelsChanged bool
	OwnershipChanged     bool
	MappingOutcomes      []MappingOutcome
}

var _ Service = (*ServiceHandler)(nil)
