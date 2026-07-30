package tasks_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
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

var (
	suiteCtx      context.Context
	redisHost     string
	redisPort     uint
	redisPassword domain.SecureString
	redisCleanup  func()
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tasks Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Tasks Suite")
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

var _ = Describe("FleetRollout", func() {
	var (
		log                *logrus.Logger
		ctx                context.Context
		orgId              uuid.UUID
		deviceStore        devicestore.Store
		newDeviceStore     devicestore.Store
		fleetStore         fleetstore.Store
		tvStore            templateversionstore.Store
		fleetSvc           fleetservice.Service
		templateVersionSvc templateversionservice.Service
		deviceSvc          deviceservice.Service
		dependencyrefSvc   dependencyrefservice.Service
		cfg                *config.Config
		db                 *gorm.DB
		dbName             string
		numDevices         int
		fleetName          string
		workerClient       worker_client.WorkerClient
		mockQueueProducer  *queues.MockQueueProducer
		ctrl               *gomock.Controller
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		numDevices = 3
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		tvStore = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		newDeviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		newFleetStore := fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		newTvStore := templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		dependencyrefStore := dependencyrefstore.NewDependencyRefStore(db, log.WithField("pkg", "dependencyref-store"))
		eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		fleetName = "myfleet"
		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer = queues.NewMockQueueProducer(ctrl)
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		workerClient = worker_client.NewWorkerClient(mockQueueProducer, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())
		eventsSvc := events.NewServiceHandler(eventStore, workerClient, log)
		fleetSvc = fleetservice.NewServiceHandler(newFleetStore, eventsSvc, log)
		templateVersionSvc = templateversionservice.NewServiceHandler(newTvStore, kvStore, eventsSvc, log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(newDeviceStore, newFleetStore, eventsSvc, kvStore, "", log)
		dependencyrefSvc = dependencyrefservice.NewServiceHandler(dependencyrefStore, log)
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		ctrl.Finish()
	})

	When("the fleet is valid", func() {
		It("its devices are rolled out successfully", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
			testutil.CreateTestDevices(ctx, numDevices, deviceStore, orgId, lo.ToPtr("Fleet/myfleet"), true)
			fleet, err := fleetStore.Get(ctx, orgId, fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))

			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.FleetKind,
					Name: fleetName,
				},
			}
			logic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
			logic.SetItemsPerPage(2)

			// First update
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", nil)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutFleet(ctx)
			Expect(err).ToNot(HaveOccurred())
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(dev.Metadata.Annotations).ToNot(BeNil())
				Expect((*dev.Metadata.Annotations)[api.DeviceAnnotationTemplateVersion]).To(Equal("1.0.0"))
			}

			// Second update
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.1", nil)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutFleet(ctx)
			Expect(err).ToNot(HaveOccurred())
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(dev.Metadata.Annotations).ToNot(BeNil())
				Expect((*dev.Metadata.Annotations)[api.DeviceAnnotationTemplateVersion]).To(Equal("1.0.1"))
			}
		})

		It("a new device is rolled out correctly", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "mydevice-1", lo.ToPtr("Fleet/myfleet"), nil, nil)
			fleet, err := fleetStore.Get(ctx, orgId, fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))

			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.DeviceKind,
					Name: "mydevice-1",
				},
			}
			logic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
			logic.SetItemsPerPage(2)

			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", nil)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutDevice(ctx)
			Expect(err).ToNot(HaveOccurred())
			dev, err := deviceStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Annotations).ToNot(BeNil())
			Expect((*dev.Metadata.Annotations)[api.DeviceAnnotationTemplateVersion]).To(Equal("1.0.0"))
		})

		When("the fleet is valid and contains parameters", func() {
			var (
				gitConfig    *api.GitConfigProviderSpec
				inlineConfig *api.InlineConfigProviderSpec
				httpConfig   *api.HttpConfigProviderSpec
			)

			BeforeEach(func() {
				gitConfig = &api.GitConfigProviderSpec{
					Name: "param-git-config",
				}
				gitConfig.GitRef.Path = "path-{{ index .metadata.labels \"key\" }}"
				gitConfig.GitRef.Repository = "repo"
				gitConfig.GitRef.TargetRevision = "rev"

				inlineConfig = &api.InlineConfigProviderSpec{
					Name: "param-inline-config",
				}
				enc := api.EncodingBase64
				inlineConfig.Inline = []api.FileSpec{
					// Unencoded: My version is {{ index .metadata.labels "version" }}
					{Path: "/etc/withparams", ContentEncoding: &enc, Content: "TXkgdmVyc2lvbiBpcyB7eyBpbmRleCAubWV0YWRhdGEubGFiZWxzICJ2ZXJzaW9uIiB9fQ=="},
				}

				httpConfig = &api.HttpConfigProviderSpec{
					Name: "param-http-config",
				}
				httpConfig.HttpRef.Repository = "http-repo"
				httpConfig.HttpRef.FilePath = "/var/http-path-{{ index .metadata.labels \"key\" }}"
				httpConfig.HttpRef.Suffix = lo.ToPtr("/http-suffix")
			})

			It("its devices are rolled out successfully", func() {
				testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)

				// Create the TV with git and inline configs, both with parameters
				gitItem := api.ConfigProviderSpec{}
				err := gitItem.FromGitConfigProviderSpec(*gitConfig)
				Expect(err).ToNot(HaveOccurred())
				inlineItem := api.ConfigProviderSpec{}
				err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
				Expect(err).ToNot(HaveOccurred())
				httpItem := api.ConfigProviderSpec{}
				err = httpItem.FromHttpConfigProviderSpec(*httpConfig)
				Expect(err).ToNot(HaveOccurred())
				status := api.TemplateVersionStatus{Config: &[]api.ConfigProviderSpec{gitItem, inlineItem, httpItem}}
				err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0", &status)
				Expect(err).ToNot(HaveOccurred())

				// Add devices to the fleet
				testutil.CreateTestDevices(ctx, numDevices, deviceStore, orgId, lo.ToPtr("Fleet/myfleet"), false)
				fleet, err := fleetStore.Get(ctx, orgId, fleetName)
				Expect(err).ToNot(HaveOccurred())
				Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))

				devices, err := deviceStore.List(ctx, orgId, devicestore.DeviceListParams{})
				Expect(err).ToNot(HaveOccurred())
				Expect(len(devices.Items)).To(Equal(numDevices))

				// Roll out the devices and check their configs
				event := api.Event{
					Reason: api.EventReasonResourceUpdated,
					InvolvedObject: api.ObjectReference{
						Kind: api.FleetKind,
						Name: fleetName,
					},
				}
				logic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
				err = logic.RolloutFleet(ctx)
				Expect(err).ToNot(HaveOccurred())
				for i := 1; i <= numDevices; i++ {
					dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
					Expect(err).ToNot(HaveOccurred())
					Expect(dev.Metadata.Annotations).ToNot(BeNil())
					Expect((*dev.Metadata.Annotations)[api.DeviceAnnotationTemplateVersion]).To(Equal("1.0"))
					Expect(dev.Spec.Config).ToNot(BeNil())
					Expect(*dev.Spec.Config).To(HaveLen(3))
					for _, configItem := range *dev.Spec.Config {
						disc, err := configItem.Type()
						Expect(err).ToNot(HaveOccurred())
						switch disc {
						case api.GitConfigProviderType:
							gitSpec, err := configItem.AsGitConfigProviderSpec()
							Expect(err).ToNot(HaveOccurred())
							Expect(gitSpec.GitRef.Path).To(Equal(fmt.Sprintf("path-value-%d", i)))
						case api.InlineConfigProviderType:
							inlineSpec, err := configItem.AsInlineConfigProviderSpec()
							Expect(err).ToNot(HaveOccurred())
							Expect(inlineSpec.Inline[0].Path).To(Equal("/etc/withparams"))
							newContents := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("My version is %d", i)))
							Expect(inlineSpec.Inline[0].Content).To(Equal(newContents))
						case api.HttpConfigProviderType:
							httpSpec, err := configItem.AsHttpConfigProviderSpec()
							Expect(err).ToNot(HaveOccurred())
							Expect(httpSpec.HttpRef.FilePath).To(Equal(fmt.Sprintf("/var/http-path-value-%d", i)))
							Expect(httpSpec.HttpRef.Suffix).To(Equal(lo.ToPtr("/http-suffix")))
						default:
							Expect("").To(Equal("unexpected discriminator"))
						}
					}
				}
			})

			It("a new device is rolled out correctly", func() {
				testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)

				// Create the TV with git and inline configs, both with parameters
				gitItem := api.ConfigProviderSpec{}
				err := gitItem.FromGitConfigProviderSpec(*gitConfig)
				Expect(err).ToNot(HaveOccurred())
				inlineItem := api.ConfigProviderSpec{}
				err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
				Expect(err).ToNot(HaveOccurred())
				httpItem := api.ConfigProviderSpec{}
				err = httpItem.FromHttpConfigProviderSpec(*httpConfig)
				Expect(err).ToNot(HaveOccurred())
				status := api.TemplateVersionStatus{Config: &[]api.ConfigProviderSpec{gitItem, inlineItem}}
				err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0", &status)
				Expect(err).ToNot(HaveOccurred())

				// Add a device to the fleet
				labels := map[string]string{"key": "some-value", "otherkey": "other-value", "version": "2"}
				testutil.CreateTestDevice(ctx, deviceStore, orgId, "mydevice-1", lo.ToPtr("Fleet/myfleet"), nil, &labels)
				fleet, err := fleetStore.Get(ctx, orgId, fleetName)
				Expect(err).ToNot(HaveOccurred())
				Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))

				// Roll out to the single device
				event := api.Event{
					Reason: api.EventReasonResourceUpdated,
					InvolvedObject: api.ObjectReference{
						Kind: api.DeviceKind,
						Name: "mydevice-1",
					},
				}
				logic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
				err = logic.RolloutDevice(ctx)
				Expect(err).ToNot(HaveOccurred())
				dev, err := deviceStore.Get(ctx, orgId, "mydevice-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(dev.Metadata.Annotations).ToNot(BeNil())
				Expect((*dev.Metadata.Annotations)[api.DeviceAnnotationTemplateVersion]).To(Equal("1.0"))
				Expect(dev.Spec.Config).ToNot(BeNil())
				Expect(*dev.Spec.Config).To(HaveLen(2))
				for _, configItem := range *dev.Spec.Config {
					disc, err := configItem.Type()
					Expect(err).ToNot(HaveOccurred())
					switch disc {
					case api.GitConfigProviderType:
						gitSpec, err := configItem.AsGitConfigProviderSpec()
						Expect(err).ToNot(HaveOccurred())
						Expect(gitSpec.GitRef.Path).To(Equal("path-some-value"))
					case api.InlineConfigProviderType:
						inlineSpec, err := configItem.AsInlineConfigProviderSpec()
						Expect(err).ToNot(HaveOccurred())
						Expect(inlineSpec.Inline[0].Path).To(Equal("/etc/withparams"))
						newContents := base64.StdEncoding.EncodeToString([]byte("My version is 2"))
						Expect(inlineSpec.Inline[0].Content).To(Equal(newContents))
					default:
						Expect("").To(Equal("unexpected discriminator"))
					}
				}
			})
		})
	})

	When("a resourceversion race occurs while rolling out a device", func() {
		It("fails if the owner changed", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "mydevice-1", lo.ToPtr("Fleet/myfleet"), nil, nil)
			fleet, err := fleetStore.Get(ctx, orgId, fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))

			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.DeviceKind,
					Name: "mydevice-1",
				},
			}
			logic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", nil)
			Expect(err).ToNot(HaveOccurred())

			// Somebody changed the owner just as it was being rolled out
			raceCalled := false
			race := func() {
				if raceCalled {
					return
				}
				raceCalled = true
				otherupdate := api.Device{
					Metadata: api.ObjectMeta{
						Name:            lo.ToPtr("mydevice-1"),
						Owner:           util.SetResourceOwner(api.FleetKind, "some-other-owner"),
						ResourceVersion: lo.ToPtr("0"),
					},
					Spec:   &api.DeviceSpec{},
					Status: &api.DeviceStatus{},
				}
				device, err := model.NewDeviceFromApiResource(&otherupdate)
				Expect(err).ToNot(HaveOccurred())
				device.OrgID = orgId
				result := db.WithContext(ctx).Updates(device)
				Expect(result.Error).ToNot(HaveOccurred())
			}
			deviceStore.SetIntegrationTestCreateOrUpdateCallback(race)
			newDeviceStore.SetIntegrationTestCreateOrUpdateCallback(race)

			err = logic.RolloutDevice(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(HaveSuffix("device owner changed, skipping rollout"))
			dev, err := deviceStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Annotations).To(HaveLen(0))
		})

		It("succeeds if the owner does not change", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "mydevice-1", lo.ToPtr("Fleet/myfleet"), nil, nil)
			fleet, err := fleetStore.Get(ctx, orgId, fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))

			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.DeviceKind,
					Name: "mydevice-1",
				},
			}
			logic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", nil)
			Expect(err).ToNot(HaveOccurred())

			// Somebody changed the owner just as it was being rolled out
			raceCalled := false
			race := func() {
				if raceCalled {
					return
				}
				raceCalled = true
				otherupdate := api.Device{
					Metadata: api.ObjectMeta{
						Name:            lo.ToPtr("mydevice-1"),
						ResourceVersion: lo.ToPtr("0"),
					},
					Spec:   &api.DeviceSpec{},
					Status: &api.DeviceStatus{},
				}
				device, err := model.NewDeviceFromApiResource(&otherupdate)
				Expect(err).ToNot(HaveOccurred())
				device.OrgID = orgId
				result := db.WithContext(ctx).Updates(device)
				Expect(result.Error).ToNot(HaveOccurred())
			}
			deviceStore.SetIntegrationTestCreateOrUpdateCallback(race)
			newDeviceStore.SetIntegrationTestCreateOrUpdateCallback(race)

			err = logic.RolloutDevice(ctx)
			Expect(err).ToNot(HaveOccurred())
			dev, err := deviceStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Annotations).To(HaveLen(1))
		})
	})
})
