package vmrender_test

import (
	"context"
	"crypto"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	icrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
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

const (
	testFleetLabelKey    = "vm-fleet"
	testFleetLabelValue  = "true"
	rhel9LauncherImage   = "registry.example.com/virt-launcher-rhel9:test"
	rhel10LauncherImage  = "registry.example.com/virt-launcher-rhel10:test"
	defaultLauncherImage = "registry.example.com/virt-launcher:default"
)

const testVmYAML = `apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: test-vm
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
      volumes:
      - name: containerdisk
        containerDisk:
          image: quay.io/containerdisks/fedora:40
`

var _ = Describe("Enrollment VM launcher image selection", func() {
	var (
		log                *logrus.Logger
		ctx                context.Context
		orgId              uuid.UUID
		cfg                *config.Config
		dbName             string
		db                 *gorm.DB
		ctrl               *gomock.Controller
		kvStoreInst        kvstore.KVStore
		deviceStore        devicestore.Store
		fleetStore         fleetstore.Store
		tvStore            templateversionstore.Store
		deviceSvc          deviceservice.Service
		fleetSvc           fleetservice.Service
		templateVersionSvc templateversionservice.Service
		dependencyrefSvc   dependencyrefservice.Service
		repositorySvc      repositoryservice.Service
		enrollmentSvc      enrollmentrequestservice.Service
		origPath           string
		origPathSet        bool
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()

		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal([]byte(`{
			"worker": {
				"vmRender": {
					"launcherImage": "`+defaultLauncherImage+`",
					"launcherImages": {
						"rhel-9": "`+rhel9LauncherImage+`",
						"rhel-10": "`+rhel10LauncherImage+`"
					}
				}
			}
		}`), cfg)).To(Succeed())

		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		tvStore = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		erStore := enrollmentrequeststore.NewEnrollmentRequestStore(db, log.WithField("pkg", "enrollmentrequest-store"))
		csrStore := certificatesigningrequeststore.NewCertificateSigningRequestStore(db, log.WithField("pkg", "csr-store"))
		eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		repoStore := repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		depStore := dependencyrefstore.NewDependencyRefStore(db, log.WithField("pkg", "dependencyref-store"))

		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer := queues.NewMockQueueProducer(ctrl)
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		workerClient := worker_client.NewWorkerClient(mockQueueProducer, log)

		kvStoreInst, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())
		eventsSvc := events.NewServiceHandler(eventStore, workerClient, log)

		caClient, _, err := icrypto.EnsureCA(cacfg.NewDefault(GinkgoT().TempDir()))
		Expect(err).ToNot(HaveOccurred())

		deviceSvc = deviceservice.NewDeviceServiceHandler(deviceStore, nil, fleetStore, eventsSvc, kvStoreInst, "", log)
		fleetSvc = fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, log)
		templateVersionSvc = templateversionservice.NewServiceHandler(tvStore, kvStoreInst, eventsSvc, log)
		dependencyrefSvc = dependencyrefservice.NewServiceHandler(depStore, log)
		repositorySvc = repositoryservice.NewServiceHandler(repoStore, eventsSvc, log)
		enrollmentSvc = enrollmentrequestservice.NewServiceHandler(erStore, deviceStore, csrStore, caClient, kvStoreInst, eventsSvc, log, []string{}, "", "")

		ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, identity.NewMappedIdentity("admin", "uid-admin", nil, nil, true, nil))

		queuesProvider, err := queues.NewRedisProvider(ctx, log, fmt.Sprintf("vm-enroll-launcher-%s", uuid.New().String()), redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
		Expect(err).ToNot(HaveOccurred())
		Expect(rendered.Bus.Initialize(ctx, kvStoreInst, queuesProvider, 10*time.Second, log)).To(Succeed())

		origPath, origPathSet = os.LookupEnv("PATH")
		Expect(os.Setenv("PATH", filepath.Dir(vmBinaryPath)+string(os.PathListSeparator)+origPath)).To(Succeed())

		testutil.CreateTestFleet(ctx, fleetStore, orgId, "vm-launcher-fleet", &map[string]string{testFleetLabelKey: testFleetLabelValue}, nil)
		vmApp := inlineTestVmApp()
		appSpec := api.ApplicationProviderSpec{}
		Expect(appSpec.FromVmApplication(vmApp)).To(Succeed())
		tvStatus := api.TemplateVersionStatus{Applications: &[]api.ApplicationProviderSpec{appSpec}}
		Expect(testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "vm-launcher-fleet", "1.0.0", &tvStatus)).To(Succeed())
	})

	AfterEach(func() {
		restoreOrigPath(origPath, origPathSet)
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		ctrl.Finish()
	})

	DescribeTable("When a device enrolls into a VM fleet it should render the virt-launcher for its OS",
		func(distroId, distroVersion, wantImage string) {
			deviceName, csr := enrollmentNameAndCSR()
			enrollmentStatus := api.NewDeviceStatus()
			enrollmentStatus.SystemInfo.Set("distroId", distroId)
			enrollmentStatus.SystemInfo.Set("distroVersion", distroVersion)

			er := api.EnrollmentRequest{
				ApiVersion: "v1beta1",
				Kind:       "EnrollmentRequest",
				Metadata:   api.ObjectMeta{Name: lo.ToPtr(deviceName)},
				Spec: api.EnrollmentRequestSpec{
					Csr:          csr,
					DeviceStatus: &enrollmentStatus,
					Labels:       &map[string]string{testFleetLabelKey: testFleetLabelValue},
				},
			}
			created, status := enrollmentSvc.CreateEnrollmentRequest(ctx, orgId, er)
			Expect(status.Code).To(BeNumerically("==", 201))
			Expect(created).ToNot(BeNil())

			_, status = enrollmentSvc.ApproveEnrollmentRequest(ctx, orgId, deviceName, api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{},
			})
			Expect(status.Code).To(BeNumerically("==", 200))

			event := api.Event{
				Reason:         api.EventReasonResourceCreated,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: deviceName},
			}
			selectorLogic := tasks.NewFleetSelectorMatchingLogic(log, deviceSvc, fleetSvc, orgId, event)
			Expect(selectorLogic.DeviceLabelsUpdated(ctx)).To(Succeed())

			device, err := deviceStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(lo.FromPtr(device.Metadata.Owner)).To(Equal("Fleet/vm-launcher-fleet"))

			rolloutLogic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
			Expect(rolloutLogic.RolloutDevice(ctx)).To(Succeed())

			renderLogic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, nil, &mockK8sClient{}, kvStoreInst, cfg, orgId, event)
			Expect(renderLogic.RenderDevice(ctx)).To(Succeed())

			renderedDevice, getStatus := deviceSvc.GetRenderedDevice(ctx, orgId, deviceName, api.GetRenderedDeviceParams{})
			Expect(getStatus.Code).To(BeNumerically("==", 200))
			Expect(renderedDevice.Spec).ToNot(BeNil())
			Expect(renderedDevice.Spec.Applications).ToNot(BeNil())
			Expect(renderedContents(*renderedDevice.Spec.Applications)).To(ContainSubstring("Image=" + wantImage))
		},
		Entry("When distro is rhel 9 it should use the rhel-9 launcher", "rhel", "9.5 (Plow)", rhel9LauncherImage),
		Entry("When distro is rhel 10 it should use the rhel-10 launcher", "rhel", "10.0 (Coughlan)", rhel10LauncherImage),
		Entry("When distro is fedora it should use launcherImage", "fedora", "42 (Adams)", defaultLauncherImage),
	)
})

func restoreOrigPath(origPath string, origPathSet bool) {
	GinkgoHelper()
	if origPathSet {
		Expect(os.Setenv("PATH", origPath)).To(Succeed())
		return
	}
	Expect(os.Unsetenv("PATH")).To(Succeed())
}

func inlineTestVmApp() api.VmApplication {
	GinkgoHelper()
	inlineSpec := api.InlineApplicationProviderSpec{
		Inline: []api.ApplicationContent{
			{Path: "vm.yaml", Content: lo.ToPtr(testVmYAML)},
		},
	}
	vmApp := api.VmApplication{
		AppType: api.AppTypeVm,
		Name:    lo.ToPtr("test-vm"),
	}
	Expect(vmApp.FromInlineApplicationProviderSpec(inlineSpec)).To(Succeed())
	return vmApp
}

func enrollmentNameAndCSR() (string, string) {
	GinkgoHelper()
	publicKey, privateKey, err := fcrypto.NewKeyPair()
	Expect(err).ToNot(HaveOccurred())
	publicKeyHash, err := fcrypto.HashPublicKey(publicKey)
	Expect(err).ToNot(HaveOccurred())
	deviceName := strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash))
	csrPEM, err := fcrypto.MakeCSR(privateKey.(crypto.Signer), deviceName)
	Expect(err).ToNot(HaveOccurred())
	return deviceName, string(csrPEM)
}

func renderedContents(apps []api.ApplicationProviderSpec) string {
	GinkgoHelper()
	var b strings.Builder
	for _, app := range apps {
		quadlet, err := app.AsQuadletApplication()
		Expect(err).ToNot(HaveOccurred())
		inline, err := quadlet.AsInlineApplicationProviderSpec()
		Expect(err).ToNot(HaveOccurred())
		for _, f := range inline.Inline {
			if f.Content != nil {
				b.WriteString(*f.Content)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
