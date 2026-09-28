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
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	enrollmenthooknotifysecrets "github.com/flightctl/flightctl/internal/store/enrollmenthooknotifysecrets"
	"github.com/flightctl/flightctl/internal/webhookdelivery"
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
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("test-device")},
		}
		data, err := buildEnrollmentApprovedPayload(device, "test-device", "")
		require.NoError(t, err)

		var result enrollmentApprovedPayload
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Nil(t, result.Labels)
		assert.Empty(t, result.CertificateSerial)
	})

	t.Run("When device is nil it should still build payload", func(t *testing.T) {
		data, err := buildEnrollmentApprovedPayload(nil, "test-device", "FF")
		require.NoError(t, err)

		var result enrollmentApprovedPayload
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Equal(t, "test-device", result.DeviceName)
		assert.Equal(t, "FF", result.CertificateSerial)
	})
}

func TestParseCertificateSerial(t *testing.T) {
	t.Run("When cert has multi-byte serial it should format as colon hex", func(t *testing.T) {
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

func TestDeliveryConfigFromAction(t *testing.T) {
	t.Run("When no retry policy it should leave zero fields for defaults", func(t *testing.T) {
		action := domain.EnrollmentHookSnapshotAction{Url: "https://example.com"}
		cfg := deliveryConfigFromAction(action).WithDefaults()
		assert.Equal(t, "https://example.com", cfg.URL)
		assert.Equal(t, webhookdelivery.DefaultMaxAttempts, cfg.MaxAttempts)
		assert.Equal(t, webhookdelivery.DefaultDeadline, cfg.Deadline)
		assert.Equal(t, webhookdelivery.DefaultMaxBackoff, cfg.MaxBackoff)
		assert.Equal(t, webhookdelivery.DefaultBackoffDelay, cfg.BackoffDelay)
		assert.Equal(t, webhookdelivery.DefaultTimeout, cfg.Timeout)
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
		cfg := deliveryConfigFromAction(action).WithDefaults()
		assert.Equal(t, 3, cfg.MaxAttempts)
		assert.Equal(t, 5*time.Minute, cfg.Deadline)
		assert.Equal(t, 1*time.Minute, cfg.MaxBackoff)
		assert.Equal(t, 1*time.Second, cfg.BackoffDelay)
		assert.Equal(t, 45*time.Second, cfg.Timeout)
	})

	t.Run("When retry policy has partial overrides it should merge with defaults", func(t *testing.T) {
		action := domain.EnrollmentHookSnapshotAction{
			Url: "https://example.com",
			Retry: &domain.EnrollmentHookRetryPolicy{
				MaxAttempts: lo.ToPtr(10),
			},
		}
		cfg := deliveryConfigFromAction(action).WithDefaults()
		assert.Equal(t, 10, cfg.MaxAttempts)
		assert.Equal(t, webhookdelivery.DefaultDeadline, cfg.Deadline)
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
		cfg := deliveryConfigFromAction(action).WithDefaults()
		assert.Equal(t, webhookdelivery.DefaultDeadline, cfg.Deadline)
		assert.Equal(t, webhookdelivery.DefaultMaxBackoff, cfg.MaxBackoff)
		assert.Equal(t, webhookdelivery.DefaultBackoffDelay, cfg.BackoffDelay)
		assert.Equal(t, webhookdelivery.DefaultTimeout, cfg.Timeout)
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

		conditionCall := mockDeviceSvc.EXPECT().SetDeviceServiceConditions(gomock.Any(), orgId, erName, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, conditions []domain.Condition) domain.Status {
				require.Len(t, conditions, 1)
				assert.Equal(t, domain.EnrollmentHooksReasonPending, conditions[0].Reason)
				return statusOK
			})

		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
				assert.Equal(t, domain.EventReasonEnrollmentHookNotifyFailed, event.Reason)
			}).After(conditionCall)

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

	t.Run("When worker context is canceled it should abort notify", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockERSvc := enrollmentrequestservice.NewMockService(ctrl)
		mockSecrets := enrollmenthooknotifysecrets.NewMockStore(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)

		started := make(chan struct{})
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			time.Sleep(5 * time.Second)
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
						FailurePolicy: domain.FailurePolicyBlock,
						ControlPlaneActions: &[]domain.EnrollmentHookSnapshotAction{{
							Index:   0,
							Url:     server.URL,
							Timeout: lo.ToPtr("5s"),
							Retry: &domain.EnrollmentHookRetryPolicy{
								MaxAttempts:  lo.ToPtr(1),
								BackoffDelay: lo.ToPtr("1ms"),
							},
						}},
					},
				},
			},
		}

		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, erName).Return(device, statusOK)
		mockERSvc.EXPECT().GetEnrollmentRequest(gomock.Any(), orgId, erName).
			Return(nil, domain.StatusResourceNotFound("EnrollmentRequest", erName))
		mockSecrets.EXPECT().ListByDevice(gomock.Any(), orgId, erName).Return(nil, nil)
		mockDeviceSvc.EXPECT().SetDeviceServiceConditions(gomock.Any(), orgId, erName, gomock.Any()).
			Return(statusOK).AnyTimes()

		workerCtx, cancelWorker := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- enrollmentHookNotifyWithClient(workerCtx, orgId, makeEvent(), mockDeviceSvc, mockERSvc, mockSecrets, mockEventSvc, log, server.Client())
		}()

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("webhook was not reached")
		}
		cancelWorker()

		select {
		case err := <-errCh:
			require.Error(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("notify did not abort after worker cancel")
		}
	})
}
