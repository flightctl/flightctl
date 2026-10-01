package store_test

import (
	"context"
	"errors"
	"fmt"
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
		deviceStore  devicestore.Store
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

	It("When ownership is queried for multiple keys it should return exact current organization-scoped mapping IDs", func() {
		_, err := mappingStore.Create(ctx, orgID, mapLabelSyncMapping("map-owner"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, orgID, newLabelSyncMapping("scalar-owner", "scalar.current"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, orgID, newLabelSyncMapping("reservation-only", "configured.only"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, orgID, mapLabelSyncMapping("second-map-owner"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, otherOrgID, mapLabelSyncMapping("map-owner"))
		Expect(err).NotTo(HaveOccurred())

		mapOwnerID := mappingID(ctx, db, orgID, "map-owner")
		scalarOwnerID := mappingID(ctx, db, orgID, "scalar-owner")
		secondMapOwnerID := mappingID(ctx, db, orgID, "second-map-owner")
		otherOrgMapOwnerID := mappingID(ctx, db, otherOrgID, "map-owner")
		createDeviceWithOwnership := func(targetOrgID uuid.UUID, name string, labels map[string]string, owners map[string]uuid.UUID) {
			testutil.CreateTestDevice(ctx, deviceStore, targetOrgID, name, nil, nil, &labels)
			snapshot, err := deviceStore.GetLabelSnapshot(ctx, targetOrgID, name)
			Expect(err).NotTo(HaveOccurred())
			desired := make(map[string]domain.DesiredDeviceLabel, len(labels))
			for key, value := range labels {
				desired[key] = domain.DesiredDeviceLabel{Value: value}
				if mappingID, found := owners[key]; found {
					ownerID := mappingID
					desired[key] = domain.DesiredDeviceLabel{Value: value, MappingID: &ownerID}
				}
			}
			_, err = deviceStore.ApplyLabels(ctx, targetOrgID, name, snapshot, desired)
			Expect(err).NotTo(HaveOccurred())
		}

		createDeviceWithOwnership(orgID, "owner-one", map[string]string{
			"shared.key": "first", "map.only": "value", "operator.key": "manual",
		}, map[string]uuid.UUID{
			"shared.key": mapOwnerID, "map.only": mapOwnerID,
		})
		createDeviceWithOwnership(orgID, "owner-two", map[string]string{
			"shared.key": "second", "scalar.current": "value",
		}, map[string]uuid.UUID{
			"shared.key": mapOwnerID, "scalar.current": scalarOwnerID,
		})
		createDeviceWithOwnership(orgID, "owner-three", map[string]string{
			"shared.key": "second-mapping",
		}, map[string]uuid.UUID{"shared.key": secondMapOwnerID})
		createDeviceWithOwnership(otherOrgID, "owner-one", map[string]string{
			"shared.key": "other-org",
		}, map[string]uuid.UUID{"shared.key": otherOrgMapOwnerID})

		actual, err := deviceStore.GetLabelSyncMappingIDsByKeys(ctx, orgID, []string{
			"shared.key", "map.only", "scalar.current", "operator.key", "configured.only", "missing.key",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(actual["shared.key"]).To(HaveLen(2))
		Expect(actual["shared.key"]).To(ContainElement(mapOwnerID))
		Expect(actual["shared.key"]).To(ContainElement(secondMapOwnerID))
		Expect(actual["map.only"]).To(Equal([]uuid.UUID{mapOwnerID}))
		Expect(actual["scalar.current"]).To(Equal([]uuid.UUID{scalarOwnerID}))
		Expect(actual).NotTo(HaveKey("operator.key"))
		Expect(actual).NotTo(HaveKey("configured.only"))
		Expect(actual).NotTo(HaveKey("missing.key"))

		otherOrgActual, err := deviceStore.GetLabelSyncMappingIDsByKeys(ctx, otherOrgID, []string{"shared.key"})
		Expect(err).NotTo(HaveOccurred())
		Expect(otherOrgActual["shared.key"]).To(Equal([]uuid.UUID{otherOrgMapOwnerID}))

		empty, err := deviceStore.GetLabelSyncMappingIDsByKeys(ctx, orgID, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(empty).To(BeEmpty())
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

		applied, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, desired)
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
		transferred, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(transferred.LabelsChanged).To(BeFalse())
		Expect(transferred.ManagedLabelsChanged).To(BeFalse())
		Expect(transferred.OwnershipChanged).To(BeTrue())
		Expect(lo.FromPtr(transferred.Device.Metadata.ResourceVersion)).To(Equal("3"))
		Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "managed-device", "promoted").Take(&promoted).Error).To(Succeed())
		Expect(promoted.LabelSyncMappingID).To(Equal(&secondID))

		snapshot, err = deviceStore.GetLabelSnapshot(ctx, orgID, "managed-device")
		Expect(err).NotTo(HaveOccurred())
		unchanged, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, desired)
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
		repaired, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(repaired.LabelsChanged).To(BeFalse())
		Expect(repaired.ManagedLabelsChanged).To(BeTrue())
		Expect(repaired.OwnershipChanged).To(BeFalse())
		Expect(lo.FromPtr(repaired.Device.Metadata.Annotations)[domain.DeviceAnnotationManagedLabels]).To(Equal(`["promoted","same"]`))

		snapshot, err = deviceStore.GetLabelSnapshot(ctx, orgID, "managed-device")
		Expect(err).NotTo(HaveOccurred())
		cleaned, err := deviceStore.ApplyLabels(ctx, orgID, "managed-device", snapshot, map[string]domain.DesiredDeviceLabel{
			"manual": {Value: "preserved"},
		})
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
		})
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
			_, err := deviceStore.ApplyLabels(testCtx, orgID, "owner-transfer-race", snapshot, map[string]domain.DesiredDeviceLabel{})
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
		})
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

func newReconciliationService(deviceStore devicestore.Store, mappingStore labelsyncmappingstore.Store, evaluator *scriptedEvaluator, log *logrus.Logger) (labelsyncmappingservice.Service, *recordingEvents) {
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
