package agentserver

import (
	"context"
	"encoding/hex"
	"net/http"
	"testing"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	tcgCSR       = "AQABAAAABOUAAABGAAABAAAAAAsAAAAgAAAACgAAAAsAAAFjAAAAAAAAAYIAAABaAAAAAAAAAAAAAAAAAAAAWgAAAK8AAABIAAAAAHRlc3QtbW9kZWx0ZXN0LXNlcmlhbC0tLS0tQkVHSU4gQ0VSVElGSUNBVEUgUkVRVUVTVC0tLS0tCk1JSE5NSFlDQVFBd0ZERVNNQkFHQTFVRUF4TUpkR1Z6ZEMxdVlXMWxNRmt3RXdZSEtvWkl6ajBDQVFZSUtvWkkKemowREFRY0RRZ0FFZnFkU2NvemxhamgzOWI3TkU0Q0FYcndBejhQWmY0Q2xRcFEwOHlJRWk3ZlEwUFhMMlJEOQpRWEtTLzBZYjRpaWFCb2JqRkpNdEJTK0pyVzFBaTEyUjA2QUFNQW9HQ0NxR1NNNDlCQU1DQTBjQU1FUUNJQlBXCnJveDRmZFphRWdGakxUYjR6bUovcTNiRVhsNGFqQ3hTZmllZ2tKWGZBaUJHTmJROG1yOG1TK1pNTHczNTg2K1EKNWlPYXZ2RFBUNzlJN21NNUNPbERLQT09Ci0tLS0tRU5EIENFUlRJRklDQVRFIFJFUVVFU1QtLS0tLQowggF+MIIBJaADAgECAgEBMAoGCCqGSM49BAMCMDgxCzAJBgNVBAYTAlVTMQ0wCwYDVQQHEwRUZXN0MRowGAYDVQQKExFGbGlnaHRDVEwgVGVzdCBFSzAeFw0yNTA5MDkxOTIxMzFaFw0yNjA5MDkxOTIxMzFaMDgxCzAJBgNVBAYTAlVTMQ0wCwYDVQQHEwRUZXN0MRowGAYDVQQKExFGbGlnaHRDVEwgVGVzdCBFSzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABGMuQrBs3F0wGnU32c9l2UXuh8QUHftT35o4D1h/qRUIwaHOM7MOA4Pm3LfratfCOuRvOQqhd+hYPKOl/Qqx0oGjIDAeMA4GA1UdDwEB/wQEAwIFIDAMBgNVHRMBAf8EAjAAMAoGCCqGSM49BAMCA0cAMEQCIBP7Ico5ahpUMhSxjxxD9t3n6Nhgx7Tov2+6VstXM0e8AiAHJl6Vo4P8Y7XcWbgJHpnsqyjC4GbB/iEdGnjVg3hejwBYACMACwAFAHIAAAAQABgACwADABAAIPsV2T9CN2yK4ueURPNPTXv+bzDw715PRwgRyeqjsaLzACBUB9ptls6+PyMGyPu2e3NotNWXLv4XnDxtVVJodwO89wBYACMACwAEAHIAAAAQABgACwADABAAIH6nUnKM5Wo4d/W+zROAgF68AM/D2X+ApUKUNPMiBIu3ACDQ0PXL2RD9QXKS/0Yb4iiaBobjFJMtBS+JrW1Ai12R0wCt/1RDR4AXACIAC6hf4o7Ig82q/rtDX2qA3KdpYN9tiXvDr6QFCqiv6r8PACAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAG9pQjbndGLG8Bd65Od6S/54EAIgAL/ci4Ojm0X6mZ/4Brd5CHG0WOql8ReqyR3htBTKdlx/oAIgALMlgj0FpEotydJ4K2cM52Sn7vZsvqJ1Dee8SKrVfC1/EAGAALACC8X+iqUANGlzVzCcEyGvO66M/J6VfgQ1iVzVm229nhyAAgm7CDS2BIyrPx+gnXuNLCrT1bsMbzyWsSNUqfGQv8CxYwRAIgMrkUGIjKwVIKw4KSfsrVdZiojsA6Cd7dmikjAEQfxHgCID6gbfkWUJ9h2FrP1YR33MjkzmDC5ylJ1jwWQaVb4l2n"
	ekCertHex    = "3082017f30820125a003020102020101300a06082a8648ce3d0403023038310b3009060355040613025553310d300b0603550407130454657374311a3018060355040a1311466c6967687443544c205465737420454b301e170d3235303930393138353035325a170d3236303930393138353035325a3038310b3009060355040613025553310d300b0603550407130454657374311a3018060355040a1311466c6967687443544c205465737420454b3059301306072a8648ce3d020106082a8648ce3d03010703420004dfdecc3525bf745eff69599af54f2c28db971236a1e4b61695591a27ec77efea080bc740118e050950f792cc92dd73a96e277929f7706fc7a7dc614de03c96e1a320301e300e0603551d0f0101ff040403020520300c0603551d130101ff04023000300a06082a8648ce3d0403020348003045022100b64f87c784ad3b8ea69df2b0719381d3e957f8bc9d2642dcf82a60e129fd794f0220697ee7d54fed8c2360eee8b9eac976eca46394f37ce9e8268ca1b5f6aa6656e7"
	attestPubHex = "00580023000b00050072000000100018000b000300100020ffcf8d2b100378fe10458408c326d18d11d518c3008f2f4bc08cdccbca15e6c60020677bca9c423d36c949dd3edf1b36ebdbf68fa4f28b1054cadf6a264c4794ad5c"
)

func TestSendErrorResponse(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		expectError bool
	}{
		{
			name:        "Send internal error",
			message:     "test error message",
			expectError: false,
		},
		{
			name:        "Send invalid argument error",
			message:     "invalid input",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStream := grpc_v1.NewMockEnrollment_TPMChallengeServer(ctrl)
			server := &AgentGrpcServer{
				log: logrus.New(),
			}

			expectedMsg := &grpc_v1.ServerChallenge{
				Payload: &grpc_v1.ServerChallenge_Error{
					Error: &grpc_v1.ChallengeError{
						Message: tt.message,
					},
				},
			}

			if tt.expectError {
				mockStream.EXPECT().Send(expectedMsg).Return(status.Error(codes.Internal, "stream error"))
			} else {
				mockStream.EXPECT().Send(expectedMsg).Return(nil)
			}

			err := server.sendErrorResponse(mockStream, tt.message)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestReceiveAndValidateInitialRequest(t *testing.T) {
	tests := []struct {
		name           string
		setupMock      func(*grpc_v1.MockEnrollment_TPMChallengeServer)
		expectError    bool
		expectedErrMsg string
	}{
		{
			name: "Valid initial request",
			setupMock: func(mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockStream.EXPECT().Recv().Return(&grpc_v1.AgentChallenge{
					Payload: &grpc_v1.AgentChallenge_ChallengeRequest{
						ChallengeRequest: &grpc_v1.ChallengeRequest{
							EnrollmentRequestName: "test-enrollment-request",
						},
					},
				}, nil)
			},
			expectError: false,
		},
		{
			name: "Stream receive error",
			setupMock: func(mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockStream.EXPECT().Recv().Return(nil, status.Error(codes.Internal, "stream error"))
			},
			expectError:    true,
			expectedErrMsg: "failed to receive challenge request",
		},
		{
			name: "Invalid message type",
			setupMock: func(mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockStream.EXPECT().Recv().Return(&grpc_v1.AgentChallenge{
					Payload: &grpc_v1.AgentChallenge_ChallengeResponse{
						ChallengeResponse: &grpc_v1.ChallengeResponse{},
					},
				}, nil)
			},
			expectError:    true,
			expectedErrMsg: "expected challenge request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStream := grpc_v1.NewMockEnrollment_TPMChallengeServer(ctrl)
			server := &AgentGrpcServer{
				log: logrus.New(),
			}
			tt.setupMock(mockStream)

			enrollmentName, err := server.receiveAndValidateInitialRequest(mockStream)
			if tt.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedErrMsg)
				require.Empty(t, enrollmentName)
			} else {
				require.NoError(t, err)
				require.Equal(t, "test-enrollment-request", enrollmentName)
			}
		})
	}
}

func TestValidateEnrollmentRequest(t *testing.T) {
	tests := []struct {
		name           string
		enrollmentName string
		setupMocks     func(*service.MockService, *grpc_v1.MockEnrollment_TPMChallengeServer)
		expectError    bool
	}{
		{
			name:           "Enrollment request not found",
			enrollmentName: "non-existent-request",
			setupMocks: func(mockService *service.MockService, mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockService.EXPECT().GetEnrollmentRequest(gomock.Any(), gomock.Any(), "non-existent-request").Return(
					nil, api.Status{Code: http.StatusNotFound, Message: "not found"},
				)
				mockStream.EXPECT().Send(gomock.Any()).Return(nil)
			},
			expectError: true,
		},
		{
			name:           "Send error response fails",
			enrollmentName: "test-request",
			setupMocks: func(mockService *service.MockService, mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockService.EXPECT().GetEnrollmentRequest(gomock.Any(), gomock.Any(), "test-request").Return(
					nil, api.Status{Code: http.StatusInternalServerError, Message: "server error"},
				)
				mockStream.EXPECT().Send(gomock.Any()).Return(status.Error(codes.Internal, "stream failed"))
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockService := service.NewMockService(ctrl)
			mockStream := grpc_v1.NewMockEnrollment_TPMChallengeServer(ctrl)
			server := &AgentGrpcServer{
				service: mockService,
				log:     logrus.New(),
			}

			tt.setupMocks(mockService, mockStream)

			enrollmentReq, parsed, err := server.validateEnrollmentRequest(context.Background(), mockStream, tt.enrollmentName)
			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, enrollmentReq)
				require.Nil(t, parsed)
			} else {
				require.NoError(t, err)
				require.NotNil(t, enrollmentReq)
				require.NotNil(t, parsed)
			}
		})
	}
}

func TestGenerateAndSendChallenge(t *testing.T) {
	tests := []struct {
		name        string
		setupMocks  func(*grpc_v1.MockEnrollment_TPMChallengeServer)
		expectError bool
	}{
		{
			name: "Send challenge fails",
			setupMocks: func(mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockStream.EXPECT().Send(gomock.Any()).Return(status.Error(codes.Internal, "stream error"))
			},
			expectError: true,
		},
		{
			name: "Send challenge succeeds",
			setupMocks: func(mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockStream.EXPECT().Send(gomock.Any()).Return(nil)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStream := grpc_v1.NewMockEnrollment_TPMChallengeServer(ctrl)
			server := &AgentGrpcServer{
				log: logrus.New(),
			}

			ekCert, err := hex.DecodeString(ekCertHex)
			require.NoError(t, err)
			attestPub, err := hex.DecodeString(attestPubHex)
			require.NoError(t, err)

			parsed := &tpm.ParsedTCGCSR{
				CSRContents: &tpm.ParsedTCGContent{
					Payload: &tpm.ParsedTCGPayload{
						EkCert:    ekCert,
						AttestPub: attestPub,
					},
				},
			}

			tt.setupMocks(mockStream)

			challenge, err := server.generateAndSendChallenge(mockStream, parsed)
			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, challenge)
			} else {
				require.NoError(t, err)
				require.NotNil(t, challenge)
			}
		})
	}
}

func TestPerformTPMChallenge(t *testing.T) {
	tests := []struct {
		name        string
		setupMocks  func(*service.MockService, *grpc_v1.MockEnrollment_TPMChallengeServer)
		expectError bool
	}{
		{
			name: "Invalid initial request - receive error",
			setupMocks: func(mockService *service.MockService, mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockStream.EXPECT().Context().Return(context.Background()).AnyTimes()
				mockStream.EXPECT().Recv().Return(nil, status.Error(codes.Internal, "stream error"))
				mockStream.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
			},
			expectError: true,
		},
		{
			name: "Invalid message type",
			setupMocks: func(mockService *service.MockService, mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockStream.EXPECT().Context().Return(context.Background()).AnyTimes()
				mockStream.EXPECT().Recv().Return(&grpc_v1.AgentChallenge{
					Payload: &grpc_v1.AgentChallenge_ChallengeResponse{
						ChallengeResponse: &grpc_v1.ChallengeResponse{},
					},
				}, nil)
				mockStream.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
			},
			expectError: true,
		},
		{
			name: "Empty enrollment request name",
			setupMocks: func(mockService *service.MockService, mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				mockStream.EXPECT().Context().Return(context.Background()).AnyTimes()
				mockStream.EXPECT().Recv().Return(&grpc_v1.AgentChallenge{
					Payload: &grpc_v1.AgentChallenge_ChallengeRequest{
						ChallengeRequest: &grpc_v1.ChallengeRequest{
							EnrollmentRequestName: "",
						},
					},
				}, nil)
				mockService.EXPECT().GetEnrollmentRequest(gomock.Any(), gomock.Any(), "").Return(
					nil, api.Status{Code: http.StatusNotFound, Message: "not found"},
				)
				mockStream.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
			},
			expectError: true,
		},
		{
			name: "challenge verification failure",
			setupMocks: func(mockService *service.MockService, mockStream *grpc_v1.MockEnrollment_TPMChallengeServer) {
				ctx := context.Background()
				enrollmentRequestName := "test-enrollment-request"

				enrollmentRequest := &api.EnrollmentRequest{
					Metadata: api.ObjectMeta{
						Name: &enrollmentRequestName,
					},
					Spec: api.EnrollmentRequestSpec{
						Csr: tcgCSR,
					},
					Status: &api.EnrollmentRequestStatus{
						Conditions: []api.Condition{
							{
								Type:    api.ConditionTypeEnrollmentRequestTPMVerified,
								Status:  api.ConditionStatusFalse,
								Reason:  api.TPMChallengeRequiredReason,
								Message: "TPM challenge required for verification",
							},
						},
					},
				}

				// Mock stream context
				mockStream.EXPECT().Context().Return(ctx).AnyTimes()

				// 1. Initial challenge request from agent
				mockStream.EXPECT().Recv().Return(&grpc_v1.AgentChallenge{
					Payload: &grpc_v1.AgentChallenge_ChallengeRequest{
						ChallengeRequest: &grpc_v1.ChallengeRequest{
							EnrollmentRequestName: enrollmentRequestName,
						},
					},
				}, nil)

				// 2. Service validates enrollment request
				mockService.EXPECT().GetEnrollmentRequest(ctx, gomock.Any(), enrollmentRequestName).Return(
					enrollmentRequest, api.Status{Code: http.StatusOK},
				)

				// 3. Server sends challenge to agent
				mockStream.EXPECT().Send(gomock.Any()).DoAndReturn(func(msg *grpc_v1.ServerChallenge) error {
					// Verify it's a challenge message
					require.NotNil(t, msg.GetChallenge())
					require.NotEmpty(t, msg.GetChallenge().CredentialBlob)
					require.NotEmpty(t, msg.GetChallenge().EncryptedSecret)
					return nil
				})

				// 4. Agent responds with challenge response (with incorrect secret)
				// currently secrets only exist within the PerformChallenge flow
				// and there is no access to the secret. Since it can't be solved, the test
				// just verifies that it's updated to failure
				mockStream.EXPECT().Recv().Return(&grpc_v1.AgentChallenge{
					Payload: &grpc_v1.AgentChallenge_ChallengeResponse{
						ChallengeResponse: &grpc_v1.ChallengeResponse{
							Secret: []byte("wrong-secret"), // This will fail verification - expected behavior
						},
					},
				}, nil)

				// 5. Server updates enrollment status to failed (due to challenge verification failure)
				mockService.EXPECT().ReplaceEnrollmentRequestStatus(ctx, gomock.Any(), enrollmentRequestName, gomock.Any()).DoAndReturn(
					func(ctx context.Context, orgId uuid.UUID, name string, req api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
						// Verify the status was updated to failed
						require.Len(t, req.Status.Conditions, 1)
						condition := req.Status.Conditions[0]
						require.Equal(t, api.ConditionTypeEnrollmentRequestTPMVerified, condition.Type)
						require.Equal(t, api.ConditionStatusFalse, condition.Status)
						require.Equal(t, api.TPMChallengeFailedReason, condition.Reason)
						return &req, api.Status{Code: http.StatusOK}
					},
				)

				// 6. Server sends error response to client
				mockStream.EXPECT().Send(gomock.Any()).DoAndReturn(func(msg *grpc_v1.ServerChallenge) error {
					// Verify it's an error message
					require.NotNil(t, msg.GetError())
					require.Equal(t, "Challenge verification failed", msg.GetError().Message)
					return nil
				})
			},
			expectError: false, // Function should complete successfully, even though challenge fails
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockService := service.NewMockService(ctrl)
			mockStream := grpc_v1.NewMockEnrollment_TPMChallengeServer(ctrl)

			server := &AgentGrpcServer{
				service: mockService,
				log:     logrus.New(),
			}

			tt.setupMocks(mockService, mockStream)

			err := server.TPMChallenge(mockStream)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestReceiveAndVerifyResponse(t *testing.T) {
	tests := []struct {
		name            string
		challengeSecret []byte
		responseSecret  []byte
		expectError     bool
		errorType       string
	}{
		{
			name:            "Successful secret verification",
			challengeSecret: []byte("correct-secret"),
			responseSecret:  []byte("correct-secret"),
			expectError:     false,
		},
		{
			name:            "Secret mismatch",
			challengeSecret: []byte("correct-secret"),
			responseSecret:  []byte("wrong-secret"),
			expectError:     true,
			errorType:       "PermissionDenied",
		},
		{
			name:            "Empty response secret",
			challengeSecret: []byte("correct-secret"),
			responseSecret:  []byte(""),
			expectError:     true,
			errorType:       "PermissionDenied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStream := grpc_v1.NewMockEnrollment_TPMChallengeServer(ctrl)
			server := &AgentGrpcServer{
				log: logrus.New(),
			}

			// Create challenge with expected secret
			challenge := &tpm.CredentialChallenge{
				ExpectedSecret: tt.challengeSecret,
			}

			// Mock stream response
			mockStream.EXPECT().Recv().Return(&grpc_v1.AgentChallenge{
				Payload: &grpc_v1.AgentChallenge_ChallengeResponse{
					ChallengeResponse: &grpc_v1.ChallengeResponse{
						Secret: tt.responseSecret,
					},
				},
			}, nil)

			err := server.receiveAndVerifyResponse(mockStream, challenge)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorType == "PermissionDenied" {
					st, ok := status.FromError(err)
					require.True(t, ok, "Expected gRPC status error")
					require.Equal(t, codes.PermissionDenied, st.Code())
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
