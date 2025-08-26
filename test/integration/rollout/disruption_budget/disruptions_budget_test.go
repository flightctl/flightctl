package disruption_budget

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rollout/disruption_budget"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

var (
	suiteCtx context.Context
)

func TestDisruptionBudget(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Disruption budget suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Disruption budget suite")
})

var _ = Describe("Rollout disruption budget test", func() {
	const (
		FleetName = "myfleet"
	)
	var (
		ctx              context.Context
		log              *logrus.Logger
		dbName           string
		cfg              *config.Config
		storeInst        store.Store
		serviceHandler   service.Service
		ctrl             *gomock.Controller
		mockWorkerClient *worker_client.MockWorkerClient
		tvName           string
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

		f, err := storeInst.Fleet().Create(ctx, store.NullOrgId, fleet, nil)
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
		tv, err := storeInst.TemplateVersion().Create(ctx, store.NullOrgId, &templateVersion, nil)
		Expect(err).ToNot(HaveOccurred())
		tvName = *tv.Metadata.Name
		annotations := map[string]string{
			api.FleetAnnotationTemplateVersion: *tv.Metadata.Name,
		}
		Expect(storeInst.Fleet().UpdateAnnotations(ctx, store.NullOrgId, FleetName, annotations, nil, nil)).ToNot(HaveOccurred())
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
		_, err := storeInst.Device().Update(ctx, store.NullOrgId, device, nil, false, nil, nil)
		Expect(err).ToNot(HaveOccurred())
	}

	setLabels := func(labels []map[string]string, numToSet []int) {
		Expect(labels).To(HaveLen(len(numToSet)))
		devices, err := storeInst.Device().List(ctx, store.NullOrgId, store.ListParams{})
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

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = util.WithOrganizationID(ctx, store.NullOrgId)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		ctrl = gomock.NewController(GinkgoT())
		mockWorkerClient = worker_client.NewMockWorkerClient(ctrl)
		publisher := queues.NewMockPublisher(ctrl)
		publisher.EXPECT().Publish(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, mockWorkerClient, kvStore, nil, log, "", "", []string{})
	})
	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
		ctrl.Finish()
	})
	Context("Query fleets", func() {
		initTest := func(d *api.DisruptionBudget, numDevices int, annotateTv, annotateRenderedTv bool) {
			_ = createTestFleet(FleetName, d)
			createTestTemplateVersion(FleetName)
			if numDevices > 0 {
				testutil.CreateTestDevices(ctx, numDevices, storeInst.Device(), store.NullOrgId, util.SetResourceOwner(api.FleetKind, FleetName), false)
				devices, err := storeInst.Device().List(ctx, store.NullOrgId, store.ListParams{})
				Expect(err).ToNot(HaveOccurred())
				for i := range devices.Items {
					d := devices.Items[i]
					d.Status.Summary.Status = "Online"
					_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, &d, nil)
					Expect(err).ToNot(HaveOccurred())
					annotations := make(map[string]string)
					if annotateTv {
						annotations[api.DeviceAnnotationTemplateVersion] = tvName
					}
					if annotateRenderedTv {
						annotations[api.DeviceAnnotationRenderedTemplateVersion] = tvName
					}
					annotations[api.DeviceAnnotationRenderedVersion] = "5"
					Expect(storeInst.Device().UpdateAnnotations(ctx, store.NullOrgId, lo.FromPtr(d.Metadata.Name), annotations, nil)).ToNot(HaveOccurred())
					d.Status.Config.RenderedVersion = "5"
					_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, &d, nil)
					Expect(err).ToNot(HaveOccurred())
				}
			}
		}
		It("One fleet - no devices", func() {
			initTest(nil, 0, false, false)
			reconciler := disruption_budget.NewReconciler(serviceHandler, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device no matching fleet", func() {
			initTest(nil, 1, false, false)
			reconciler := disruption_budget.NewReconciler(serviceHandler, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device with matching fleet - non matching disruption budget", func() {
			initTest(nil, 1, true, false)
			mockWorkerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any())
			reconciler := disruption_budget.NewReconciler(serviceHandler, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device no matching fleet", func() {
			initTest(nil, 1, true, true)
			reconciler := disruption_budget.NewReconciler(serviceHandler, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device with matching fleet - with matching disruption budget", func() {
			initTest(disruptionBudget(lo.ToPtr(1), lo.ToPtr(1), nil), 1, true, false)
			reconciler := disruption_budget.NewReconciler(serviceHandler, log)
			mockWorkerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
		})
		It("One fleet - two devices with matching fleet - with matching disruption budget", func() {
			initTest(disruptionBudget(lo.ToPtr(1), lo.ToPtr(1), nil), 2, true, false)
			reconciler := disruption_budget.NewReconciler(serviceHandler, log)
			mockWorkerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
		})
		It("One fleet - 6 devices with matching fleet - with matching disruption budget - with labels", func() {
			initTest(disruptionBudget(lo.ToPtr(1), lo.ToPtr(1), lo.ToPtr([]string{"label-1", "label-2"})), 6, true, false)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 1})
			reconciler := disruption_budget.NewReconciler(serviceHandler, log)

			mockWorkerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any()).Times(3)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - 6 devices with matching fleet - with matching disruption budget - with labels - without unavailable", func() {
			initTest(disruptionBudget(nil, lo.ToPtr(1), lo.ToPtr([]string{"label-1", "label-2"})), 9, true, false)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 3})
			reconciler := disruption_budget.NewReconciler(serviceHandler, log)
			mockWorkerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any()).Times(6)
			reconciler.Reconcile(ctx)
		})
	})
})
