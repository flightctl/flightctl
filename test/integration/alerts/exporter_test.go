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
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
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
		callbackManager   tasks_client.CallbackManager
		mockPublisher     *queues.MockPublisher
		ctrl              *gomock.Controller
		checkpointManager *alert_exporter.CheckpointManager
		eventProcessor    *alert_exporter.EventProcessor
		alertSender       *alert_exporter.AlertSender
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, db = store.PrepareDBForUnitTests(ctx, log)
		ctrl = gomock.NewController(GinkgoT())
		mockPublisher = queues.NewMockPublisher(ctrl)
		callbackManager = tasks_client.NewCallbackManager(mockPublisher, log)
		mockPublisher.EXPECT().Publish(gomock.Any(), gomock.Any()).AnyTimes()
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, callbackManager, kvStore, nil, log, "", "")
		checkpointManager = alert_exporter.NewCheckpointManager(log, serviceHandler)
		eventProcessor = alert_exporter.NewEventProcessor(log, serviceHandler)
		alertSender = alert_exporter.NewAlertSender(log, cfg.Alertmanager.Hostname, cfg.Alertmanager.Port)

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

	It("publishes an alert when a relevant event occurs", func() {
		var err error
		checkpoint := checkpointManager.LoadCheckpoint(ctx)
		prefix := "publishAlert"

		createEvent(ctx, serviceHandler, api.DeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
		createEvent(ctx, serviceHandler, api.ResourceCreated, api.FleetKind, fmt.Sprintf("%s-flt1", prefix))
		createEvent(ctx, serviceHandler, api.DeviceConnected, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))

		checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint)
		Expect(err).ToNot(HaveOccurred())
		err = alertSender.SendAlerts(checkpoint)
		Expect(err).ToNot(HaveOccurred())

		alerts := getAlerts(prefix)
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

		createEvent(ctx, serviceHandler, api.DeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
		checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint)
		Expect(err).ToNot(HaveOccurred())
		err = alertSender.SendAlerts(checkpoint)
		Expect(err).ToNot(HaveOccurred())

		alerts := getAlerts(prefix)
		Expect(alerts).To(HaveLen(1))
		Expect(alerts[0].Labels).To(HaveKeyWithValue("resource", fmt.Sprintf("%s-dev1", prefix)))
		Expect(alerts[0].Labels).To(HaveKeyWithValue("alertname", "DeviceCPUWarning"))
		Expect(alerts[0].StartsAt).ToNot(BeZero())
		Expect(alerts[0].Status.State).To(Equal("active"))

		createEvent(ctx, serviceHandler, api.ResourceDeleted, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
		checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint)
		Expect(err).ToNot(HaveOccurred())
		err = alertSender.SendAlerts(checkpoint)
		Expect(err).ToNot(HaveOccurred())

		alerts = getAlerts(prefix)
		Expect(alerts).To(HaveLen(0))
	})

	It("clears alerts when they are resolved", func() {
		var err error
		checkpoint := checkpointManager.LoadCheckpoint(ctx)
		prefix := "clearAlertWhenResolved"

		createEvent(ctx, serviceHandler, api.DeviceCPUCritical, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
		createEvent(ctx, serviceHandler, api.DeviceMemoryCritical, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))
		createEvent(ctx, serviceHandler, api.DeviceDiskCritical, api.DeviceKind, fmt.Sprintf("%s-dev3", prefix))
		createEvent(ctx, serviceHandler, api.DeviceApplicationError, api.DeviceKind, fmt.Sprintf("%s-dev4", prefix))
		createEvent(ctx, serviceHandler, api.DeviceDisconnected, api.DeviceKind, fmt.Sprintf("%s-dev5", prefix))

		checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint)
		Expect(err).ToNot(HaveOccurred())
		err = alertSender.SendAlerts(checkpoint)
		Expect(err).ToNot(HaveOccurred())

		alerts := getAlerts(prefix)
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

		createEvent(ctx, serviceHandler, api.DeviceCPUNormal, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))
		createEvent(ctx, serviceHandler, api.DeviceMemoryNormal, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))
		createEvent(ctx, serviceHandler, api.DeviceDiskNormal, api.DeviceKind, fmt.Sprintf("%s-dev3", prefix))
		createEvent(ctx, serviceHandler, api.DeviceApplicationHealthy, api.DeviceKind, fmt.Sprintf("%s-dev4", prefix))
		createEvent(ctx, serviceHandler, api.DeviceConnected, api.DeviceKind, fmt.Sprintf("%s-dev5", prefix))
		checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint)
		Expect(err).ToNot(HaveOccurred())
		err = alertSender.SendAlerts(checkpoint)
		Expect(err).ToNot(HaveOccurred())

		alerts = getAlerts(prefix)
		Expect(alerts).To(HaveLen(0))
	})

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

func getAlerts(prefix string) []AlertmanagerAlert {
	resp, err := http.Get("http://localhost:9093/api/v2/alerts")
	if err != nil {
		GinkgoWriter.Printf("error querying Alertmanager: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		GinkgoWriter.Printf("unexpected status from Alertmanager: %d\n", resp.StatusCode)
		return nil
	}

	var alerts []AlertmanagerAlert
	if err := json.NewDecoder(resp.Body).Decode(&alerts); err != nil {
		return nil
	}

	var activeAlerts []AlertmanagerAlert
	for _, alert := range alerts {
		if strings.HasPrefix(alert.Labels["resource"], prefix) {
			activeAlerts = append(activeAlerts, alert)
		}
	}
	return activeAlerts
}

func replayEventsFromFreshState(ctx context.Context, prefix string, db *gorm.DB, serviceHandler service.Service, checkpointManager *alert_exporter.CheckpointManager, eventProcessor *alert_exporter.EventProcessor, alertSender *alert_exporter.AlertSender, checkpointSetup func() bool) {
	// Add an alert for dev1
	var err error
	checkpoint := checkpointManager.LoadCheckpoint(ctx)
	createEvent(ctx, serviceHandler, api.DeviceCPUWarning, api.DeviceKind, fmt.Sprintf("%s-dev1", prefix))

	checkpoint, err = eventProcessor.ProcessLatestEvents(ctx, checkpoint)
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
	createEvent(ctx, serviceHandler, api.DeviceMemoryWarning, api.DeviceKind, fmt.Sprintf("%s-dev2", prefix))

	newCheckpoint, err = eventProcessor.ProcessLatestEvents(ctx, newCheckpoint)
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
