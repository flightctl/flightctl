package disruption_budget

import (
	"context"
	"reflect"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rollout/disruption_budget"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

func TestDisruptionBudget(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Disruption budget suite")
}

type equalLabelsMatcher func(name string) bool

func (e equalLabelsMatcher) Matches(a any) bool {
	if s, ok := a.(string); ok {
		return e(s)
	}
	return false
}

func (e equalLabelsMatcher) String() string {
	return "Equal labels"
}

var _ = Describe("Rollout disruption budget test", func() {
	const (
		FleetName = "myfleet"
	)
	var (
		ctx                 context.Context
		log                 *logrus.Logger
		dbName              string
		cfg                 *config.Config
		storeInst           store.Store
		serviceHandler      *service.ServiceHandler
		ctrl                *gomock.Controller
		mockCallbackManager *tasks_client.MockCallbackManager
		tvName              string
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
		Expect(storeInst.Fleet().UpdateAnnotations(ctx, store.NullOrgId, FleetName, annotations, nil)).ToNot(HaveOccurred())
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
		_, _, err := storeInst.Device().Update(ctx, store.NullOrgId, device, nil, false, nil, nil)
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

	equalLabels := func(l map[string]string) equalLabelsMatcher {
		return func(name string) bool {
			device, err := storeInst.Device().Get(ctx, store.NullOrgId, name)
			Expect(err).ToNot(HaveOccurred())
			return reflect.DeepEqual(lo.FromPtr(device.Metadata.Labels), l)
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(log)
		ctrl = gomock.NewController(GinkgoT())
		mockCallbackManager = tasks_client.NewMockCallbackManager(ctrl)
		publisher := queues.NewMockPublisher(ctrl)
		publisher.EXPECT().Publish(gomock.Any()).Return(nil).AnyTimes()
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, mockCallbackManager, kvStore, nil, log, "", "")
	})
	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
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
					_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, &d)
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
					_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, &d)
					Expect(err).ToNot(HaveOccurred())
				}
			}
		}
		It("One fleet - no devices", func() {
			initTest(nil, 0, false, false)
			reconciler := disruption_budget.NewReconciler(serviceHandler, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device no matching fleet", func() {
			initTest(nil, 1, false, false)
			reconciler := disruption_budget.NewReconciler(serviceHandler, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device with matching fleet - non matching disruption budget", func() {
			initTest(nil, 1, true, false)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), gomock.Any())
			reconciler := disruption_budget.NewReconciler(serviceHandler, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device no matching fleet", func() {
			initTest(nil, 1, true, true)
			reconciler := disruption_budget.NewReconciler(serviceHandler, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device with matching fleet - with matching disruption budget", func() {
			initTest(disruptionBudget(lo.ToPtr(1), lo.ToPtr(1), nil), 1, true, false)
			reconciler := disruption_budget.NewReconciler(serviceHandler, mockCallbackManager, log)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
		})
		It("One fleet - two devices with matching fleet - with matching disruption budget", func() {
			initTest(disruptionBudget(lo.ToPtr(1), lo.ToPtr(1), nil), 2, true, false)
			reconciler := disruption_budget.NewReconciler(serviceHandler, mockCallbackManager, log)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
		})
		It("One fleet - 6 devices with matching fleet - with matching disruption budget - with labels", func() {
			initTest(disruptionBudget(lo.ToPtr(1), lo.ToPtr(1), lo.ToPtr([]string{"label-1", "label-2"})), 6, true, false)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 1})
			reconciler := disruption_budget.NewReconciler(serviceHandler, mockCallbackManager, log)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), equalLabels(labels1))
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), equalLabels(labels2))
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
		})
		It("One fleet - 6 devices with matching fleet - with matching disruption budget - with labels - without unavailable", func() {
			initTest(disruptionBudget(nil, lo.ToPtr(1), lo.ToPtr([]string{"label-1", "label-2"})), 9, true, false)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 3})
			reconciler := disruption_budget.NewReconciler(serviceHandler, mockCallbackManager, log)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), equalLabels(labels2)).Times(2)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), equalLabels(labels1)).Times(3)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
		})
	})
})
