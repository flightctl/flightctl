package tasks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	enrollmenthooknotifysecrets "github.com/flightctl/flightctl/internal/store/enrollmenthooknotifysecrets"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// generateTestCert creates a self-signed certificate with the given serial
// number and returns its PEM encoding.
func generateTestCert(t *testing.T, serial *big.Int) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
}

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		wantRetry   bool
		wantMessage string
	}{
		{
			name:        "When 200 OK it should be success",
			statusCode:  200,
			wantRetry:   false,
			wantMessage: "",
		},
		{
			name:        "When 204 No Content it should be success",
			statusCode:  204,
			wantRetry:   false,
			wantMessage: "",
		},
		{
			name:        "When 400 Bad Request it should be non-retryable",
			statusCode:  400,
			wantRetry:   false,
			wantMessage: "non-retryable HTTP 400",
		},
		{
			name:        "When 401 Unauthorized it should be non-retryable",
			statusCode:  401,
			wantRetry:   false,
			wantMessage: "non-retryable HTTP 401",
		},
		{
			name:        "When 403 Forbidden it should be non-retryable",
			statusCode:  403,
			wantRetry:   false,
			wantMessage: "non-retryable HTTP 403",
		},
		{
			name:        "When 404 Not Found it should be non-retryable",
			statusCode:  404,
			wantRetry:   false,
			wantMessage: "non-retryable HTTP 404",
		},
		{
			name:        "When 429 Too Many Requests it should be retryable",
			statusCode:  429,
			wantRetry:   true,
			wantMessage: "retryable HTTP 429",
		},
		{
			name:        "When 500 Internal Server Error it should be retryable",
			statusCode:  500,
			wantRetry:   true,
			wantMessage: "retryable HTTP 500",
		},
		{
			name:        "When 502 Bad Gateway it should be retryable",
			statusCode:  502,
			wantRetry:   true,
			wantMessage: "retryable HTTP 502",
		},
		{
			name:        "When 503 Service Unavailable it should be retryable",
			statusCode:  503,
			wantRetry:   true,
			wantMessage: "retryable HTTP 503",
		},
		{
			name:        "When 408 Request Timeout it should be non-retryable",
			statusCode:  408,
			wantRetry:   false,
			wantMessage: "non-retryable HTTP 408",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retryable, message := classifyHTTPStatus(tt.statusCode)
			assert.Equal(t, tt.wantRetry, retryable)
			assert.Equal(t, tt.wantMessage, message)
		})
	}
}

func TestBuildEnrollmentApprovedPayload(t *testing.T) {
	t.Run("When device has labels it should include them", func(t *testing.T) {
		labels := map[string]string{"env": "prod", "region": "us-west"}
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("test-device"),
				Labels: &labels,
			},
		}
		data, err := buildEnrollmentApprovedPayload(device, "test-device", "AA:BB:CC")
		require.NoError(t, err)

		var result enrollmentApprovedPayload
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Equal(t, "v1beta1", result.APIVersion)
		assert.Equal(t, "EnrollmentApproved", result.Kind)
		assert.Equal(t, "test-device", result.DeviceName)
		assert.Equal(t, "AA:BB:CC", result.CertificateSerial)
		require.NotNil(t, result.Labels)
		assert.Equal(t, "prod", (*result.Labels)["env"])
	})

	t.Run("When device has no labels it should omit them", func(t *testing.T) {
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr("test-device"),
			},
		}
		data, err := buildEnrollmentApprovedPayload(device, "test-device", "")
		require.NoError(t, err)

		var result enrollmentApprovedPayload
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Nil(t, result.Labels)
		assert.Empty(t, result.CertificateSerial)
	})

	t.Run("When device is nil it should still produce valid JSON", func(t *testing.T) {
		data, err := buildEnrollmentApprovedPayload(nil, "test-device", "FF")
		require.NoError(t, err)

		var result enrollmentApprovedPayload
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Equal(t, "test-device", result.DeviceName)
		assert.Equal(t, "FF", result.CertificateSerial)
	})
}

func TestParseCertificateSerial(t *testing.T) {
	t.Run("When certificate has a multi-byte serial it should return colon-hex", func(t *testing.T) {
		certPEM := generateTestCert(t, big.NewInt(0x1A2B3C))
		serial, err := parseCertificateSerial(certPEM)
		require.NoError(t, err)
		assert.Equal(t, "1A:2B:3C", serial)
	})

	t.Run("When certificate has serial 1 it should return 01", func(t *testing.T) {
		certPEM := generateTestCert(t, big.NewInt(1))
		serial, err := parseCertificateSerial(certPEM)
		require.NoError(t, err)
		assert.Equal(t, "01", serial)
	})

	t.Run("When certificate has a large serial it should format correctly", func(t *testing.T) {
		certPEM := generateTestCert(t, big.NewInt(0xDEADBEEF))
		serial, err := parseCertificateSerial(certPEM)
		require.NoError(t, err)
		assert.Equal(t, "DE:AD:BE:EF", serial)
	})

	t.Run("When PEM is invalid it should return error", func(t *testing.T) {
		_, err := parseCertificateSerial("not-a-pem")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PEM block")
	})

	t.Run("When PEM contains invalid certificate it should return error", func(t *testing.T) {
		badPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-a-cert")}))
		_, err := parseCertificateSerial(badPEM)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse certificate")
	})
}

func TestResolveRetryConfig(t *testing.T) {
	t.Run("When no retry policy it should return defaults", func(t *testing.T) {
		action := domain.EnrollmentHookSnapshotAction{Url: "https://example.com"}
		maxAttempts, deadline, maxBackoff, backoffDelay, timeout := resolveRetryConfig(action)
		assert.Equal(t, 5, maxAttempts)
		assert.Equal(t, 10*time.Minute, deadline)
		assert.Equal(t, 2*time.Minute, maxBackoff)
		assert.Equal(t, 2*time.Second, backoffDelay)
		assert.Equal(t, 30*time.Second, timeout)
	})

	t.Run("When retry policy overrides all values it should use them", func(t *testing.T) {
		action := domain.EnrollmentHookSnapshotAction{
			Url:     "https://example.com",
			Timeout: lo.ToPtr("45s"),
			Retry: &domain.EnrollmentHookRetryPolicy{
				MaxAttempts:  lo.ToPtr(3),
				Deadline:     lo.ToPtr("5m"),
				MaxBackoff:   lo.ToPtr("1m"),
				BackoffDelay: lo.ToPtr("1s"),
			},
		}
		maxAttempts, deadline, maxBackoff, backoffDelay, timeout := resolveRetryConfig(action)
		assert.Equal(t, 3, maxAttempts)
		assert.Equal(t, 5*time.Minute, deadline)
		assert.Equal(t, 1*time.Minute, maxBackoff)
		assert.Equal(t, 1*time.Second, backoffDelay)
		assert.Equal(t, 45*time.Second, timeout)
	})

	t.Run("When retry policy has partial overrides it should merge with defaults", func(t *testing.T) {
		action := domain.EnrollmentHookSnapshotAction{
			Url: "https://example.com",
			Retry: &domain.EnrollmentHookRetryPolicy{
				MaxAttempts: lo.ToPtr(10),
			},
		}
		maxAttempts, deadline, _, _, _ := resolveRetryConfig(action)
		assert.Equal(t, 10, maxAttempts)
		assert.Equal(t, 10*time.Minute, deadline) // default
	})

	t.Run("When durations are zero or negative it should keep defaults", func(t *testing.T) {
		action := domain.EnrollmentHookSnapshotAction{
			Url:     "https://example.com",
			Timeout: lo.ToPtr("0s"),
			Retry: &domain.EnrollmentHookRetryPolicy{
				Deadline:     lo.ToPtr("-1s"),
				MaxBackoff:   lo.ToPtr("0s"),
				BackoffDelay: lo.ToPtr("-5ms"),
			},
		}
		_, deadline, maxBackoff, backoffDelay, timeout := resolveRetryConfig(action)
		assert.Equal(t, 10*time.Minute, deadline)
		assert.Equal(t, 2*time.Minute, maxBackoff)
		assert.Equal(t, 2*time.Second, backoffDelay)
		assert.Equal(t, 30*time.Second, timeout)
	})
}

func TestExecuteNotifyAction_HTTPSOnly(t *testing.T) {
	t.Run("When URL is HTTP it should reject as non-retryable", func(t *testing.T) {
		action := domain.EnrollmentHookSnapshotAction{
			Url: "http://example.com/hook",
		}
		log := logrus.NewEntry(logrus.New())
		err := executeNotifyAction(context.Background(), action, "", nil, "test/0", log, newNotifyHTTPClient())
		require.Error(t, err)
		assert.True(t, isNonRetryableError(err))
		assert.Contains(t, err.Error(), "insecure or invalid URL rejected")
	})

	t.Run("When URL has no scheme it should reject as non-retryable", func(t *testing.T) {
		action := domain.EnrollmentHookSnapshotAction{
			Url: "example.com/hook",
		}
		log := logrus.NewEntry(logrus.New())
		err := executeNotifyAction(context.Background(), action, "", nil, "test/0", log, newNotifyHTTPClient())
		require.Error(t, err)
		assert.True(t, isNonRetryableError(err))
		assert.Contains(t, err.Error(), "insecure or invalid URL rejected")
	})
}

func TestExecuteNotifyAction_WithTLSServer(t *testing.T) {
	t.Run("When server returns 200 it should succeed", func(t *testing.T) {
		var receivedHeaders http.Header
		var receivedBody string
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedHeaders = r.Header
			body := make([]byte, 1024)
			n, _ := r.Body.Read(body)
			receivedBody = string(body[:n])
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		action := domain.EnrollmentHookSnapshotAction{
			Url:     server.URL,
			Timeout: lo.ToPtr("5s"),
			Retry:   &domain.EnrollmentHookRetryPolicy{MaxAttempts: lo.ToPtr(1)},
		}

		log := logrus.NewEntry(logrus.New())
		err := executeNotifyAction(context.Background(), action, "test-token", []byte(`{"test":true}`), "dev1/0", log, server.Client())
		require.NoError(t, err)

		assert.Equal(t, "dev1/0", receivedHeaders.Get("X-Flightctl-Delivery-Id"))
		assert.Equal(t, "1", receivedHeaders.Get("X-Flightctl-Delivery-Attempt"))
		assert.Equal(t, "Bearer test-token", receivedHeaders.Get("Authorization"))
		assert.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))
		assert.Equal(t, `{"test":true}`, receivedBody)
	})

	t.Run("When server returns 400 it should fail without retry", func(t *testing.T) {
		var callCount atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()

		action := domain.EnrollmentHookSnapshotAction{
			Url:     server.URL,
			Timeout: lo.ToPtr("5s"),
			Retry:   &domain.EnrollmentHookRetryPolicy{MaxAttempts: lo.ToPtr(3)},
		}

		log := logrus.NewEntry(logrus.New())
		err := executeNotifyAction(context.Background(), action, "", nil, "dev1/0", log, server.Client())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-retryable HTTP 400")
		assert.Equal(t, int32(1), callCount.Load(), "should not retry on 400")
	})

	t.Run("When server returns 500 then 200 it should retry and succeed", func(t *testing.T) {
		var callCount atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := callCount.Add(1)
			if count == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		action := domain.EnrollmentHookSnapshotAction{
			Url:     server.URL,
			Timeout: lo.ToPtr("5s"),
			Retry: &domain.EnrollmentHookRetryPolicy{
				MaxAttempts:  lo.ToPtr(3),
				BackoffDelay: lo.ToPtr("10ms"),
				Deadline:     lo.ToPtr("30s"),
			},
		}

		log := logrus.NewEntry(logrus.New())
		err := executeNotifyAction(context.Background(), action, "", nil, "dev1/0", log, server.Client())
		require.NoError(t, err)
		assert.Equal(t, int32(2), callCount.Load(), "should retry once after 500")
	})

	t.Run("When server returns 500 for all attempts it should exhaust retries", func(t *testing.T) {
		var callCount atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		action := domain.EnrollmentHookSnapshotAction{
			Url:     server.URL,
			Timeout: lo.ToPtr("5s"),
			Retry: &domain.EnrollmentHookRetryPolicy{
				MaxAttempts:  lo.ToPtr(2),
				BackoffDelay: lo.ToPtr("10ms"),
				Deadline:     lo.ToPtr("30s"),
			},
		}

		log := logrus.NewEntry(logrus.New())
		err := executeNotifyAction(context.Background(), action, "", nil, "dev1/0", log, server.Client())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attempts exhausted")
		assert.Equal(t, int32(2), callCount.Load())
	})

	t.Run("When no Authorization header it should omit it", func(t *testing.T) {
		var authHeader string
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		action := domain.EnrollmentHookSnapshotAction{
			Url:     server.URL,
			Timeout: lo.ToPtr("5s"),
			Retry:   &domain.EnrollmentHookRetryPolicy{MaxAttempts: lo.ToPtr(1)},
		}

		log := logrus.NewEntry(logrus.New())
		err := executeNotifyAction(context.Background(), action, "", nil, "dev1/0", log, server.Client())
		require.NoError(t, err)
		assert.Empty(t, authHeader)
	})
}

func TestDeliveryIDStability(t *testing.T) {
	t.Run("When notify runs twice with different event names it should send the same delivery ID", func(t *testing.T) {
		orgId := uuid.New()
		erName := "my-device-123"
		log := logrus.NewEntry(logrus.New())
		statusOK := domain.StatusOK()

		var deliveryIDs []string
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deliveryIDs = append(deliveryIDs, r.Header.Get("X-Flightctl-Delivery-Id"))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		device := &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(erName)},
			Status: &domain.DeviceStatus{
				Conditions: []domain.Condition{{
					Type:   domain.ConditionTypeDeviceEnrollmentHooks,
					Status: domain.ConditionStatusFalse,
					Reason: domain.EnrollmentHooksReasonNotifyPending,
				}},
				EnrollmentHooks: &domain.DeviceEnrollmentHooksStatus{
					Snapshot: &domain.EnrollmentHookSnapshot{
						FailurePolicy: domain.FailurePolicyContinue,
						ControlPlaneActions: &[]domain.EnrollmentHookSnapshotAction{{
							Index:   2,
							Url:     server.URL,
							Timeout: lo.ToPtr("5s"),
							Retry:   &domain.EnrollmentHookRetryPolicy{MaxAttempts: lo.ToPtr(1)},
						}},
					},
				},
			},
		}

		runNotify := func(eventName string) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockDeviceSvc := deviceservice.NewMockService(ctrl)
			mockERSvc := enrollmentrequestservice.NewMockService(ctrl)
			mockSecrets := enrollmenthooknotifysecrets.NewMockStore(ctrl)
			mockEventSvc := eventservice.NewMockService(ctrl)

			mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, erName).Return(device, statusOK)
			mockERSvc.EXPECT().GetEnrollmentRequest(gomock.Any(), orgId, erName).
				Return(nil, domain.StatusResourceNotFound("EnrollmentRequest", erName))
			mockSecrets.EXPECT().ListByDevice(gomock.Any(), orgId, erName).Return(nil, nil)
			mockDeviceSvc.EXPECT().SetDeviceServiceConditions(gomock.Any(), orgId, erName, gomock.Any()).
				Return(statusOK)

			event := domain.Event{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr(eventName)},
				InvolvedObject: domain.ObjectReference{
					Kind: domain.EnrollmentRequestKind,
					Name: erName,
				},
				Reason: domain.EventReasonEnrollmentRequestApproved,
			}

			// Use the TLS test server's client so the notify worker trusts the cert.
			err := enrollmentHookNotifyWithClient(context.Background(), orgId, event, mockDeviceSvc, mockERSvc, mockSecrets, mockEventSvc, log, server.Client())
			require.NoError(t, err)
		}

		runNotify("event-aaa")
		runNotify("event-bbb")

		require.Len(t, deliveryIDs, 2)
		assert.Equal(t, "my-device-123/2", deliveryIDs[0])
		assert.Equal(t, deliveryIDs[0], deliveryIDs[1])
	})
}

func TestEnrollmentHookNotify_EndToEnd(t *testing.T) {
	orgId := uuid.New()
	erName := "test-device"
	log := logrus.NewEntry(logrus.New())
	statusOK := domain.StatusOK()

	makeEvent := func() domain.Event {
		return domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: domain.EnrollmentRequestKind,
				Name: erName,
			},
			Reason: domain.EventReasonEnrollmentRequestApproved,
		}
	}

	t.Run("When device has no enrollment hooks snapshot it should skip", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockERSvc := enrollmentrequestservice.NewMockService(ctrl)
		mockSecrets := enrollmenthooknotifysecrets.NewMockStore(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)

		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, erName).Return(&domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(erName)},
			Status:   &domain.DeviceStatus{},
		}, statusOK)

		err := enrollmentHookNotify(context.Background(), orgId, makeEvent(), mockDeviceSvc, mockERSvc, mockSecrets, mockEventSvc, log)
		require.NoError(t, err)
	})

	t.Run("When condition is not NotifyPending it should skip (idempotency)", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockERSvc := enrollmentrequestservice.NewMockService(ctrl)
		mockSecrets := enrollmenthooknotifysecrets.NewMockStore(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)

		device := &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(erName)},
			Status: &domain.DeviceStatus{
				Conditions: []domain.Condition{{
					Type:   domain.ConditionTypeDeviceEnrollmentHooks,
					Status: domain.ConditionStatusFalse,
					Reason: domain.EnrollmentHooksReasonPending, // not NotifyPending
				}},
				EnrollmentHooks: &domain.DeviceEnrollmentHooksStatus{
					Snapshot: &domain.EnrollmentHookSnapshot{
						ControlPlaneActions: &[]domain.EnrollmentHookSnapshotAction{{
							Index: 0,
							Url:   "https://example.com/hook",
						}},
					},
				},
			},
		}
		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, erName).Return(device, statusOK)

		err := enrollmentHookNotify(context.Background(), orgId, makeEvent(), mockDeviceSvc, mockERSvc, mockSecrets, mockEventSvc, log)
		require.NoError(t, err)
	})

	t.Run("When no control plane actions it should skip and set Pending", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockERSvc := enrollmentrequestservice.NewMockService(ctrl)
		mockSecrets := enrollmenthooknotifysecrets.NewMockStore(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)

		device := &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(erName)},
			Status: &domain.DeviceStatus{
				Conditions: []domain.Condition{{
					Type:   domain.ConditionTypeDeviceEnrollmentHooks,
					Status: domain.ConditionStatusFalse,
					Reason: domain.EnrollmentHooksReasonNotifyPending,
				}},
				EnrollmentHooks: &domain.DeviceEnrollmentHooksStatus{
					Snapshot: &domain.EnrollmentHookSnapshot{
						FailurePolicy: domain.FailurePolicyBlock,
					},
				},
			},
		}
		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, erName).Return(device, statusOK)

		mockDeviceSvc.EXPECT().SetDeviceServiceConditions(gomock.Any(), orgId, erName, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, conditions []domain.Condition) domain.Status {
				require.Len(t, conditions, 1)
				assert.Equal(t, domain.EnrollmentHooksReasonPending, conditions[0].Reason)
				return statusOK
			})

		err := enrollmentHookNotify(context.Background(), orgId, makeEvent(), mockDeviceSvc, mockERSvc, mockSecrets, mockEventSvc, log)
		require.NoError(t, err)
	})

	t.Run("When Block policy and action fails it should set Failed condition", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)

		mockDeviceSvc.EXPECT().SetDeviceServiceConditions(gomock.Any(), orgId, erName, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, conditions []domain.Condition) domain.Status {
				require.Len(t, conditions, 1)
				assert.Equal(t, domain.EnrollmentHooksReasonFailed, conditions[0].Reason)
				assert.Contains(t, conditions[0].Message, "test failure")
				return statusOK
			})

		err := handleNotifyFailure(context.Background(), mockDeviceSvc, mockEventSvc, orgId, erName,
			domain.FailurePolicyBlock, fmt.Errorf("test failure"), log)
		require.Error(t, err)
	})

	t.Run("When condition update fails it should return the error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)

		mockDeviceSvc.EXPECT().SetDeviceServiceConditions(gomock.Any(), orgId, erName, gomock.Any()).
			Return(domain.StatusInternalServerError("db unavailable"))

		err := handleNotifyFailure(context.Background(), mockDeviceSvc, mockEventSvc, orgId, erName,
			domain.FailurePolicyBlock, fmt.Errorf("test failure"), log)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to set EnrollmentHooks condition")
	})

	t.Run("When Continue policy and action fails it should set Pending and emit NotifyFailed", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)

		// Condition-first for redelivery safety
		conditionCall := mockDeviceSvc.EXPECT().SetDeviceServiceConditions(gomock.Any(), orgId, erName, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, conditions []domain.Condition) domain.Status {
				require.Len(t, conditions, 1)
				assert.Equal(t, domain.EnrollmentHooksReasonPending, conditions[0].Reason)
				return statusOK
			})

		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
				assert.Equal(t, domain.EventReasonEnrollmentHookNotifyFailed, event.Reason)
			}).After(conditionCall) // event AFTER condition

		err := handleNotifyFailure(context.Background(), mockDeviceSvc, mockEventSvc, orgId, erName,
			domain.FailurePolicyContinue, fmt.Errorf("test failure"), log)
		require.NoError(t, err, "Continue policy should return nil")
	})

	t.Run("When device not found it should return error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockERSvc := enrollmentrequestservice.NewMockService(ctrl)
		mockSecrets := enrollmenthooknotifysecrets.NewMockStore(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)

		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, erName).
			Return(nil, domain.StatusResourceNotFound("Device", erName))

		err := enrollmentHookNotify(context.Background(), orgId, makeEvent(), mockDeviceSvc, mockERSvc, mockSecrets, mockEventSvc, log)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get device")
	})
}

func TestComputeBackoff(t *testing.T) {
	t.Run("When first attempt it should return base delay", func(t *testing.T) {
		b := computeBackoff(1, 2*time.Second, 2*time.Minute)
		assert.Equal(t, 2*time.Second, b)
	})

	t.Run("When second attempt it should double", func(t *testing.T) {
		b := computeBackoff(2, 2*time.Second, 2*time.Minute)
		assert.Equal(t, 4*time.Second, b)
	})

	t.Run("When backoff exceeds max it should cap", func(t *testing.T) {
		b := computeBackoff(10, 2*time.Second, 30*time.Second)
		assert.Equal(t, 30*time.Second, b)
	})
}
