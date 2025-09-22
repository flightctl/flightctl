package identity

import (
	"context"
	"errors"
	"testing"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	testSecretCorrect = "test-secret-123"
	testSecretWrong   = "wrong-secret"
	testDeviceName    = "test-device"
)

var testEnrollmentRequest = "test-enrollment-request"

func TestTPMProvider_IsTPMVerificationNeeded(t *testing.T) {
	tests := []struct {
		name           string
		enrollmentReq  *api.EnrollmentRequest
		expectedResult bool
	}{
		{
			name: "no status",
			enrollmentReq: &api.EnrollmentRequest{
				Metadata: api.ObjectMeta{Name: &testEnrollmentRequest},
			},
			expectedResult: true,
		},
		{
			name: "already verified",
			enrollmentReq: &api.EnrollmentRequest{
				Metadata: api.ObjectMeta{Name: &testEnrollmentRequest},
				Status: &api.EnrollmentRequestStatus{
					Conditions: []api.Condition{
						{
							Type:   api.ConditionTypeEnrollmentRequestTPMVerified,
							Status: api.ConditionStatusTrue,
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "verification failed",
			enrollmentReq: &api.EnrollmentRequest{
				Metadata: api.ObjectMeta{Name: &testEnrollmentRequest},
				Status: &api.EnrollmentRequestStatus{
					Conditions: []api.Condition{
						{
							Type:   api.ConditionTypeEnrollmentRequestTPMVerified,
							Status: api.ConditionStatusFalse,
							Reason: api.TPMVerificationFailedReason,
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "challenge required",
			enrollmentReq: &api.EnrollmentRequest{
				Metadata: api.ObjectMeta{Name: &testEnrollmentRequest},
				Status: &api.EnrollmentRequestStatus{
					Conditions: []api.Condition{
						{
							Type:   api.ConditionTypeEnrollmentRequestTPMVerified,
							Status: api.ConditionStatusFalse,
							Reason: api.TPMChallengeRequiredReason,
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "challenge failed",
			enrollmentReq: &api.EnrollmentRequest{
				Metadata: api.ObjectMeta{Name: &testEnrollmentRequest},
				Status: &api.EnrollmentRequestStatus{
					Conditions: []api.Condition{
						{
							Type:   api.ConditionTypeEnrollmentRequestTPMVerified,
							Status: api.ConditionStatusFalse,
							Reason: api.TPMChallengeFailedReason,
						},
					},
				},
			},
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &tpmProvider{
				log: log.NewPrefixLogger("test"),
			}

			result := provider.isTPMVerificationNeeded(tt.enrollmentReq)
			require.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestTPMProvider_ProcessChallenge(t *testing.T) {
	tests := []struct {
		name               string
		challenge          *grpc_v1.Challenge
		setupMocks         func(*tpm.MockClient, *grpc_v1.MockEnrollment_TPMChallengeClient)
		expectError        bool
		expectedErrMessage string
	}{
		{
			name: "success",
			challenge: &grpc_v1.Challenge{
				CredentialBlob:  []byte("valid-credential-blob"),
				EncryptedSecret: []byte("valid-encrypted-secret"),
			},
			setupMocks: func(mockClient *tpm.MockClient, mockStream *grpc_v1.MockEnrollment_TPMChallengeClient) {
				mockClient.EXPECT().SolveChallenge([]byte("valid-credential-blob"), []byte("valid-encrypted-secret")).Return([]byte(testSecretCorrect), nil)
				mockStream.EXPECT().Send(gomock.Any()).Return(nil)
				mockStream.EXPECT().Recv().Return(&grpc_v1.ServerChallenge{
					Payload: &grpc_v1.ServerChallenge_Success{
						Success: &grpc_v1.ChallengeComplete{},
					},
				}, nil)
			},
			expectError: false,
		},
		{
			name: "fail to solve challenge",
			challenge: &grpc_v1.Challenge{
				CredentialBlob:  []byte("invalid-credential-blob"),
				EncryptedSecret: []byte("invalid-encrypted-secret"),
			},
			setupMocks: func(mockClient *tpm.MockClient, mockStream *grpc_v1.MockEnrollment_TPMChallengeClient) {
				mockClient.EXPECT().SolveChallenge([]byte("invalid-credential-blob"), []byte("invalid-encrypted-secret")).Return(nil, errors.New("TPM challenge solve failed"))
			},
			expectError:        true,
			expectedErrMessage: "failed to solve TPM challenge",
		},
		{
			name: "failed to send challenge response",
			challenge: &grpc_v1.Challenge{
				CredentialBlob:  []byte("valid-credential-blob"),
				EncryptedSecret: []byte("valid-encrypted-secret"),
			},
			setupMocks: func(mockClient *tpm.MockClient, mockStream *grpc_v1.MockEnrollment_TPMChallengeClient) {
				mockClient.EXPECT().SolveChallenge([]byte("valid-credential-blob"), []byte("valid-encrypted-secret")).Return([]byte(testSecretCorrect), nil)
				mockStream.EXPECT().Send(gomock.Any()).Return(status.Error(codes.Internal, "send failed"))
			},
			expectError:        true,
			expectedErrMessage: "failed to send challenge response",
		},
		{
			name: "failed to receive final result",
			challenge: &grpc_v1.Challenge{
				CredentialBlob:  []byte("valid-credential-blob"),
				EncryptedSecret: []byte("valid-encrypted-secret"),
			},
			setupMocks: func(mockClient *tpm.MockClient, mockStream *grpc_v1.MockEnrollment_TPMChallengeClient) {
				mockClient.EXPECT().SolveChallenge([]byte("valid-credential-blob"), []byte("valid-encrypted-secret")).Return([]byte(testSecretCorrect), nil)
				mockStream.EXPECT().Send(gomock.Any()).Return(nil)
				mockStream.EXPECT().Recv().Return(nil, status.Error(codes.Internal, "receive failed"))
			},
			expectError:        true,
			expectedErrMessage: "failed to receive final result",
		},
		{
			name: "challenge failure",
			challenge: &grpc_v1.Challenge{
				CredentialBlob:  []byte("valid-credential-blob"),
				EncryptedSecret: []byte("valid-encrypted-secret"),
			},
			setupMocks: func(mockClient *tpm.MockClient, mockStream *grpc_v1.MockEnrollment_TPMChallengeClient) {
				mockClient.EXPECT().SolveChallenge([]byte("valid-credential-blob"), []byte("valid-encrypted-secret")).Return([]byte(testSecretWrong), nil)
				mockStream.EXPECT().Send(gomock.Any()).Return(nil)
				mockStream.EXPECT().Recv().Return(&grpc_v1.ServerChallenge{
					Payload: &grpc_v1.ServerChallenge_Error{
						Error: &grpc_v1.ChallengeError{
							Message: "Challenge verification failed",
						},
					},
				}, nil)
			},
			expectError:        true,
			expectedErrMessage: "TPM challenge failed: Challenge verification failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := tpm.NewMockClient(ctrl)
			mockStream := grpc_v1.NewMockEnrollment_TPMChallengeClient(ctrl)

			provider := &tpmProvider{
				client: mockClient,
				config: agent_config.NewDefault(),
				log:    log.NewPrefixLogger("test"),
			}

			if tt.setupMocks != nil {
				tt.setupMocks(mockClient, mockStream)
			}

			ctx := context.Background()
			err := provider.processChallenge(ctx, mockStream, tt.challenge)

			if tt.expectError {
				require.Error(t, err)
				if tt.expectedErrMessage != "" {
					require.Contains(t, err.Error(), tt.expectedErrMessage)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
