package vmrender_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
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
)

// mockK8sClient is a no-op K8s client sufficient for VM render tests,
// which do not exercise any K8s secret or RBAC paths.
type mockK8sClient struct{}

func (m *mockK8sClient) GetSecret(_ context.Context, _, _ string) (*corev1.Secret, error) {
	return &corev1.Secret{}, nil
}

func (m *mockK8sClient) PostCRD(_ context.Context, _ string, _ []byte, _ ...k8sclient.Option) ([]byte, error) {
	return nil, nil
}

func (m *mockK8sClient) ListRoleBindings(_ context.Context, _ string) (*rbacv1.RoleBindingList, error) {
	return nil, nil
}

func (m *mockK8sClient) ListProjects(_ context.Context, _ string, _ ...k8sclient.ListProjectsOption) ([]byte, error) {
	return nil, nil
}

func (m *mockK8sClient) ListRoleBindingsForUser(_ context.Context, _, _ string, _ []string) ([]string, error) {
	return nil, nil
}

var _ = Describe("VmApplicationRender", func() {
	var (
		log               *logrus.Logger
		ctx               context.Context
		orgId             uuid.UUID
		deviceStore       store.Device
		storeInst         store.Store
		serviceHandler    service.Service
		cfg               *config.Config
		dbName            string
		db                *gorm.DB
		workerClient      worker_client.WorkerClient
		mockQueueProducer *queues.MockQueueProducer
		ctrl              *gomock.Controller
		kvStoreInst       kvstore.KVStore
		queuesProvider    queues.Provider
		deviceName        string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		deviceName = "vm-test-device-" + uuid.New().String()[:8]

		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		storeInst = store.NewStore(db, log.WithField("pkg", "store"))
		deviceStore = storeInst.Device()

		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer = queues.NewMockQueueProducer(ctrl)
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		workerClient = worker_client.NewWorkerClient(mockQueueProducer, log)

		kvStoreInst, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())

		serviceHandler = service.NewServiceHandler(storeInst, workerClient, kvStoreInst, nil, log, "", "", []string{}, false)

		if queuesProvider == nil {
			processID := fmt.Sprintf("vm-render-test-%s", uuid.New().String())
			queuesProvider, err = queues.NewRedisProvider(ctx, log, processID, redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
			Expect(err).ToNot(HaveOccurred())
			err = rendered.Bus.Initialize(ctx, kvStoreInst, queuesProvider, 10*time.Second, log)
			Expect(err).ToNot(HaveOccurred())
		}
	})

	AfterEach(func() {
		if kvStoreInst != nil {
			kvStoreInst.Close()
		}
		_ = storeInst.Close()
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		ctrl.Finish()
	})

	// buildAndRenderVmDevice creates a device with the given VmApplication spec,
	// runs RenderDevice using the suite-level vmConverter, and returns the
	// rendered applications from the store.
	buildAndRenderVmDevice := func(vmApp api.VmApplication) []api.ApplicationProviderSpec {
		GinkgoHelper()

		appSpec := api.ApplicationProviderSpec{}
		Expect(appSpec.FromVmApplication(vmApp)).To(Succeed())

		device := &api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName)},
			Spec:     &api.DeviceSpec{Applications: &[]api.ApplicationProviderSpec{appSpec}},
		}
		_, err := deviceStore.Create(ctx, orgId, device, nil)
		Expect(err).ToNot(HaveOccurred())

		event := api.Event{
			Reason:         api.EventReasonResourceUpdated,
			InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: deviceName},
		}

		logic := tasks.NewDeviceRenderLogic(log, serviceHandler, &mockK8sClient{}, kvStoreInst, nil, orgId, event).
			WithVmConverter(vmConverter)
		Expect(logic.RenderDevice(ctx)).To(Succeed())

		rendered, err := deviceStore.GetRendered(ctx, orgId, deviceName, nil, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(rendered.Spec).ToNot(BeNil())
		Expect(rendered.Spec.Applications).ToNot(BeNil())
		return *rendered.Spec.Applications
	}

	// newInlineVmApp constructs a VmApplication with an inline KubeVirt VirtualMachine manifest.
	newInlineVmApp := func(name, vmYAML string) api.VmApplication {
		GinkgoHelper()
		inlineSpec := api.InlineApplicationProviderSpec{
			Inline: []api.ApplicationContent{
				{Path: "vm.yaml", Content: lo.ToPtr(vmYAML)},
			},
		}
		vmApp := api.VmApplication{
			AppType: api.AppTypeVm,
			Name:    lo.ToPtr(name),
		}
		Expect(vmApp.FromInlineApplicationProviderSpec(inlineSpec)).To(Succeed())
		return vmApp
	}

	Context("when a VmApplication with an inline vm.yaml is rendered", func() {
		It("should produce a QuadletApplication with pod.yaml and <name>.kube inline files", func() {
			vmYAML := fmt.Sprintf(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: %s
spec:
  running: true
  template:
    spec:
      domain:
        cpu:
          cores: 2
        memory:
          guest: 2Gi
        devices:
          disks:
          - name: containerdisk
            disk:
              bus: virtio
          interfaces:
          - name: default
            masquerade: {}
      networks:
      - name: default
        pod: {}
      volumes:
      - name: containerdisk
        containerDisk:
          image: quay.io/containerdisks/fedora:40
`, deviceName)

			vmApp := newInlineVmApp(deviceName, vmYAML)

			apps := buildAndRenderVmDevice(vmApp)
			Expect(apps).To(HaveLen(1))

			quadlet, err := apps[0].AsQuadletApplication()
			Expect(err).ToNot(HaveOccurred())
			Expect(quadlet.AppType).To(Equal(api.AppTypeQuadlet))

			inline, err := quadlet.AsInlineApplicationProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(inline.Inline).To(HaveLen(2))

			paths := []string{inline.Inline[0].Path, inline.Inline[1].Path}
			Expect(paths).To(ContainElement("pod.yaml"))
			Expect(paths).To(ContainElement(fmt.Sprintf("%s.kube", deviceName)))
		})
	})

	Context("when a VmApplication with a user-provided .kube file is rendered", func() {
		It("should preserve the user-provided .kube unit in the output", func() {
			vmYAML := fmt.Sprintf(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: %s
spec:
  running: true
  template:
    spec:
      domain:
        cpu:
          cores: 1
        memory:
          guest: 1Gi
        devices:
          disks:
          - name: containerdisk
            disk:
              bus: virtio
          interfaces:
          - name: default
            masquerade: {}
      networks:
      - name: default
        pod: {}
      volumes:
      - name: containerdisk
        containerDisk:
          image: quay.io/containerdisks/fedora:40
`, deviceName)

			customKube := "[Kube]\nYaml=pod.yaml\nPublishPort=8080:8080/tcp\n"
			inlineSpec := api.InlineApplicationProviderSpec{
				Inline: []api.ApplicationContent{
					{Path: "vm.yaml", Content: lo.ToPtr(vmYAML)},
					{Path: fmt.Sprintf("%s.kube", deviceName), Content: lo.ToPtr(customKube)},
				},
			}
			vmApp := api.VmApplication{AppType: api.AppTypeVm, Name: lo.ToPtr(deviceName)}
			Expect(vmApp.FromInlineApplicationProviderSpec(inlineSpec)).To(Succeed())

			apps := buildAndRenderVmDevice(vmApp)
			Expect(apps).To(HaveLen(1))

			quadlet, err := apps[0].AsQuadletApplication()
			Expect(err).ToNot(HaveOccurred())

			inline, err := quadlet.AsInlineApplicationProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(inline.Inline).To(HaveLen(2))

			paths := []string{inline.Inline[0].Path, inline.Inline[1].Path}
			Expect(paths).To(ContainElement("pod.yaml"))
			Expect(paths).To(ContainElement(fmt.Sprintf("%s.kube", deviceName)))
		})
	})
})
