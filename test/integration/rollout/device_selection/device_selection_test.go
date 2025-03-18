package device_selection

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rollout/device_selection"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
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
	"gorm.io/gorm"
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
		ctx            context.Context
		log            *logrus.Logger
		dbName         string
		cfg            *config.Config
		storeInst      store.Store
		serviceHandler *service.ServiceHandler
		tvName         string
		db             *gorm.DB
	)
	percentageLimit := func(p api.Percentage) *api.Batch_Limit {
		ret := &api.Batch_Limit{}
		Expect(ret.FromPercentage(p)).ToNot(HaveOccurred())
		return ret
	}
	intLimit := func(i int) *api.Batch_Limit {
		ret := &api.Batch_Limit{}
		Expect(ret.FromBatchLimit1(i)).ToNot(HaveOccurred())
		return ret
	}

	setLastSuccessPercentage := func(fleetName string, percentage int64) {
		annotations := map[string]string{
			api.FleetAnnotationLastBatchCompletionReport: fmt.Sprintf(`{"successPercentage":%d}`, percentage),
		}
		Expect(storeInst.Fleet().UpdateAnnotations(ctx, store.NullOrgId, fleetName, annotations, nil)).ToNot(HaveOccurred())
	}
	setAutomaticApproval := func(fleetName string) {
		annotations := map[string]string{
			api.FleetAnnotationRolloutApprovalMethod: "automatic",
		}
		Expect(storeInst.Fleet().UpdateAnnotations(ctx, store.NullOrgId, fleetName, annotations, nil)).ToNot(HaveOccurred())
	}
	rolloutDeviceSelection := func(b api.BatchSequence) *api.RolloutDeviceSelection {
		ret := &api.RolloutDeviceSelection{}
		Expect(ret.FromBatchSequence(b)).ToNot(HaveOccurred())
		return ret
	}
	createTestFleetWithThreshold := func(name string, b api.BatchSequence, threshold *api.Percentage) *api.Fleet {

		fleet := &api.Fleet{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr(name),
			},
			Spec: api.FleetSpec{
				RolloutPolicy: &api.RolloutPolicy{
					DeviceSelection:  rolloutDeviceSelection(b),
					SuccessThreshold: threshold,
				},
			},
		}

		f, err := storeInst.Fleet().Create(ctx, store.NullOrgId, fleet, nil)
		Expect(err).ToNot(HaveOccurred())
		return f
	}

	createTestFleet := func(name string, b api.BatchSequence) *api.Fleet {
		return createTestFleetWithThreshold(name, b, nil)
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
	updateDeviceLabels := func(device *api.Device, labels map[string]string) {
		device.Metadata.Labels = &labels
		_, err := storeInst.Device().Update(ctx, store.NullOrgId, device, nil, false, nil, nil)
		Expect(err).ToNot(HaveOccurred())
	}
	setRolledOut := func(deviceName string) {
		annotations := map[string]string{
			api.DeviceAnnotationTemplateVersion: tvName,
		}
		Expect(storeInst.Device().UpdateAnnotations(ctx, store.NullOrgId, deviceName, annotations, nil)).ToNot(HaveOccurred())
	}

	setRendered := func(deviceName string) {
		annotations := map[string]string{
			api.DeviceAnnotationRenderedTemplateVersion: tvName,
			api.DeviceAnnotationRenderedVersion:         "5",
		}
		Expect(storeInst.Device().UpdateAnnotations(ctx, store.NullOrgId, deviceName, annotations, nil)).ToNot(HaveOccurred())
		Expect(db.Model(&model.Device{}).Where("org_id = ? and name = ?", store.NullOrgId, deviceName).Update("render_timestamp", time.Now()).Error).ToNot(HaveOccurred())
	}

	setRenderTimestamp := func(deviceName string, durationDelta time.Duration) {
		timeToSet := time.Now().Add(-durationDelta)
		Expect(db.Model(&model.Device{}).Where("org_id = ? and name = ?", store.NullOrgId, deviceName).Update("render_timestamp", timeToSet).Error).ToNot(HaveOccurred())
	}

	setRenderedVersion := func(deviceName string) {
		Expect(db.Model(&model.Device{}).Where("name = ?", deviceName).Update("status",
			gorm.Expr(`jsonb_set(status, '{config,renderedVersion}', '"5"')`)).Error).ToNot(HaveOccurred())
	}

	setFailed := func(deviceName string) {
		device, err := storeInst.Device().Get(ctx, store.NullOrgId, deviceName)
		Expect(err).ToNot(HaveOccurred())
		var condition *api.Condition
		for i := range device.Status.Conditions {
			c := &device.Status.Conditions[i]
			if c.Type == api.ConditionTypeDeviceUpdating {
				condition = c
				break
			}
		}
		if condition == nil {
			device.Status.Conditions = append(device.Status.Conditions, api.Condition{
				Type: api.ConditionTypeDeviceUpdating,
			})
			condition = &device.Status.Conditions[len(device.Status.Conditions)-1]
		}
		condition.Reason = "Error"
		_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, device)
		Expect(err).ToNot(HaveOccurred())
	}

	var (
		singleElementBatchSequence = api.BatchSequence{
			Sequence: &[]api.Batch{
				{
					Limit: percentageLimit("100%"),
				},
			},
		}
		batchSequenceWithSelection = api.BatchSequence{
			Sequence: &[]api.Batch{
				{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-1",
							"label-2": "value-2",
						},
					},
					Limit: intLimit(1),
				},
				{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-1",
							"label-2": "value-2",
						},
					},
					Limit: percentageLimit("50%"),
				},
				{
					Selector: &api.LabelSelector{
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
		incompleteBatchSequenceWithSelection = api.BatchSequence{
			Sequence: &[]api.Batch{
				{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-1",
							"label-2": "value-2",
						},
					},
					Limit: intLimit(1),
				},
				{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-1",
							"label-2": "value-2",
						},
					},
					Limit: percentageLimit("50%"),
				},
				{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-3",
							"label-2": "value-2",
						},
					},
				},
				{
					Limit: percentageLimit("80%"),
				},
			},
		}
		batchSequenceWithAbsoluteLimit = api.BatchSequence{
			Sequence: &[]api.Batch{
				{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-1",
							"label-2": "value-2",
						},
					},
					Limit: intLimit(1),
				},
				{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-1",
							"label-2": "value-2",
						},
					},
					Limit: intLimit(1),
				},
				{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{
							"label-1": "value-3",
							"label-2": "value-2",
						},
					},
				},
				{
					Limit: intLimit(1),
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
		storeInst, cfg, dbName, db = store.PrepareDBForUnitTests(log)
		ctrl := gomock.NewController(GinkgoT())
		publisher := queues.NewMockPublisher(ctrl)
		publisher.EXPECT().Publish(gomock.Any()).Return(nil).AnyTimes()
		callbackManager := tasks_client.NewCallbackManager(publisher, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, callbackManager, kvStore, nil, log, "", "")
	})
	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
	})
	Context("device selection", func() {

		initTestWithThreshold := func(sequence api.BatchSequence, numDevices int, updateTimeout *api.Duration, threshold *api.Percentage) device_selection.RolloutDeviceSelector {
			fleet := createTestFleetWithThreshold(FleetName, sequence, threshold)
			createTestTemplateVersion(FleetName)
			if numDevices > 0 {
				testutil.CreateTestDevices(ctx, numDevices, storeInst.Device(), store.NullOrgId, util.SetResourceOwner(api.FleetKind, FleetName), false)
				devices, err := storeInst.Device().List(ctx, store.NullOrgId, store.ListParams{})
				Expect(err).ToNot(HaveOccurred())
				for i := range devices.Items {
					device := &devices.Items[i]
					device.Status.Summary.Status = "Online"
					_, err = storeInst.Device().UpdateStatus(ctx, store.NullOrgId, device)
					Expect(err).ToNot(HaveOccurred())
				}
			}
			selector, err := device_selection.NewRolloutDeviceSelector(fleet.Spec.RolloutPolicy.DeviceSelection, updateTimeout, serviceHandler, store.NullOrgId, fleet, tvName, log)
			Expect(err).ToNot(HaveOccurred())
			return selector
		}

		initTest := func(sequence api.BatchSequence, numDevices int, updateTimeout *api.Duration) device_selection.RolloutDeviceSelector {
			return initTestWithThreshold(sequence, numDevices, updateTimeout, nil)
		}

		setSelected := func(deviceName string) {
			annotations := map[string]string{
				api.DeviceAnnotationSelectedForRollout: "",
			}
			Expect(storeInst.Device().UpdateAnnotations(ctx, store.NullOrgId, deviceName, annotations, nil)).ToNot(HaveOccurred())
		}

		It("single batch - no devices", func() {
			selector := initTest(singleElementBatchSequence, 0, nil)
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeFalse())
		})

		It("single batch - with devices", func() {
			selector := initTest(singleElementBatchSequence, 3, nil)
			processBatch(selector, 3, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeFalse())
		})

		It("multiple batches - with devices and label selector", func() {
			selector := initTest(batchSequenceWithSelection, 6, nil)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 1})
			processBatch(selector, 1, labels1)
			processBatch(selector, 1, labels1)
			processBatch(selector, 1, labels2)
			processBatch(selector, 3, nil)
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
			processBatch(selector, 0, nil)
			Expect(selector.HasMoreSelections(ctx)).To(BeFalse())
		})
		It("update timeout", func() {
			selector := initTest(singleElementBatchSequence, 6, lo.ToPtr("20s"))
			Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
			Expect(selector.Advance(ctx)).ToNot(HaveOccurred())
			selection, err := selector.CurrentSelection(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.IsRolledOut(ctx)).To(Equal(false))
			Expect(selection.IsComplete(ctx)).To(Equal(false))
			devices, err := selection.Devices(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(devices.Items).To(HaveLen(6))
			for _, d := range devices.Items {
				setRolledOut(lo.FromPtr(d.Metadata.Name))
				setRendered(lo.FromPtr(d.Metadata.Name))
			}
			Expect(selection.IsRolledOut(ctx)).To(Equal(true))
			Expect(selection.IsComplete(ctx)).To(Equal(false))
			for _, d := range devices.Items {
				setRenderTimestamp(lo.FromPtr(d.Metadata.Name), 20*time.Second)
			}
			Expect(selection.IsRolledOut(ctx)).To(Equal(true))
			Expect(selection.IsComplete(ctx)).To(Equal(true))
		})
		DescribeTable("may approve automatically",
			func(lastSuccessPercentage int, automaticApproval bool, threshold *string, expectedMayApprove bool) {
				selector := initTestWithThreshold(singleElementBatchSequence, 1, lo.ToPtr("20s"), threshold)
				Expect(selector.HasMoreSelections(ctx)).To(BeTrue())
				Expect(selector.Advance(ctx)).ToNot(HaveOccurred())
				setLastSuccessPercentage(FleetName, int64(lastSuccessPercentage))
				if automaticApproval {
					setAutomaticApproval(FleetName)
				}
				selection, err := selector.CurrentSelection(ctx)
				Expect(err).ToNot(HaveOccurred())
				mayApprove, err := selection.MayApproveAutomatically()
				Expect(err).ToNot(HaveOccurred())
				Expect(mayApprove).To(Equal(expectedMayApprove))
			},
			Entry("approval not automatic", 100, false, nil, false),
			Entry("approval is automatic", 100, true, nil, true),
			Entry("approval is automatic - last success percentage below 90", 89, true, nil, false),
			Entry("approval is automatic - last success percentage below 90, threshold below success percentage", 89, true, lo.ToPtr("88%"), true),
		)

		type Bounds struct {
			start  int
			length int
		}

		bnds := func(vals ...int) *Bounds {
			switch len(vals) {
			case 0:
				return nil
			case 1:
				return &Bounds{
					start:  0,
					length: vals[0],
				}
			case 2:
				return &Bounds{
					start:  vals[0],
					length: vals[1],
				}
			default:
				Fail(fmt.Sprintf("Invalid bounds length %d", len(vals)))
			}
			return nil
		}

		lowerBound := func(b *Bounds) int {
			if b == nil {
				return 0
			}
			return b.start
		}
		upperBound := func(b *Bounds) int {
			if b == nil {
				return 0
			}
			return b.start + b.length
		}

		setupCompletion := func(numDevices int, selected, rolledOut, rendered, renderedVersion, failed, timedOut *Bounds) device_selection.RolloutDeviceSelector {
			selector := initTest(singleElementBatchSequence, numDevices, lo.ToPtr("20h"))
			var devices []*model.Device
			Expect(db.Find(&devices).Error).ToNot(HaveOccurred())
			Expect(devices).To(HaveLen(numDevices))
			applyRange := func(b *Bounds, f func(name string)) {
				Expect(numDevices).To(BeNumerically(">=", upperBound(b)))
				for i := lowerBound(b); i != upperBound(b); i++ {
					f(devices[i].Name)
				}

			}
			applyRange(selected, setSelected)
			applyRange(rolledOut, setRolledOut)
			applyRange(rendered, setRendered)
			applyRange(renderedVersion, setRenderedVersion)
			applyRange(failed, setFailed)
			applyRange(timedOut, func(name string) { setRenderTimestamp(name, 21*time.Hour) })
			return selector
		}

		DescribeTable("batch completion",
			func(numDevices int, selected, rolledOut, rendered, renderedVersion, failed, timedOut *Bounds, expectedCompletion bool) {
				selector := setupCompletion(numDevices, selected, rolledOut, rendered, renderedVersion, failed, timedOut)
				selection, err := selector.CurrentSelection(ctx)
				Expect(err).ToNot(HaveOccurred())
				isComplete, err := selection.IsComplete(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(isComplete).To(Equal(expectedCompletion))
			},
			Entry("no devices", 0, nil, nil, nil, nil, nil, nil, true),
			Entry("no selected device", 1, nil, nil, nil, nil, nil, nil, true),
			Entry("multiple devices some selected devices with template version and mixed completion reasons - 1 incomplete", 10, bnds(6), bnds(6), bnds(6), bnds(2), bnds(2, 2), bnds(4, 1), false),
			Entry("multiple devices some selected devices with template version and mixed completion reasons - all complete", 10, bnds(6), bnds(6), bnds(6), bnds(2), bnds(2, 2), bnds(4, 2), true),
			Entry("multiple devices some selected devices with template version and mixed completion reasons - one complete out of selected range", 10, bnds(6), bnds(7), bnds(7), bnds(2), bnds(2, 2), bnds(5, 2), false),
		)
		DescribeTable("success percentage",
			func(numDevices int, selected, rolledOut, rendered, renderedVersion, failed, timedOut *Bounds, expectedSuccessPercentage int) {
				selector := setupCompletion(numDevices, selected, rolledOut, rendered, renderedVersion, failed, timedOut)
				selection, err := selector.CurrentSelection(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(selection.SetCompletionReport(ctx)).ToNot(HaveOccurred())
				fleet, err := storeInst.Fleet().Get(ctx, store.NullOrgId, FleetName)
				Expect(err).ToNot(HaveOccurred())
				val, exists := util.GetFromMap(lo.FromPtr(fleet.Metadata.Annotations), api.FleetAnnotationLastBatchCompletionReport)
				Expect(exists).To(Equal(selected != nil && selected.length > 0))
				if exists {
					var report device_selection.CompletionReport
					Expect(json.Unmarshal([]byte(val), &report)).ToNot(HaveOccurred())
					Expect(report.SuccessPercentage).To(Equal(int64(expectedSuccessPercentage)))
				}
			},
			Entry("no devices", 0, nil, nil, nil, nil, nil, nil, 100),
			Entry("no selected device", 1, nil, nil, nil, nil, nil, nil, 100),
			Entry("multiple devices some selected devices with template version and mixed completion reasons - 1 incomplete", 10, bnds(6), bnds(6), bnds(6), bnds(2), bnds(2, 2), bnds(4, 1), 33),
			Entry("multiple devices some selected devices with template version and mixed completion reasons - all complete", 10, bnds(6), bnds(6), bnds(6), bnds(2), bnds(2, 2), bnds(4, 2), 33),
			Entry("multiple devices some selected devices with template version and mixed completion reasons - one complete out of selected range", 10, bnds(6), bnds(7), bnds(7), bnds(2), bnds(2, 2), bnds(5, 2), 33),
		)
	})
	Context("reconciler", func() {
		var (
			ctrl                *gomock.Controller
			mockCallbackManager *tasks_client.MockCallbackManager
		)
		BeforeEach(func() {
			ctrl = gomock.NewController(GinkgoT())
			mockCallbackManager = tasks_client.NewMockCallbackManager(ctrl)
		})
		AfterEach(func() {
			ctrl.Finish()
		})
		initFleet := func(name string, sequence api.BatchSequence, numDevices int, withTemplateVersion bool) {
			_ = createTestFleet(name, sequence)
			if withTemplateVersion {
				createTestTemplateVersion(name)
			}
			if numDevices > 0 {
				testutil.CreateTestDevices(ctx, numDevices, storeInst.Device(), store.NullOrgId, util.SetResourceOwner(api.FleetKind, name), false)
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

		setDevicesComplete := func(fleetName, tvName string) {
			devices, err := storeInst.Device().List(ctx, store.NullOrgId, store.ListParams{
				AnnotationSelector: selector.NewAnnotationSelectorOrDie(api.MatchExpression{
					Key:      api.DeviceAnnotationSelectedForRollout,
					Operator: api.Exists,
				}.String()),
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"metadata.owner": util.ResourceOwner(api.FleetKind, fleetName)}),
			})
			Expect(err).ToNot(HaveOccurred())
			for i := range devices.Items {
				d := devices.Items[i]
				renderedVersion := "5"
				annotations := map[string]string{
					api.DeviceAnnotationTemplateVersion:         tvName,
					api.DeviceAnnotationRenderedTemplateVersion: tvName,
					api.DeviceAnnotationRenderedVersion:         renderedVersion,
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
			ann, exists := m[api.FleetAnnotationBatchNumber]
			if !exists {
				return -1
			}
			i, err := strconv.ParseInt(ann, 10, 64)
			Expect(err).ToNot(HaveOccurred())
			return int(i)
		}

		It("single fleet - no devices", func() {
			initFleet(FleetName, batchSequenceWithSelection, 0, false)
			reconciler := device_selection.NewReconciler(serviceHandler, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(-1))
		})
		It("single fleet - single device", func() {
			initFleet(FleetName, batchSequenceWithSelection, 1, false)
			reconciler := device_selection.NewReconciler(serviceHandler, mockCallbackManager, log)
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(-1))
		})
		It("single fleet with template version - single device", func() {
			initFleet(FleetName, batchSequenceWithSelection, 1, true)
			reconciler := device_selection.NewReconciler(serviceHandler, mockCallbackManager, log)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(3))
			setDevicesComplete(FleetName, tvName)
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(5))
		})
		It("single fleet with template version - multiple devices", func() {
			initFleet(FleetName, incompleteBatchSequenceWithSelection, 10, true)
			setLabels([]map[string]string{labels1, labels2}, []int{4, 1})
			reconciler := device_selection.NewReconciler(serviceHandler, mockCallbackManager, log)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(0))
			setDevicesComplete(FleetName, tvName)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(1))
			setDevicesComplete(FleetName, tvName)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(2))
			setDevicesComplete(FleetName, tvName)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(3))
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(3))
			setDevicesComplete(FleetName, tvName)
			mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(4))
			setDevicesComplete(FleetName, tvName)
			reconciler.Reconcile(ctx)
			Expect(getBatchLocation(FleetName)).To(Equal(5))
		})
		Context("definition updated", func() {
			updateDefinition := func(definition *api.RolloutDeviceSelection) {
				fleet, err := storeInst.Fleet().Get(ctx, store.NullOrgId, FleetName)
				Expect(err).ToNot(HaveOccurred())
				if fleet.Spec.RolloutPolicy == nil {
					fleet.Spec.RolloutPolicy = &api.RolloutPolicy{}
				}
				fleet.Spec.RolloutPolicy.DeviceSelection = definition
				_, err = storeInst.Fleet().Update(ctx, store.NullOrgId, fleet, nil, false, nil)
				Expect(err).ToNot(HaveOccurred())
			}
			fromBatchSequence := func(b api.BatchSequence) *api.RolloutDeviceSelection {
				var ret api.RolloutDeviceSelection
				Expect(ret.FromBatchSequence(b)).ToNot(HaveOccurred())
				return &ret
			}
			checkFleetAnnotations := func(expected bool) {
				fleet, err := storeInst.Fleet().Get(ctx, store.NullOrgId, FleetName)
				Expect(err).ToNot(HaveOccurred())
				fleetAnnotations := []string{
					api.FleetAnnotationBatchNumber,
					api.FleetAnnotationLastBatchCompletionReport,
					api.FleetAnnotationRolloutApproved,
					api.FleetAnnotationRolloutApprovalMethod,
					api.FleetAnnotationDeployingTemplateVersion,
					api.FleetAnnotationDeviceSelectionConfigDigest,
				}
				Expect(lo.NoneBy(fleetAnnotations, func(ann string) bool {
					return lo.HasKey(lo.CoalesceMapOrEmpty(lo.FromPtr(fleet.Metadata.Annotations)), ann)
				})).To(Equal(expected))
			}
			It("device selection definition updated", func() {
				initFleet(FleetName, incompleteBatchSequenceWithSelection, 10, true)
				setLabels([]map[string]string{labels1, labels2}, []int{4, 1})
				reconciler := device_selection.NewReconciler(serviceHandler, mockCallbackManager, log)
				mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
				checkFleetAnnotations(true)
				reconciler.Reconcile(ctx)
				Expect(getBatchLocation(FleetName)).To(Equal(0))
				setDevicesComplete(FleetName, tvName)
				mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
				checkFleetAnnotations(false)
				reconciler.Reconcile(ctx)
				Expect(getBatchLocation(FleetName)).To(Equal(1))
				updateDefinition(fromBatchSequence(batchSequenceWithAbsoluteLimit))
				mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
				checkFleetAnnotations(false)
				reconciler.Reconcile(ctx)
				Expect(getBatchLocation(FleetName)).To(Equal(0))
				setDevicesComplete(FleetName, tvName)
				mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
				checkFleetAnnotations(false)
				reconciler.Reconcile(ctx)
				Expect(getBatchLocation(FleetName)).To(Equal(1))
				checkFleetAnnotations(false)
				updateDefinition(nil)
				mockCallbackManager.EXPECT().FleetRolloutSelectionUpdated(gomock.Any(), gomock.Any())
				reconciler.Reconcile(ctx)
				checkFleetAnnotations(true)
			})
		})
	})
})
