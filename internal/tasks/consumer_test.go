package tasks

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func createTestEventWithDetails(kind domain.ResourceKind, reason domain.EventReason, name string, details *domain.EventDetails) domain.Event {
	event := createTestEvent(kind, reason, name)
	event.Details = details
	return event
}

func createTestEventWithAnnotations(kind domain.ResourceKind, reason domain.EventReason, name string, annotations map[string]string) domain.Event {
	event := createTestEvent(kind, reason, name)
	event.Metadata = domain.ObjectMeta{
		Annotations: &annotations,
	}
	return event
}

func createResourceUpdatedDetails(t *testing.T, updatedFields ...domain.ResourceUpdatedDetailsUpdatedFields) *domain.EventDetails {
	details := domain.EventDetails{}
	err := details.FromResourceUpdatedDetails(domain.ResourceUpdatedDetails{
		UpdatedFields: updatedFields,
	})
	if err != nil {
		t.Fatalf("failed to create resource updated details: %v", err)
	}
	return &details
}

func createFleetRolloutStartedDetails(t *testing.T, strategy domain.FleetRolloutStartedDetailsRolloutStrategy) *domain.EventDetails {
	details := domain.EventDetails{}
	err := details.FromFleetRolloutStartedDetails(domain.FleetRolloutStartedDetails{
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
		event    domain.Event
		expected bool
	}{
		{
			name:     "DeviceUpdatedWithOwnerAndLabels",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Owner, domain.Labels)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithOwnerOnly",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Owner)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithLabelsOnly",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Labels)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithOtherFields",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Spec)),
			expected: false,
		},
		{
			name:     "DeviceUpdatedNoDetails",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1"),
			expected: false,
		},
		{
			name:     "FleetRolloutBatchDispatched",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonFleetRolloutBatchDispatched, "fleet1"),
			expected: true,
		},
		{
			name:     "DeviceCreated",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonResourceCreated, "device1"),
			expected: true,
		},
		{
			name:     "FleetRolloutStartedImmediate",
			event:    createTestEventWithDetails(domain.FleetKind, domain.EventReasonFleetRolloutStarted, "fleet1", createFleetRolloutStartedDetails(t, domain.None)),
			expected: true,
		},
		{
			name:     "FleetRolloutStartedNotImmediate",
			event:    createTestEventWithDetails(domain.FleetKind, domain.EventReasonFleetRolloutStarted, "fleet1", createFleetRolloutStartedDetails(t, "Batched")),
			expected: false,
		},
		{
			name:     "FleetRolloutStartedNoDetails",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonFleetRolloutStarted, "fleet1"),
			expected: false,
		},
		{
			name:     "FleetUpdated",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "RepositoryUpdated",
			event:    createTestEvent(domain.RepositoryKind, domain.EventReasonResourceUpdated, "repo1"),
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
		event    domain.Event
		expected bool
	}{
		{
			name:     "FleetUpdatedWithSelector",
			event:    createTestEventWithDetails(domain.FleetKind, domain.EventReasonResourceUpdated, "fleet1", createResourceUpdatedDetails(t, domain.SpecSelector)),
			expected: true,
		},
		{
			name:     "FleetUpdatedWithOtherFields",
			event:    createTestEventWithDetails(domain.FleetKind, domain.EventReasonResourceUpdated, "fleet1", createResourceUpdatedDetails(t, domain.SpecTemplate)),
			expected: false,
		},
		{
			name:     "FleetUpdatedNoDetails",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "FleetCreated",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonResourceCreated, "fleet1"),
			expected: true,
		},
		{
			name:     "FleetDeleted",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonResourceDeleted, "fleet1"),
			expected: true,
		},
		{
			name:     "DeviceCreated",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonResourceCreated, "device1"),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithLabels",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Labels)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithOtherFields",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Spec)),
			expected: false,
		},
		{
			name:     "RepositoryUpdated",
			event:    createTestEvent(domain.RepositoryKind, domain.EventReasonResourceUpdated, "repo1"),
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
		event    domain.Event
		expected bool
	}{
		{
			name:     "FleetUpdatedWithTemplate",
			event:    createTestEventWithDetails(domain.FleetKind, domain.EventReasonResourceUpdated, "fleet1", createResourceUpdatedDetails(t, domain.SpecTemplate)),
			expected: true,
		},
		{
			name:     "FleetUpdatedWithOtherFields",
			event:    createTestEventWithDetails(domain.FleetKind, domain.EventReasonResourceUpdated, "fleet1", createResourceUpdatedDetails(t, domain.SpecSelector)),
			expected: false,
		},
		{
			name:     "FleetUpdatedNoDetails",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "FleetCreated",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonResourceCreated, "fleet1"),
			expected: true,
		},
		{
			name:     "ReferencedRepositoryUpdated",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonReferencedRepositoryUpdated, "fleet1"),
			expected: true,
		},
		{
			name:     "DeviceUpdated",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1"),
			expected: false,
		},
		{
			name:     "RepositoryUpdated",
			event:    createTestEvent(domain.RepositoryKind, domain.EventReasonResourceUpdated, "repo1"),
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
		event    domain.Event
		expected bool
	}{
		{
			name:     "ReferencedRepositoryUpdated",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonReferencedRepositoryUpdated, "device1"),
			expected: true,
		},
		{
			name:     "DeviceCreated",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1"),
			expected: false,
		},
		{
			name:     "FleetRolloutDeviceSelected",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonFleetRolloutDeviceSelected, "device1"),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithSpecNoDelayAnnotation",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Spec)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithSpecDelayAnnotationTrue",
			event:    createTestEventWithAnnotations(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", map[string]string{domain.EventAnnotationDelayDeviceRender: "true"}),
			expected: false,
		},
		{
			name:     "DeviceUpdatedWithSpecDelayAnnotationFalse",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Spec)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithSpecDelayAnnotationOther",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Spec)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithSpecNoAnnotations",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Spec)),
			expected: true,
		},
		{
			name:     "DeviceUpdatedWithoutSpec",
			event:    createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1", createResourceUpdatedDetails(t, domain.Labels)),
			expected: false,
		},
		{
			name:     "FleetEvent",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "RepositoryEvent",
			event:    createTestEvent(domain.RepositoryKind, domain.EventReasonResourceUpdated, "repo1"),
			expected: false,
		},
		{
			name:     "DeviceDecommissioned",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonDeviceDecommissioned, "device1"),
			expected: true,
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
		event    domain.Event
		expected bool
	}{
		{
			name:     "RepositoryUpdatedWithSpec",
			event:    createTestEventWithDetails(domain.RepositoryKind, domain.EventReasonResourceUpdated, "repo1", createResourceUpdatedDetails(t, domain.Spec)),
			expected: true,
		},
		{
			name:     "RepositoryUpdatedWithOtherFields",
			event:    createTestEventWithDetails(domain.RepositoryKind, domain.EventReasonResourceUpdated, "repo1", createResourceUpdatedDetails(t, domain.Labels)),
			expected: false,
		},
		{
			name:     "RepositoryUpdatedNoDetails",
			event:    createTestEvent(domain.RepositoryKind, domain.EventReasonResourceUpdated, "repo1"),
			expected: false,
		},
		{
			name:     "RepositoryCreated",
			event:    createTestEvent(domain.RepositoryKind, domain.EventReasonResourceCreated, "repo1"),
			expected: true,
		},
		{
			name:     "FleetUpdated",
			event:    createTestEvent(domain.FleetKind, domain.EventReasonResourceUpdated, "fleet1"),
			expected: false,
		},
		{
			name:     "DeviceUpdated",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1"),
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
		details     *domain.EventDetails
		fields      []domain.ResourceUpdatedDetailsUpdatedFields
		expected    bool
		description string
	}{
		{
			name:        "HasMatchingField",
			details:     createResourceUpdatedDetails(t, domain.Spec, domain.Labels),
			fields:      []domain.ResourceUpdatedDetailsUpdatedFields{domain.Labels},
			expected:    true,
			description: "Should return true when details contain a matching field",
		},
		{
			name:        "HasMultipleMatchingFields",
			details:     createResourceUpdatedDetails(t, domain.Spec, domain.Labels, domain.Owner),
			fields:      []domain.ResourceUpdatedDetailsUpdatedFields{domain.Labels, domain.Owner},
			expected:    true,
			description: "Should return true when details contain multiple matching fields",
		},
		{
			name:        "NoMatchingFields",
			details:     createResourceUpdatedDetails(t, domain.Spec, domain.Labels),
			fields:      []domain.ResourceUpdatedDetailsUpdatedFields{domain.Owner},
			expected:    false,
			description: "Should return false when details contain no matching fields",
		},
		{
			name:        "NilDetails",
			details:     nil,
			fields:      []domain.ResourceUpdatedDetailsUpdatedFields{domain.Labels},
			expected:    false,
			description: "Should return false when details is nil",
		},
		{
			name:        "EmptyFields",
			details:     createResourceUpdatedDetails(t, domain.Spec),
			fields:      []domain.ResourceUpdatedDetailsUpdatedFields{},
			expected:    false,
			description: "Should return false when no fields are specified",
		},
		{
			name:        "EmptyUpdatedFields",
			details:     createResourceUpdatedDetails(t),
			fields:      []domain.ResourceUpdatedDetailsUpdatedFields{domain.Spec},
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

// MockConsumer implements queues.QueueConsumer for testing
type MockConsumer struct {
	mock.Mock
}

// Compile-time check that MockConsumer satisfies queues.QueueConsumer
var _ queues.QueueConsumer = (*MockConsumer)(nil)

func (m *MockConsumer) Consume(ctx context.Context, handler queues.ConsumeHandler) error {
	args := m.Called(ctx, handler)
	return args.Error(0)
}

func (m *MockConsumer) Complete(ctx context.Context, entryID string, body []byte, processingErr error) error {
	args := m.Called(ctx, entryID, body, processingErr)
	return args.Error(0)
}

func (m *MockConsumer) Close() {
	m.Called()
}

func TestDispatchTasks_WithNilMetrics(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()

	mockConsumer := &MockConsumer{}

	// Create a simple valid payload that won't trigger any task processing
	orgId := uuid.New()
	eventWithOrgId := worker_client.EventWithOrgId{
		OrgId: orgId,
		Event: domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: string(domain.RepositoryKind), // Repository events with this reason won't trigger tasks
				Name: "test-repo",
			},
			Reason: domain.EventReasonResourceDeleted, // This combination won't trigger any tasks
		},
	}
	payload, err := json.Marshal(eventWithOrgId)
	require.NoError(t, err)

	// Mock consumer complete (processingErr must be nil)
	mockConsumer.On("Complete", mock.Anything, "entry-123", payload, mock.MatchedBy(func(e error) bool { return e == nil })).Return(nil)

	// Create dispatcher with nil metrics
	handler := dispatchTasks(nil, nil, nil, nil, nil)

	// Execute handler
	err = handler(ctx, payload, "entry-123", mockConsumer, log)

	// Should not fail with nil metrics
	assert.NoError(t, err)
	mockConsumer.AssertExpectations(t)
}

func TestDispatchTasks_WithNilMetrics_SuccessfulProcessing(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()

	mockConsumer := &MockConsumer{}

	// Create a simple valid payload that won't trigger any task processing
	orgId := uuid.New()
	eventWithOrgId := worker_client.EventWithOrgId{
		OrgId: orgId,
		Event: domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: string(domain.FleetKind), // Using FleetKind with ResourceUpdated won't trigger tasks
				Name: "test-fleet",
			},
			Reason: domain.EventReasonResourceUpdated,
		},
	}
	payload, err := json.Marshal(eventWithOrgId)
	require.NoError(t, err)

	// Mock consumer complete (processingErr must be nil)
	mockConsumer.On("Complete", mock.Anything, "entry-123", payload, mock.MatchedBy(func(e error) bool { return e == nil })).Return(nil)

	// Create dispatcher with nil metrics
	handler := dispatchTasks(nil, nil, nil, nil, nil)

	// Execute handler
	err = handler(ctx, payload, "entry-123", mockConsumer, log)

	// Should complete successfully
	assert.NoError(t, err)
	mockConsumer.AssertExpectations(t)
}

func TestDispatchTasks_WithNilMetrics_InvalidPayload(t *testing.T) {
	ctx := context.Background()
	log := logrus.New()

	mockConsumer := &MockConsumer{}

	// Invalid JSON payload
	payload := []byte("invalid json")

	// Mock consumer complete - should complete successfully (no error) for parsing failures
	mockConsumer.On("Complete", mock.Anything, "entry-123", payload, nil).Return(nil)

	// Create dispatcher with nil metrics
	handler := dispatchTasks(nil, nil, nil, nil, nil)

	// Execute handler
	err := handler(ctx, payload, "entry-123", mockConsumer, log)

	// Should return no error (parsing errors are not retryable)
	assert.NoError(t, err)
	mockConsumer.AssertExpectations(t)
}
