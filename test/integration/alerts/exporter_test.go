package alert_exporter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/alert_exporter"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

var (
	suiteCtx context.Context
)

func TestExporterIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Alert Exporter Integration Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Tasks Suite")
})

var _ = Describe("Alert Exporter", func() {
	var (
		log               *logrus.Logger
		ctx               context.Context
		storeInst         store.Store
		serviceHandler    service.Service
		cfg               *config.Config
		db                *gorm.DB
		dbName            string
		workerClient      worker_client.WorkerClient
		mockProducer      *queues.MockQueueProducer
		ctrl              *gomock.Controller
		checkpointManager *alert_exporter.CheckpointManager
		eventProcessor    *alert_exporter.EventProcessor
		alertSender       *alert_exporter.AlertSender
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, db = store.PrepareDBForUnitTests(ctx, log)
		ctrl = gomock.NewController(GinkgoT())
		mockProducer = queues.NewMockQueueProducer(ctrl)
		workerClient = worker_client.NewWorkerClient(mockProducer, log)
		mockProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, workerClient, kvStore, nil, log, "", "", []string{})
		checkpointManager = alert_exporter.NewCheckpointManager(log, serviceHandler)
		eventProcessor = alert_exporter.NewEventProcessor(log, serviceHandler)
		alertSender = alert_exporter.NewAlertSender(log, cfg.Alertmanager.Hostname, cfg.Alertmanager.Port, cfg)

		err = db.WithContext(ctx).Exec(`
				DELETE FROM checkpoints
				WHERE consumer = ? AND key = ?`, alert_exporter.AlertCheckpointConsumer, alert_exporter.AlertCheckpointKey).Error
		Expect(err).ToNot(HaveOccurred())
		err = db.WithContext(ctx).Exec(`DELETE FROM events`).Error
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
		ctrl.Finish()
	})

	Context("Basic Alert Operations", func() {
		It("publishes an alert when a relevant event occurs", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "publishAlert"

			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonResourceCreated, api.FleetKind, fmt.Sprintf("%s-flt1", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceConnected, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))

			metrics := &alert_exporter.ProcessingMetrics{}
			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, metrics)
			Expect(err).ToNot(HaveOccurred())
			err = alertSender.SendAlerts(checkpoint)
			Expect(err).ToNot(HaveOccurred())

			alerts, err := getAlerts(cfg, prefix)
			Expect(err).ToNot(HaveOccurred())
			Expect(alerts).To(HaveLen(1))
			Expect(alerts[0].Labels).To(HaveKeyWithValue("resource", fmt.Sprintf("%s-dev1", prefix)))
			Expect(alerts[0].Labels).To(HaveKeyWithValue("alertname", "DeviceCPUWarning"))
			Expect(alerts[0].StartsAt).ToNot(BeZero())
			Expect(alerts[0].Status.State).To(Equal("active"))
		})

		It("clears an alert when the resource is deleted", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "clearAlertWhenDeleted"

			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			metrics := &alert_exporter.ProcessingMetrics{}
			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, metrics)
			Expect(err).ToNot(HaveOccurred())
			err = alertSender.SendAlerts(checkpoint)
			Expect(err).ToNot(HaveOccurred())

			alerts, err := getAlerts(cfg, prefix)
			Expect(err).ToNot(HaveOccurred())
			Expect(alerts).To(HaveLen(1))
			Expect(alerts[0].Labels).To(HaveKeyWithValue("resource", fmt.Sprintf("%s-dev1", prefix)))
			Expect(alerts[0].Labels).To(HaveKeyWithValue("alertname", "DeviceCPUWarning"))
			Expect(alerts[0].StartsAt).ToNot(BeZero())
			Expect(alerts[0].Status.State).To(Equal("active"))

			createEvent(ctx, serviceHandler, api.EventReasonResourceDeleted, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			metrics = &alert_exporter.ProcessingMetrics{}
			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, metrics)
			Expect(err).ToNot(HaveOccurred())
			err = alertSender.SendAlerts(checkpoint)
			Expect(err).ToNot(HaveOccurred())

			alerts, err = getAlerts(cfg, prefix)
			Expect(err).ToNot(HaveOccurred())
			Expect(alerts).To(HaveLen(0))
		})

		It("clears alerts when they are resolved", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "clearAlertWhenResolved"

			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUCritical, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceMemoryCritical, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceDiskCritical, api.DeviceKind, fmt.Sprintf("%s-dev3", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceApplicationError, api.DeviceKind, fmt.Sprintf("%s-dev4", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceDisconnected, api.DeviceKind, fmt.Sprintf("%s-dev5", prefix))

			metrics := &alert_exporter.ProcessingMetrics{}
			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, metrics)
			Expect(err).ToNot(HaveOccurred())
			err = alertSender.SendAlerts(checkpoint)
			Expect(err).ToNot(HaveOccurred())

			alerts, err := getAlerts(cfg, prefix)
			Expect(err).ToNot(HaveOccurred())
			Expect(alerts).To(HaveLen(5))
			// Check that the 5 alerts have the correct labels
			Expect(alerts).To(ConsistOf(
				MatchFields(IgnoreExtras, Fields{
					"Labels": SatisfyAll(
						HaveKeyWithValue("resource", fmt.Sprintf("%s-dev1", prefix)),
						HaveKeyWithValue("alertname", "DeviceCPUCritical"),
					),
					"StartsAt": Not(BeZero()),
					"Status": MatchFields(IgnoreExtras, Fields{
						"State": Equal("active"),
					}),
				}),
				MatchFields(IgnoreExtras, Fields{
					"Labels": SatisfyAll(
						HaveKeyWithValue("resource", fmt.Sprintf("%s-dev2", prefix)),
						HaveKeyWithValue("alertname", "DeviceMemoryCritical"),
					),
					"StartsAt": Not(BeZero()),
					"Status": MatchFields(IgnoreExtras, Fields{
						"State": Equal("active"),
					}),
				}),
				MatchFields(IgnoreExtras, Fields{
					"Labels": SatisfyAll(
						HaveKeyWithValue("resource", fmt.Sprintf("%s-dev3", prefix)),
						HaveKeyWithValue("alertname", "DeviceDiskCritical"),
					),
					"StartsAt": Not(BeZero()),
					"Status": MatchFields(IgnoreExtras, Fields{
						"State": Equal("active"),
					}),
				}),
				MatchFields(IgnoreExtras, Fields{
					"Labels": SatisfyAll(
						HaveKeyWithValue("resource", fmt.Sprintf("%s-dev4", prefix)),
						HaveKeyWithValue("alertname", "DeviceApplicationError"),
					),
					"StartsAt": Not(BeZero()),
					"Status": MatchFields(IgnoreExtras, Fields{
						"State": Equal("active"),
					}),
				}),
				MatchFields(IgnoreExtras, Fields{
					"Labels": SatisfyAll(
						HaveKeyWithValue("resource", fmt.Sprintf("%s-dev5", prefix)),
						HaveKeyWithValue("alertname", "DeviceDisconnected"),
					),
					"StartsAt": Not(BeZero()),
					"Status": MatchFields(IgnoreExtras, Fields{
						"State": Equal("active"),
					}),
				}),
			))

			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUNormal, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceMemoryNormal, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceDiskNormal, api.DeviceKind, fmt.Sprintf("%s-dev3", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceApplicationHealthy, api.DeviceKind, fmt.Sprintf("%s-dev4", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceConnected, api.DeviceKind, fmt.Sprintf("%s-dev5", prefix))
			metrics = &alert_exporter.ProcessingMetrics{}
			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, metrics)
			Expect(err).ToNot(HaveOccurred())
			err = alertSender.SendAlerts(checkpoint)
			Expect(err).ToNot(HaveOccurred())

			alerts, err = getAlerts(cfg, prefix)
			Expect(err).ToNot(HaveOccurred())
			Expect(alerts).To(HaveLen(0))
		})
	})

	Context("Checkpoint Recovery Scenarios", func() {
		It("replays events if the checkpoint is deleted", func() {
			replayEventsFromFreshState(ctx, "replayEventsIfCheckpointDeleted", db, serviceHandler, checkpointManager, eventProcessor, alertSender, func() bool {
				err := db.WithContext(ctx).Exec(`
					DELETE FROM checkpoints
					WHERE consumer = ? AND key = ?`, alert_exporter.AlertCheckpointConsumer, alert_exporter.AlertCheckpointKey).Error
				Expect(err).ToNot(HaveOccurred())
				return true
			})
		})

		It("replays events if the checkpoint is garbage", func() {
			replayEventsFromFreshState(ctx, "replayEventsIfCheckpointGarbage", db, serviceHandler, checkpointManager, eventProcessor, alertSender, func() bool {
				err := db.WithContext(ctx).Exec(`
					UPDATE checkpoints SET value = 'corrupted json here'
					WHERE consumer = ? AND key = ?`, alert_exporter.AlertCheckpointConsumer, alert_exporter.AlertCheckpointKey).Error
				Expect(err).ToNot(HaveOccurred())
				return true
			})
		})

		It("starts fresh if the checkpoint and all events are deleted", func() {
			replayEventsFromFreshState(ctx, "replayEventsIfDBDeleted", db, serviceHandler, checkpointManager, eventProcessor, alertSender, func() bool {
				err := db.WithContext(ctx).Exec(`
					DELETE FROM checkpoints WHERE consumer = ? AND key = ?`, alert_exporter.AlertCheckpointConsumer, alert_exporter.AlertCheckpointKey).Error
				Expect(err).ToNot(HaveOccurred())

				err = db.WithContext(ctx).Exec(`DELETE FROM events`).Error
				Expect(err).ToNot(HaveOccurred())
				return false
			})
		})
	})

	Context("Error Handling Scenarios", func() {
		It("handles Alertmanager being unreachable", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "alertmanagerUnreachable"

			// Create an alert
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).ToNot(HaveOccurred())

			// Create AlertSender with invalid hostname to simulate unreachable Alertmanager
			badAlertSender := alert_exporter.NewAlertSender(log, "invalid-hostname", 9999, cfg)

			// This should fail but not crash
			err = badAlertSender.SendAlerts(checkpoint)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("error sending alerts"))
		})

		It("handles Alertmanager returning HTTP errors", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "alertmanagerHTTPError"

			// Create an alert
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).ToNot(HaveOccurred())

			// Mock Alertmanager with wrong port (should return connection refused)
			badAlertSender := alert_exporter.NewAlertSender(log, "localhost", 9999, cfg)

			err = badAlertSender.SendAlerts(checkpoint)
			Expect(err).To(HaveOccurred())
			// Should contain connection error details
			Expect(err.Error()).To(Or(
				ContainSubstring("connection refused"),
				ContainSubstring("error sending alerts"),
				ContainSubstring("dial tcp"),
			))
		})

		It("continues processing after partial failures", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "partialFailure"

			// Create alerts
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceMemoryWarning, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))

			// Process events successfully
			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(checkpoint.Alerts)).To(BeNumerically(">", 0))

			// Even if alert sending fails, the checkpoint should contain the alerts
			badAlertSender := alert_exporter.NewAlertSender(log, "invalid-hostname", 9999, cfg)
			err = badAlertSender.SendAlerts(checkpoint)
			Expect(err).To(HaveOccurred())

			// Verify checkpoint still contains the processed alerts
			Expect(len(checkpoint.Alerts)).To(BeNumerically(">", 0))

			// Recovery: proper alert sender should work
			err = alertSender.SendAlerts(checkpoint)
			Expect(err).ToNot(HaveOccurred())
		})

		It("handles database connection failures gracefully", func() {
			// This test verifies that database errors are properly propagated
			// rather than causing panics or silent failures

			// Close the database connection to simulate failure
			sqlDB, err := db.DB()
			Expect(err).ToNot(HaveOccurred())
			sqlDB.Close()

			checkpoint := checkpointManager.LoadCheckpoint(ctx)

			// This should fail with a clear database error
			_, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to list organizations"))
		})

		It("recovers from malformed checkpoint data", func() {
			// Insert malformed JSON checkpoint
			err := db.WithContext(ctx).Exec(`
				INSERT INTO checkpoints (consumer, key, value, created_at, updated_at) 
				VALUES (?, ?, ?, NOW(), NOW()) 
				ON CONFLICT (consumer, key) DO UPDATE SET value = EXCLUDED.value`,
				alert_exporter.AlertCheckpointConsumer,
				alert_exporter.AlertCheckpointKey,
				`{"malformed": json data here}`).Error
			Expect(err).ToNot(HaveOccurred())

			// LoadCheckpoint should handle malformed data gracefully by starting fresh
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			Expect(checkpoint).ToNot(BeNil())
			Expect(checkpoint.Version).To(Equal(alert_exporter.CurrentAlertCheckpointVersion))
			Expect(checkpoint.Alerts).To(BeEmpty())

			// Should be able to process normally after recovery
			prefix := "malformedRecovery"
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))

			newCheckpoint, err := eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(newCheckpoint.Alerts)).To(BeNumerically(">", 0))
		})

		It("handles events with missing required fields", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)

			// Create event with nil timestamp
			ev := &api.Event{
				Reason:         api.EventReasonDeviceCPUWarning,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: "test-device"},
				Metadata:       api.ObjectMeta{Name: lo.ToPtr("test-event-nil-timestamp")},
				// CreationTimestamp is nil
			}
			serviceHandler.CreateEvent(ctx, ev)

			// Create event with empty object name
			ev2 := &api.Event{
				Reason:         api.EventReasonDeviceMemoryWarning,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: ""}, // Empty name
				Metadata: api.ObjectMeta{
					Name:              lo.ToPtr("test-event-empty-name"),
					CreationTimestamp: lo.ToPtr(time.Now()),
				},
			}
			serviceHandler.CreateEvent(ctx, ev2)

			// Create valid event
			createEvent(ctx, serviceHandler, api.EventReasonDeviceDiskWarning, api.DeviceKind, "valid-device")

			// Processing should skip invalid events but process valid ones
			newCheckpoint, err := eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).ToNot(HaveOccurred())

			// Should have 2 alerts: the nil timestamp event gets auto-populated by GORM
			// so only the empty name event gets skipped
			totalAlerts := 0
			for _, reasons := range newCheckpoint.Alerts {
				totalAlerts += len(reasons)
			}
			Expect(totalAlerts).To(Equal(2))
		})

		It("handles high volume of events without timeout", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "highVolume"

			// Create many events to test performance - each for a different device
			numDevices := 50 // Reduced for integration test speed
			for i := 0; i < numDevices; i++ {
				deviceName := fmt.Sprintf("%s-device%d", prefix, i)
				// Each device gets one type of alert to ensure we get numDevices alerts
				createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, deviceName)
			}

			// Processing should complete within reasonable time
			startTime := time.Now()
			newCheckpoint, err := eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			processingTime := time.Since(startTime)

			Expect(err).ToNot(HaveOccurred())
			Expect(processingTime).To(BeNumerically("<", 30*time.Second)) // Should complete in under 30s

			// Verify all devices have alerts (one alert per device)
			totalAlerts := 0
			for _, reasons := range newCheckpoint.Alerts {
				totalAlerts += len(reasons)
			}
			Expect(totalAlerts).To(Equal(numDevices))
		})

		It("handles checkpoint storage failures gracefully", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "checkpointFailure"

			// Process some events
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			newCheckpoint, err := eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).ToNot(HaveOccurred())

			// Close DB to simulate storage failure
			sqlDB, err := db.DB()
			Expect(err).ToNot(HaveOccurred())
			sqlDB.Close()

			// Storing checkpoint should fail gracefully
			err = checkpointManager.StoreCheckpoint(ctx, newCheckpoint)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to store checkpoint"))
		})
	})

	Context("Alert Deduplication and Edge Cases", func() {
		It("handles duplicate alerts correctly", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "duplicateAlerts"

			deviceName := fmt.Sprintf("%s-dev1", prefix)

			// Send same alert multiple times
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, deviceName)
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, deviceName)
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, deviceName)

			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).ToNot(HaveOccurred())
			err = alertSender.SendAlerts(checkpoint)
			Expect(err).ToNot(HaveOccurred())

			alerts, err := getAlerts(cfg, prefix)
			Expect(err).ToNot(HaveOccurred())
			// Should have only 1 active alert despite multiple duplicate events
			Expect(alerts).To(HaveLen(1))
			Expect(alerts[0].Labels).To(HaveKeyWithValue("resource", deviceName))
			Expect(alerts[0].Labels).To(HaveKeyWithValue("alertname", "DeviceCPUWarning"))
		})

		It("handles rapid alert state transitions", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "rapidTransitions"

			deviceName := fmt.Sprintf("%s-dev1", prefix)

			// Rapid state changes: Warning → Critical → Normal → Warning
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, deviceName)
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUCritical, api.DeviceKind, deviceName)
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUNormal, api.DeviceKind, deviceName)
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, deviceName)

			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).ToNot(HaveOccurred())
			err = alertSender.SendAlerts(checkpoint)
			Expect(err).ToNot(HaveOccurred())

			alerts, err := getAlerts(cfg, prefix)
			Expect(err).ToNot(HaveOccurred())
			// Should end up with only the final Warning alert
			Expect(alerts).To(HaveLen(1))
			Expect(alerts[0].Labels).To(HaveKeyWithValue("alertname", "DeviceCPUWarning"))
		})

		It("processes non-alertable events without creating alerts", func() {
			var err error
			checkpoint := checkpointManager.LoadCheckpoint(ctx)
			prefix := "nonAlertable"

			// These events should not create alerts
			createEvent(ctx, serviceHandler, api.EventReasonResourceCreated, api.FleetKind, fmt.Sprintf("%s-fleet1", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonResourceUpdated, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceContentOutOfDate, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))
			createEvent(ctx, serviceHandler, api.EventReasonDeviceContentUpdating, api.DeviceKind, fmt.Sprintf("%s-dev3", prefix))

			// Add one alertable event to ensure processing works
			createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev4", prefix))

			checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
			Expect(err).ToNot(HaveOccurred())
			err = alertSender.SendAlerts(checkpoint)
			Expect(err).ToNot(HaveOccurred())

			alerts, err := getAlerts(cfg, prefix)
			Expect(err).ToNot(HaveOccurred())
			// Should have only 1 alert from the CPU warning, not from the non-alertable events
			Expect(alerts).To(HaveLen(1))
			Expect(alerts[0].Labels).To(HaveKeyWithValue("alertname", "DeviceCPUWarning"))
		})
	})
})

func createEvent(ctx context.Context, handler service.Service, reason api.EventReason, kind, name string) {
	ev := &api.Event{
		Reason:         reason,
		InvolvedObject: api.ObjectReference{Kind: kind, Name: name},
		Metadata:       api.ObjectMeta{Name: lo.ToPtr(fmt.Sprintf("test-event-%d", rand.Int63()))}, //nolint:gosec
	}
	handler.CreateEvent(ctx, ev)
}

type AlertmanagerAlert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Status       struct {
		State string `json:"state"` // "active" or "suppressed"
	} `json:"status"`
}

func getAlerts(cfg *config.Config, prefix string) ([]AlertmanagerAlert, error) {
	alertmanagerURL := fmt.Sprintf("http://%s:%d/api/v2/alerts", cfg.Alertmanager.Hostname, cfg.Alertmanager.Port)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(alertmanagerURL)
	if err != nil {
		return nil, fmt.Errorf("error querying Alertmanager at %s: %w", alertmanagerURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status from Alertmanager: %d", resp.StatusCode)
	}

	var alerts []AlertmanagerAlert
	if err := json.NewDecoder(resp.Body).Decode(&alerts); err != nil {
		return nil, fmt.Errorf("error decoding Alertmanager response: %w", err)
	}

	var activeAlerts []AlertmanagerAlert
	for _, alert := range alerts {
		if strings.HasPrefix(alert.Labels["resource"], prefix) {
			activeAlerts = append(activeAlerts, alert)
		}
	}
	return activeAlerts, nil
}

func replayEventsFromFreshState(ctx context.Context, prefix string, db *gorm.DB, serviceHandler service.Service, checkpointManager *alert_exporter.CheckpointManager, eventProcessor *alert_exporter.EventProcessor, alertSender *alert_exporter.AlertSender, checkpointSetup func() bool) {
	// Add an alert for dev1
	var err error
	checkpoint := checkpointManager.LoadCheckpoint(ctx)
	createEvent(ctx, serviceHandler, api.EventReasonDeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))

	checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint, &alert_exporter.ProcessingMetrics{})
	Expect(err).ToNot(HaveOccurred())
	err = alertSender.SendAlerts(checkpoint)
	Expect(err).ToNot(HaveOccurred())
	err = checkpointManager.StoreCheckpoint(ctx, checkpoint)
	Expect(err).ToNot(HaveOccurred())

	// Verify alert for dev1 exists
	checkpointBytes, status := serviceHandler.GetCheckpoint(ctx, alert_exporter.AlertCheckpointConsumer, alert_exporter.AlertCheckpointKey)
	Expect(status.Code).To(Equal(int32(http.StatusOK)))
	Expect(checkpointBytes).ToNot(BeNil())
	Expect(string(checkpointBytes)).To(ContainSubstring(`"DeviceCPUWarning"`))

	// Apply scenario-specific setup (e.g., delete or corrupt checkpoint)
	firstAlertShouldExist := checkpointSetup()

	// Replay events for dev2
	newCheckpoint := checkpointManager.LoadCheckpoint(ctx)
	createEvent(ctx, serviceHandler, api.EventReasonDeviceMemoryWarning, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))

	newCheckpoint, err = eventProcessor.ProcessLatestEvents(ctx, newCheckpoint, &alert_exporter.ProcessingMetrics{})
	Expect(err).ToNot(HaveOccurred())
	err = alertSender.SendAlerts(newCheckpoint)
	Expect(err).ToNot(HaveOccurred())
	err = checkpointManager.StoreCheckpoint(ctx, newCheckpoint)
	Expect(err).ToNot(HaveOccurred())

	// Validate both dev1 and dev2 alerts are present
	checkpointBytes, status = serviceHandler.GetCheckpoint(ctx, alert_exporter.AlertCheckpointConsumer, alert_exporter.AlertCheckpointKey)
	Expect(status.Code).To(Equal(int32(http.StatusOK)))
	Expect(checkpointBytes).ToNot(BeNil())
	Expect(string(checkpointBytes)).To(ContainSubstring(`"DeviceMemoryWarning"`))
	if firstAlertShouldExist {
		Expect(string(checkpointBytes)).To(ContainSubstring(`"DeviceCPUWarning"`))
	} else {
		Expect(string(checkpointBytes)).ToNot(ContainSubstring(`"DeviceCPUWarning"`))
	}
}
