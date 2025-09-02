package tasks

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func createTestEventWithDetails(kind api.ResourceKind, reason api.EventReason, name string, details *api.EventDetails) api.Event {
	event := createTestEvent(kind, reason, name)
	event.Details = details
	return event
}

func createTestEventWithAnnotations(kind api.ResourceKind, reason api.EventReason, name string, annotations map[string]string) api.Event {
	event := createTestEvent(kind, reason, name)
	event.Metadata = api.ObjectMeta{
		Annotations: &annotations,
	}
	return event
}

func createResourceUpdatedDetails(t *testing.T, updatedFields ...api.ResourceUpdatedDetailsUpdatedFields) *api.EventDetails {
	details := api.EventDetails{}
	err := details.FromResourceUpdatedDetails(api.ResourceUpdatedDetails{
		UpdatedFields: updatedFields,
	})
	if err != nil {
		t.Fatalf("failed to create resource updated details: %v", err)
	}
	return &details
}

func createFleetRolloutStartedDetails(t *testing.T, strategy api.FleetRolloutStartedDetailsRolloutStrategy) *api.EventDetails {
	details := api.EventDetails{}
	err := details.FromFleetRolloutStartedDetails(api.FleetRolloutStartedDetails{
		RolloutStrategy: strategy,
	})
	if err != nil {
		t.Fatalf("failed to create fleet rollout started details: %v", err)
	}
	return &details
}

func TestShouldRolloutFleet(t *testing.T) {
	tests := []struct {
		name     string
		event    api.Event
		expected bool
	}{
		{
			name:     "DeviceUpdatedWithOwnerAndLabels",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Owner, api.Labels)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithOwnerOnly",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Owner)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithLabelsOnly",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Labels)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithOtherFields",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Spec)),
			expected: false,
		},
		{
			name:     "DeviceUpdatedNoDetails",
			event:    createTestEvent(api.DeviceKind, api.EventReasonResourceUpdated, "device1"),
			expected: false,
		},
		{
			name:     "FleetRolloutBatchDispatched",
			event:    createTestEvent(api.FleetKind, api.EventReasonFleetRolloutBatchDispatched, "fleet1"),
			expected: true,
		},
		{
			name:     "DeviceCreated",
			event:    createTestEvent(api.DeviceKind, api.EventReasonResourceCreated, "device1"),
			expected: true,
		},
		{
			name:     "FleetRolloutStartedImmediate",
			event:    createTestEventWithDetails(api.FleetKind, api.EventReasonFleetRolloutStarted, "fleet1", createFleetRolloutStartedDetails(t, api.None)),
			expected: true,
		},
		{
			name:     "FleetRolloutStartedNotImmediate",
			event:    createTestEventWithDetails(api.FleetKind, api.EventReasonFleetRolloutStarted, "fleet1", createFleetRolloutStartedDetails(t, "Batched")),
			expected: false,
		},
		{
			name:     "FleetRolloutStartedNoDetails",
			event:    createTestEvent(api.FleetKind, api.EventReasonFleetRolloutStarted, "fleet1"),
			expected: false,
		},
		{
			name:     "FleetUpdated",
			event:    createTestEvent(api.FleetKind, api.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "RepositoryUpdated",
			event:    createTestEvent(api.RepositoryKind, api.EventReasonResourceUpdated, "repo1"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logrus.New()
			result := shouldRolloutFleet(context.Background(), tt.event, log)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldReconcileDeviceOwnership(t *testing.T) {
	tests := []struct {
		name     string
		event    api.Event
		expected bool
	}{
		{
			name:     "FleetUpdatedWithSelector",
			event:    createTestEventWithDetails(api.FleetKind, api.EventReasonResourceUpdated, "fleet1", createResourceUpdatedDetails(t, api.SpecSelector)),
			expected: true,
		},
		{
			name:     "FleetUpdatedWithOtherFields",
			event:    createTestEventWithDetails(api.FleetKind, api.EventReasonResourceUpdated, "fleet1", createResourceUpdatedDetails(t, api.SpecTemplate)),
			expected: false,
		},
		{
			name:     "FleetUpdatedNoDetails",
			event:    createTestEvent(api.FleetKind, api.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "FleetCreated",
			event:    createTestEvent(api.FleetKind, api.EventReasonResourceCreated, "fleet1"),
			expected: true,
		},
		{
			name:     "FleetDeleted",
			event:    createTestEvent(api.FleetKind, api.EventReasonResourceDeleted, "fleet1"),
			expected: true,
		},
		{
			name:     "DeviceCreated",
			event:    createTestEvent(api.DeviceKind, api.EventReasonResourceCreated, "device1"),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithLabels",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Labels)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithOtherFields",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Spec)),
			expected: false,
		},
		{
			name:     "RepositoryUpdated",
			event:    createTestEvent(api.RepositoryKind, api.EventReasonResourceUpdated, "repo1"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logrus.New()
			result := shouldReconcileDeviceOwnership(context.Background(), tt.event, log)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldValidateFleet(t *testing.T) {
	tests := []struct {
		name     string
		event    api.Event
		expected bool
	}{
		{
			name:     "FleetUpdatedWithTemplate",
			event:    createTestEventWithDetails(api.FleetKind, api.EventReasonResourceUpdated, "fleet1", createResourceUpdatedDetails(t, api.SpecTemplate)),
			expected: true,
		},
		{
			name:     "FleetUpdatedWithOtherFields",
			event:    createTestEventWithDetails(api.FleetKind, api.EventReasonResourceUpdated, "fleet1", createResourceUpdatedDetails(t, api.SpecSelector)),
			expected: false,
		},
		{
			name:     "FleetUpdatedNoDetails",
			event:    createTestEvent(api.FleetKind, api.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "FleetCreated",
			event:    createTestEvent(api.FleetKind, api.EventReasonResourceCreated, "fleet1"),
			expected: true,
		},
		{
			name:     "ReferencedRepositoryUpdated",
			event:    createTestEvent(api.FleetKind, api.EventReasonReferencedRepositoryUpdated, "fleet1"),
			expected: true,
		},
		{
			name:     "DeviceUpdated",
			event:    createTestEvent(api.DeviceKind, api.EventReasonResourceUpdated, "device1"),
			expected: false,
		},
		{
			name:     "RepositoryUpdated",
			event:    createTestEvent(api.RepositoryKind, api.EventReasonResourceUpdated, "repo1"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logrus.New()
			result := shouldValidateFleet(context.Background(), tt.event, log)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldRenderDevice(t *testing.T) {
	tests := []struct {
		name     string
		event    api.Event
		expected bool
	}{
		{
			name:     "ReferencedRepositoryUpdated",
			event:    createTestEvent(api.DeviceKind, api.EventReasonReferencedRepositoryUpdated, "device1"),
			expected: true,
		},
		{
			name:     "DeviceCreated",
			event:    createTestEvent(api.DeviceKind, api.EventReasonResourceUpdated, "device1"),
			expected: false,
		},
		{
			name:     "FleetRolloutDeviceSelected",
			event:    createTestEvent(api.DeviceKind, api.EventReasonFleetRolloutDeviceSelected, "device1"),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithSpecNoDelayAnnotation",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Spec)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithSpecDelayAnnotationTrue",
			event:    createTestEventWithAnnotations(api.DeviceKind, api.EventReasonResourceUpdated, "device1", map[string]string{api.EventAnnotationDelayDeviceRender: "true"}),
			expected: false,
		},
		{
			name:     "DeviceUpdatedWithSpecDelayAnnotationFalse",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Spec)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithSpecDelayAnnotationOther",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Spec)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithSpecNoAnnotations",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Spec)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithoutSpec",
			event:    createTestEventWithDetails(api.DeviceKind, api.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, api.Labels)),
			expected: false,
		},
		{
			name:     "FleetEvent",
			event:    createTestEvent(api.FleetKind, api.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "RepositoryEvent",
			event:    createTestEvent(api.RepositoryKind, api.EventReasonResourceUpdated, "repo1"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logrus.New()
			result := shouldRenderDevice(context.Background(), tt.event, log)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldUpdateRepositoryReferers(t *testing.T) {
	tests := []struct {
		name     string
		event    api.Event
		expected bool
	}{
		{
			name:     "RepositoryUpdatedWithSpec",
			event:    createTestEventWithDetails(api.RepositoryKind, api.EventReasonResourceUpdated, "repo1", createResourceUpdatedDetails(t, api.Spec)),
			expected: true,
		},
		{
			name:     "RepositoryUpdatedWithOtherFields",
			event:    createTestEventWithDetails(api.RepositoryKind, api.EventReasonResourceUpdated, "repo1", createResourceUpdatedDetails(t, api.Labels)),
			expected: false,
		},
		{
			name:     "RepositoryUpdatedNoDetails",
			event:    createTestEvent(api.RepositoryKind, api.EventReasonResourceUpdated, "repo1"),
			expected: false,
		},
		{
			name:     "RepositoryCreated",
			event:    createTestEvent(api.RepositoryKind, api.EventReasonResourceCreated, "repo1"),
			expected: true,
		},
		{
			name:     "FleetUpdated",
			event:    createTestEvent(api.FleetKind, api.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "DeviceUpdated",
			event:    createTestEvent(api.DeviceKind, api.EventReasonResourceUpdated, "device1"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logrus.New()
			result := shouldUpdateRepositoryReferers(context.Background(), tt.event, log)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHasUpdatedFields(t *testing.T) {
	tests := []struct {
		name        string
		details     *api.EventDetails
		fields      []api.ResourceUpdatedDetailsUpdatedFields
		expected    bool
		description string
	}{
		{
			name:        "HasMatchingField",
			details:     createResourceUpdatedDetails(t, api.Spec, api.Labels),
			fields:      []api.ResourceUpdatedDetailsUpdatedFields{api.Labels},
			expected:    true,
			description: "Should return true when details contain a matching field",
		},
		{
			name:        "HasMultipleMatchingFields",
			details:     createResourceUpdatedDetails(t, api.Spec, api.Labels, api.Owner),
			fields:      []api.ResourceUpdatedDetailsUpdatedFields{api.Labels, api.Owner},
			expected:    true,
			description: "Should return true when details contain multiple matching fields",
		},
		{
			name:        "NoMatchingFields",
			details:     createResourceUpdatedDetails(t, api.Spec, api.Labels),
			fields:      []api.ResourceUpdatedDetailsUpdatedFields{api.Owner},
			expected:    false,
			description: "Should return false when details contain no matching fields",
		},
		{
			name:        "NilDetails",
			details:     nil,
			fields:      []api.ResourceUpdatedDetailsUpdatedFields{api.Labels},
			expected:    false,
			description: "Should return false when details is nil",
		},
		{
			name:        "EmptyFields",
			details:     createResourceUpdatedDetails(t, api.Spec),
			fields:      []api.ResourceUpdatedDetailsUpdatedFields{},
			expected:    false,
			description: "Should return false when no fields are specified",
		},
		{
			name:        "EmptyUpdatedFields",
			details:     createResourceUpdatedDetails(t),
			fields:      []api.ResourceUpdatedDetailsUpdatedFields{api.Spec},
			expected:    false,
			description: "Should return false when details have no updated fields",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logrus.New()
			result := hasUpdatedFields(tt.details, log, tt.fields...)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}
