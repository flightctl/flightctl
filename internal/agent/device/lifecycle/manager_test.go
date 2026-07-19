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

func TestLifecycleManager_Initialize_NoBannerOnEnrollmentFailure(t *testing.T) {
	tests := []struct {
		name        string
		setupMocks  func(*client.MockEnrollment, *identity.MockProvider, *fileio.MockReadWriter)
		expectError string
	}{
		{
			name: "When enrollment request creation fails it should not write the QR banner",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider, mockReadWriter *fileio.MockReadWriter) {
				mockIdentity.EXPECT().HasCertificate().Return(false)
				mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(nil, errors.New("not found")).AnyTimes()
				mockEnrollment.EXPECT().CreateEnrollmentRequest(gomock.Any(), gomock.Any()).Return(nil, errors.New("connection refused")).AnyTimes()
				// WriteFile for the banner must NOT be called
			},
			expectError: "creating enrollment request",
		},
		{
			name: "When enrollment request succeeds it should write the QR banner",
			setupMocks: func(mockEnrollment *client.MockEnrollment, mockIdentity *identity.MockProvider, mockReadWriter *fileio.MockReadWriter) {
				mockIdentity.EXPECT().HasCertificate().Return(false)
				mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(nil, errors.New("not found")).AnyTimes()
				mockEnrollment.EXPECT().CreateEnrollmentRequest(gomock.Any(), gomock.Any()).Return(nil, nil)
				// Banner file should be written after successful enrollment request
				mockReadWriter.EXPECT().WriteFile(BannerFile, gomock.Any(), gomock.Any()).Return(nil)
				// Then verifyEnrollment is called — return denied to end the loop quickly
				mockEnrollment.EXPECT().GetEnrollmentRequest(gomock.Any(), "test-device").Return(&v1beta1.EnrollmentRequest{
					Status: &v1beta1.EnrollmentRequestStatus{
						Conditions: []v1beta1.Condition{
							{Type: "Denied", Reason: "test", Message: "test"},
						},
					},
				}, nil)
			},
			expectError: "enrollment request denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockEnrollment := client.NewMockEnrollment(ctrl)
			mockIdentity := identity.NewMockProvider(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			manager := &LifecycleManager{
				deviceName:           "test-device",
				enrollmentUIEndpoint: "http://localhost:9001",
				enrollmentClient:     mockEnrollment,
				identityProvider:     mockIdentity,
				deviceReadWriter:     mockReadWriter,
				systemdClient:        client.NewSystemd(nil, ""),
				backoff: wait.Backoff{
					Steps:    1,
					Duration: time.Millisecond,
				},
				log: log.NewPrefixLogger("test"),
			}

			tt.setupMocks(mockEnrollment, mockIdentity, mockReadWriter)

			ctx := context.Background()
			err := manager.Initialize(ctx, &v1beta1.DeviceStatus{})

			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectError)
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
		{
			name: "When systemInfo value contains spaces it should be sanitized",
			labelFromSystemInfo: map[string]string{
				"os-name": "distroName",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname":   "test-host",
						"distroName": "CentOS Stream",
					},
				},
			},
			expected: map[string]string{
				"os-name": "CentOS-Stream", // Sanitized: space replaced with hyphen
				"alias":   "test-host",
			},
		},
		{
			name: "When systemInfo value has special characters it should be sanitized",
			labelFromSystemInfo: map[string]string{
				"distro": "distroVersion",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname":      "test-host",
						"distroVersion": "9.5 (Plow)",
					},
				},
			},
			expected: map[string]string{
				"distro": "9.5--Plow", // Sanitized: space and parens replaced
				"alias":  "test-host",
			},
		},
		{
			name:                "When default-labels contain invalid values they should be skipped",
			labelFromSystemInfo: map[string]string{},
			defaultLabels: map[string]string{
				"valid-label":   "valid-value",
				"invalid-label": "value with spaces", // Invalid: contains spaces
			},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname": "test-host",
					},
				},
			},
			expected: map[string]string{
				"valid-label": "valid-value",
				"alias":       "test-host",
				// "invalid-label" is skipped
			},
		},
		{
			name: "When hostname contains spaces it should be sanitized",
			labelFromSystemInfo: map[string]string{
				"arch": "architecture",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					Architecture: "x86_64",
					AdditionalProperties: map[string]string{
						"hostname": "my host name",
					},
				},
			},
			expected: map[string]string{
				"arch":  "x86_64",
				"alias": "my-host-name", // Sanitized hostname
			},
		},
		{
			name: "When sanitization results in empty string it should skip the label",
			labelFromSystemInfo: map[string]string{
				"bad-label": "customField",
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					AdditionalProperties: map[string]string{
						"hostname":    "test-host",
						"customField": "!!!", // Cannot be sanitized to valid label
					},
				},
			},
			expected: map[string]string{
				"alias": "test-host",
				// "bad-label" is skipped because sanitization results in empty string
			},
		},
		{
			name: "When label-from-systeminfo has invalid key it should be skipped",
			labelFromSystemInfo: map[string]string{
				"valid-key":           "architecture",
				"bad key with spaces": "distroName", // Invalid key
			},
			defaultLabels: map[string]string{},
			deviceStatus: &v1beta1.DeviceStatus{
				SystemInfo: v1beta1.DeviceSystemInfo{
					Architecture: "x86_64",
					AdditionalProperties: map[string]string{
						"hostname":   "test-host",
						"distroName": "CentOS Stream",
					},
				},
			},
			expected: map[string]string{
				"valid-key": "x86_64",
				"alias":     "test-host",
				// "bad key with spaces" is skipped due to invalid key
			},
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
