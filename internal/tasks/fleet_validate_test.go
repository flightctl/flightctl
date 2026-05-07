package tasks

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestFleetValidateLogic_CreateNewTemplateVersionIfFleetValid_ImmediateRollout(t *testing.T) {
	tests := []struct {
		name              string
		rolloutPolicy     *domain.RolloutPolicy
		expectedImmediate bool
		description       string
	}{
		{
			name:              "NoRolloutPolicy",
			rolloutPolicy:     nil,
			expectedImmediate: true,
			description:       "immediateRollout should be true when RolloutPolicy is nil",
		},
		{
			name: "RolloutPolicyWithoutDeviceSelection",
			rolloutPolicy: &domain.RolloutPolicy{
				DeviceSelection: nil,
			},
			expectedImmediate: true,
			description:       "immediateRollout should be true when RolloutPolicy exists but DeviceSelection is nil",
		},
		{
			name: "RolloutPolicyWithDeviceSelection",
			rolloutPolicy: &domain.RolloutPolicy{
				DeviceSelection: &domain.RolloutDeviceSelection{},
			},
			expectedImmediate: false,
			description:       "immediateRollout should be false when RolloutPolicy.DeviceSelection exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			fleetName := "test-fleet"
			fleet := createTestFleet(fleetName, tt.rolloutPolicy)
			event := createTestEvent(domain.FleetKind, "some-reason", fleetName)
			orgId := uuid.New()
			log := logrus.New()

			mockService := service.NewMockService(ctrl)
			mockK8SClient := k8sclient.NewMockK8SClient(ctrl)

			// Mock GetFleet to return our test fleet
			mockService.EXPECT().GetFleet(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(fleet, domain.Status{Code: http.StatusOK})

			// Mock OverwriteFleetRepositoryRefs to succeed
			mockService.EXPECT().OverwriteFleetRepositoryRefs(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(domain.Status{Code: http.StatusOK})

			// Mock CreateTemplateVersion to capture the immediateRollout parameter
			var capturedImmediateRollout bool
			mockService.EXPECT().CreateTemplateVersion(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, orgId uuid.UUID, tv domain.TemplateVersion, immediateRollout bool) (*domain.TemplateVersion, domain.Status) {
					capturedImmediateRollout = immediateRollout
					return &domain.TemplateVersion{
						Metadata: domain.ObjectMeta{
							Name: &[]string{"test-tv"}[0],
						},
					}, domain.Status{Code: http.StatusCreated}
				})

			// Mock UpdateFleetAnnotations to succeed
			mockService.EXPECT().UpdateFleetAnnotations(gomock.Any(), gomock.Any(), fleetName, gomock.Any(), gomock.Any()).Return(domain.Status{Code: http.StatusOK})

			// Mock UpdateToOutOfDateByOwner to succeed
			mockService.EXPECT().SetOutOfDate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

			// Mock UpdateFleetConditions to succeed
			mockService.EXPECT().UpdateFleetConditions(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(domain.Status{Code: http.StatusOK})

			// Create FleetValidateLogic instance
			logic := NewFleetValidateLogic(log, mockService, mockK8SClient, orgId, event)

			// Execute
			err := logic.CreateNewTemplateVersionIfFleetValid(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.expectedImmediate, capturedImmediateRollout, tt.description)
		})
	}
}

func TestGenerateTemplateVersionName(t *testing.T) {
	require := require.New(t)

	makeFleet := func(name string, generation int64) *domain.Fleet {
		return &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:       &name,
				Generation: &generation,
			},
		}
	}

	tests := []struct {
		name        string
		fleetName   string
		generation  int64
		fingerprint string
		expected    string
	}{
		{
			name:        "When fingerprint is empty it should return v{generation}",
			fleetName:   "my-fleet",
			generation:  1,
			fingerprint: "",
			expected:    "v1",
		},
		{
			name:        "When fingerprint is empty with large generation it should return v{generation}",
			fleetName:   "my-fleet",
			generation:  9999999999,
			fingerprint: "",
			expected:    "v9999999999",
		},
		{
			name:        "When fingerprint is empty with 253-char fleet name it should return v{generation}",
			fleetName:   strings.Repeat("a", 253),
			generation:  42,
			fingerprint: "",
			expected:    "v42",
		},
		{
			name:        "When fingerprint is provided it should return v{generation}-{short-hash}",
			fleetName:   "my-fleet",
			generation:  3,
			fingerprint: "abc123def456789",
			expected:    "v3-abc123de",
		},
		{
			name:        "When fingerprint is shorter than 8 chars it should use full fingerprint",
			fleetName:   "my-fleet",
			generation:  1,
			fingerprint: "abc",
			expected:    "v1-abc",
		},
		{
			name:        "When fingerprint is exactly 8 chars it should use full fingerprint",
			fleetName:   "my-fleet",
			generation:  1,
			fingerprint: "abcd1234",
			expected:    "v1-abcd1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateTemplateVersionName(makeFleet(tt.fleetName, tt.generation), tt.fingerprint)
			require.Equal(tt.expected, result)
		})
	}
}

func TestFleetValidateLogic_GetFingerprint(t *testing.T) {
	tests := []struct {
		name     string
		event    domain.Event
		expected string
	}{
		{
			name: "When event is DependencyChangeDetected it should return the fingerprint",
			event: func() domain.Event {
				details := domain.EventDetails{}
				_ = details.FromDependencyChangeDetectedDetails(domain.DependencyChangeDetectedDetails{
					DetailType:  domain.DependencyChangeDetected,
					ResourceKey: "git:my-repo/main",
					Fingerprint: "abc123def456",
				})
				return domain.Event{
					Reason:  domain.EventReasonDependencyChangeDetected,
					Details: &details,
				}
			}(),
			expected: "abc123def456",
		},
		{
			name: "When event is ResourceUpdated it should return empty string",
			event: domain.Event{
				Reason: domain.EventReasonResourceUpdated,
			},
			expected: "",
		},
		{
			name: "When event is DependencyChangeDetected with nil details it should return empty string",
			event: domain.Event{
				Reason: domain.EventReasonDependencyChangeDetected,
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logic := FleetValidateLogic{
				log:   logrus.New(),
				event: tt.event,
			}
			result := logic.getFingerprint()
			assert.Equal(t, tt.expected, result)
		})
	}
}
