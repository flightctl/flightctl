package tasks_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

var _ = Describe("FleetRollout application lifecycle overlay", func() {
	var (
		log               *logrus.Logger
		ctx               context.Context
		orgId             uuid.UUID
		deviceStore       store.Device
		fleetStore        store.Fleet
		tvStore           store.TemplateVersion
		storeInst         store.Store
		serviceHandler    service.Service
		cfg               *config.Config
		db                *gorm.DB
		dbName            string
		fleetName         string
		deviceName        string
		workerClient      worker_client.WorkerClient
		mockQueueProducer *queues.MockQueueProducer
		ctrl              *gomock.Controller
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		fleetName = "lifecycle-fleet"
		deviceName = "lifecycle-device-1"
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		storeInst = store.NewStore(db, log.WithField("pkg", "store"))
		deviceStore = storeInst.Device()
		fleetStore = storeInst.Fleet()
		tvStore = storeInst.TemplateVersion()
		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer = queues.NewMockQueueProducer(ctrl)
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		workerClient = worker_client.NewWorkerClient(mockQueueProducer, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, workerClient, kvStore, nil, log, "", "", []string{}, false)
	})

	AfterEach(func() {
		_ = storeInst.Close()
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		ctrl.Finish()
	})

	containerAppTemplate := func(name string) api.ApplicationProviderSpec {
		containerApp := api.ContainerApplication{
			AppType: api.AppTypeContainer,
			Name:    lo.ToPtr(name),
			Image:   "quay.io/test/app:v1",
		}
		var app api.ApplicationProviderSpec
		Expect(app.FromContainerApplication(containerApp)).To(Succeed())
		return app
	}

	It("bakes a device's application lifecycle annotation into its rolled-out spec", func() {
		testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
		status := api.TemplateVersionStatus{
			Applications: &[]api.ApplicationProviderSpec{containerAppTemplate("app-1")},
		}
		Expect(testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", &status)).To(Succeed())
		testutil.CreateTestDevice(ctx, deviceStore, orgId, deviceName, lo.ToPtr("Fleet/"+fleetName), nil, nil)

		event := api.Event{
			Reason: api.EventReasonResourceUpdated,
			InvolvedObject: api.ObjectReference{
				Kind: api.DeviceKind,
				Name: deviceName,
			},
		}
		logic := tasks.NewFleetRolloutsLogic(log, serviceHandler, orgId, event)

		By("rolling out the device for the first time with no lifecycle override")
		Expect(logic.RolloutDevice(ctx)).To(Succeed())
		dev, err := deviceStore.Get(ctx, orgId, deviceName)
		Expect(err).ToNot(HaveOccurred())
		Expect(dev.Spec.Applications).ToNot(BeNil())
		Expect(*dev.Spec.Applications).To(HaveLen(1))
		app1, err := (*dev.Spec.Applications)[0].AsContainerApplication()
		Expect(err).ToNot(HaveOccurred())
		Expect(app1.DesiredState).To(BeNil())

		By("setting a device-level lifecycle override via the annotation")
		annotations := map[string]string{
			domain.DeviceAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped","restartGeneration":1}}`,
		}
		setStatus := serviceHandler.UpdateDeviceAnnotations(ctx, orgId, deviceName, annotations, nil)
		Expect(setStatus.Code).To(Equal(int32(200)))

		By("rolling out the device again, the override should be baked into the spec")
		Expect(logic.RolloutDevice(ctx)).To(Succeed())
		dev, err = deviceStore.Get(ctx, orgId, deviceName)
		Expect(err).ToNot(HaveOccurred())
		Expect(dev.Spec.Applications).ToNot(BeNil())
		Expect(*dev.Spec.Applications).To(HaveLen(1))
		app1, err = (*dev.Spec.Applications)[0].AsContainerApplication()
		Expect(err).ToNot(HaveOccurred())
		Expect(app1.DesiredState).ToNot(BeNil())
		Expect(*app1.DesiredState).To(Equal(api.ApplicationDesiredStateStopped))
		Expect(app1.RestartGeneration).ToNot(BeNil())
		Expect(*app1.RestartGeneration).To(Equal(1))

		By("clearing the override and rolling out again should revert the application to the template's state")
		clearStatus := serviceHandler.UpdateDeviceAnnotations(ctx, orgId, deviceName, nil, []string{domain.DeviceAnnotationApplicationLifecycle})
		Expect(clearStatus.Code).To(Equal(int32(200)))

		Expect(logic.RolloutDevice(ctx)).To(Succeed())
		dev, err = deviceStore.Get(ctx, orgId, deviceName)
		Expect(err).ToNot(HaveOccurred())
		app1, err = (*dev.Spec.Applications)[0].AsContainerApplication()
		Expect(err).ToNot(HaveOccurred())
		Expect(app1.DesiredState).To(BeNil())
	})
})
