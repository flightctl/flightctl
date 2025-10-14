package tasks

import (
	"context"
	"net/http"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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
		rolloutPolicy     *api.RolloutPolicy
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
			rolloutPolicy: &api.RolloutPolicy{
				DeviceSelection: nil,
			},
			expectedImmediate: true,
			description:       "immediateRollout should be true when RolloutPolicy exists but DeviceSelection is nil",
		},
		{
			name: "RolloutPolicyWithDeviceSelection",
			rolloutPolicy: &api.RolloutPolicy{
				DeviceSelection: &api.RolloutDeviceSelection{},
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
			event := createTestEvent(api.FleetKind, "some-reason", fleetName)
			orgId := uuid.New()
			log := logrus.New()

			mockService := service.NewMockService(ctrl)
			mockK8SClient := k8sclient.NewMockK8SClient(ctrl)

			// Mock GetFleet to return our test fleet
			mockService.EXPECT().GetFleet(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(fleet, api.Status{Code: http.StatusOK})

			// Mock OverwriteFleetRepositoryRefs to succeed
			mockService.EXPECT().OverwriteFleetRepositoryRefs(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(api.Status{Code: http.StatusOK})

			// Mock CreateTemplateVersion to capture the immediateRollout parameter
			var capturedImmediateRollout bool
			mockService.EXPECT().CreateTemplateVersion(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, orgId uuid.UUID, tv api.TemplateVersion, immediateRollout bool) (*api.TemplateVersion, api.Status) {
					capturedImmediateRollout = immediateRollout
					return &api.TemplateVersion{
						Metadata: api.ObjectMeta{
							Name: &[]string{"test-tv"}[0],
						},
					}, api.Status{Code: http.StatusCreated}
				})

			// Mock UpdateFleetAnnotations to succeed
			mockService.EXPECT().UpdateFleetAnnotations(gomock.Any(), gomock.Any(), fleetName, gomock.Any(), gomock.Any()).Return(api.Status{Code: http.StatusOK})

			// Mock UpdateToOutOfDateByOwner to succeed
			mockService.EXPECT().SetOutOfDate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

			// Mock UpdateFleetConditions to succeed
			mockService.EXPECT().UpdateFleetConditions(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(api.Status{Code: http.StatusOK})

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
