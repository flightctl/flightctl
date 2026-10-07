package periodic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store"
	checkpointstore "github.com/flightctl/flightctl/internal/store/checkpoint"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	"github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	"github.com/flightctl/flightctl/internal/tasks"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

const mappingScanCheckpointConsumer = "label-sync-mapping-scan"
const (
	mappingScanTestPageSize         = 100
	changedSystemInfoMappingName    = "system-info-architecture"
	customInfoMappingName           = "custom-info"
	mappingScanCustomInfoExpression = `(has(status.systemInfo.customInfo) && status.systemInfo.customInfo != null
  ? status.systemInfo.customInfo
  : {}).transformMapEntry(
  k,
  v,
  {"custominfo/" + k: v}
)`
)

var mappingScanSystemInfoMappings = []struct {
	name  string
	field string
}{
	{name: "system-info-agent-version", field: "agentVersion"},
	{name: changedSystemInfoMappingName, field: "architecture"},
	{name: "system-info-boot-id", field: "bootID"},
	{name: "system-info-operating-system", field: "operatingSystem"},
	{name: "system-info-hostname", field: "hostname"},
	{name: "system-info-kernel", field: "kernel"},
	{name: "system-info-distro-name", field: "distroName"},
	{name: "system-info-distro-version", field: "distroVersion"},
	{name: "system-info-distro-id", field: "distroId"},
	{name: "system-info-product-name", field: "productName"},
}

type mappingScanCheckpointSnapshot struct {
	Version  int               `json:"version"`
	Cursor   *string           `json:"cursor"`
	Mappings []json.RawMessage `json:"mappings"`
}

var _ = Describe("Label mapping scan integration", func() {
	var (
		ctx            context.Context
		cfg            *config.Config
		dbName         string
		db             *gorm.DB
		orgID          uuid.UUID
		deviceStore    *devicestore.DeviceStore
		eventStore     eventstore.Store
		deviceSvc      deviceservice.Service
		mappingService labelsyncmappingservice.Service
		checkpointSvc  checkpointservice.Service
		scanTask       *tasks.LabelMappingScanTask
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log := flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())

		orgID = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), orgID)).To(Succeed())

		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		mappingStore := labelsyncmapping.NewStore(db, log.WithField("pkg", "label-sync-mapping-store"))
		evaluator, err := labelsyncmappingservice.NewEvaluator()
		Expect(err).NotTo(HaveOccurred())
		eventStore = eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		eventsSvc := events.NewServiceHandler(eventStore, nil, log)
		mappingHandler, err := labelsyncmappingservice.NewServiceHandler(mappingStore, deviceStore, evaluator, eventsSvc, log)
		Expect(err).NotTo(HaveOccurred())
		mappingService = labelsyncmappingservice.WrapWithTracing(mappingHandler)
		deviceSvc = deviceservice.NewDeviceServiceHandler(deviceStore, nil, nil, eventsSvc, nil, "", log)
		checkpointSvc = checkpointservice.NewServiceHandler(checkpointstore.NewCheckpointStore(db, log))
		scanTask, err = tasks.NewLabelMappingScanTask(mappingService, deviceSvc, checkpointSvc, tasks.LabelMappingScanConfig{
			PageSize:   mappingScanTestPageSize,
			TimeBudget: time.Nanosecond,
		}, log)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, flightlog.InitLogs(), cfg, db, dbName)).To(Succeed())
	})

	createMapping := func(name string, key *string, expression string) *domain.LabelSyncMapping {
		mapping := domain.LabelSyncMapping{
			ApiVersion: domain.LabelSyncMappingAPIVersion,
			Kind:       domain.LabelSyncMappingKind,
			Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec: domain.LabelSyncMappingSpec{
				ResourceType: domain.LabelSyncMappingDevice,
				Key:          key,
				Expression:   expression,
			},
		}
		created, status := mappingService.CreateLabelSyncMapping(ctx, orgID, mapping)
		Expect(status.Code).To(Equal(domain.StatusCreated().Code), status.Message)
		return created
	}

	getReadyCondition := func(name string) domain.Condition {
		mapping, status := mappingService.GetLabelSyncMapping(ctx, orgID, name)
		Expect(status.Code).To(Equal(domain.StatusOK().Code))
		return *domain.FindStatusCondition(lo.FromPtr(mapping.Status.Conditions), domain.ConditionType("Ready"))
	}

	getCheckpoint := func() mappingScanCheckpointSnapshot {
		data, status := checkpointSvc.GetCheckpoint(ctx, mappingScanCheckpointConsumer, orgID.String())
		Expect(status.Code).To(Equal(domain.StatusOK().Code))
		var checkpoint mappingScanCheckpointSnapshot
		Expect(json.Unmarshal(data, &checkpoint)).To(Succeed())
		return checkpoint
	}

	checkpointRowCount := func() int64 {
		var count int64
		Expect(db.Model(&model.Checkpoint{}).
			Where("consumer = ? AND key = ?", mappingScanCheckpointConsumer, orgID.String()).
			Count(&count).Error).To(Succeed())
		return count
	}

	It("When a multi-page campaign resumes after a mapping token changes it should reuse one checkpoint row and complete each current mapping", func() {
		const deviceCount = 250
		for _, mapping := range mappingScanSystemInfoMappings {
			createMapping(mapping.name, lo.ToPtr("systeminfo/"+mapping.field), "status.systemInfo."+mapping.field)
		}
		createMapping(customInfoMappingName, nil, mappingScanCustomInfoExpression)
		changedMappingID := mappingID(ctx, db, orgID, changedSystemInfoMappingName)
		customInfoID := mappingID(ctx, db, orgID, customInfoMappingName)
		Expect(insertMappingScanDevices(ctx, db, orgID, deviceCount)).To(Succeed())

		scanTask.Poll(ctx, orgID)
		firstCheckpoint := getCheckpoint()
		Expect(firstCheckpoint.Version).To(Equal(1))
		Expect(firstCheckpoint.Cursor).NotTo(BeNil())
		Expect(firstCheckpoint.Mappings).To(HaveLen(len(mappingScanSystemInfoMappings) + 1))
		Expect(checkpointRowCount()).To(Equal(int64(1)))
		Expect(getReadyCondition(changedSystemInfoMappingName).Status).To(Equal(domain.ConditionStatusFalse))
		Expect(getReadyCondition(customInfoMappingName).Status).To(Equal(domain.ConditionStatusFalse))

		targets, err := mappingService.ListMappingScanTargets(ctx, orgID)
		Expect(err).NotTo(HaveOccurred())
		var changedMappingToken labelsyncmappingservice.MappingScanToken
		for _, target := range targets {
			if target.MappingID == changedMappingID {
				changedMappingToken = target
			}
		}
		updatedToken, saved, err := mappingService.RecordMappingScanFailure(ctx, orgID, changedMappingToken, "temporary evaluation failure")
		Expect(err).NotTo(HaveOccurred())
		Expect(saved).To(BeTrue())
		Expect(updatedToken.FailureRevision).To(Equal(changedMappingToken.FailureRevision + 1))

		scanTask.Poll(ctx, orgID)
		resumedCheckpoint := getCheckpoint()
		// Stale targets retain the full cursor and do not pin later devices.
		Expect(resumedCheckpoint.Cursor).NotTo(Equal(firstCheckpoint.Cursor))
		Expect(resumedCheckpoint.Mappings).To(HaveLen(len(mappingScanSystemInfoMappings) + 1))
		Expect(checkpointRowCount()).To(Equal(int64(1)))

		for attempts := 0; getReadyCondition(customInfoMappingName).Status != domain.ConditionStatusTrue && attempts < deviceCount/mappingScanTestPageSize+4; attempts++ {
			scanTask.Poll(ctx, orgID)
		}
		Expect(getReadyCondition(customInfoMappingName).Reason).To(Equal("Success"))
		Expect(getReadyCondition(changedSystemInfoMappingName).Status).To(Equal(domain.ConditionStatusFalse))

		for attempts := 0; getReadyCondition(changedSystemInfoMappingName).Status != domain.ConditionStatusTrue && attempts < deviceCount/mappingScanTestPageSize+4; attempts++ {
			scanTask.Poll(ctx, orgID)
		}
		Expect(getReadyCondition(changedSystemInfoMappingName).Reason).To(Equal("Success"))
		for _, mapping := range mappingScanSystemInfoMappings {
			Expect(getReadyCondition(mapping.name).Reason).To(Equal("Success"))
		}
		Expect(getReadyCondition(customInfoMappingName).Reason).To(Equal("Success"))
		Expect(checkpointRowCount()).To(Equal(int64(1)))
		Expect(getCheckpoint().Mappings).To(BeEmpty())

		var ownedLabels int64
		mappingIDs := []uuid.UUID{customInfoID}
		for _, mapping := range mappingScanSystemInfoMappings {
			mappingIDs = append(mappingIDs, mappingID(ctx, db, orgID, mapping.name))
		}
		Expect(db.Model(&model.DeviceLabel{}).
			Where("org_id = ? AND label_sync_mapping_id IN ?", orgID, mappingIDs).
			Count(&ownedLabels).Error).To(Succeed())
		Expect(ownedLabels).To(Equal(int64(deviceCount * (len(mappingScanSystemInfoMappings) + 4))))

		var customInfoLabels []struct {
			DeviceName string
			Count      int64
		}
		Expect(db.Model(&model.DeviceLabel{}).
			Select("device_name, COUNT(*) AS count").
			Where("org_id = ? AND label_sync_mapping_id = ?", orgID, customInfoID).
			Group("device_name").
			Find(&customInfoLabels).Error).To(Succeed())
		Expect(customInfoLabels).To(HaveLen(deviceCount))
		for _, labels := range customInfoLabels {
			Expect(labels.Count).To(Equal(int64(4)))
		}

		scanTask.Poll(ctx, orgID)
		Expect(checkpointRowCount()).To(Equal(int64(1)))
	})

	It("When a mapping is deleted it should remove owned labels before finalizing", func() {
		const mappingName = "terminating-scan-mapping"
		const deviceName = "terminating-scan-device"
		createMapping(mappingName, lo.ToPtr("systeminfo/architecture"), "status.systemInfo.architecture")
		mappingID := mappingID(ctx, db, orgID, mappingName)

		deviceStatus := domain.NewDeviceStatus()
		deviceStatus.SystemInfo.Architecture = "x86_64"
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(deviceName)},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "os"}},
			Status:   &deviceStatus,
		}
		_, err := deviceStore.Create(ctx, orgID, &device, nil)
		Expect(err).NotTo(HaveOccurred())

		scanTask.Poll(ctx, orgID)
		created, err := deviceStore.Get(ctx, orgID, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(created.Metadata.Labels)).To(HaveKeyWithValue("systeminfo/architecture", "x86_64"))
		Expect(getReadyCondition(mappingName).Reason).To(Equal("Success"))

		var ownedLabels int64
		Expect(db.Model(&model.DeviceLabel{}).
			Where("org_id = ? AND label_sync_mapping_id = ?", orgID, mappingID).
			Count(&ownedLabels).Error).To(Succeed())
		Expect(ownedLabels).To(Equal(int64(1)))

		deleteStatus := mappingService.DeleteLabelSyncMapping(ctx, orgID, mappingName)
		Expect(deleteStatus.Code).To(Equal(domain.StatusOK().Code))
		terminating, status := mappingService.GetLabelSyncMapping(ctx, orgID, mappingName)
		Expect(status.Code).To(Equal(domain.StatusOK().Code))
		Expect(terminating.Metadata.DeletionTimestamp).NotTo(BeNil())

		scanTask.Poll(ctx, orgID)

		_, status = mappingService.GetLabelSyncMapping(ctx, orgID, mappingName)
		Expect(status.Code).To(BeEquivalentTo(http.StatusNotFound))
		cleaned, err := deviceStore.Get(ctx, orgID, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(cleaned.Metadata.Labels)).NotTo(HaveKey("systeminfo/architecture"))
		Expect(db.Model(&model.DeviceLabel{}).
			Where("org_id = ? AND label_sync_mapping_id = ?", orgID, mappingID).
			Count(&ownedLabels).Error).To(Succeed())
		Expect(ownedLabels).To(BeZero())
	})

	It("When the first device fails it should finish the pass and recover mapping status after event reconciliation", func() {
		createMapping("architecture", lo.ToPtr("architecture"), `status.systemInfo.architecture == "broken" ? string(int(status.systemInfo.architecture)) : status.systemInfo.architecture`)
		createMapping("clean", lo.ToPtr("boot"), "status.systemInfo.bootID")
		for _, name := range []string{"a", "b", "c", "d"} {
			status := domain.NewDeviceStatus()
			status.SystemInfo.Architecture, status.SystemInfo.BootID = "x86_64", "boot"
			if name == "a" {
				status.SystemInfo.Architecture = "broken"
			}
			_, err := deviceStore.Create(ctx, orgID, &domain.Device{Metadata: domain.ObjectMeta{Name: &name}, Spec: &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "os"}}, Status: &status}, nil)
			Expect(err).NotTo(HaveOccurred())
		}
		var err error
		scanTask, err = tasks.NewLabelMappingScanTask(mappingService, deviceSvc, checkpointSvc, tasks.LabelMappingScanConfig{PageSize: 1, TimeBudget: time.Nanosecond}, flightlog.InitLogs())
		Expect(err).NotTo(HaveOccurred())
		for attempts := 0; getReadyCondition("clean").Status != domain.ConditionStatusTrue && attempts < 10; attempts++ {
			scanTask.Poll(ctx, orgID)
		}
		Expect(getReadyCondition("clean").Reason).To(Equal("Success"))
		Expect(getReadyCondition("architecture").Reason).To(Equal("Degraded"))
		failed, err := deviceStore.Get(ctx, orgID, "a")
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.FindStatusCondition(failed.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced).Status).To(Equal(domain.ConditionStatusFalse))
		Expect(lo.FromPtr(failed.Metadata.Labels)).To(HaveKeyWithValue("boot", "boot"))
		healthy, err := deviceStore.Get(ctx, orgID, "d")
		Expect(err).NotTo(HaveOccurred())
		Expect(*healthy.Metadata.Labels).To(HaveKeyWithValue("architecture", "x86_64"))
		Expect(domain.FindStatusCondition(healthy.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced)).To(BeNil())
		healthyVersion := *healthy.Metadata.ResourceVersion
		// A later periodic poll checks failure conditions without reconciling
		// either failed or healthy devices again.
		scanTask.Poll(ctx, orgID)
		stillFailed, err := deviceStore.Get(ctx, orgID, "a")
		Expect(err).NotTo(HaveOccurred())
		Expect(stillFailed.Metadata.ResourceVersion).To(Equal(failed.Metadata.ResourceVersion))
		var architecture interface{} = "x86_64"
		_, status := deviceSvc.PatchDeviceStatus(ctx, orgID, "a", domain.PatchRequest{{
			Op: "replace", Path: "/status/systemInfo/architecture", Value: &architecture,
		}})
		Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
		events, err := eventStore.List(ctx, orgID, store.ListParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var statusEvent *domain.Event
		for i := range events.Items {
			event := &events.Items[i]
			if event.InvolvedObject.Kind == domain.DeviceKind && event.InvolvedObject.Name == "a" &&
				event.Reason == domain.EventReasonResourceUpdated && event.Details == nil {
				statusEvent = event
				break
			}
		}
		Expect(statusEvent).NotTo(BeNil())
		logic, err := tasks.NewDeviceLabelReconciliationLogic(flightlog.InitLogs(), mappingService, orgID, *statusEvent)
		Expect(err).NotTo(HaveOccurred())
		Expect(logic.Reconcile(ctx)).To(Succeed())
		reconciled, err := deviceStore.Get(ctx, orgID, "a")
		Expect(err).NotTo(HaveOccurred())
		reconciledCondition := domain.FindStatusCondition(reconciled.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced)
		Expect(reconciledCondition.Status).To(Equal(domain.ConditionStatusTrue))
		for attempts := 0; getReadyCondition("architecture").Status != domain.ConditionStatusTrue && attempts < 4; attempts++ {
			scanTask.Poll(ctx, orgID)
		}
		Expect(getReadyCondition("architecture").Reason).To(Equal("Success"))
		recovered, err := deviceStore.Get(ctx, orgID, "a")
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.FindStatusCondition(recovered.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced).Status).To(Equal(domain.ConditionStatusTrue))
		healthy, err = deviceStore.Get(ctx, orgID, "d")
		Expect(err).NotTo(HaveOccurred())
		Expect(*healthy.Metadata.ResourceVersion).To(Equal(healthyVersion))
		Expect(checkpointRowCount()).To(Equal(int64(1)))
	})

	It("When two map mappings each emit 50 labels it should persist the 100-label device limit", func() {
		createMapping("first-map", nil, mapExpression("a", "x"))
		createMapping("second-map", nil, mapExpression("b", "y"))
		deviceName := "map-output-boundary"
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(deviceName)},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "os"}},
		}
		_, err := deviceStore.Create(ctx, orgID, &device, nil)
		Expect(err).NotTo(HaveOccurred())

		scanTask.Poll(ctx, orgID)

		created, err := deviceStore.Get(ctx, orgID, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(created.Metadata.Labels)).To(HaveLen(100))
		var ownedLabels int64
		Expect(db.Model(&model.DeviceLabel{}).
			Where("org_id = ? AND device_name = ? AND label_sync_mapping_id IN ?", orgID, deviceName, []uuid.UUID{mappingID(ctx, db, orgID, "first-map"), mappingID(ctx, db, orgID, "second-map")}).
			Count(&ownedLabels).Error).To(Succeed())
		Expect(ownedLabels).To(Equal(int64(100)))
		Expect(getReadyCondition("first-map").Reason).To(Equal("Success"))
		Expect(getReadyCondition("second-map").Reason).To(Equal("Success"))
	})
})

func insertMappingScanDevices(ctx context.Context, db *gorm.DB, orgID uuid.UUID, count int) error {
	devices := make([]model.Device, count)
	for index := range count {
		name := fmt.Sprintf("scan-device-%05d", index)
		customInfo := domain.CustomDeviceInfo{
			"site":        "site-a",
			"environment": "integration",
			"rack":        "rack-1",
			"role":        "worker",
		}
		status := domain.NewDeviceStatus()
		status.SystemInfo = domain.DeviceSystemInfo{
			AgentVersion:    "1.0.0",
			Architecture:    "x86_64",
			BootID:          "boot-" + name,
			OperatingSystem: "linux",
			CustomInfo:      &customInfo,
			AdditionalProperties: map[string]string{
				"hostname":      name,
				"kernel":        "6.12.0",
				"distroName":    "Fedora",
				"distroVersion": "42",
				"distroId":      "fedora",
				"productName":   "Flight Control Test Device",
			},
		}
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "os"}},
			Status:   &status,
		}
		stored, err := model.NewDeviceFromApiResource(&device)
		if err != nil {
			return err
		}
		stored.OrgID = orgID
		stored.Generation = lo.ToPtr(int64(1))
		stored.ResourceVersion = lo.ToPtr(int64(1))
		devices[index] = *stored
	}
	return db.WithContext(ctx).Omit("DeviceTimestamp").CreateInBatches(&devices, 500).Error
}

func mappingID(ctx context.Context, db *gorm.DB, orgID uuid.UUID, name string) uuid.UUID {
	var mapping model.LabelSyncMapping
	Expect(db.WithContext(ctx).Select("id").Where("org_id = ? AND name = ?", orgID, name).Take(&mapping).Error).To(Succeed())
	return mapping.ID
}

func mapExpression(prefix, valuePrefix string) string {
	entries := make([]string, 50)
	for index := range 50 {
		key := fmt.Sprintf("%s%02d", prefix, index)
		value := valuePrefix
		entries[index] = fmt.Sprintf("%q: %q", key, value)
	}
	return "{" + strings.Join(entries, ", ") + "}"
}
