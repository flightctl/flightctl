package device_selection

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/rollout/device_selection"
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

func TestDeviceSelection(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Device selection suite")
}

var _ = Describe("Rollout batch sequence test", func() {
	const (
		FleetName = "myfleet"
	)
	var (
		ctx       context.Context
		log       *logrus.Logger
		dbName    string
		cfg       *config.Config
		storeInst store.Store
		tvName    string
	)
	percentageLimit := func(p v1alpha1.Percentage) *v1alpha1.Batch_Limit {
		ret := &v1alpha1.Batch_Limit{}
		Expect(ret.FromPercentage(p)).ToNot(HaveOccurred())
		return ret
	}
	intLimit := func(i int) *v1alpha1.Batch_Limit {
		ret := &v1alpha1.Batch_Limit{}
		Expect(ret.FromBatchLimit1(i)).ToNot(HaveOccurred())
		return ret
	}

	rolloutDeviceSelection := func(b v1alpha1.BatchSequence) *v1alpha1.RolloutDeviceSelection {
		ret := &v1alpha1.RolloutDeviceSelection{}
		Expect(ret.FromBatchSequence(b)).ToNot(HaveOccurred())
		return ret
	}
	createTestFleet := func(name string, b v1alpha1.BatchSequence) *v1alpha1.Fleet {

		fleet := &v1alpha1.Fleet{
			Metadata: v1alpha1.ObjectMeta{
				Name: lo.ToPtr(name),
			},
			Spec: v1alpha1.FleetSpec{
				RolloutPolicy: &v1alpha1.RolloutPolicy{
					DeviceSelection: rolloutDeviceSelection(b),
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
	updateDeviceLabels := func(device *v1alpha1.Device, labels map[string]string) {
		device.Metadata.Labels = &labels
		_, err := storeInst.Device().Update(ctx, store.NullOrgId, device, nil, false, func(b, a *model.Device) {})
		Expect(err).ToNot(HaveOccurred())
	}
	setRolledOut := func(deviceName string) {
		annotations := map[string]string{
			v1alpha1.DeviceAnnotationTemplateVersion: tvName,
		}
		Expect(storeInst.Device().UpdateAnnotations(ctx, store.NullOrgId, deviceName, annotations, nil)).ToNot(HaveOccurred())
	}
	var (
		singleElementBatchSequence = v1alpha1.BatchSequence{
			Sequence: &[]v1alpha1.Batch{
				{
					Limit: percentageLimit("100%"),
				},
			},
		}
		batchSequenceWithSelection = v1alpha1.BatchSequence{
			Sequence: &[]v1alpha1.Batch{
				{
					Selector: &v1alpha1.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-1",
							"label-2": "value-2",
						},
					},
					Limit: intLimit(1),
				},
				{
					Selector: &v1alpha1.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-1",
							"label-2": "value-2",
						},
					},
					Limit: percentageLimit("50%"),
				},
				{
					Selector: &v1alpha1.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-3",
							"label-2": "value-2",
						},
					},
				},
				{
					Limit: percentageLimit("100%"),
				},
			},
		}
		labels1 = map[string]string{
			"label-1": "value-1",
			"label-2": "value-2",
		}
		labels2 = map[string]string{
			"label-1": "value-3",
			"label-2": "value-2",
		}
	)

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

	processBatch := func(selector device_selection.RolloutDeviceSelector, numDevices int, expectedLabels map[string]string) device_selection.Selection {
		Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
		Expect(selector.Advance(ctx)).ToNot(HaveOccurred())
		selection, err := selector.CurrentSelection(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(selection.IsRolledOut(ctx)).To(Equal(numDevices == 0))
		Expect(selection.IsComplete(ctx)).To(Equal(numDevices == 0))
		devices, err := selection.Devices(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(devices.Items).To(HaveLen(numDevices))
		if expectedLabels != nil {
			for _, d := range devices.Items {
				Expect(lo.FromPtr(d.Metadata.Labels)).To(Equal(expectedLabels))
			}
		}
		for _, d := range devices.Items {
			setRolledOut(lo.FromPtr(d.Metadata.Name))
		}
		Expect(selection.IsRolledOut(ctx)).To(BeTrue())
		return selection
	}

	BeforeEach(func() {
		ctx = context.Background()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(log)
	})
	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
	})
	Context("device selection", func() {

		initTest := func(sequence v1alpha1.BatchSequence, numDevices int) device_selection.RolloutDeviceSelector {
			fleet := createTestFleet(FleetName, sequence)
			createTestTemplateVersion(FleetName)
			if numDevices > 0 {
				testutil.CreateTestDevices(ctx, numDevices, storeInst.Device(), store.NullOrgId, util.SetResourceOwner(v1alpha1.FleetKind, FleetName), false)
				devices, err := storeInst.Device().List(ctx, store.NullOrgId, store.ListParams{})
				Expect(err).ToNot(HaveOccurred())
				for i := range devices.Items {
					device := &devices.Items[i]
					device.Status.Summary.Status = "Online"
					_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, device)
					Expect(err).ToNot(HaveOccurred())
				}
			}
			selector, err := device_selection.NewRolloutDeviceSelector(fleet.Spec.RolloutPolicy.DeviceSelection, storeInst, store.NullOrgId, fleet, tvName, log)
			Expect(err).ToNot(HaveOccurred())
			return selector
		}

		It("single batch - no devices", func() {
			selector := initTest(singleElementBatchSequence, 0)
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeFalse())
		})

		It("single batch - with devices", func() {
			selector := initTest(singleElementBatchSequence, 3)
			processBatch(selector, 3, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeFalse())
		})

		It("multiple batches - with devices and label selector", func() {
			selector := initTest(batchSequenceWithSelection, 6)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 1})
			processBatch(selector, 1, labels1)
			processBatch(selector, 1, labels1)
			processBatch(selector, 1, labels2)
			processBatch(selector, 3, nil)
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeFalse())
		})
	})
	Context("reconciler", func() {
		var (
			ctrl                *gomock.Controller
			mockCallbackManager *tasks.MockCallbackManager
		)
		BeforeEach(func() {
			ctrl = gomock.NewController(GinkgoT())
			mockCallbackManager = tasks.NewMockCallbackManager(ctrl)
		})
		AfterEach(func() {
			ctrl.Finish()
		})
		initFleet := func(name string, sequence v1alpha1.BatchSequence, numDevices int, withTemplateVersion bool) {
			_ = createTestFleet(name, sequence)
			if withTemplateVersion {
				createTestTemplateVersion(name)
			}
			if numDevices > 0 {
				testutil.CreateTestDevices(ctx, numDevices, storeInst.Device(), store.NullOrgId, util.SetResourceOwner(v1alpha1.FleetKind, name), false)
				devices, err := storeInst.Device().List(ctx, store.NullOrgId, store.ListParams{})
				Expect(err).ToNot(HaveOccurred())
				for i := range devices.Items {
					d := devices.Items[i]
					d.Status.Summary.Status = "Online"
					_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, &d)
					Expect(err).ToNot(HaveOccurred())
				}
			}
		}
		approveRollout := func(fleetName string) {
			annotations := map[string]string{
				model.FleetAnnotationRolloutApproved: "true",
			}
			Expect(storeInst.Fleet().UpdateAnnotations(ctx, store.NullOrgId, fleetName, annotations, nil)).ToNot(HaveOccurred())
		}
		setAutomaticApproval := func(fleetName string) {
			annotations := map[string]string{
				model.FleetAnnotationRolloutApprovalMethod: "automatic",
			}
			Expect(storeInst.Fleet().UpdateAnnotations(ctx, store.NullOrgId, fleetName, annotations, nil)).ToNot(HaveOccurred())
		}

		setDevicesComplete := func(fleetName, tvName string) {
			devices, err := storeInst.Device().List(ctx, store.NullOrgId, store.ListParams{
				Filter: map[string][]any{
					"selected_for_rollout": {true},
				},
				Owners: []string{fmt.Sprintf("%s/%s", model.FleetKind, fleetName)},
			})
			Expect(err).ToNot(HaveOccurred())
			for i := range devices.Items {
				d := devices.Items[i]
				renderedVersion := "5"
				annotations := map[string]string{
					v1alpha1.DeviceAnnotationTemplateVersion:         tvName,
					v1alpha1.DeviceAnnotationRenderedTemplateVersion: tvName,
					v1alpha1.DeviceAnnotationRenderedVersion:         renderedVersion,
				}
				Expect(storeInst.Device().UpdateAnnotations(ctx, store.NullOrgId, lo.FromPtr(d.Metadata.Name), annotations, nil)).ToNot(HaveOccurred())
				d.Status.Config.RenderedVersion = renderedVersion
				_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, &d)
				Expect(err).ToNot(HaveOccurred())
			}
		}
		getBatchLocation := func(fleetName string) int {
			fleet, err := storeInst.Fleet().Get(ctx, store.NullOrgId, fleetName)
			Expect(err).ToNot(HaveOccurred())
			m := lo.FromPtr(fleet.Metadata.Annotations)
			if m == nil {
				return -1
			}
			ann, exists := m[model.FleetAnnotationBatchNumber]
			if !exists {
				return -1
			}
			i, err := strconv.ParseInt(ann, 10, 64)
			Expect(err).ToNot(HaveOccurred())
			return int(i)
		}

		It("single fleet - no devices", func() {
			initFleet(FleetName, batchSequenceWithSelection, 0, false)
			reconciler := device_selection.NewReconciler(storeInst, mockCallbackManager, log)
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(-1))
		})
		It("single fleet - single device", func() {
			initFleet(FleetName, batchSequenceWithSelection, 1, false)
			reconciler := device_selection.NewReconciler(storeInst, mockCallbackManager, log)
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(-1))
		})
		It("single fleet with template version - single device", func() {
			initFleet(FleetName, batchSequenceWithSelection, 1, true)
			reconciler := device_selection.NewReconciler(storeInst, mockCallbackManager, log)
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(3))
			approveRollout(FleetName)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any())
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(3))
			setDevicesComplete(FleetName, tvName)
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(4))
		})
		It("single fleet with template version - multiple devices", func() {
			initFleet(FleetName, batchSequenceWithSelection, 6, true)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 1})
			reconciler := device_selection.NewReconciler(storeInst, mockCallbackManager, log)
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(0))
			approveRollout(FleetName)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any())
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(0))
			setDevicesComplete(FleetName, tvName)
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(1))
			approveRollout(FleetName)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any())
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(1))
			setDevicesComplete(FleetName, tvName)
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(2))
			setAutomaticApproval(FleetName)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any())
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(2))
			setDevicesComplete(FleetName, tvName)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any())
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(3))
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any())
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(3))
			setDevicesComplete(FleetName, tvName)
			reconciler.Reconcile()
			Expect(getBatchLocation(FleetName)).To(Equal(4))
		})
	})
})
