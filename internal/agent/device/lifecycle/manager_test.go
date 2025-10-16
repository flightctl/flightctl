package lifecycle

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/identity"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/util/wait"
)

// generateTestCertificate creates a valid test certificate for testing
func generateTestCertificate(t *testing.T) string {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test-device",
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(time.Hour * 24 * 365),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: nil,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	return string(certPEM)
}

func TestLifecycleManager_verifyEnrollment(t *testing.T) {
	tests := []struct {
		name           string
		setupMocks     func(*client.MockEnrollment, *identity.MockProvider)
		expectedResult bool
		expectedError  string
	}{
		{
			name: "identity proof required and succeeds",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider) {
				enrollmentRequest := &v1alpha1.EnrollmentRequest{
					Status: &v1alpha1.EnrollmentRequestStatus{
						Conditions: []v1alpha1.Condition{
							// No "Approved" condition, so identity proof is required
						},
					},
				}
				mockEnrollment.EXPECT().GetEnrollmentRequest(gomock.Any(), "test-device").Return(enrollmentRequest, nil)
				mockIdentity.EXPECT().ProveIdentity(gomock.Any(), enrollmentRequest).Return(nil)
			},
			expectedResult: false,
			expectedError:  "",
		},
		{
			name: "identity proof fails with ErrIdentityProofFailed",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider) {
				enrollmentRequest := &v1alpha1.EnrollmentRequest{
					Status: &v1alpha1.EnrollmentRequestStatus{
						Conditions: []v1alpha1.Condition{
							// No "Approved" condition, so identity proof is required
						},
					},
				}
				mockEnrollment.EXPECT().GetEnrollmentRequest(gomock.Any(), "test-device").Return(enrollmentRequest, nil)
				mockIdentity.EXPECT().ProveIdentity(gomock.Any(), enrollmentRequest).Return(identity.ErrIdentityProofFailed)
			},
			expectedResult: false,
			expectedError:  "proving identity: identity proof failed",
		},
		{
			name: "identity proof fails with other error",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider) {
				enrollmentRequest := &v1alpha1.EnrollmentRequest{
					Status: &v1alpha1.EnrollmentRequestStatus{
						Conditions: []v1alpha1.Condition{
							// No "Approved" condition, so identity proof is required
						},
					},
				}
				mockEnrollment.EXPECT().GetEnrollmentRequest(gomock.Any(), "test-device").Return(enrollmentRequest, nil)
				mockIdentity.EXPECT().ProveIdentity(gomock.Any(), enrollmentRequest).Return(errors.New("network error"))
			},
			expectedResult: false,
			expectedError:  "",
		},
		{
			name: "enrollment denied",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider) {
				enrollmentRequest := &v1alpha1.EnrollmentRequest{
					Status: &v1alpha1.EnrollmentRequestStatus{
						Conditions: []v1alpha1.Condition{
							{
								Type:    "Denied",
								Reason:  "PolicyViolation",
								Message: "Device does not meet policy requirements",
							},
						},
					},
				}
				mockEnrollment.EXPECT().GetEnrollmentRequest(gomock.Any(), "test-device").Return(enrollmentRequest, nil)
			},
			expectedResult: false,
			expectedError:  "enrollment request denied: reason: PolicyViolation, message: Device does not meet policy requirements",
		},
		{
			name: "enrollment failed",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider) {
				enrollmentRequest := &v1alpha1.EnrollmentRequest{
					Status: &v1alpha1.EnrollmentRequestStatus{
						Conditions: []v1alpha1.Condition{
							{
								Type:    "Failed",
								Reason:  "ProcessingError",
								Message: "Failed to process enrollment request",
							},
						},
					},
				}
				mockEnrollment.EXPECT().GetEnrollmentRequest(gomock.Any(), "test-device").Return(enrollmentRequest, nil)
			},
			expectedResult: false,
			expectedError:  "enrollment request failed: reason: ProcessingError, message: Failed to process enrollment request",
		},
		{
			name: "enrollment approved with certificate",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider) {
				certificate := generateTestCertificate(t)
				enrollmentRequest := &v1alpha1.EnrollmentRequest{
					Status: &v1alpha1.EnrollmentRequestStatus{
						Conditions: []v1alpha1.Condition{
							{
								Type: "Approved",
							},
						},
						Certificate: &certificate,
					},
				}
				mockEnrollment.EXPECT().GetEnrollmentRequest(gomock.Any(), "test-device").Return(enrollmentRequest, nil)
				mockIdentity.EXPECT().StoreCertificate([]byte(certificate)).Return(nil)
			},
			expectedResult: true,
			expectedError:  "",
		},
		{
			name: "enrollment approved but no certificate yet",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider) {
				enrollmentRequest := &v1alpha1.EnrollmentRequest{
					Status: &v1alpha1.EnrollmentRequestStatus{
						Conditions: []v1alpha1.Condition{
							{
								Type: "Approved",
							},
						},
						Certificate: nil,
					},
				}
				mockEnrollment.EXPECT().GetEnrollmentRequest(gomock.Any(), "test-device").Return(enrollmentRequest, nil)
			},
			expectedResult: false,
			expectedError:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockEnrollment := client.NewMockEnrollment(ctrl)
			mockIdentity := identity.NewMockProvider(ctrl)
			mockStatus := status.NewMockManager(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			manager := &LifecycleManager{
				deviceName:       "test-device",
				enrollmentClient: mockEnrollment,
				identityProvider: mockIdentity,
				statusManager:    mockStatus,
				deviceReadWriter: mockReadWriter,
				backoff:          wait.Backoff{},
				log:              log.NewPrefixLogger("test"),
			}

			tt.setupMocks(mockEnrollment, mockIdentity)

			ctx := context.Background()
			result, err := manager.verifyEnrollment(ctx)

			require.Equal(t, tt.expectedResult, result)
			if tt.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedError)
			}
		})
	}
}
