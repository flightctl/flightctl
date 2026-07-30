package disruption_budget

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rollout/disruption_budget"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

var (
	suiteCtx      context.Context
	redisHost     string
	redisPort     uint
	redisPassword domain.SecureString
	redisCleanup  func()
)

func TestDisruptionBudget(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Disruption budget suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Disruption budget suite")
	Expect(integrationstack.EnsureRunning(suiteCtx)).To(Succeed())

	var err error
	redisHost, redisPort, redisPassword, redisCleanup, err = testdb.CreateTestRedis(
		suiteCtx, flightlog.InitLogs())
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if redisCleanup != nil {
		redisCleanup()
	}
})

var _ = Describe("Rollout disruption budget test", func() {
	const (
		FleetName = "myfleet"
	)
	var (
		ctx              context.Context
		log              *logrus.Logger
		dbName           string
		db               *gorm.DB
		cfg              *config.Config
		fleetStore       fleetstore.Store
		deviceStore      devicestore.Store
		tvStore          templateversionstore.Store
		deviceSvc        deviceservice.Service
		fleetSvc         fleetservice.Service
		eventSvc         eventservice.Service
		ctrl             *gomock.Controller
		mockWorkerClient *worker_client.MockWorkerClient
		tvName           string
		capturedEvents   []api.Event
	)

	disruptionBudget := func(maxUnavailable, minAvailable *int, groupBy *[]string) *api.DisruptionBudget {
		return &api.DisruptionBudget{
			GroupBy:        groupBy,
			MaxUnavailable: maxUnavailable,
			MinAvailable:   minAvailable,
		}
	}
	createTestFleet := func(name string, d *api.DisruptionBudget) *api.Fleet {

		fleet := &api.Fleet{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr(name),
			},
			Spec: api.FleetSpec{
				RolloutPolicy: &api.RolloutPolicy{
					DisruptionBudget: d,
				},
			},
		}

		f, err := fleetStore.Create(ctx, store.NullOrgId, fleet, nil)
		Expect(err).ToNot(HaveOccurred())
		return f
	}

	createTestTemplateVersion := func(ownerName string) {
		templateVersion := api.TemplateVersion{
			Metadata: api.ObjectMeta{
				Name:  util.TimeStampStringPtr(),
				Owner: util.SetResourceOwner(api.FleetKind, ownerName),
			},
			Spec:   api.TemplateVersionSpec{Fleet: ownerName},
			Status: &api.TemplateVersionStatus{},
		}
		tv, err := tvStore.Create(ctx, store.NullOrgId, &templateVersion, nil)
		Expect(err).ToNot(HaveOccurred())
		tvName = *tv.Metadata.Name
		annotations := map[string]string{
			api.FleetAnnotationTemplateVersion: *tv.Metadata.Name,
		}
		Expect(fleetStore.UpdateAnnotations(ctx, store.NullOrgId, FleetName, annotations, nil, nil)).ToNot(HaveOccurred())
	}
	var (
		labels1 = map[string]string{
			"label-1": "value-1",
			"label-2": "value-2",
		}
		labels2 = map[string]string{
			"label-1": "value-3",
			"label-2": "value-2",
		}
	)
	updateDeviceLabels := func(device *api.Device, labels map[string]string) {
		device.Metadata.Labels = &labels
		_, err := deviceStore.Update(ctx, store.NullOrgId, device, nil, nil, nil)
		Expect(err).ToNot(HaveOccurred())
	}

	setLabels := func(labels []map[string]string, numToSet []int) {
		Expect(labels).To(HaveLen(len(numToSet)))
		devices, err := deviceStore.List(ctx, store.NullOrgId, devicestore.DeviceListParams{})
		Expect(err).ToNot(HaveOccurred())
		Expect(len(devices.Items)).To(BeNumerically(">=", lo.Sum(numToSet)))
		offset := 0
		for i := 0; i != len(labels); i++ {
			for j := range devices.Items[offset : offset+numToSet[i]] {
				updateDeviceLabels(&devices.Items[j+offset], labels[i])
			}
			offset += numToSet[i]
		}
	}

	// Helper function to capture events and verify their details
	captureAndVerifyEvents := func(expectedCount int, expectedReason api.EventReason, expectedKind api.ResourceKind) {
		capturedEvents = make([]api.Event, 0, expectedCount)

		// Set up the mock to capture events
		mockWorkerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, orgId interface{}, event *api.Event) error {
				if event != nil {
					capturedEvents = append(capturedEvents, *event)
				}
				return nil
			}).Times(expectedCount)
	}

	// Helper function to verify event details
	verifyEventDetails := func(expectedReason api.EventReason, expectedKind api.ResourceKind) {
		Expect(len(capturedEvents)).To(BeNumerically(">", 0))

		// Log summary of captured events
		log.Infof("Verifying %d captured events", len(capturedEvents))

		for i, event := range capturedEvents {
			// Verify event reason
			Expect(event.Reason).To(Equal(expectedReason), "Event %d reason should match expected value", i)

			// Verify event kind
			Expect(event.InvolvedObject.Kind).To(Equal(string(expectedKind)), "Event %d kind should match expected value", i)

			// Verify event has a device name (since these are device-related events)
			Expect(event.InvolvedObject.Name).ToNot(BeEmpty(), "Event %d should have a device name", i)

			// Verify device identity format (should be like "mydevice-1", "mydevice-2", etc.)
			Expect(event.InvolvedObject.Name).To(MatchRegexp(`^mydevice-\d+$`), "Event %d device name should match expected format 'mydevice-<number>'", i)

			// Verify event details contain fleet information by parsing the details
			if event.Details != nil {
				details, err := event.Details.AsFleetRolloutDeviceSelectedDetails()
				if err == nil {
					Expect(details.FleetName).To(Equal("myfleet"), "Event %d fleet name should match expected value", i)
					Expect(details.TemplateVersion).ToNot(BeEmpty(), "Event %d template version should not be empty", i)

					// Log the parsed details for debugging
					log.Infof("Event %d details - Fleet: %s, TemplateVersion: %s", i, details.FleetName, details.TemplateVersion)
				} else {
					// If we can't parse the details, at least verify the message contains fleet info
					Expect(event.Message).To(ContainSubstring("myfleet"), "Event %d message should contain fleet name", i)
				}
			}

			// Log detailed event information for debugging
			log.Infof("Event %d verified - Device: %s, Kind: %s, Reason: %s",
				i, event.InvolvedObject.Name, event.InvolvedObject.Kind, event.Reason)
		}

		// Additional verification: ensure we have unique device names (no duplicate events for same device)
		deviceNames := make(map[string]bool)
		for _, event := range capturedEvents {
			deviceNames[event.InvolvedObject.Name] = true
		}
		Expect(len(deviceNames)).To(Equal(len(capturedEvents)), "All events should be for different devices (no duplicates)")

		// Log the unique device names for verification
		uniqueDevices := make([]string, 0, len(deviceNames))
		for deviceName := range deviceNames {
			uniqueDevices = append(uniqueDevices, deviceName)
		}
		log.Infof("Event verification completed successfully for %d unique devices: %v", len(deviceNames), uniqueDevices)
	}

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = util.WithOrganizationID(ctx, store.NullOrgId)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		tvStore = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		newFleetStore := fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		newDeviceStore := devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		ctrl = gomock.NewController(GinkgoT())
		mockWorkerClient = worker_client.NewMockWorkerClient(ctrl)
		kvStore, err := kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())

		eventsSvc := events.NewServiceHandler(eventStore, mockWorkerClient, log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(newDeviceStore, newFleetStore, eventsSvc, kvStore, "", log)
		fleetSvc = fleetservice.NewServiceHandler(newFleetStore, eventsSvc, log)
		eventSvc = eventservice.NewServiceHandler(eventStore, eventsSvc)
		capturedEvents = make([]api.Event, 0)
	})
	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		ctrl.Finish()
	})
	Context("Query fleets", func() {
		initTest := func(d *api.DisruptionBudget, numDevices int, annotateTv, annotateRenderedTv bool) {
			_ = createTestFleet(FleetName, d)
			createTestTemplateVersion(FleetName)
			if numDevices > 0 {
				testutil.CreateTestDevices(ctx, numDevices, deviceStore, store.NullOrgId, util.SetResourceOwner(api.FleetKind, FleetName), false)
				devices, err := deviceStore.List(ctx, store.NullOrgId, devicestore.DeviceListParams{})
				Expect(err).ToNot(HaveOccurred())
				for i := range devices.Items {
					d := devices.Items[i]
					d.Status.Summary.Status = "Online"
					_, err = deviceStore.UpdateStatus(ctx, store.NullOrgId, &d, nil)
					Expect(err).ToNot(HaveOccurred())
					annotations := make(map[string]string)
					if annotateTv {
						annotations[api.DeviceAnnotationTemplateVersion] = tvName
					}
					if annotateRenderedTv {
						annotations[api.DeviceAnnotationRenderedTemplateVersion] = tvName
					}
					annotations[api.DeviceAnnotationRenderedVersion] = "5"
					Expect(deviceStore.UpdateAnnotations(ctx, store.NullOrgId, lo.FromPtr(d.Metadata.Name), annotations, nil)).ToNot(HaveOccurred())
					d.Status.Config.RenderedVersion = "5"
					_, err = deviceStore.UpdateStatus(ctx, store.NullOrgId, &d, nil)
					Expect(err).ToNot(HaveOccurred())
				}
			}
		}
		It("One fleet - no devices", func() {
			initTest(nil, 0, false, false)
			reconciler := disruption_budget.NewReconciler(deviceSvc, fleetSvc, eventSvc, log)
			reconciler.Reconcile(ctx, store.NullOrgId)
		})
		It("One fleet - one device no matching fleet", func() {
			initTest(nil, 1, false, false)
			reconciler := disruption_budget.NewReconciler(deviceSvc, fleetSvc, eventSvc, log)
			reconciler.Reconcile(ctx, store.NullOrgId)
		})
		It("One fleet - one device with matching fleet - non matching disruption budget", func() {
			initTest(nil, 1, true, false)
			captureAndVerifyEvents(1, api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
			reconciler := disruption_budget.NewReconciler(deviceSvc, fleetSvc, eventSvc, log)
			reconciler.Reconcile(ctx, store.NullOrgId)
			verifyEventDetails(api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
		})
		It("One fleet - one device no matching fleet", func() {
			initTest(nil, 1, true, true)
			reconciler := disruption_budget.NewReconciler(deviceSvc, fleetSvc, eventSvc, log)
			reconciler.Reconcile(ctx, store.NullOrgId)
		})
		It("One fleet - one device with matching fleet - with matching disruption budget", func() {
			initTest(disruptionBudget(lo.ToPtr(1), lo.ToPtr(1), nil), 1, true, false)
			reconciler := disruption_budget.NewReconciler(deviceSvc, fleetSvc, eventSvc, log)
			captureAndVerifyEvents(1, api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
			reconciler.Reconcile(ctx, store.NullOrgId)
			verifyEventDetails(api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
		})
		It("One fleet - two devices with matching fleet - with matching disruption budget", func() {
			initTest(disruptionBudget(lo.ToPtr(1), lo.ToPtr(1), nil), 2, true, false)
			reconciler := disruption_budget.NewReconciler(deviceSvc, fleetSvc, eventSvc, log)
			captureAndVerifyEvents(1, api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
			reconciler.Reconcile(ctx, store.NullOrgId)
			verifyEventDetails(api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
		})
		It("One fleet - 6 devices with matching fleet - with matching disruption budget - with labels", func() {
			initTest(disruptionBudget(lo.ToPtr(1), lo.ToPtr(1), lo.ToPtr([]string{"label-1", "label-2"})), 6, true, false)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 1})
			reconciler := disruption_budget.NewReconciler(deviceSvc, fleetSvc, eventSvc, log)

			captureAndVerifyEvents(3, api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
			reconciler.Reconcile(ctx, store.NullOrgId)
			verifyEventDetails(api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
		})
		It("One fleet - 6 devices with matching fleet - with matching disruption budget - with labels - without unavailable", func() {
			initTest(disruptionBudget(nil, lo.ToPtr(1), lo.ToPtr([]string{"label-1", "label-2"})), 9, true, false)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 3})
			reconciler := disruption_budget.NewReconciler(deviceSvc, fleetSvc, eventSvc, log)
			captureAndVerifyEvents(6, api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
			reconciler.Reconcile(ctx, store.NullOrgId)
			verifyEventDetails(api.EventReasonFleetRolloutDeviceSelected, api.DeviceKind)
		})
	})
})
