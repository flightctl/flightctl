package labelsyncmapping

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	eventservice "github.com/flightctl/flightctl/internal/service/events"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestReconciliationAppliesConditionAtomically(t *testing.T) {
	for _, tc := range []struct {
		name        string
		evaluateErr error
		applyErr    error
		markerErr   error
		wantFalse   bool
		wantInfra   bool
	}{
		{name: "When labels are unchanged it should apply the success condition"},
		{name: "When a mapping fails it should apply False together with the other labels", evaluateErr: errors.New("invalid output"), wantFalse: true},
		{name: "When atomic application fails it should persist False while preserving labels", applyErr: errors.New("apply failed"), wantFalse: true},
		{name: "When conflict retries are exhausted it should persist False", applyErr: flterrors.ErrResourceVersionConflict, wantFalse: true},
		{name: "When a device call times out with a live caller context it should persist False", applyErr: context.DeadlineExceeded, wantFalse: true},
		{name: "When the failure condition cannot be persisted it should retain scan work", applyErr: errors.New("apply failed"), markerErr: errors.New("write failed"), wantFalse: true, wantInfra: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			orgID, mappingID := uuid.New(), uuid.New()
			mapping := testDeviceMapping(mappingID, "architecture", "mapped", lo.ToPtr("architecture"))
			mappings := labelsyncmappingstore.NewMockStore(ctrl)
			mappings.EXPECT().GetDeviceMappingsSnapshot(gomock.Any(), orgID).Return(labelsyncmappingstore.DeviceMappingsSnapshot{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 7}, nil).AnyTimes()
			devices := NewMockReconciliationDeviceStore(ctrl)
			snapshot := deviceLabelSnapshot("edge", "4", map[string]string{"manual": "keep"}, nil)
			devices.EXPECT().GetLabelSnapshot(gomock.Any(), orgID, "edge").Return(snapshot, nil).AnyTimes()
			attempts, markers := 0, 0
			devices.EXPECT().ApplyLabels(gomock.Any(), orgID, "edge", gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, _ uuid.UUID, _ string, _ domain.DeviceLabelSnapshot, desired map[string]domain.DesiredDeviceLabel, condition *domain.Condition) (domain.DeviceLabelApplyResult, error) {
					require.Equal(t, domain.ConditionTypeDeviceLabelsSynced, condition.Type)
					require.Equal(t, domain.DesiredDeviceLabel{Value: "keep"}, desired["manual"])
					if tc.applyErr != nil && len(desired) > 1 {
						attempts++
						require.Equal(t, domain.ConditionStatusTrue, condition.Status)
						return domain.DeviceLabelApplyResult{}, tc.applyErr
					}
					if tc.wantFalse {
						markers++
						require.Equal(t, domain.ConditionStatusFalse, condition.Status)
						require.Equal(t, "ReconciliationFailed", condition.Reason)
					} else {
						require.Equal(t, domain.ConditionStatusTrue, condition.Status)
					}
					return domain.DeviceLabelApplyResult{Device: &snapshot.Device}, tc.markerErr
				}).AnyTimes()
			evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{"mapped": {result: ScalarResult("x86_64"), err: tc.evaluateErr}}}
			handler, err := NewServiceHandler(mappings, devices, evaluator, eventservice.NewMockService(ctrl), nil)
			require.NoError(t, err)
			result, err := handler.ReconcileDeviceLabels(context.Background(), orgID, "edge")
			require.Equal(t, tc.wantInfra, errors.Is(err, ErrConditionPersistence))
			if tc.evaluateErr != nil {
				require.NoError(t, err)
				require.Error(t, result.MappingOutcomes[0].Err)
			}
			if tc.applyErr != nil {
				require.ErrorIs(t, err, tc.applyErr)
				if errors.Is(tc.applyErr, flterrors.ErrResourceVersionConflict) {
					require.Equal(t, maxReconciliationAttempts, attempts)
				} else {
					require.Equal(t, 1, attempts)
				}
			}
			if tc.wantFalse {
				require.Equal(t, 1, markers)
			}
		})
	}
}

func TestReconciliationConfirmsDeletion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deleted bool
	}{
		{"When a device is deleted it should skip the failed apply", true},
		{"When a foreign key fails for an existing device it should persist False", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mappings := labelsyncmappingstore.NewMockStore(ctrl)
			mappings.EXPECT().GetDeviceMappingsSnapshot(gomock.Any(), gomock.Any()).Return(labelsyncmappingstore.DeviceMappingsSnapshot{}, nil).AnyTimes()
			devices := NewMockReconciliationDeviceStore(ctrl)
			snapshot := deviceLabelSnapshot("edge", "1", nil, nil)
			devices.EXPECT().GetLabelSnapshot(gomock.Any(), gomock.Any(), "edge").Return(snapshot, nil)
			devices.EXPECT().ApplyLabels(gomock.Any(), gomock.Any(), "edge", gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.DeviceLabelApplyResult{}, flterrors.ErrResourceNotFound)
			if tc.deleted {
				devices.EXPECT().GetLabelSnapshot(gomock.Any(), gomock.Any(), "edge").Return(domain.DeviceLabelSnapshot{}, flterrors.ErrResourceNotFound)
			} else {
				devices.EXPECT().GetLabelSnapshot(gomock.Any(), gomock.Any(), "edge").Return(snapshot, nil)
				devices.EXPECT().ApplyLabels(gomock.Any(), gomock.Any(), "edge", gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.DeviceLabelSnapshot, _ map[string]domain.DesiredDeviceLabel, condition *domain.Condition) (domain.DeviceLabelApplyResult, error) {
					require.Equal(t, domain.ConditionStatusFalse, condition.Status)
					return domain.DeviceLabelApplyResult{Device: &snapshot.Device}, nil
				})
			}
			handler, err := NewServiceHandler(mappings, devices, handlerTestEvaluator{}, eventservice.NewMockService(ctrl), nil)
			require.NoError(t, err)
			result, err := handler.ReconcileDeviceLabels(context.Background(), uuid.New(), "edge")
			require.Equal(t, tc.deleted, result.DeviceDeleted)
			if tc.deleted {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, flterrors.ErrResourceNotFound)
			}
		})
	}
}
