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

	"github.com/flightctl/flightctl/api/core/v1beta1"
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
		setupMocks     func(*client.MockEnrollment, *identity.MockProvider, *fileio.MockReadWriter)
		expectedResult bool
		expectedError  string
	}{
		{
			name: "identity proof required and succeeds",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider, mockReadWriter *fileio.MockReadWriter) {
				enrollmentRequest := &v1beta1.EnrollmentRequest{
					Status: &v1beta1.EnrollmentRequestStatus{
						Conditions: []v1beta1.Condition{
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
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider, mockReadWriter *fileio.MockReadWriter) {
				enrollmentRequest := &v1beta1.EnrollmentRequest{
					Status: &v1beta1.EnrollmentRequestStatus{
						Conditions: []v1beta1.Condition{
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
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider, mockReadWriter *fileio.MockReadWriter) {
				enrollmentRequest := &v1beta1.EnrollmentRequest{
					Status: &v1beta1.EnrollmentRequestStatus{
						Conditions: []v1beta1.Condition{
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
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider, mockReadWriter *fileio.MockReadWriter) {
				enrollmentRequest := &v1beta1.EnrollmentRequest{
					Status: &v1beta1.EnrollmentRequestStatus{
						Conditions: []v1beta1.Condition{
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
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider, mockReadWriter *fileio.MockReadWriter) {
				enrollmentRequest := &v1beta1.EnrollmentRequest{
					Status: &v1beta1.EnrollmentRequestStatus{
						Conditions: []v1beta1.Condition{
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
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider, mockReadWriter *fileio.MockReadWriter) {
				certificate := generateTestCertificate(t)
				enrollmentRequest := &v1beta1.EnrollmentRequest{
					Status: &v1beta1.EnrollmentRequestStatus{
						Conditions: []v1beta1.Condition{
							{
								Type: "Approved",
							},
						},
						Certificate: &certificate,
					},
				}
				mockEnrollment.EXPECT().GetEnrollmentRequest(gomock.Any(), "test-device").Return(enrollmentRequest, nil)
				mockIdentity.EXPECT().StoreCertificate([]byte(certificate)).Return(nil)
				// CSR cleanup now uses standalone functions (identity.LoadCSR/StoreCSR) with mockReadWriter
				mockReadWriter.EXPECT().PathExists("certs/agent.csr").Return(true, nil)
				mockReadWriter.EXPECT().ReadFile("certs/agent.csr").Return([]byte("test-csr"), nil)
				mockReadWriter.EXPECT().PathExists("certs/agent.csr").Return(true, nil)
				mockReadWriter.EXPECT().OverwriteAndWipe("certs/agent.csr").Return(nil)
			},
			expectedResult: true,
			expectedError:  "",
		},
		{
			name: "enrollment approved but no certificate yet",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider, mockReadWriter *fileio.MockReadWriter) {
				enrollmentRequest := &v1beta1.EnrollmentRequest{
					Status: &v1beta1.EnrollmentRequestStatus{
						Conditions: []v1beta1.Condition{
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

			tt.setupMocks(mockEnrollment, mockIdentity, mockReadWriter)

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

func TestLifecycleManager_buildEnrollmentLabels(t *testing.T) {
	tests := []struct {
		name                string
		labelFromSystemInfo map[string]string
		defaultLabels       map[string]string
		deviceStatus        *v1beta1.DeviceStatus
		expected            map[string]string
	}{
		{
			name:                "When no label mappings it should add default alias and default labels",
			labelFromSystemInfo: map[string]string{},
			defaultLabels: map[string]string{
				"env": "prod",
			},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					Architecture: "x86_64",
					AdditionalProperties: map[string]string{
						"hostname": "test-host",
					},
				},
			},
			expected: map[string]string{
				"alias": "test-host",
				"env":   "prod",
			},
		},
		{
			name: "When mapping systemInfo fields it should extract them correctly and add default alias",
			labelFromSystemInfo: map[string]string{
				"arch": "architecture",
				"os":   "operatingSystem",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					Architecture:    "x86_64",
					OperatingSystem: "linux",
					AdditionalProperties: map[string]string{
						"hostname": "test-host",
					},
				},
			},
			expected: map[string]string{
				"arch":  "x86_64",
				"os":    "linux",
				"alias": "test-host",
			},
		},
		{
			name: "When mapping customInfo fields it should extract them correctly and add default alias",
			labelFromSystemInfo: map[string]string{
				"site": "customInfo.siteId",
				"rack": "customInfo.rackNumber",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "test-host",
					},
					CustomInfo: &v1beta1.CustomDeviceInfo{
						"siteId":     "site-123",
						"rackNumber": "rack-5",
					},
				},
			},
			expected: map[string]string{
				"site":  "site-123",
				"rack":  "rack-5",
				"alias": "test-host",
			},
		},
		{
			name: "When alias is configured it should not add default alias",
			labelFromSystemInfo: map[string]string{
				"alias": "productName",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname":    "test-host",
						"productName": "my-product",
					},
				},
			},
			expected: map[string]string{
				"alias": "my-product",
			},
		},
		{
			name: "When defaultLabels conflict with mapped labels it should use defaultLabels",
			labelFromSystemInfo: map[string]string{
				"env": "architecture",
			},
			defaultLabels: map[string]string{
				"env": "prod",
			},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					Architecture: "x86_64",
					AdditionalProperties: map[string]string{
						"hostname": "test-host",
					},
				},
			},
			expected: map[string]string{
				"env":   "prod", // defaultLabels takes precedence
				"alias": "test-host",
			},
		},
		{
			name: "When mixing built-in and customInfo fields it should extract both and add default alias",
			labelFromSystemInfo: map[string]string{
				"arch": "architecture",
				"site": "customInfo.siteId",
			},
			defaultLabels: map[string]string{
				"env": "prod",
			},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					Architecture: "arm64",
					AdditionalProperties: map[string]string{
						"hostname": "test-host",
					},
					CustomInfo: &v1beta1.CustomDeviceInfo{
						"siteId": "site-456",
					},
				},
			},
			expected: map[string]string{
				"arch":  "arm64",
				"site":  "site-456",
				"env":   "prod",
				"alias": "test-host",
			},
		},
		{
			name: "When built-in field does not exist it should skip that label",
			labelFromSystemInfo: map[string]string{
				"arch":    "architecture",
				"missing": "nonexistent",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					Architecture: "x86_64",
					AdditionalProperties: map[string]string{
						"hostname": "test-host",
					},
					CustomInfo: &v1beta1.CustomDeviceInfo{},
				},
			},
			expected: map[string]string{
				"arch":  "x86_64",
				"alias": "test-host",
				// "missing" should not be present
			},
		},
		{
			name: "When customInfo field does not exist it should skip that label",
			labelFromSystemInfo: map[string]string{
				"arch":    "architecture",
				"missing": "customInfo.nonexistent",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					Architecture: "x86_64",
					AdditionalProperties: map[string]string{
						"hostname": "test-host",
					},
					CustomInfo: &v1beta1.CustomDeviceInfo{
						"siteId": "site-123",
					},
				},
			},
			expected: map[string]string{
				"arch":  "x86_64",
				"alias": "test-host",
				// "missing" should not be present
			},
		},
		{
			name: "When mapping AdditionalProperties it should extract them correctly and add default alias",
			labelFromSystemInfo: map[string]string{
				"customField": "myCustomField",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname":      "test-host",
						"myCustomField": "custom-value",
					},
				},
			},
			expected: map[string]string{
				"customField": "custom-value",
				"alias":       "test-host",
			},
		},
		{
			name:                "When hostname is empty it should not add default alias",
			labelFromSystemInfo: map[string]string{},
			defaultLabels:       map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "",
					},
				},
			},
			expected: map[string]string{},
		},
		{
			name:                "When hostname is (none) it should not add default alias",
			labelFromSystemInfo: map[string]string{},
			defaultLabels:       map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "(none)",
					},
				},
			},
			expected: map[string]string{},
		},
		{
			name:                "When hostname is localhost it should not add default alias",
			labelFromSystemInfo: map[string]string{},
			defaultLabels:       map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "localhost",
					},
				},
			},
			expected: map[string]string{},
		},
		{
			name:                "When hostname is localhost.localdomain it should not add default alias",
			labelFromSystemInfo: map[string]string{},
			defaultLabels:       map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "localhost.localdomain",
					},
				},
			},
			expected: map[string]string{},
		},
		{
			name:                "When hostname is localhost6.localdomain6 it should not add default alias",
			labelFromSystemInfo: map[string]string{},
			defaultLabels:       map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "localhost6.localdomain6",
					},
				},
			},
			expected: map[string]string{},
		},
		{
			name:                "When hostname is LOCALHOST (uppercase) it should not add default alias",
			labelFromSystemInfo: map[string]string{},
			defaultLabels:       map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "LOCALHOST",
					},
				},
			},
			expected: map[string]string{},
		},
		{
			name:                "When hostname is 127.0.0.1 it should not add default alias",
			labelFromSystemInfo: map[string]string{},
			defaultLabels:       map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "127.0.0.1",
					},
				},
			},
			expected: map[string]string{},
		},
		{
			name:                "When hostname is ::1 it should not add default alias",
			labelFromSystemInfo: map[string]string{},
			defaultLabels:       map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "::1",
					},
				},
			},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &LifecycleManager{
				labelFromSystemInfo: tt.labelFromSystemInfo,
				defaultLabels:       tt.defaultLabels,
				log:                 log.NewPrefixLogger("test"),
			}

			result := manager.buildEnrollmentLabels(tt.deviceStatus)
			require.Equal(t, tt.expected, result)
		})
	}
}
