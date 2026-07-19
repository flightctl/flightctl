package fleet

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func prepareTestFleetForEvents(name string) *domain.Fleet {
	return &domain.Fleet{
		ApiVersion: "v1beta1",
		Kind:       "Fleet",
		Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name)},
		Spec: domain.FleetSpec{
			Selector: &domain.LabelSelector{MatchLabels: &map[string]string{"k": "v"}},
			Template: struct {
				Metadata *domain.ObjectMeta `json:"metadata,omitempty"`
				Spec     domain.DeviceSpec  `json:"spec"`
			}{Spec: domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}}},
		},
	}
}

func TestEmitFleetRolloutStartedEvent(t *testing.T) {
	ev := &fakeEventsService{}
	EmitFleetRolloutStartedEvent(context.Background(), ev, uuid.New(), "tv1", "fleet1", false)
	require.Len(t, ev.created, 1)
}

func TestEmitFleetUpdatedEvent(t *testing.T) {
	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		ev := &fakeEventsService{}
		fleet := prepareTestFleetForEvents("f1")
		EmitFleetUpdatedEvent(context.Background(), ev, logrus.New(), domain.FleetKind, uuid.New(), "f1", nil, fleet, true, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})

	t.Run("When the fleet-valid condition transitions to true it should emit a FleetValid event", func(t *testing.T) {
		ev := &fakeEventsService{}
		name := "f1"
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusFalse, Reason: string(domain.EventReasonFleetInvalid)},
			}},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusTrue, Reason: string(domain.EventReasonFleetValid)},
			}},
		}
		emitFleetValidEvents(context.Background(), ev, uuid.New(), name, oldFleet, newFleet)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonFleetValid, ev.created[0].Reason)
	})

	t.Run("When the fleet-valid condition transitions to false it should emit a FleetInvalid event", func(t *testing.T) {
		ev := &fakeEventsService{}
		name := "f1"
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusTrue, Reason: string(domain.EventReasonFleetValid)},
			}},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusFalse, Reason: string(domain.EventReasonFleetInvalid), Message: "bad config"},
			}},
		}
		emitFleetValidEvents(context.Background(), ev, uuid.New(), name, oldFleet, newFleet)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonFleetInvalid, ev.created[0].Reason)
	})

	t.Run("When the fleet-valid condition is unchanged it should not emit an event", func(t *testing.T) {
		ev := &fakeEventsService{}
		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("f1")},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusTrue, Reason: string(domain.EventReasonFleetValid)},
			}},
		}
		emitFleetValidEvents(context.Background(), ev, uuid.New(), "f1", fleet, fleet)
		require.Empty(t, ev.created)
	})

	t.Run("When the new fleet has no status it should not emit an event", func(t *testing.T) {
		ev := &fakeEventsService{}
		fleet := &domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr("f1")}}
		emitFleetValidEvents(context.Background(), ev, uuid.New(), "f1", fleet, fleet)
		require.Empty(t, ev.created)
	})

	t.Run("When a new rollout annotation appears it should emit a FleetRolloutNew event", func(t *testing.T) {
		ev := &fakeEventsService{}
		name := "f1"
		oldFleet := &domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr(name),
				Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"},
			},
		}
		EmitFleetUpdatedEvent(context.Background(), ev, logrus.New(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range ev.created {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutCreated)
	})

	t.Run("When a rollout batch completes it should emit a FleetRolloutBatchCompleted event", func(t *testing.T) {
		ev := &fakeEventsService{}
		name := "f1"
		report := `{"batchName":"batch-1","successPercentage":100,"total":1,"successful":1}`
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr(name),
				Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"},
			},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr(name),
				Annotations: &map[string]string{
					domain.FleetAnnotationDeployingTemplateVersion:  "v2",
					domain.FleetAnnotationLastBatchCompletionReport: report,
				},
			},
		}
		EmitFleetUpdatedEvent(context.Background(), ev, logrus.New(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range ev.created {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutBatchCompleted)
	})

	t.Run("When the final rollout batch completes it should also emit a FleetRolloutCompleted event", func(t *testing.T) {
		ev := &fakeEventsService{}
		name := "f1"
		report := `{"batchName":"` + domain.FinalImplicitBatchName + `","successPercentage":100,"total":1,"successful":1}`
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr(name),
				Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"},
			},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr(name),
				Annotations: &map[string]string{
					domain.FleetAnnotationDeployingTemplateVersion:  "v2",
					domain.FleetAnnotationLastBatchCompletionReport: report,
				},
			},
		}
		EmitFleetUpdatedEvent(context.Background(), ev, logrus.New(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range ev.created {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutBatchCompleted)
		require.Contains(t, reasons, domain.EventReasonFleetRolloutCompleted)
	})

	t.Run("When the rollout-in-progress condition becomes inactive it should emit a FleetRolloutCompleted event", func(t *testing.T) {
		ev := &fakeEventsService{}
		name := "f1"
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"}},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetRolloutInProgress, Status: domain.ConditionStatusTrue, Reason: "Active"},
			}},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"}},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetRolloutInProgress, Status: domain.ConditionStatusFalse, Reason: domain.RolloutInactiveReason},
			}},
		}
		EmitFleetUpdatedEvent(context.Background(), ev, logrus.New(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range ev.created {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutCompleted)
	})

	t.Run("When the rollout-in-progress condition becomes suspended it should emit a FleetRolloutFailed event", func(t *testing.T) {
		ev := &fakeEventsService{}
		name := "f1"
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"}},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetRolloutInProgress, Status: domain.ConditionStatusTrue, Reason: "Active"},
			}},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"}},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetRolloutInProgress, Status: domain.ConditionStatusFalse, Reason: domain.RolloutSuspendedReason, Message: "paused"},
			}},
		}
		EmitFleetUpdatedEvent(context.Background(), ev, logrus.New(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range ev.created {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutFailed)
	})
}
