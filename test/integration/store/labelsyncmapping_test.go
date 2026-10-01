package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("LabelSyncMapping reconciliation", func() {
	var (
		ctx          context.Context
		log          *logrus.Logger
		cfg          *config.Config
		dbName       string
		db           *gorm.DB
		orgID        uuid.UUID
		otherOrgID   uuid.UUID
		deviceStore  *devicestore.DeviceStore
		mappingStore labelsyncmappingstore.Store
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		mappingStore = labelsyncmappingstore.NewStore(db, log.WithField("pkg", "labelsyncmapping-store"))
		organizationStore := organizationstore.NewOrganizationStore(db)
		orgID = uuid.New()
		otherOrgID = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationStore, orgID)).To(Succeed())
		Expect(testutil.CreateTestOrganization(ctx, organizationStore, otherOrgID)).To(Succeed())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	It("When label reconciliation observations change it should preserve conditions and fence stale success and failure", func() {
		device := newLabelSyncDevice("condition-device", nil)
		device.Status = lo.ToPtr(domain.NewDeviceStatus())
		domain.SetStatusCondition(&device.Status.Conditions, domain.Condition{Type: domain.ConditionTypeDeviceSpecValid, Status: domain.ConditionStatusTrue, Reason: "Valid"})
		created, err := deviceStore.Create(ctx, orgID, device, nil)
		Expect(err).NotTo(HaveOccurred())
		observe := func(resource *domain.Device, status domain.ConditionStatus) (domain.DeviceLabelApplyResult, error) {
			condition := domain.Condition{Type: domain.ConditionTypeDeviceLabelsSynced, Status: status, Reason: "Reconciled", Message: "Device labels are reconciled"}
			if status == domain.ConditionStatusFalse {
				condition.Reason, condition.Message = "ReconciliationFailed", "invalid output"
			}
			snapshot, err := deviceStore.GetLabelSnapshot(ctx, orgID, "condition-device")
			Expect(err).NotTo(HaveOccurred())
			snapshot.Device = *resource
			return deviceStore.ApplyLabels(ctx, orgID, "condition-device", snapshot, map[string]domain.DesiredDeviceLabel{}, &condition)
		}
		absent, err := observe(created, domain.ConditionStatusTrue)
		Expect(err).NotTo(HaveOccurred())
		Expect(absent.Device.Metadata.ResourceVersion).To(Equal(created.Metadata.ResourceVersion))
		Expect(domain.FindStatusCondition(absent.Device.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced)).To(BeNil())
		failed, err := observe(created, domain.ConditionStatusFalse)
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.FindStatusCondition(failed.Device.Status.Conditions, domain.ConditionTypeDeviceSpecValid)).NotTo(BeNil())
		repeated, err := observe(failed.Device, domain.ConditionStatusFalse)
		Expect(err).NotTo(HaveOccurred())
		Expect(repeated.Device.Metadata.ResourceVersion).To(Equal(failed.Device.Metadata.ResourceVersion))
		_, err = observe(created, domain.ConditionStatusTrue)
		Expect(err).To(MatchError(flterrors.ErrResourceVersionConflict))
		recovered, err := observe(failed.Device, domain.ConditionStatusTrue)
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.FindStatusCondition(recovered.Device.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced).Status).To(Equal(domain.ConditionStatusTrue))
		_, err = observe(failed.Device, domain.ConditionStatusFalse)
		Expect(err).To(MatchError(flterrors.ErrResourceVersionConflict))
		unchanged, err := observe(recovered.Device, domain.ConditionStatusTrue)
		Expect(err).NotTo(HaveOccurred())
		Expect(unchanged.Device.Metadata.ResourceVersion).To(Equal(recovered.Device.Metadata.ResourceVersion))
		replacement := *unchanged.Device
		replacement.Status = lo.ToPtr(domain.NewDeviceStatus())
		_, _, err = deviceStore.UpdateStatus(ctx, orgID, &replacement, unchanged.Device)
		Expect(err).NotTo(HaveOccurred())
		updated, err := deviceStore.Get(ctx, orgID, "condition-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.FindStatusCondition(updated.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced).Status).To(Equal(domain.ConditionStatusTrue))
		Expect(domain.FindStatusCondition(updated.Status.Conditions, domain.ConditionTypeDeviceSpecValid)).NotTo(BeNil())
	})

	It("When False devices are queried it should use an index-compatible predicate and paginate within the organization", func() {
		for _, name := range []string{"a", "b", "healthy", "absent"} {
			device := newLabelSyncDevice(name, nil)
			if name != "absent" {
				device.Status = lo.ToPtr(domain.NewDeviceStatus())
				status := domain.ConditionStatusFalse
				if name == "healthy" {
					status = domain.ConditionStatusTrue
				}
				domain.SetStatusCondition(&device.Status.Conditions, domain.Condition{Type: domain.ConditionTypeDeviceLabelsSynced, Status: status, Reason: "Test"})
			}
			_, err := deviceStore.Create(ctx, orgID, device, nil)
			Expect(err).NotTo(HaveOccurred())
		}
		other := newLabelSyncDevice("other", nil)
		other.Status = lo.ToPtr(domain.NewDeviceStatus())
		domain.SetStatusCondition(&other.Status.Conditions, domain.Condition{Type: domain.ConditionTypeDeviceLabelsSynced, Status: domain.ConditionStatusFalse, Reason: "Test"})
		_, err := deviceStore.Create(ctx, otherOrgID, other, nil)
		Expect(err).NotTo(HaveOccurred())
		first, err := deviceStore.ListDevicesByServiceCondition(ctx, orgID, "LabelsSynced", "False", store.ListParams{Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Items).To(HaveLen(1))
		Expect(*first.Items[0].Metadata.Name).To(Equal("a"))
		cursor, err := store.ParseContinueString(first.Metadata.Continue)
		Expect(err).NotTo(HaveOccurred())
		second, err := deviceStore.ListDevicesByServiceCondition(ctx, orgID, "LabelsSynced", "False", store.ListParams{Limit: 1, Continue: cursor})
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Items).To(HaveLen(1))
		Expect(*second.Items[0].Metadata.Name).To(Equal("b"))
		Expect(second.Metadata.Continue).To(BeNil())
		// A small fixture can favor a sequential scan; disabling it proves
		// that the condition predicate can use the existing expression index.
		var plan []string
		Expect(db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
				return err
			}
			return tx.Raw(`EXPLAIN SELECT name FROM devices WHERE service_conditions IS NOT NULL AND (service_conditions->'conditions') @> jsonb_build_array(jsonb_build_object('type', ?::text, 'status', ?::text))`, "LabelsSynced", "False").Scan(&plan).Error
		})).To(Succeed())
		Expect(strings.Join(plan, "\n")).To(ContainSubstring("idx_devices_service_conditions"))
		GinkgoWriter.Printf("condition query plan:\n%s\n", strings.Join(plan, "\n"))
	})

	It("When label ownership fails it should roll back labels and the reconciliation condition together", func() {
		created, err := deviceStore.Create(ctx, orgID, newLabelSyncDevice("atomic-device", nil), nil)
		Expect(err).NotTo(HaveOccurred())
		snapshot, err := deviceStore.GetLabelSnapshot(ctx, orgID, "atomic-device")
		Expect(err).NotTo(HaveOccurred())
		missingOwner := uuid.New()
		desired := map[string]domain.DesiredDeviceLabel{"architecture": {Value: "x86_64", MappingID: &missingOwner}}
		condition := domain.Condition{Type: domain.ConditionTypeDeviceLabelsSynced, Status: domain.ConditionStatusFalse, Reason: "ReconciliationFailed", Message: "failed mapping"}
		_, err = deviceStore.ApplyLabels(ctx, orgID, "atomic-device", snapshot, desired, &condition)
		Expect(err).To(HaveOccurred())
		unchanged, err := deviceStore.Get(ctx, orgID, "atomic-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(unchanged.Metadata.ResourceVersion).To(Equal(created.Metadata.ResourceVersion))
		Expect(lo.FromPtr(unchanged.Metadata.Labels)).To(BeEmpty())
		Expect(domain.FindStatusCondition(unchanged.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced)).To(BeNil())

		desired["architecture"] = domain.DesiredDeviceLabel{Value: "x86_64"}
		applied, err := deviceStore.ApplyLabels(ctx, orgID, "atomic-device", snapshot, desired, &condition)
		Expect(err).NotTo(HaveOccurred())
		Expect(applied.Device.Metadata.ResourceVersion).To(Equal(lo.ToPtr("2")))
		Expect(lo.FromPtr(applied.Device.Metadata.Labels)).To(HaveKeyWithValue("architecture", "x86_64"))
		Expect(domain.FindStatusCondition(applied.Device.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced).Status).To(Equal(domain.ConditionStatusFalse))
		persisted, err := deviceStore.Get(ctx, orgID, "atomic-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(persisted.Metadata.ResourceVersion).To(Equal(applied.Device.Metadata.ResourceVersion))
		Expect(domain.FindStatusCondition(persisted.Status.Conditions, domain.ConditionTypeDeviceLabelsSynced).Status).To(Equal(domain.ConditionStatusFalse))
	})

	It("When mapping state is loaded it should include current and terminating mappings with one revision", func() {
		_, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, orgID, newLabelSyncMapping("site", "site"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, otherOrgID, newLabelSyncMapping("other-org", "other-org"))
		Expect(err).NotTo(HaveOccurred())
		deleted, err := mappingStore.Delete(ctx, orgID, "site")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())

		snapshot, err := mappingStore.GetDeviceMappingsSnapshot(ctx, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.Revision).To(Equal(int64(3)))
		Expect(snapshot.Mappings).To(HaveLen(2))
		byName := make(map[string]labelsyncmappingstore.DeviceMapping, len(snapshot.Mappings))
		for _, mapping := range snapshot.Mappings {
			byName[lo.FromPtr(mapping.Mapping.Metadata.Name)] = mapping
		}
		Expect(byName["architecture"].ID).NotTo(Equal(uuid.Nil))
		Expect(byName["site"].ID).NotTo(Equal(uuid.Nil))
		Expect(byName["site"].Mapping.Metadata.DeletionTimestamp).NotTo(BeNil())
		Expect(byName["architecture"].Mapping.Metadata.DeletionTimestamp).To(BeNil())
	})

	It("When a mapping scan token changes it should fence only that mapping completion", func() {
		names := []string{"generation-stale", "failure-stale", "deletion-stale", "current"}
		namesByID := make(map[uuid.UUID]string, len(names))
		for _, name := range names {
			_, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping(name, name))
			Expect(err).NotTo(HaveOccurred())
			namesByID[mappingID(ctx, db, orgID, name)] = name
			status := domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{{
				Type:               domain.ConditionTypeLabelSyncMappingReady,
				Status:             domain.ConditionStatusFalse,
				Reason:             "Pending",
				ObservedGeneration: lo.ToPtr(int64(1)),
			}}}
			Expect(db.Model(&model.LabelSyncMapping{}).Where("org_id = ? AND name = ?", orgID, name).
				Update("status", model.MakeJSONField(status)).Error).To(Succeed())
		}

		targets, err := mappingStore.ListMappingScanTargets(ctx, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(targets).To(HaveLen(len(names)))
		tokens := make(map[string]labelsyncmappingstore.MappingScanRecord, len(targets))
		for _, token := range targets {
			tokens[namesByID[token.MappingID]] = token
		}

		generationMapping, err := mappingStore.Get(ctx, orgID, "generation-stale")
		Expect(err).NotTo(HaveOccurred())
		generationMapping.Spec.Expression += " + '-updated'"
		_, _, err = mappingStore.Update(ctx, orgID, generationMapping)
		Expect(err).NotTo(HaveOccurred())

		failureToken, updated, err := mappingStore.RecordMappingScanFailure(ctx, orgID, tokens["failure-stale"], "mapping evaluation failed")
		Expect(err).NotTo(HaveOccurred())
		Expect(updated).To(BeTrue())
		Expect(failureToken.FailureRevision).To(Equal(tokens["failure-stale"].FailureRevision + 1))

		deleted, err := mappingStore.Delete(ctx, orgID, "deletion-stale")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())

		completed, err := mappingStore.CompleteMappingScan(ctx, orgID, targets)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(HaveKeyWithValue(tokens["generation-stale"].MappingID, false))
		Expect(completed).To(HaveKeyWithValue(tokens["failure-stale"].MappingID, false))
		Expect(completed).To(HaveKeyWithValue(tokens["deletion-stale"].MappingID, false))
		Expect(completed).To(HaveKeyWithValue(tokens["current"].MappingID, true))

		current, err := mappingStore.Get(ctx, orgID, "current")
		Expect(err).NotTo(HaveOccurred())
		ready := domain.FindStatusCondition(lo.FromPtr(current.Status.Conditions), domain.ConditionTypeLabelSyncMappingReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("Success"))
	})

	It("When labels are applied it should atomically write takeover ownership and sorted managed metadata", func() {
		_, err := mappingStore.Create(ctx, orgID, mapLabelSyncMapping("first"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, orgID, mapLabelSyncMapping("second"))
		Expect(err).NotTo(HaveOccurred())
		firstID := mappingID(ctx, db, orgID, "first")
		secondID := mappingID(ctx, db, orgID, "second")
		Expect(firstID).NotTo(Equal(secondID))

		labels := map[string]string{"manual": "preserved", "promoted": "user-value", "same": "unchanged"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "managed-device", nil, nil, &labels)
		snapshot, err := deviceStore.GetLabelSnapshot(ctx, orgID, "managed-device")
		Expect(err).NotTo(HaveOccurred())
		desired := map[string]domain.DesiredDeviceLabel{
			"manual":   {Value: "preserved"},
			"promoted": {Value: "mapped-value", MappingID: &firstID},
			"same":     {Value: "unchanged", MappingID: &firstID},
		}

		applied, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, desired, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(applied.LabelsChanged).To(BeTrue())
		Expect(applied.ManagedLabelsChanged).To(BeTrue())
		Expect(applied.OwnershipChanged).To(BeTrue())
		Expect(lo.FromPtr(applied.Device.Metadata.Annotations)[domain.DeviceAnnotationManagedLabels]).To(Equal(`["promoted","same"]`))
		Expect(lo.FromPtr(applied.Device.Metadata.ResourceVersion)).To(Equal("2"))

		device, err := deviceStore.Get(ctx, orgID, "managed-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{"manual": "preserved", "promoted": "mapped-value", "same": "unchanged"}))
		var promoted model.DeviceLabel
		Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "managed-device", "promoted").Take(&promoted).Error).To(Succeed())
		Expect(promoted.LabelSyncMappingID).To(Equal(&firstID))

		snapshot, err = deviceStore.GetLabelSnapshot(ctx, orgID, "managed-device")
		Expect(err).NotTo(HaveOccurred())
		for key, label := range desired {
			if label.MappingID != nil {
				label.MappingID = &secondID
				desired[key] = label
			}
		}
		transferred, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, desired, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(transferred.LabelsChanged).To(BeFalse())
		Expect(transferred.ManagedLabelsChanged).To(BeFalse())
		Expect(transferred.OwnershipChanged).To(BeTrue())
		Expect(lo.FromPtr(transferred.Device.Metadata.ResourceVersion)).To(Equal("3"))
		Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "managed-device", "promoted").Take(&promoted).Error).To(Succeed())
		Expect(promoted.LabelSyncMappingID).To(Equal(&secondID))

		snapshot, err = deviceStore.GetLabelSnapshot(ctx, orgID, "managed-device")
		Expect(err).NotTo(HaveOccurred())
		unchanged, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, desired, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(unchanged.LabelsChanged).To(BeFalse())
		Expect(unchanged.ManagedLabelsChanged).To(BeFalse())
		Expect(unchanged.OwnershipChanged).To(BeFalse())

		_, _, _, err = deviceStore.Mutate(ctx, orgID, "managed-device", nil, func(mutation *devicestore.DeviceMutation) error {
			annotations := lo.FromPtr(mutation.Device.Metadata.Annotations)
			annotations[domain.DeviceAnnotationManagedLabels] = `[]`
			mutation.Device.Metadata.Annotations = &annotations
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		device, err = deviceStore.Get(ctx, orgID, "managed-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationManagedLabels]).To(Equal(`[]`))

		snapshot, err = deviceStore.GetLabelSnapshot(ctx, orgID, "managed-device")
		Expect(err).NotTo(HaveOccurred())
		repaired, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, desired, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(repaired.LabelsChanged).To(BeFalse())
		Expect(repaired.ManagedLabelsChanged).To(BeTrue())
		Expect(repaired.OwnershipChanged).To(BeFalse())
		Expect(lo.FromPtr(repaired.Device.Metadata.Annotations)[domain.DeviceAnnotationManagedLabels]).To(Equal(`["promoted","same"]`))

		snapshot, err = deviceStore.GetLabelSnapshot(ctx, orgID, "managed-device")
		Expect(err).NotTo(HaveOccurred())
		cleaned, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, map[string]domain.DesiredDeviceLabel{
			"manual": {Value: "preserved"},
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(cleaned.LabelsChanged).To(BeTrue())
		Expect(cleaned.ManagedLabelsChanged).To(BeTrue())
		Expect(lo.FromPtr(cleaned.Device.Metadata.Labels)).To(Equal(map[string]string{"manual": "preserved"}))
		Expect(lo.FromPtr(cleaned.Device.Metadata.Annotations)).NotTo(HaveKey(domain.DeviceAnnotationManagedLabels))
		var remaining int64
		Expect(db.Model(&model.DeviceLabel{}).Where("org_id = ? AND device_name = ?", orgID, "managed-device").Count(&remaining).Error).To(Succeed())
		Expect(remaining).To(Equal(int64(1)))
	})

	It("When an ownership transfer wins against stale label removal it should preserve the new owner", func() {
		_, err := mappingStore.Create(ctx, orgID, mapLabelSyncMapping("owner-a"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, orgID, mapLabelSyncMapping("owner-b"))
		Expect(err).NotTo(HaveOccurred())
		ownerA := mappingID(ctx, db, orgID, "owner-a")
		ownerB := mappingID(ctx, db, orgID, "owner-b")

		labels := map[string]string{"managed": "value"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "owner-transfer-race", nil, nil, &labels)
		snapshot, err := deviceStore.GetLabelSnapshot(ctx, orgID, "owner-transfer-race")
		Expect(err).NotTo(HaveOccurred())
		_, err = deviceStore.ApplyLabels(ctx, orgID, "owner-transfer-race", snapshot, map[string]domain.DesiredDeviceLabel{
			"managed": {Value: "value", MappingID: &ownerA},
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		snapshot, err = deviceStore.GetLabelSnapshot(ctx, orgID, "owner-transfer-race")
		Expect(err).NotTo(HaveOccurred())

		const callbackName = "test:pause-before-device-labels-cas"
		deviceUpdateStarted := make(chan struct{})
		continueDeviceUpdate := make(chan struct{})
		var pauseStarted atomic.Bool
		var releaseOnce sync.Once
		releaseDeviceUpdate := func() { releaseOnce.Do(func() { close(continueDeviceUpdate) }) }
		Expect(db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == "devices" && pauseStarted.CompareAndSwap(false, true) {
				close(deviceUpdateStarted)
				<-continueDeviceUpdate
			}
		})).To(Succeed())
		DeferCleanup(func() {
			releaseDeviceUpdate()
			Expect(db.Callback().Update().Remove(callbackName)).To(Succeed())
		})

		testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		removeResult := make(chan error, 1)
		go func() {
			_, err := deviceStore.ApplyLabels(testCtx, orgID, "owner-transfer-race", snapshot, map[string]domain.DesiredDeviceLabel{}, nil)
			removeResult <- err
		}()
		select {
		case <-deviceUpdateStarted:
		case <-testCtx.Done():
			Fail(fmt.Sprintf("label removal did not reach its device resource-version check: %v", testCtx.Err()))
		}

		transferSnapshot, err := deviceStore.GetLabelSnapshot(testCtx, orgID, "owner-transfer-race")
		Expect(err).NotTo(HaveOccurred())
		transferred, err := deviceStore.ApplyLabels(testCtx, orgID, "owner-transfer-race", transferSnapshot, map[string]domain.DesiredDeviceLabel{
			"managed": {Value: "value", MappingID: &ownerB},
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(transferred.OwnershipChanged).To(BeTrue())
		Expect(lo.FromPtr(transferred.Device.Metadata.ResourceVersion)).To(Equal("3"))

		releaseDeviceUpdate()
		select {
		case err := <-removeResult:
			Expect(errors.Is(err, flterrors.ErrResourceVersionConflict)).To(BeTrue())
		case <-testCtx.Done():
			Fail(fmt.Sprintf("stale label removal did not fail after ownership transfer: %v", testCtx.Err()))
		}

		device, err := deviceStore.Get(ctx, orgID, "owner-transfer-race")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(labels))
		var persisted model.DeviceLabel
		Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "owner-transfer-race", "managed").Take(&persisted).Error).To(Succeed())
		Expect(persisted.LabelSyncMappingID).To(Equal(&ownerB))
	})

	It("When a mapping is finalized before ownership assignment it should retry with the latest mappings", func() {
		mapping := newLabelSyncMapping("finalize-race", "managed")
		mapping.Spec.Expression = "finalize-output"
		_, err := mappingStore.Create(ctx, orgID, mapping)
		Expect(err).NotTo(HaveOccurred())
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "finalize-race", nil, nil, nil)

		const callbackName = "test:pause-before-device-label-owner-write"
		ownerWriteStarted := make(chan struct{})
		continueOwnerWrite := make(chan struct{})
		var pauseOnce sync.Once
		var releaseOnce sync.Once
		releaseOwnerWrite := func() { releaseOnce.Do(func() { close(continueOwnerWrite) }) }
		Expect(db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == "device_labels" {
				pauseOnce.Do(func() {
					close(ownerWriteStarted)
					<-continueOwnerWrite
				})
			}
		})).To(Succeed())
		DeferCleanup(func() {
			releaseOwnerWrite()
			Expect(db.Callback().Update().Remove(callbackName)).To(Succeed())
		})

		evaluator := newScriptedEvaluator(map[string]labelsyncmappingservice.Result{
			"finalize-output": labelsyncmappingservice.ScalarResult("mapped"),
		}, "", 0)
		service, events := newReconciliationService(deviceStore, mappingStore, evaluator, log)
		testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		reconcileResult := make(chan error, 1)
		go func() {
			_, err := service.ReconcileDeviceLabels(testCtx, orgID, "finalize-race")
			reconcileResult <- err
		}()
		select {
		case <-ownerWriteStarted:
		case <-testCtx.Done():
			Fail(fmt.Sprintf("reconciliation did not reach ownership assignment: %v", testCtx.Err()))
		}

		deleted, err := mappingStore.Delete(testCtx, orgID, "finalize-race")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		finalized, err := mappingStore.FinalizeDelete(testCtx, orgID, "finalize-race")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())

		releaseOwnerWrite()
		select {
		case err := <-reconcileResult:
			Expect(err).NotTo(HaveOccurred())
		case <-testCtx.Done():
			Fail(fmt.Sprintf("reconciliation did not retry after mapping finalization: %v", testCtx.Err()))
		}

		device, err := deviceStore.Get(ctx, orgID, "finalize-race")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(BeEmpty())
		var remaining int64
		Expect(db.Model(&model.DeviceLabel{}).Where("org_id = ? AND device_name = ?", orgID, "finalize-race").Count(&remaining).Error).To(Succeed())
		Expect(remaining).To(BeZero())
		Expect(evaluator.callsSnapshot()).To(Equal([]string{"finalize-output"}))
		Expect(events.snapshot()).To(BeEmpty())
	})

	It("When two reconciliations overlap on one device they should converge with one visible write", func() {
		mapping := mapLabelSyncMapping("multi-output")
		mapping.Spec.Expression = "multi-output"
		_, err := mappingStore.Create(ctx, orgID, mapping)
		Expect(err).NotTo(HaveOccurred())
		labels := map[string]string{"manual": "preserved"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "overlap-device", nil, nil, &labels)

		evaluator := newScriptedEvaluator(map[string]labelsyncmappingservice.Result{
			"multi-output": labelsyncmappingservice.MapResult{"architecture": "x86_64", "site": "west"},
		}, "multi-output", 2)
		service, events := newReconciliationService(deviceStore, mappingStore, evaluator, log)
		testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := service.ReconcileDeviceLabels(testCtx, orgID, "overlap-device")
				results <- err
			}()
		}
		for range 2 {
			select {
			case <-evaluator.entered:
			case <-testCtx.Done():
				Fail(fmt.Sprintf("both reconcilers did not reach evaluation: %v", testCtx.Err()))
			}
		}
		close(evaluator.release)
		for range 2 {
			select {
			case err := <-results:
				Expect(err).NotTo(HaveOccurred())
			case <-testCtx.Done():
				Fail(fmt.Sprintf("reconcilers did not converge: %v", testCtx.Err()))
			}
		}

		device, err := deviceStore.Get(ctx, orgID, "overlap-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"architecture": "x86_64", "manual": "preserved", "site": "west",
		}))
		Expect(lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationManagedLabels]).To(Equal(`["architecture","site"]`))
		Expect(events.snapshot()).To(HaveLen(1))
	})

	It("When an operator label update races with evaluation it should preserve both updates", func() {
		mapping := newLabelSyncMapping("architecture", "architecture")
		mapping.Spec.Expression = "architecture-output"
		_, err := mappingStore.Create(ctx, orgID, mapping)
		Expect(err).NotTo(HaveOccurred())
		labels := map[string]string{"manual": "before"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "operator-race", nil, nil, &labels)

		evaluator := newScriptedEvaluator(map[string]labelsyncmappingservice.Result{
			"architecture-output": labelsyncmappingservice.ScalarResult("aarch64"),
		}, "architecture-output", 1)
		service, events := newReconciliationService(deviceStore, mappingStore, evaluator, log)
		testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		reconcileResult := make(chan error, 1)
		go func() {
			_, err := service.ReconcileDeviceLabels(testCtx, orgID, "operator-race")
			reconcileResult <- err
		}()
		select {
		case <-evaluator.entered:
		case <-testCtx.Done():
			Fail(fmt.Sprintf("reconciler did not begin evaluation: %v", testCtx.Err()))
		}

		before, err := deviceStore.Get(testCtx, orgID, "operator-race")
		Expect(err).NotTo(HaveOccurred())
		_, _, _, err = deviceStore.Mutate(testCtx, orgID, "operator-race", before, func(mutation *devicestore.DeviceMutation) error {
			updated := lo.FromPtr(mutation.Device.Metadata.Labels)
			updated["manual"] = "operator-update"
			mutation.Device.Metadata.Labels = &updated
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		close(evaluator.release)
		select {
		case err := <-reconcileResult:
			Expect(err).NotTo(HaveOccurred())
		case <-testCtx.Done():
			Fail(fmt.Sprintf("reconciliation did not finish: %v", testCtx.Err()))
		}

		device, err := deviceStore.Get(ctx, orgID, "operator-race")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"architecture": "aarch64", "manual": "operator-update",
		}))
		Expect(events.snapshot()).To(HaveLen(1))
	})

	It("When a mapping changes during evaluation it should retry against the latest revision", func() {
		mapping := newLabelSyncMapping("architecture", "architecture")
		mapping.Spec.Expression = "old-output"
		created, err := mappingStore.Create(ctx, orgID, mapping)
		Expect(err).NotTo(HaveOccurred())
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "mapping-race", nil, nil, nil)

		evaluator := newScriptedEvaluator(map[string]labelsyncmappingservice.Result{
			"old-output":     labelsyncmappingservice.ScalarResult("stale"),
			"current-output": labelsyncmappingservice.ScalarResult("current"),
		}, "old-output", 1)
		service, events := newReconciliationService(deviceStore, mappingStore, evaluator, log)
		testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		resultCh := make(chan error, 1)
		go func() {
			_, err := service.ReconcileDeviceLabels(testCtx, orgID, "mapping-race")
			resultCh <- err
		}()
		select {
		case <-evaluator.entered:
		case <-testCtx.Done():
			Fail(fmt.Sprintf("reconciler did not begin evaluation: %v", testCtx.Err()))
		}

		created.Spec.Expression = "current-output"
		_, _, err = mappingStore.Update(testCtx, orgID, created)
		Expect(err).NotTo(HaveOccurred())
		close(evaluator.release)
		select {
		case err := <-resultCh:
			Expect(err).NotTo(HaveOccurred())
		case <-testCtx.Done():
			Fail(fmt.Sprintf("reconciliation did not finish: %v", testCtx.Err()))
		}

		device, err := deviceStore.Get(ctx, orgID, "mapping-race")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)["architecture"]).To(Equal("current"))
		Expect(evaluator.callsSnapshot()).To(Equal([]string{"old-output", "current-output"}))
		Expect(events.snapshot()).To(HaveLen(2))
	})
})

func newReconciliationService(deviceStore *devicestore.DeviceStore, mappingStore labelsyncmappingstore.Store, evaluator *scriptedEvaluator, log *logrus.Logger) (labelsyncmappingservice.Service, *recordingEvents) {
	events := &recordingEvents{}
	service, err := labelsyncmappingservice.NewServiceHandler(mappingStore, deviceStore, evaluator, events, log)
	Expect(err).NotTo(HaveOccurred())
	return service, events
}

type scriptedEvaluator struct {
	mu              sync.Mutex
	results         map[string]labelsyncmappingservice.Result
	calls           []string
	blockExpression string
	blockCalls      int
	blocked         int
	entered         chan string
	release         chan struct{}
}

func newScriptedEvaluator(results map[string]labelsyncmappingservice.Result, blockExpression string, blockCalls int) *scriptedEvaluator {
	return &scriptedEvaluator{
		results:         results,
		blockExpression: blockExpression,
		blockCalls:      blockCalls,
		entered:         make(chan string, blockCalls),
		release:         make(chan struct{}),
	}
}

func (e *scriptedEvaluator) ValidateExpressionIs(string, labelsyncmappingservice.ResultKind) error {
	return nil
}

func (e *scriptedEvaluator) Evaluate(expression string, _ labelsyncmappingservice.Activation) (labelsyncmappingservice.Result, error) {
	e.mu.Lock()
	e.calls = append(e.calls, expression)
	block := expression == e.blockExpression && e.blocked < e.blockCalls
	if block {
		e.blocked++
	}
	result := e.results[expression]
	e.mu.Unlock()
	if block {
		e.entered <- expression
		<-e.release
	}
	return result, nil
}

func (e *scriptedEvaluator) callsSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

type recordingEvents struct {
	mu     sync.Mutex
	events []*domain.Event
}

func (e *recordingEvents) CreateEvent(_ context.Context, _ uuid.UUID, event *domain.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (*recordingEvents) HandleGenericResourceDeletedEvents(context.Context, domain.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
}

func (e *recordingEvents) snapshot() []*domain.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*domain.Event(nil), e.events...)
}

func mappingID(ctx context.Context, db *gorm.DB, orgID uuid.UUID, name string) uuid.UUID {
	var mapping model.LabelSyncMapping
	Expect(db.WithContext(ctx).Select("id").Where("org_id = ? AND name = ?", orgID, name).Take(&mapping).Error).To(Succeed())
	return mapping.ID
}

func newLabelSyncDevice(name string, labels map[string]string) *domain.Device {
	return &domain.Device{
		Metadata: domain.ObjectMeta{Name: &name, Labels: &labels},
		Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "os"}},
	}
}

func newLabelSyncMapping(name, key string) *api.LabelSyncMapping {
	return &api.LabelSyncMapping{
		ApiVersion: "flightctl.io/v1beta1",
		Kind:       api.LabelSyncMappingKindLabelSyncMapping,
		Metadata:   api.ObjectMeta{Name: &name},
		Spec: api.LabelSyncMappingSpec{
			ResourceType: api.LabelSyncMappingSpecResourceTypeDevice,
			Key:          lo.ToPtr(key),
			Expression:   "device.status.systemInfo.architecture",
		},
	}
}

func mapLabelSyncMapping(name string) *api.LabelSyncMapping {
	mapping := newLabelSyncMapping(name, "")
	mapping.Spec.Key = nil
	return mapping
}
