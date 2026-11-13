package tasks

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	certificatesigningrequestservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestShouldProcessEnrollmentCredentialRevocation(t *testing.T) {
	tests := []struct {
		name     string
		event    domain.Event
		expected bool
	}{
		{
			name:     "When an enrollment credential is revoked it should return true",
			event:    createTestEvent(domain.CertificateSigningRequestKind, domain.EventReasonEnrollmentCredentialRevoked, "credential-1"),
			expected: true,
		},
		{
			name:     "When a different CSR event is received it should return false",
			event:    createTestEvent(domain.CertificateSigningRequestKind, domain.EventReasonResourceUpdated, "credential-1"),
			expected: false,
		},
		{
			name:     "When the revocation event is for another resource it should return false",
			event:    createTestEvent(domain.DeviceKind, domain.EventReasonEnrollmentCredentialRevoked, "device-1"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, shouldProcessEnrollmentCredentialRevocation(tt.event))
		})
	}
}

func TestProcessEnrollmentCredentialRevocation(t *testing.T) {
	t.Run("When a revocation event is processed it should deny requests, flag devices, and mark completion", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()
		orgId := uuid.New()
		credentialName := "credential-1"
		mockER := enrollmentrequestservice.NewMockService(ctrl)
		mockDevice := deviceservice.NewMockService(ctrl)
		mockCSR := certificatesigningrequestservice.NewMockService(ctrl)

		mockER.EXPECT().DenyEnrollmentRequestsForRevokedCredential(gomock.Any(), orgId, credentialName).Return(int64(3), domain.StatusOK())
		mockDevice.EXPECT().RevokeDevicesForEnrollmentCredential(gomock.Any(), orgId, credentialName).Return(int64(2), domain.StatusOK())
		mockCSR.EXPECT().MarkEnrollmentCredentialFlaggingCompleted(gomock.Any(), orgId, credentialName).Return(&domain.CertificateSigningRequest{}, domain.StatusOK())

		err := processEnrollmentCredentialRevocation(ctx, orgId, createTestEvent(domain.CertificateSigningRequestKind, domain.EventReasonEnrollmentCredentialRevoked, credentialName), mockDevice, mockER, mockCSR, logrus.New())
		require.NoError(t, err)
	})

	t.Run("When enrollment request denial fails it should not flag devices", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockER := enrollmentrequestservice.NewMockService(ctrl)
		mockDevice := deviceservice.NewMockService(ctrl)
		mockCSR := certificatesigningrequestservice.NewMockService(ctrl)
		orgId := uuid.New()

		mockER.EXPECT().DenyEnrollmentRequestsForRevokedCredential(gomock.Any(), orgId, "credential-1").Return(int64(0), domain.StatusInternalServerError("boom"))

		err := processEnrollmentCredentialRevocation(context.Background(), orgId, createTestEvent(domain.CertificateSigningRequestKind, domain.EventReasonEnrollmentCredentialRevoked, "credential-1"), mockDevice, mockER, mockCSR, logrus.New())
		require.Error(t, err)
	})

	t.Run("When device flagging fails it should return an error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockER := enrollmentrequestservice.NewMockService(ctrl)
		mockDevice := deviceservice.NewMockService(ctrl)
		mockCSR := certificatesigningrequestservice.NewMockService(ctrl)
		orgId := uuid.New()

		mockER.EXPECT().DenyEnrollmentRequestsForRevokedCredential(gomock.Any(), orgId, "credential-1").Return(int64(0), domain.StatusOK())
		mockDevice.EXPECT().RevokeDevicesForEnrollmentCredential(gomock.Any(), orgId, "credential-1").Return(int64(0), domain.StatusInternalServerError("boom"))

		err := processEnrollmentCredentialRevocation(context.Background(), orgId, createTestEvent(domain.CertificateSigningRequestKind, domain.EventReasonEnrollmentCredentialRevoked, "credential-1"), mockDevice, mockER, mockCSR, logrus.New())
		require.Error(t, err)
	})

	t.Run("When the event has no credential name it should fail", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		err := processEnrollmentCredentialRevocation(context.Background(), uuid.New(), createTestEvent(domain.CertificateSigningRequestKind, domain.EventReasonEnrollmentCredentialRevoked, ""), deviceservice.NewMockService(ctrl), enrollmentrequestservice.NewMockService(ctrl), certificatesigningrequestservice.NewMockService(ctrl), logrus.New())
		require.Error(t, err)
	})

	t.Run("When the services are missing it should fail without panic", func(t *testing.T) {
		err := processEnrollmentCredentialRevocation(context.Background(), uuid.New(), createTestEvent(domain.CertificateSigningRequestKind, domain.EventReasonEnrollmentCredentialRevoked, "credential-1"), nil, nil, nil, logrus.New())
		require.Error(t, err)
	})
}

func TestDispatchTasks_EnrollmentCredentialRevocation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgId := uuid.New()
	credentialName := "credential-1"
	mockER := enrollmentrequestservice.NewMockService(ctrl)
	mockDevice := deviceservice.NewMockService(ctrl)
	mockCSR := certificatesigningrequestservice.NewMockService(ctrl)
	mockConsumer := &MockConsumer{}

	payload, err := json.Marshal(worker_client.EventWithOrgId{
		OrgId: orgId,
		Event: createTestEvent(domain.CertificateSigningRequestKind, domain.EventReasonEnrollmentCredentialRevoked, credentialName),
	})
	require.NoError(t, err)
	mockConsumer.On("Complete", mock.Anything, "entry-123", payload, mock.Anything).Return(nil)
	mockER.EXPECT().DenyEnrollmentRequestsForRevokedCredential(gomock.Any(), orgId, credentialName).Return(int64(0), domain.StatusOK())
	mockDevice.EXPECT().RevokeDevicesForEnrollmentCredential(gomock.Any(), orgId, credentialName).Return(int64(0), domain.StatusOK())
	mockCSR.EXPECT().MarkEnrollmentCredentialFlaggingCompleted(gomock.Any(), orgId, credentialName).Return(&domain.CertificateSigningRequest{}, domain.StatusOK())

	handler := TaskConsumer{DeviceSvc: mockDevice, EnrollmentRequestSvc: mockER, CSRService: mockCSR}.dispatch()
	err = handler(context.Background(), payload, "entry-123", mockConsumer, logrus.New())
	require.NoError(t, err)
	mockConsumer.AssertExpectations(t)
}
