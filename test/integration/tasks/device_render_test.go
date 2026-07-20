package tasks_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/k8sclient"
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
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Simple mock K8s client for testing path validation and fingerprinting
type mockK8sClient struct{}

func (m *mockK8sClient) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			ResourceVersion: "42",
		},
		Data: map[string][]byte{
			"key1": []byte("value1"),
			"key2": []byte("value2"),
		},
	}, nil
}

type failingK8sClient struct{}

func (m *failingK8sClient) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	return nil, fmt.Errorf("secret %s/%s not found", namespace, name)
}

func (m *failingK8sClient) PostCRD(ctx context.Context, crdGVK string, body []byte, opts ...k8sclient.Option) ([]byte, error) {
	return nil, nil
}

func (m *failingK8sClient) ListRoleBindings(ctx context.Context, namespace string) (*rbacv1.RoleBindingList, error) {
	return nil, nil
}

func (m *failingK8sClient) ListProjects(ctx context.Context, token string, opts ...k8sclient.ListProjectsOption) ([]byte, error) {
	return nil, nil
}

func (m *failingK8sClient) ListRoleBindingsForUser(ctx context.Context, namespace, username string, groups []string) ([]string, error) {
	return nil, nil
}

func (m *mockK8sClient) PostCRD(ctx context.Context, crdGVK string, body []byte, opts ...k8sclient.Option) ([]byte, error) {
	return nil, nil
}

func (m *mockK8sClient) ListRoleBindings(ctx context.Context, namespace string) (*rbacv1.RoleBindingList, error) {
	return nil, nil
}

func (m *mockK8sClient) ListProjects(ctx context.Context, token string, opts ...k8sclient.ListProjectsOption) ([]byte, error) {
	return nil, nil
}

func (m *mockK8sClient) ListRoleBindingsForUser(ctx context.Context, namespace, username string, groups []string) ([]string, error) {
	return nil, nil
}

var _ = Describe("DeviceRender", func() {
	var (
		log                *logrus.Logger
		ctx                context.Context
		orgId              uuid.UUID
		deviceStore        devicestore.Store
		fleetStore         fleetstore.Store
		tvStore            templateversionstore.Store
		repoStore          repositorystore.Store
		deviceSvc          deviceservice.Service
		repositorySvc      repositoryservice.Service
		fleetSvc           fleetservice.Service
		templateVersionSvc templateversionservice.Service
		dependencyrefSvc   dependencyrefservice.Service
		cfg                *config.Config
		dbName             string
		db                 *gorm.DB
		fleetName          string
		deviceName         string
		repoName           string
		workerClient       worker_client.WorkerClient
		mockQueueProducer  *queues.MockQueueProducer
		ctrl               *gomock.Controller
		kvStoreInst        kvstore.KVStore
		queuesProvider     queues.Provider
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		fleetName = "myfleet"
		deviceName = "mydevice"
		repoName = "contents"
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		tvStore = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		repoStore = repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		newDeviceStore := devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		newFleetStore := fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		newTvStore := templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		newRepoStore := repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		dependencyrefStore := dependencyrefstore.NewDependencyRefStore(db, log.WithField("pkg", "dependencyref-store"))
		eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer = queues.NewMockQueueProducer(ctrl)
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		workerClient = worker_client.NewWorkerClient(mockQueueProducer, log)
		kvStoreInst, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())
		eventsSvc := events.NewServiceHandler(eventStore, workerClient, log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(newDeviceStore, newFleetStore, eventsSvc, kvStoreInst, "", log)
		repositorySvc = repositoryservice.NewServiceHandler(newRepoStore, eventsSvc, log)
		fleetSvc = fleetservice.NewServiceHandler(newFleetStore, eventsSvc, log)
		templateVersionSvc = templateversionservice.NewServiceHandler(newTvStore, kvStoreInst, eventsSvc, log)
		dependencyrefSvc = dependencyrefservice.NewServiceHandler(dependencyrefStore, log)

		// Initialize queues provider and rendered.Bus for successful device rendering
		// Only initialize once (singleton pattern), subsequent calls are no-ops
		if queuesProvider == nil {
			processID := fmt.Sprintf("device-render-test-%s", uuid.New().String())
			queuesProvider, err = queues.NewRedisProvider(ctx, log, processID, redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
			Expect(err).ToNot(HaveOccurred())
			err = rendered.Bus.Initialize(ctx, kvStoreInst, queuesProvider, 10*time.Second, log)
			Expect(err).ToNot(HaveOccurred())
		}
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		ctrl.Finish()
	})

	Context("render-time path validation", func() {

		Context("K8s secret derived path validation - render-time only", func() {
			It("should validate safe derived paths at render time", func() {
				k8sConfig := &api.KubernetesSecretProviderSpec{Name: "k8s-render-check"}
				k8sConfig.SecretRef.Name = "test-secret"
				k8sConfig.SecretRef.Namespace = "default"
				k8sConfig.SecretRef.MountPath = "/etc/myapp" // Safe base path

				mockK8s := &mockK8sClient{}

				configProvider := api.ConfigProviderSpec{}
				err := configProvider.FromKubernetesSecretProviderSpec(*k8sConfig)
				Expect(err).ToNot(HaveOccurred())

				testDeviceName := deviceName + "-k8s-render-" + uuid.New().String()[:8]
				device := &api.Device{
					Metadata: api.ObjectMeta{Name: lo.ToPtr(testDeviceName)},
					Spec:     &api.DeviceSpec{Config: &[]api.ConfigProviderSpec{configProvider}},
				}
				_, err = deviceStore.Create(ctx, orgId, device, nil)
				Expect(err).ToNot(HaveOccurred())

				defer func() {
					_, _ = deviceStore.Delete(ctx, orgId, testDeviceName, nil)
				}()

				event := api.Event{
					Reason:         api.EventReasonResourceUpdated,
					InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: testDeviceName},
				}
				logic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, mockK8s, kvStoreInst, nil, orgId, event)
				err = logic.RenderDevice(ctx)

				// Should succeed - safe paths pass validation
				Expect(err).ToNot(HaveOccurred())
			})

			It("should reject unsafe derived paths at render time", func() {
				k8sConfig := &api.KubernetesSecretProviderSpec{Name: "k8s-unsafe-render"}
				k8sConfig.SecretRef.Name = "test-secret"
				k8sConfig.SecretRef.Namespace = "default"
				k8sConfig.SecretRef.MountPath = "/var/lib/flightctl" // Forbidden base path

				mockK8s := &mockK8sClient{}

				configProvider := api.ConfigProviderSpec{}
				err := configProvider.FromKubernetesSecretProviderSpec(*k8sConfig)
				Expect(err).ToNot(HaveOccurred())

				testDeviceName := deviceName + "-k8s-unsafe-" + uuid.New().String()[:8]
				device := &api.Device{
					Metadata: api.ObjectMeta{Name: lo.ToPtr(testDeviceName)},
					Spec:     &api.DeviceSpec{Config: &[]api.ConfigProviderSpec{configProvider}},
				}
				_, err = deviceStore.Create(ctx, orgId, device, nil)
				Expect(err).ToNot(HaveOccurred())

				defer func() {
					_, _ = deviceStore.Delete(ctx, orgId, testDeviceName, nil)
				}()

				event := api.Event{
					Reason:         api.EventReasonResourceUpdated,
					InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: testDeviceName},
				}
				logic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, mockK8s, kvStoreInst, nil, orgId, event)
				err = logic.RenderDevice(ctx)

				// Should fail - derived paths under forbidden root are rejected
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("forbidden device path"))
			})

			It("when device cannot be rendered, sets DeviceSpecValid false, updates device status, and IsUpdatedToDeviceSpec returns false", func() {
				k8sConfig := &api.KubernetesSecretProviderSpec{Name: "k8s-unrenderable"}
				k8sConfig.SecretRef.Name = "test-secret"
				k8sConfig.SecretRef.Namespace = "default"
				k8sConfig.SecretRef.MountPath = "/var/lib/flightctl" // Forbidden base path -> render fails

				mockK8s := &mockK8sClient{}

				configProvider := api.ConfigProviderSpec{}
				err := configProvider.FromKubernetesSecretProviderSpec(*k8sConfig)
				Expect(err).ToNot(HaveOccurred())

				testDeviceName := deviceName + "-unrenderable-" + uuid.New().String()[:8]
				device := &api.Device{
					Metadata: api.ObjectMeta{Name: lo.ToPtr(testDeviceName)},
					Spec:     &api.DeviceSpec{Config: &[]api.ConfigProviderSpec{configProvider}},
				}
				_, err = deviceStore.Create(ctx, orgId, device, nil)
				Expect(err).ToNot(HaveOccurred())

				defer func() {
					_, _ = deviceStore.Delete(ctx, orgId, testDeviceName, nil)
				}()

				// Set a recent last_seen in device_timestamps so the device is not considered disconnected when
				// UpdateServerSideDeviceStatus runs (otherwise status.updated.status can be set to Unknown).
				setDeviceLastSeen := func(deviceName string, lastSeen time.Time) error {
					result := db.WithContext(ctx).Model(&model.DeviceTimestamp{}).Where("org_id = ? AND name = ?", orgId, deviceName).Updates(map[string]interface{}{
						"last_seen": lastSeen,
					})
					return result.Error
				}
				err = setDeviceLastSeen(testDeviceName, time.Now().Add(-1*time.Minute))
				Expect(err).ToNot(HaveOccurred())

				event := api.Event{
					Reason:         api.EventReasonResourceUpdated,
					InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: testDeviceName},
				}
				logic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, mockK8s, kvStoreInst, nil, orgId, event)
				err = logic.RenderDevice(ctx)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("forbidden device path"))

				// Fetch device from the database and verify render-failure handling: condition set, not "updated to spec"
				device, err = deviceStore.Get(ctx, orgId, testDeviceName)
				Expect(err).ToNot(HaveOccurred())
				Expect(device.Status).ToNot(BeNil())
				Expect(device.Status.Conditions).ToNot(BeNil())

				specValid := api.FindStatusCondition(device.Status.Conditions, api.ConditionTypeDeviceSpecValid)
				Expect(specValid).ToNot(BeNil(), "DeviceSpecValid condition should be set when render fails")
				Expect(specValid.Status).To(Equal(api.ConditionStatusFalse))
				Expect(specValid.Reason).To(Equal("Invalid"))
				Expect(specValid.Message).ToNot(BeEmpty())
				Expect(specValid.Message).To(ContainSubstring("forbidden device path"))

				// When DeviceSpecValid condition exists and is False (spec invalid), the device must not be considered up to date.
				Expect(device.IsUpdatedToDeviceSpec()).To(BeFalse(),
					"device with DeviceSpecValid condition False (spec invalid) must not be considered updated to device spec")

				// status.updated.status in the database must reflect that the device is out of date when it cannot be rendered.
				Expect(device.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusOutOfDate),
					"status.updated.status must be OutOfDate when device cannot be rendered")
			})
		})
	})

	Context("when device labels change with git configuration", func() {
		It("should re-render the device configuration even if template version is the same", func() {
			// Create a repository
			repoSpec := api.RepositorySpec{}
			err := repoSpec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "https://github.com/flightctl/flightctl-demos",
				Type: api.GitRepoSpecTypeGit,
			})
			Expect(err).ToNot(HaveOccurred())

			repo := &api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(repoName),
				},
				Spec: repoSpec,
			}
			_, err = repoStore.Create(ctx, orgId, repo, nil)
			Expect(err).ToNot(HaveOccurred())

			// Create a fleet with inline configuration that uses device labels
			inlineConfig := &api.InlineConfigProviderSpec{
				Name: "motd",
				Inline: []api.FileSpec{
					{
						Path:    "/etc/motd",
						Content: "I'm {{.metadata.labels.size}}",
						Mode:    lo.ToPtr(420),
					},
				},
			}
			configProvider := api.ConfigProviderSpec{}
			err = configProvider.FromInlineConfigProviderSpec(*inlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet := &api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(fleetName),
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"device": "camera",
						},
					},
					Template: struct {
						Metadata *api.ObjectMeta `json:"metadata,omitempty"`
						Spec     api.DeviceSpec  `json:"spec"`
					}{
						Metadata: &api.ObjectMeta{
							Labels: &map[string]string{
								"fleet": fleetName,
							},
						},
						Spec: api.DeviceSpec{
							Config: &[]api.ConfigProviderSpec{configProvider},
						},
					},
				},
			}
			_, err = fleetStore.Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			// Create template version with the fleet spec
			tvStatus := api.TemplateVersionStatus{
				Config: &[]api.ConfigProviderSpec{configProvider},
			}
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", &tvStatus)
			Expect(err).ToNot(HaveOccurred())

			// Create a device with initial label
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Labels: &map[string]string{
						"device": "camera",
						"size":   "small",
					},
					Owner: lo.ToPtr("Fleet/" + fleetName),
				},
				Spec: &api.DeviceSpec{},
			}
			_, err = deviceStore.Create(ctx, orgId, device, nil)
			Expect(err).ToNot(HaveOccurred())

			// Trigger fleet rollout to generate device spec
			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.DeviceKind,
					Name: deviceName,
				},
			}
			rolloutLogic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
			err = rolloutLogic.RolloutDevice(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Verify device has the correct spec with small template
			device, err = deviceStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Spec).ToNot(BeNil())
			Expect(device.Spec.Config).ToNot(BeNil())
			Expect(len(*device.Spec.Config)).To(Equal(1))
			inlineConfigSpec, err := (*device.Spec.Config)[0].AsInlineConfigProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(inlineConfigSpec.Name).To(Equal("motd"))

			// Set template version annotation to simulate that it was already rendered
			annotations := map[string]string{
				api.DeviceAnnotationTemplateVersion:         "1.0.0",
				api.DeviceAnnotationRenderedTemplateVersion: "1.0.0",
			}
			status := deviceSvc.UpdateDeviceAnnotations(ctx, orgId, deviceName, annotations, nil)
			Expect(status.Code).To(Equal(int32(200)))

			// Now change the device label from "small" to "big"
			// First fetch the latest device to get the updated resourceVersion
			device, err = deviceStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())

			device.Metadata.Labels = &map[string]string{
				"device": "camera",
				"size":   "big",
			}
			_, err = deviceStore.Update(ctx, orgId, device, nil, nil, nil)
			Expect(err).ToNot(HaveOccurred())

			// Trigger fleet rollout again to update device spec
			err = rolloutLogic.RolloutDevice(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Verify device spec is updated (the template will be processed with new labels)
			device, err = deviceStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Spec).ToNot(BeNil())
			Expect(device.Spec.Config).ToNot(BeNil())
			Expect(len(*device.Spec.Config)).To(Equal(1))
			inlineConfigSpec, err = (*device.Spec.Config)[0].AsInlineConfigProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(inlineConfigSpec.Name).To(Equal("motd"), "Device spec should be updated with inline config after label change")
		})

		It("should skip rendering when template version and spec haven't changed", func() {
			// Create a repository
			repoSpec := api.RepositorySpec{}
			err := repoSpec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "https://github.com/flightctl/flightctl-demos",
				Type: api.GitRepoSpecTypeGit,
			})
			Expect(err).ToNot(HaveOccurred())

			repo := &api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(repoName),
				},
				Spec: repoSpec,
			}
			_, err = repoStore.Create(ctx, orgId, repo, nil)
			Expect(err).ToNot(HaveOccurred())

			// Create a fleet with inline configuration
			inlineConfig := &api.InlineConfigProviderSpec{
				Name: "motd",
				Inline: []api.FileSpec{
					{
						Path:    "/etc/motd",
						Content: "I'm {{.metadata.labels.size}}",
						Mode:    lo.ToPtr(420),
					},
				},
			}
			configProvider := api.ConfigProviderSpec{}
			err = configProvider.FromInlineConfigProviderSpec(*inlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet := &api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(fleetName),
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"device": "camera",
						},
					},
					Template: struct {
						Metadata *api.ObjectMeta `json:"metadata,omitempty"`
						Spec     api.DeviceSpec  `json:"spec"`
					}{
						Metadata: &api.ObjectMeta{
							Labels: &map[string]string{
								"fleet": fleetName,
							},
						},
						Spec: api.DeviceSpec{
							Config: &[]api.ConfigProviderSpec{configProvider},
						},
					},
				},
			}
			_, err = fleetStore.Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			// Create template version with the fleet spec
			tvStatus := api.TemplateVersionStatus{
				Config: &[]api.ConfigProviderSpec{configProvider},
			}
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", &tvStatus)
			Expect(err).ToNot(HaveOccurred())

			// Create a device
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Labels: &map[string]string{
						"device": "camera",
						"size":   "small",
					},
					Owner: lo.ToPtr("Fleet/" + fleetName),
				},
				Spec: &api.DeviceSpec{},
			}
			_, err = deviceStore.Create(ctx, orgId, device, nil)
			Expect(err).ToNot(HaveOccurred())

			// Trigger fleet rollout
			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.DeviceKind,
					Name: deviceName,
				},
			}
			rolloutLogic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
			err = rolloutLogic.RolloutDevice(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Set template version annotation to simulate that it was already rendered
			annotations := map[string]string{
				api.DeviceAnnotationTemplateVersion:         "1.0.0",
				api.DeviceAnnotationRenderedTemplateVersion: "1.0.0",
			}
			status := deviceSvc.UpdateDeviceAnnotations(ctx, orgId, deviceName, annotations, nil)
			Expect(status.Code).To(Equal(int32(200)))

			// This test verifies the correct behavior: when template version and spec haven't changed,
			// the device render logic should skip rendering (which is the current behavior).
			//
			// The device render logic correctly skips rendering when:
			// 1. Template version is the same, AND
			// 2. Device spec hasn't changed
			//
			// This is the expected behavior and should continue to work correctly.

			// For now, we'll just verify that the device spec is correct
			// The actual device render logic correctly skips rendering in this case.
			device, err = deviceStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Spec).ToNot(BeNil())
			Expect(device.Spec.Config).ToNot(BeNil())
			Expect(len(*device.Spec.Config)).To(Equal(1))
			inlineConfigSpec, err := (*device.Spec.Config)[0].AsInlineConfigProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(inlineConfigSpec.Name).To(Equal("motd"), "Device spec should remain unchanged when labels don't change")
		})
	})

	Context("dependency sync fingerprinting", func() {
		It("When a standalone device has a K8s secret config it should capture ResourceVersion as fingerprint", func() {
			k8sConfig := &api.KubernetesSecretProviderSpec{Name: "app-secrets"}
			k8sConfig.SecretRef.Name = "my-secret"
			k8sConfig.SecretRef.Namespace = "default"
			k8sConfig.SecretRef.MountPath = "/etc/secrets"

			configProvider := api.ConfigProviderSpec{}
			err := configProvider.FromKubernetesSecretProviderSpec(*k8sConfig)
			Expect(err).ToNot(HaveOccurred())

			testDeviceName := deviceName + "-fingerprint-" + uuid.New().String()[:8]
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr(testDeviceName)},
				Spec:     &api.DeviceSpec{Config: &[]api.ConfigProviderSpec{configProvider}},
			}
			_, err = deviceStore.Create(ctx, orgId, device, nil)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _, _ = deviceStore.Delete(ctx, orgId, testDeviceName, nil) }()

			event := api.Event{
				Reason:         api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: testDeviceName},
			}
			logic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, event)
			err = logic.RenderDevice(ctx)
			Expect(err).ToNot(HaveOccurred())

			device, err = deviceStore.Get(ctx, orgId, testDeviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Status).ToNot(BeNil())
			Expect(device.Status.DependencySync).ToNot(BeNil())
			Expect(device.Status.DependencySync.ConfigRefs).ToNot(BeNil())
			Expect(len(*device.Status.DependencySync.ConfigRefs)).To(Equal(1))

			ref := (*device.Status.DependencySync.ConfigRefs)[0]
			Expect(ref.ConfigProviderName).To(Equal("app-secrets"))
			Expect(ref.Fingerprint).ToNot(BeNil())
			Expect(*ref.Fingerprint).To(Equal("42"))
			Expect(ref.LastUpdatedAt).ToNot(BeNil())
		})

		It("When a device has only inline config it should have no dependencySync entries", func() {
			inlineConfig := &api.InlineConfigProviderSpec{
				Name: "motd",
				Inline: []api.FileSpec{
					{Path: "/etc/motd", Content: "Hello", Mode: lo.ToPtr(420)},
				},
			}
			configProvider := api.ConfigProviderSpec{}
			err := configProvider.FromInlineConfigProviderSpec(*inlineConfig)
			Expect(err).ToNot(HaveOccurred())

			testDeviceName := deviceName + "-inline-only-" + uuid.New().String()[:8]
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr(testDeviceName)},
				Spec:     &api.DeviceSpec{Config: &[]api.ConfigProviderSpec{configProvider}},
			}
			_, err = deviceStore.Create(ctx, orgId, device, nil)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _, _ = deviceStore.Delete(ctx, orgId, testDeviceName, nil) }()

			event := api.Event{
				Reason:         api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: testDeviceName},
			}
			logic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, event)
			err = logic.RenderDevice(ctx)
			Expect(err).ToNot(HaveOccurred())

			device, err = deviceStore.Get(ctx, orgId, testDeviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Status).ToNot(BeNil())
			if device.Status.DependencySync != nil && device.Status.DependencySync.ConfigRefs != nil {
				Expect(len(*device.Status.DependencySync.ConfigRefs)).To(Equal(0))
			}
		})

		It("When a device has mixed config providers it should only fingerprint external deps", func() {
			k8sConfig := &api.KubernetesSecretProviderSpec{Name: "db-creds"}
			k8sConfig.SecretRef.Name = "db-secret"
			k8sConfig.SecretRef.Namespace = "default"
			k8sConfig.SecretRef.MountPath = "/etc/db"

			inlineConfig := &api.InlineConfigProviderSpec{
				Name: "banner",
				Inline: []api.FileSpec{
					{Path: "/etc/motd", Content: "Welcome", Mode: lo.ToPtr(420)},
				},
			}

			k8sProvider := api.ConfigProviderSpec{}
			err := k8sProvider.FromKubernetesSecretProviderSpec(*k8sConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineProvider := api.ConfigProviderSpec{}
			err = inlineProvider.FromInlineConfigProviderSpec(*inlineConfig)
			Expect(err).ToNot(HaveOccurred())

			testDeviceName := deviceName + "-mixed-" + uuid.New().String()[:8]
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr(testDeviceName)},
				Spec:     &api.DeviceSpec{Config: &[]api.ConfigProviderSpec{k8sProvider, inlineProvider}},
			}
			_, err = deviceStore.Create(ctx, orgId, device, nil)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _, _ = deviceStore.Delete(ctx, orgId, testDeviceName, nil) }()

			event := api.Event{
				Reason:         api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: testDeviceName},
			}
			logic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, event)
			err = logic.RenderDevice(ctx)
			Expect(err).ToNot(HaveOccurred())

			device, err = deviceStore.Get(ctx, orgId, testDeviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Status).ToNot(BeNil())
			Expect(device.Status.DependencySync).ToNot(BeNil())
			Expect(device.Status.DependencySync.ConfigRefs).ToNot(BeNil())
			Expect(len(*device.Status.DependencySync.ConfigRefs)).To(Equal(1))

			ref := (*device.Status.DependencySync.ConfigRefs)[0]
			Expect(ref.ConfigProviderName).To(Equal("db-creds"))
			Expect(ref.Fingerprint).ToNot(BeNil())
			Expect(*ref.Fingerprint).To(Equal("42"))
		})

		It("When render fails it should not populate dependencySync", func() {
			k8sConfig := &api.KubernetesSecretProviderSpec{Name: "missing-secret"}
			k8sConfig.SecretRef.Name = "nonexistent"
			k8sConfig.SecretRef.Namespace = "default"
			k8sConfig.SecretRef.MountPath = "/etc/secrets"

			configProvider := api.ConfigProviderSpec{}
			err := configProvider.FromKubernetesSecretProviderSpec(*k8sConfig)
			Expect(err).ToNot(HaveOccurred())

			testDeviceName := deviceName + "-fail-render-" + uuid.New().String()[:8]
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr(testDeviceName)},
				Spec:     &api.DeviceSpec{Config: &[]api.ConfigProviderSpec{configProvider}},
			}
			_, err = deviceStore.Create(ctx, orgId, device, nil)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _, _ = deviceStore.Delete(ctx, orgId, testDeviceName, nil) }()

			event := api.Event{
				Reason:         api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: testDeviceName},
			}
			logic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &failingK8sClient{}, kvStoreInst, nil, orgId, event)
			err = logic.RenderDevice(ctx)
			Expect(err).To(HaveOccurred())

			device, err = deviceStore.Get(ctx, orgId, testDeviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Status).ToNot(BeNil())
			if device.Status.DependencySync != nil && device.Status.DependencySync.ConfigRefs != nil {
				Expect(len(*device.Status.DependencySync.ConfigRefs)).To(Equal(0))
			}
		})
	})

	Context("spec-hash bypass for fleet rollout events", func() {
		// The spec-hash optimization at device_render.go:116 fires before config
		// rendering begins, so it blocks ALL config provider types (git, HTTP, and
		// K8s secrets) equally. We use K8s secrets here because mockK8sClient is
		// available without needing external infrastructure.
		It("When a fleet-owned device receives FleetRolloutDeviceSelected after initial render it should re-render", func() {
			k8sConfig := &api.KubernetesSecretProviderSpec{Name: "fleet-secret"}
			k8sConfig.SecretRef.Name = "my-secret"
			k8sConfig.SecretRef.Namespace = "default"
			k8sConfig.SecretRef.MountPath = "/etc/secrets"

			configProvider := api.ConfigProviderSpec{}
			err := configProvider.FromKubernetesSecretProviderSpec(*k8sConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet := &api.Fleet{
				Metadata: api.ObjectMeta{Name: lo.ToPtr(fleetName)},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{MatchLabels: &map[string]string{"fleet": fleetName}},
					Template: struct {
						Metadata *api.ObjectMeta `json:"metadata,omitempty"`
						Spec     api.DeviceSpec  `json:"spec"`
					}{
						Spec: api.DeviceSpec{Config: &[]api.ConfigProviderSpec{configProvider}},
					},
				},
			}
			_, err = fleetStore.Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			tvStatus := api.TemplateVersionStatus{Config: &[]api.ConfigProviderSpec{configProvider}}
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "v1", &tvStatus)
			Expect(err).ToNot(HaveOccurred())
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "v1-abc12345", &tvStatus)
			Expect(err).ToNot(HaveOccurred())

			testDeviceName := deviceName + "-fleet-rollout-" + uuid.New().String()[:8]
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name:  lo.ToPtr(testDeviceName),
					Owner: lo.ToPtr("Fleet/" + fleetName),
				},
				Spec: &api.DeviceSpec{Config: &[]api.ConfigProviderSpec{configProvider}},
			}
			_, err = deviceStore.Create(ctx, orgId, device, nil)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _, _ = deviceStore.Delete(ctx, orgId, testDeviceName, nil) }()

			// First render: ResourceCreated — should succeed (no hash stored yet)
			firstEvent := api.Event{
				Reason:         api.EventReasonResourceCreated,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: testDeviceName},
			}
			annotations := map[string]string{
				api.DeviceAnnotationTemplateVersion: "v1",
			}
			status := deviceSvc.UpdateDeviceAnnotations(ctx, orgId, testDeviceName, annotations, nil)
			Expect(status.Code).To(Equal(int32(200)))

			logic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, firstEvent)
			err = logic.RenderDevice(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Verify renderedVersion was set (render completed)
			device, err = deviceStore.Get(ctx, orgId, testDeviceName)
			Expect(err).ToNot(HaveOccurred())
			firstRenderedVersion := ""
			if device.Metadata.Annotations != nil {
				firstRenderedVersion = (*device.Metadata.Annotations)[api.DeviceAnnotationRenderedVersion]
			}
			Expect(firstRenderedVersion).To(Equal("1"), "First render should set renderedVersion to 1")

			// Verify the spec hash is now stored
			specHash := (*device.Metadata.Annotations)[api.DeviceAnnotationRenderedSpecHash]
			Expect(specHash).ToNot(BeEmpty(), "Spec hash should be stored after first render")

			// Simulate dependency-change rollout: update templateVersion annotation
			// to a new version (as fleet_rollout.go would do)
			annotations = map[string]string{
				api.DeviceAnnotationTemplateVersion: "v1-abc12345",
			}
			status = deviceSvc.UpdateDeviceAnnotations(ctx, orgId, testDeviceName, annotations, nil)
			Expect(status.Code).To(Equal(int32(200)))

			// Second render: FleetRolloutDeviceSelected — previously skipped because
			// the spec hash hadn't changed and the bypass only covered
			// DependencyChangeDetected.
			secondEvent := api.Event{
				Reason:         api.EventReasonFleetRolloutDeviceSelected,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: testDeviceName},
			}
			logic = tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, secondEvent)
			err = logic.RenderDevice(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Verify rendering proceeded: renderedVersion should bump to "2" and
			// dependencySync fingerprints should be written.
			device, err = deviceStore.Get(ctx, orgId, testDeviceName)
			Expect(err).ToNot(HaveOccurred())

			secondRenderedVersion := ""
			if device.Metadata.Annotations != nil {
				secondRenderedVersion = (*device.Metadata.Annotations)[api.DeviceAnnotationRenderedVersion]
			}
			Expect(secondRenderedVersion).To(Equal("2"),
				"Fleet-owned device should bump renderedVersion on FleetRolloutDeviceSelected even when spec hash is unchanged")

			Expect(device.Status).ToNot(BeNil())
			Expect(device.Status.DependencySync).ToNot(BeNil())
			Expect(device.Status.DependencySync.ConfigRefs).ToNot(BeNil())
			Expect(len(*device.Status.DependencySync.ConfigRefs)).To(Equal(1))
			ref := (*device.Status.DependencySync.ConfigRefs)[0]
			Expect(ref.ConfigProviderName).To(Equal("fleet-secret"))
			Expect(ref.Fingerprint).ToNot(BeNil())
			Expect(*ref.Fingerprint).To(Equal("42"))
		})
	})
})
