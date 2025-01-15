package disruption_allowance

import (
	"context"
	"reflect"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/rollout/disruption_allowance"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

func TestDisruptionAllowance(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Disruption allowance suite")
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

var _ = Describe("Rollout disruption allowance test", func() {
	const (
		FleetName = "myfleet"
	)
	var (
		ctx                 context.Context
		log                 *logrus.Logger
		dbName              string
		cfg                 *config.Config
		storeInst           store.Store
		ctrl                *gomock.Controller
		mockCallbackManager *tasks.MockCallbackManager
		tvName              string
	)

	disruptionAllowance := func(maxUnavailable, minAvailable *int, groupBy *[]string) *v1alpha1.DisruptionAllowance {
		return &v1alpha1.DisruptionAllowance{
			GroupBy:        groupBy,
			MaxUnavailable: maxUnavailable,
			MinAvailable:   minAvailable,
		}
	}
	createTestFleet := func(name string, d *v1alpha1.DisruptionAllowance) *v1alpha1.Fleet {

		fleet := &v1alpha1.Fleet{
			Metadata: v1alpha1.ObjectMeta{
				Name: lo.ToPtr(name),
			},
			Spec: v1alpha1.FleetSpec{
				RolloutPolicy: &v1alpha1.RolloutPolicy{
					DisruptionAllowance: d,
				},
			},
		}

		f, err := storeInst.Fleet().Create(ctx, store.NullOrgId, fleet, func(_, _ *model.Fleet) {})
		Expect(err).ToNot(HaveOccurred())
		return f
	}

	createTestTemplateVersion := func(ownerName string) {
		templateVersion := v1alpha1.TemplateVersion{
			Metadata: v1alpha1.ObjectMeta{
				Name:  util.TimeStampStringPtr(),
				Owner: util.SetResourceOwner(v1alpha1.FleetKind, ownerName),
			},
			Spec:   v1alpha1.TemplateVersionSpec{Fleet: ownerName},
			Status: &v1alpha1.TemplateVersionStatus{},
		}
		tv, err := storeInst.TemplateVersion().Create(ctx, store.NullOrgId, &templateVersion, func(_ *model.TemplateVersion) {})
		Expect(err).ToNot(HaveOccurred())
		tvName = *tv.Metadata.Name
		annotations := map[string]string{
			v1alpha1.FleetAnnotationTemplateVersion: *tv.Metadata.Name,
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
	updateDeviceLabels := func(device *v1alpha1.Device, labels map[string]string) {
		device.Metadata.Labels = &labels
		_, err := storeInst.Device().Update(ctx, store.NullOrgId, device, nil, false, func(b, a *model.Device) {})
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
		mockCallbackManager = tasks.NewMockCallbackManager(ctrl)
	})
	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
		ctrl.Finish()
	})
	Context("Query fleets", func() {
		initTest := func(d *v1alpha1.DisruptionAllowance, numDevices int, annotateTv, annotateRenderedTv bool) {
			_ = createTestFleet(FleetName, d)
			createTestTemplateVersion(FleetName)
			if numDevices > 0 {
				testutil.CreateTestDevices(ctx, numDevices, storeInst.Device(), store.NullOrgId, util.SetResourceOwner(v1alpha1.FleetKind, FleetName), false)
				devices, err := storeInst.Device().List(ctx, store.NullOrgId, store.ListParams{})
				Expect(err).ToNot(HaveOccurred())
				for i := range devices.Items {
					d := devices.Items[i]
					d.Status.Summary.Status = "Online"
					_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, &d)
					Expect(err).ToNot(HaveOccurred())
					annotations := make(map[string]string)
					if annotateTv {
						annotations[v1alpha1.DeviceAnnotationTemplateVersion] = tvName
					}
					if annotateRenderedTv {
						annotations[v1alpha1.DeviceAnnotationRenderedTemplateVersion] = tvName
					}
					annotations[v1alpha1.DeviceAnnotationRenderedVersion] = "5"
					Expect(storeInst.Device().UpdateAnnotations(ctx, store.NullOrgId, lo.FromPtr(d.Metadata.Name), annotations, nil)).ToNot(HaveOccurred())
					d.Status.Config.RenderedVersion = "5"
					_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, &d)
					Expect(err).ToNot(HaveOccurred())
				}
			}
		}
		It("One fleet - no devices", func() {
			initTest(nil, 0, false, false)
			reconciler := disruption_allowance.NewReconciler(storeInst, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device no matching fleet", func() {
			initTest(nil, 1, false, false)
			reconciler := disruption_allowance.NewReconciler(storeInst, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device with matching fleet - non matching disruption allowance", func() {
			initTest(nil, 1, true, false)
			reconciler := disruption_allowance.NewReconciler(storeInst, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device no matching fleet", func() {
			initTest(nil, 1, true, true)
			reconciler := disruption_allowance.NewReconciler(storeInst, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - one device with matching fleet - with matching disruption allowance", func() {
			initTest(disruptionAllowance(lo.ToPtr(1), lo.ToPtr(1), nil), 1, true, false)
			reconciler := disruption_allowance.NewReconciler(storeInst, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
		})
		It("One fleet - two devices with matching fleet - with matching disruption allowance", func() {
			initTest(disruptionAllowance(lo.ToPtr(1), lo.ToPtr(1), nil), 2, true, false)
			reconciler := disruption_allowance.NewReconciler(storeInst, mockCallbackManager, log)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
		})
		It("One fleet - 6 devices with matching fleet - with matching disruption allowance - with labels", func() {
			initTest(disruptionAllowance(lo.ToPtr(1), lo.ToPtr(1), lo.ToPtr([]string{"label-1", "label-2"})), 6, true, false)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 1})
			reconciler := disruption_allowance.NewReconciler(storeInst, mockCallbackManager, log)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
		})
		It("One fleet - 6 devices with matching fleet - with matching disruption allowance - with labels - without unavailable", func() {
			initTest(disruptionAllowance(nil, lo.ToPtr(1), lo.ToPtr([]string{"label-1", "label-2"})), 9, true, false)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 3})
			reconciler := disruption_allowance.NewReconciler(storeInst, mockCallbackManager, log)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), equalLabels(labels2)).Times(2)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), equalLabels(labels1)).Times(3)
			mockCallbackManager.EXPECT().DeviceSourceUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
		})
	})
})
