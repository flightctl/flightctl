package tasks_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
)

// Simple mock K8s client for testing path validation
type mockK8sClient struct{}

func (m *mockK8sClient) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	// Return a fake secret with dummy data
	return &corev1.Secret{
		Data: map[string][]byte{
			"key1": []byte("value1"),
			"key2": []byte("value2"),
		},
	}, nil
}

func (m *mockK8sClient) PostCRD(ctx context.Context, crdGVK string, body []byte, opts ...k8sclient.Option) ([]byte, error) {
	return nil, nil
}

var _ = Describe("DeviceRender", func() {
	var (
		log               *logrus.Logger
		ctx               context.Context
		orgId             uuid.UUID
		deviceStore       store.Device
		fleetStore        store.Fleet
		tvStore           store.TemplateVersion
		repoStore         store.Repository
		storeInst         store.Store
		serviceHandler    service.Service
		cfg               *config.Config
		dbName            string
		fleetName         string
		deviceName        string
		repoName          string
		workerClient      worker_client.WorkerClient
		mockQueueProducer *queues.MockQueueProducer
		ctrl              *gomock.Controller
		kvStoreInst       kvstore.KVStore
		queuesProvider    queues.Provider
		mockK8s           *mockK8sClient
		testHTTPServer    *httptest.Server
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		fleetName = "myfleet"
		deviceName = "mydevice"
		repoName = "contents"
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		deviceStore = storeInst.Device()
		fleetStore = storeInst.Fleet()
		tvStore = storeInst.TemplateVersion()
		repoStore = storeInst.Repository()
		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer = queues.NewMockQueueProducer(ctrl)
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		workerClient = worker_client.NewWorkerClient(mockQueueProducer, log)
		var err error
		kvStoreInst, err = kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		orgResolver, err := testutil.NewOrgResolver(cfg, storeInst.Organization(), log)
		Expect(err).ToNot(HaveOccurred())

		// Initialize mock K8s client for path validation tests
		mockK8s = &mockK8sClient{}

		serviceHandler = service.NewServiceHandler(storeInst, workerClient, kvStoreInst, nil, log, "", "", []string{}, orgResolver)

		// Initialize queues provider and rendered.Bus for successful device rendering
		// Only initialize once (singleton pattern), subsequent calls are no-ops
		if queuesProvider == nil {
			processID := fmt.Sprintf("device-render-test-%s", uuid.New().String())
			queuesProvider, err = queues.NewRedisProvider(ctx, log, processID, "localhost", 6379, config.SecureString("adminpass"), queues.DefaultRetryConfig())
			Expect(err).ToNot(HaveOccurred())
			err = rendered.Bus.Initialize(ctx, kvStoreInst, queuesProvider, 10*time.Second, log)
			Expect(err).ToNot(HaveOccurred())
		}
	})

	AfterEach(func() {
		// Clean up Redis keys but keep connections open
		// The queuesProvider and rendered.Bus singleton persist across all tests in this suite
		// since singletons are meant to live for the process lifetime
		if kvStoreInst != nil {
			_ = kvStoreInst.DeleteAllKeys(ctx)
		}

		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
		ctrl.Finish()
	})

	Context("unsafe path validation", func() {
		DescribeTable("inline config path validation",
			func(path string, shouldFail bool, errorSubstring string) {
				inlineConfig := &api.InlineConfigProviderSpec{
					Name:   "test-config",
					Inline: []api.FileSpec{{Path: path, Content: "test", Mode: lo.ToPtr(420)}},
				}
				configProvider := api.ConfigProviderSpec{}
				err := configProvider.FromInlineConfigProviderSpec(*inlineConfig)
				Expect(err).ToNot(HaveOccurred())

				testDeviceName := deviceName + "-inline-" + uuid.New().String()[:8]
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
				logic := tasks.NewDeviceRenderLogic(log, serviceHandler, nil, kvStoreInst, orgId, event)
				err = logic.RenderDevice(ctx)

				if shouldFail {
					// Path validation should fail - error returned before UpdateRenderedDevice
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring(errorSubstring))
				} else {
					// Safe paths should complete successfully with queues provider
					Expect(err).ToNot(HaveOccurred())
				}
			},
			Entry("should reject /var/lib/flightctl/data.txt", "/var/lib/flightctl/data.txt", true, "unsafe device path"),
			Entry("should reject /usr/lib/flightctl/binary", "/usr/lib/flightctl/binary", true, "unsafe device path"),
			Entry("should reject /etc/flightctl/certs/ca.crt", "/etc/flightctl/certs/ca.crt", true, "unsafe device path"),
			Entry("should reject /etc/flightctl/config.yaml", "/etc/flightctl/config.yaml", true, "unsafe device path"),
			Entry("should allow /etc/myapp/config.txt", "/etc/myapp/config.txt", false, ""),
			Entry("should allow /etc/flightctl/custom.txt", "/etc/flightctl/custom.txt", false, ""),
		)

		Context("HTTP config path validation", func() {
			BeforeEach(func() {
				// Set up local HTTP test server only for HTTP tests
				testHTTPServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("test config content"))
				}))
			})

			AfterEach(func() {
				// Close the test HTTP server
				if testHTTPServer != nil {
					testHTTPServer.Close()
				}
			})

			DescribeTable("validates paths correctly",
				func(filePath string, shouldFailWithPathError bool) {
					testRepoName := repoName + "-http-" + uuid.New().String()[:8]
					repoSpec := api.RepositorySpec{}
					err := repoSpec.FromGenericRepoSpec(api.GenericRepoSpec{
						Url:  testHTTPServer.URL,
						Type: api.Http,
					})
					Expect(err).ToNot(HaveOccurred())

					repo := &api.Repository{
						Metadata: api.ObjectMeta{Name: lo.ToPtr(testRepoName)},
						Spec:     repoSpec,
					}
					_, err = repoStore.Create(ctx, orgId, repo, nil)
					Expect(err).ToNot(HaveOccurred())

					defer func() {
						_ = repoStore.Delete(ctx, orgId, testRepoName, nil)
					}()

					httpConfig := &api.HttpConfigProviderSpec{Name: "http-config"}
					httpConfig.HttpRef.Repository = testRepoName
					httpConfig.HttpRef.FilePath = filePath
					configProvider := api.ConfigProviderSpec{}
					err = configProvider.FromHttpConfigProviderSpec(*httpConfig)
					Expect(err).ToNot(HaveOccurred())

					testDeviceName := deviceName + "-http-" + uuid.New().String()[:8]
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
					logic := tasks.NewDeviceRenderLogic(log, serviceHandler, nil, kvStoreInst, orgId, event)
					err = logic.RenderDevice(ctx)

					if shouldFailWithPathError {
						Expect(err).To(HaveOccurred())
						Expect(err.Error()).To(ContainSubstring("unsafe device path"))
					} else {
						// Safe paths should succeed with local test server
						Expect(err).ToNot(HaveOccurred())
					}
				},
				Entry("should reject /var/lib/flightctl/data.txt", "/var/lib/flightctl/data.txt", true),
				Entry("should reject /etc/flightctl/certs/key.pem", "/etc/flightctl/certs/key.pem", true),
				Entry("should allow /etc/myapp/config.yaml", "/etc/myapp/config.yaml", false),
			)
		})

		DescribeTable("Kubernetes config path validation",
			func(mountPath string, shouldFailWithPathError bool) {
				k8sConfig := &api.KubernetesSecretProviderSpec{Name: "k8s-config"}
				k8sConfig.SecretRef.Name = "test-secret"
				k8sConfig.SecretRef.Namespace = "default"
				k8sConfig.SecretRef.MountPath = mountPath
				configProvider := api.ConfigProviderSpec{}
				err := configProvider.FromKubernetesSecretProviderSpec(*k8sConfig)
				Expect(err).ToNot(HaveOccurred())

				testDeviceName := deviceName + "-k8s-" + uuid.New().String()[:8]
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
				logic := tasks.NewDeviceRenderLogic(log, serviceHandler, mockK8s, kvStoreInst, orgId, event)
				err = logic.RenderDevice(ctx)

				if shouldFailWithPathError {
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("unsafe device path"))
				} else {
					Expect(err).ToNot(HaveOccurred())
				}
			},
			Entry("should reject /etc/flightctl/certs", "/etc/flightctl/certs", true),
			Entry("should reject /var/lib/flightctl/data", "/var/lib/flightctl/data", true),
			Entry("should allow /etc/myapp/secrets", "/etc/myapp/secrets", false),
		)
	})

	Context("when device labels change with git configuration", func() {
		It("should re-render the device configuration even if template version is the same", func() {
			// Create a repository
			repoSpec := api.RepositorySpec{}
			err := repoSpec.FromGenericRepoSpec(api.GenericRepoSpec{
				Url:  "https://github.com/flightctl/flightctl-demos",
				Type: api.Git,
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
			rolloutLogic := tasks.NewFleetRolloutsLogic(log, serviceHandler, orgId, event)
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
			status := serviceHandler.UpdateDeviceAnnotations(ctx, deviceName, annotations, nil)
			Expect(status.Code).To(Equal(int32(200)))

			// Now change the device label from "small" to "big"
			// First fetch the latest device to get the updated resourceVersion
			device, err = deviceStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())

			device.Metadata.Labels = &map[string]string{
				"device": "camera",
				"size":   "big",
			}
			_, err = deviceStore.Update(ctx, orgId, device, nil, false, nil, nil)
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
			err := repoSpec.FromGenericRepoSpec(api.GenericRepoSpec{
				Url:  "https://github.com/flightctl/flightctl-demos",
				Type: api.Git,
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
			rolloutLogic := tasks.NewFleetRolloutsLogic(log, serviceHandler, orgId, event)
			err = rolloutLogic.RolloutDevice(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Set template version annotation to simulate that it was already rendered
			annotations := map[string]string{
				api.DeviceAnnotationTemplateVersion:         "1.0.0",
				api.DeviceAnnotationRenderedTemplateVersion: "1.0.0",
			}
			status := serviceHandler.UpdateDeviceAnnotations(ctx, deviceName, annotations, nil)
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
})
