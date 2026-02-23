package tasks

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util/validation"
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
		name       string
		fleetName  string
		generation int64
		expected   string // if non-empty, assert exact match
	}{
		{
			name:       "short name uses simple form",
			fleetName:  "my-fleet",
			generation: 5,
			expected:   "my-fleet-5",
		},
		{
			name:       "max name that fits simple form",
			fleetName:  strings.Repeat("a", 233),
			generation: 1,
		},
		{
			name:       "253-char name at generation 1",
			fleetName:  strings.Repeat("a", 253),
			generation: 1,
		},
		{
			name:       "253-char name at large generation",
			fleetName:  strings.Repeat("a", 253),
			generation: 9999999999,
		},
		{
			name:       "253-char different name for uniqueness check",
			fleetName:  strings.Repeat("a", 252) + "b",
			generation: 1,
		},
		{
			name:       "253-char name at generation 999 for stability check",
			fleetName:  strings.Repeat("a", 253),
			generation: 999,
		},
		{
			name:       "240-char name at large generation",
			fleetName:  strings.Repeat("b", 240),
			generation: 99999999999999,
		},
		{
			name:       "name with dot at truncation boundary",
			fleetName:  strings.Repeat("a", 241) + "." + strings.Repeat("a", 11),
			generation: 1,
		},
	}

	results := make(map[string]string)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateTemplateVersionName(makeFleet(tt.fleetName, tt.generation))
			results[tt.name] = result
			require.LessOrEqual(len(result), validation.DNS1123MaxLength,
				"generated name %q has %d chars, exceeds %d", result, len(result), validation.DNS1123MaxLength)
			errs := validation.ValidateResourceName(&result)
			require.Empty(errs, "generated name %q is not a valid DNS subdomain: %v", result, errs)
			if tt.expected != "" {
				require.Equal(tt.expected, result)
			}
		})
	}

	require.NotEqual(results["253-char name at generation 1"], results["253-char different name for uniqueness check"],
		"different long names should produce different results")

	r1 := results["253-char name at generation 1"]
	r2 := results["253-char name at generation 999 for stability check"]
	require.Equal(r1[:strings.LastIndex(r1, "-")], r2[:strings.LastIndex(r2, "-")],
		"hash form prefix should be stable across generations")
}
