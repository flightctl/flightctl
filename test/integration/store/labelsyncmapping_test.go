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
			_, err = deviceStore.ApplyLabels(ctx, targetOrgID, name, snapshot, desired, nil)
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

	It("When mapping outputs change or a mapping is recreated it should report only current organization-scoped owners", func() {
		firstMapping := mapLabelSyncMapping("shared-owner")
		firstMapping.Spec.Expression = `{"old.key":"old","null.key":"before","shared.key":"first-org"}`
		_, err := mappingStore.Create(ctx, orgID, firstMapping)
		Expect(err).NotTo(HaveOccurred())
		otherOrgMapping := mapLabelSyncMapping("shared-owner")
		otherOrgMapping.Spec.Expression = `{"other-org.key":"other","shared.key":"other-org"}`
		_, err = mappingStore.Create(ctx, otherOrgID, otherOrgMapping)
		Expect(err).NotTo(HaveOccurred())
		firstID := mappingID(ctx, db, orgID, "shared-owner")
		otherOrgIDForMapping := mappingID(ctx, db, otherOrgID, "shared-owner")
		Expect(otherOrgIDForMapping).NotTo(Equal(firstID))

		testutil.CreateTestDevice(ctx, deviceStore, orgID, "shared-device", nil, nil, nil)
		testutil.CreateTestDevice(ctx, deviceStore, otherOrgID, "shared-device", nil, nil, nil)
		evaluator, err := labelsyncmappingservice.NewEvaluator()
		Expect(err).NotTo(HaveOccurred())
		service, err := labelsyncmappingservice.NewServiceHandler(mappingStore, deviceStore, evaluator, &recordingEvents{}, log)
		Expect(err).NotTo(HaveOccurred())
		_, err = service.ReconcileDeviceLabels(ctx, orgID, "shared-device")
		Expect(err).NotTo(HaveOccurred())
		_, err = service.ReconcileDeviceLabels(ctx, otherOrgID, "shared-device")
		Expect(err).NotTo(HaveOccurred())

		initial, status := service.GetLabelSyncProvenance(ctx, orgID, []string{"old.key", "null.key", "shared.key", "other-org.key"})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(initial.Items).To(Equal([]domain.LabelSyncProvenanceItem{
			{Key: "old.key", Owners: []string{"shared-owner"}},
			{Key: "null.key", Owners: []string{"shared-owner"}},
			{Key: "shared.key", Owners: []string{"shared-owner"}},
			{Key: "other-org.key", Owners: []string{}},
		}))
		otherOrgInitial, status := service.GetLabelSyncProvenance(ctx, otherOrgID, []string{"other-org.key", "old.key", "shared.key"})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(otherOrgInitial.Items).To(Equal([]domain.LabelSyncProvenanceItem{
			{Key: "other-org.key", Owners: []string{"shared-owner"}},
			{Key: "old.key", Owners: []string{}},
			{Key: "shared.key", Owners: []string{"shared-owner"}},
		}))

		deviceInitial, status := service.GetDeviceLabelSyncProvenance(ctx, orgID, "shared-device")
		Expect(status.Code).To(Equal(int32(200)))
		Expect(deviceInitial.Items).To(Equal([]domain.LabelSyncProvenanceItem{
			{Key: "null.key", Owners: []string{"shared-owner"}},
			{Key: "old.key", Owners: []string{"shared-owner"}},
			{Key: "shared.key", Owners: []string{"shared-owner"}},
		}))

		updated, err := mappingStore.Get(ctx, orgID, "shared-owner")
		Expect(err).NotTo(HaveOccurred())
		updated.Spec.Expression = `{"new.key":"new","null.key":null,"shared.key":"updated-first-org"}`
		_, _, err = mappingStore.Update(ctx, orgID, updated)
		Expect(err).NotTo(HaveOccurred())
		_, err = service.ReconcileDeviceLabels(ctx, orgID, "shared-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(mappingID(ctx, db, orgID, "shared-owner")).To(Equal(firstID))

		afterUpdate, status := service.GetLabelSyncProvenance(ctx, orgID, []string{"old.key", "null.key", "new.key", "shared.key", "other-org.key"})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(afterUpdate.Items).To(Equal([]domain.LabelSyncProvenanceItem{
			{Key: "old.key", Owners: []string{}},
			{Key: "null.key", Owners: []string{}},
			{Key: "new.key", Owners: []string{"shared-owner"}},
			{Key: "shared.key", Owners: []string{"shared-owner"}},
			{Key: "other-org.key", Owners: []string{}},
		}))
		deviceAfterUpdate, status := service.GetDeviceLabelSyncProvenance(ctx, orgID, "shared-device")
		Expect(status.Code).To(Equal(int32(200)))
		Expect(deviceAfterUpdate.Items).To(Equal([]domain.LabelSyncProvenanceItem{
			{Key: "new.key", Owners: []string{"shared-owner"}},
			{Key: "shared.key", Owners: []string{"shared-owner"}},
		}))
		device, err := deviceStore.Get(ctx, orgID, "shared-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"new.key":    "new",
			"shared.key": "updated-first-org",
		}))
		Expect(lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationManagedLabels]).To(Equal(`["new.key","shared.key"]`))

		deleted, err := mappingStore.Delete(ctx, orgID, "shared-owner")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		_, err = service.ReconcileDeviceLabels(ctx, orgID, "shared-device")
		Expect(err).NotTo(HaveOccurred())
		deletedProvenance, status := service.GetLabelSyncProvenance(ctx, orgID, []string{"new.key", "shared.key"})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(deletedProvenance.Items).To(Equal([]domain.LabelSyncProvenanceItem{
			{Key: "new.key", Owners: []string{}},
			{Key: "shared.key", Owners: []string{}},
		}))
		finalized, err := mappingStore.FinalizeDelete(ctx, orgID, "shared-owner")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())

		recreated := mapLabelSyncMapping("shared-owner")
		recreated.Spec.Expression = `{"recreated.key":"fresh"}`
		_, err = mappingStore.Create(ctx, orgID, recreated)
		Expect(err).NotTo(HaveOccurred())
		recreatedID := mappingID(ctx, db, orgID, "shared-owner")
		Expect(recreatedID).NotTo(Equal(firstID))
		_, err = service.ReconcileDeviceLabels(ctx, orgID, "shared-device")
		Expect(err).NotTo(HaveOccurred())
		var recreatedLabel model.DeviceLabel
		Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "shared-device", "recreated.key").Take(&recreatedLabel).Error).To(Succeed())
		Expect(recreatedLabel.LabelSyncMappingID).To(Equal(&recreatedID))
		recreatedDeviceProvenance, status := service.GetDeviceLabelSyncProvenance(ctx, orgID, "shared-device")
		Expect(status.Code).To(Equal(int32(200)))
		Expect(recreatedDeviceProvenance.Items).To(Equal([]domain.LabelSyncProvenanceItem{
			{Key: "recreated.key", Owners: []string{"shared-owner"}},
		}))
		afterRecreate, status := service.GetLabelSyncProvenance(ctx, orgID, []string{"new.key", "shared.key", "recreated.key", "other-org.key"})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(afterRecreate.Items).To(Equal([]domain.LabelSyncProvenanceItem{
			{Key: "new.key", Owners: []string{}},
			{Key: "shared.key", Owners: []string{}},
			{Key: "recreated.key", Owners: []string{"shared-owner"}},
			{Key: "other-org.key", Owners: []string{}},
		}))

		otherOrgAfterRecreate, status := service.GetLabelSyncProvenance(ctx, otherOrgID, []string{"other-org.key", "recreated.key"})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(otherOrgAfterRecreate.Items).To(Equal([]domain.LabelSyncProvenanceItem{
			{Key: "other-org.key", Owners: []string{"shared-owner"}},
			{Key: "recreated.key", Owners: []string{}},
		}))
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

	It("When scalar keys are claimed it should enforce exact-key uniqueness within one organization", func() {
		key := "systeminfo/site"
		_, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("first", key))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, orgID, newLabelSyncMapping("duplicate", key))
		Expect(err).To(MatchError(flterrors.ErrLabelSyncConflict))
		_, err = mappingStore.Create(ctx, orgID, newLabelSyncMapping("prefix-sharing", key+"-extra"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, otherOrgID, newLabelSyncMapping("other-org", key))
		Expect(err).NotTo(HaveOccurred())
	})

	It("When a mapping key or mode changes and it is deleted it should retain ownership until reconciliation cleanup", func() {
		mapping := newLabelSyncMapping("lifecycle", "old/scalar-key")
		mapping.Spec.Expression = "old-output"
		_, err := mappingStore.Create(ctx, orgID, mapping)
		Expect(err).NotTo(HaveOccurred())

		labels := map[string]string{"manual": "preserved"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "lifecycle-device", nil, nil, &labels)
		evaluator := newScriptedEvaluator(map[string]labelsyncmappingservice.Result{
			"old-output":    labelsyncmappingservice.ScalarResult("old-value"),
			"new-output":    labelsyncmappingservice.ScalarResult("new-value"),
			"map-output":    labelsyncmappingservice.MapResult{"map/new-key": "map-value"},
			"delete-output": labelsyncmappingservice.ScalarResult("delete-value"),
		}, "", 0)
		service, _ := newReconciliationService(deviceStore, mappingStore, evaluator, log)

		_, err = service.ReconcileDeviceLabels(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		device, err := deviceStore.Get(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"manual": "preserved", "old/scalar-key": "old-value",
		}))

		current, err := mappingStore.Get(ctx, orgID, "lifecycle")
		Expect(err).NotTo(HaveOccurred())
		changedKey := *current
		changedKey.Spec.Key = lo.ToPtr("new/scalar-key")
		changedKey.Spec.Expression = "new-output"
		pending, status := service.ReplaceLabelSyncMapping(ctx, orgID, "lifecycle", changedKey)
		Expect(status.Code).To(Equal(int32(200)))
		ready := domain.FindStatusCondition(lo.FromPtr(pending.Status.Conditions), domain.ConditionTypeLabelSyncMappingReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(domain.ConditionStatusFalse))
		Expect(ready.Reason).To(Equal("Pending"))
		device, err = deviceStore.Get(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"manual": "preserved", "old/scalar-key": "old-value",
		}))

		_, err = service.ReconcileDeviceLabels(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		device, err = deviceStore.Get(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"manual": "preserved", "new/scalar-key": "new-value",
		}))

		current, err = mappingStore.Get(ctx, orgID, "lifecycle")
		Expect(err).NotTo(HaveOccurred())
		changedMode := *current
		changedMode.Spec.Key = nil
		changedMode.Spec.Expression = "map-output"
		pending, status = service.ReplaceLabelSyncMapping(ctx, orgID, "lifecycle", changedMode)
		Expect(status.Code).To(Equal(int32(200)))
		ready = domain.FindStatusCondition(lo.FromPtr(pending.Status.Conditions), domain.ConditionTypeLabelSyncMappingReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(domain.ConditionStatusFalse))
		Expect(ready.Reason).To(Equal("Pending"))
		device, err = deviceStore.Get(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"manual": "preserved", "new/scalar-key": "new-value",
		}))

		_, err = service.ReconcileDeviceLabels(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		device, err = deviceStore.Get(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"manual": "preserved", "map/new-key": "map-value",
		}))

		status = service.DeleteLabelSyncMapping(ctx, orgID, "lifecycle")
		Expect(status.Code).To(Equal(int32(200)))
		terminating, status := service.GetLabelSyncMapping(ctx, orgID, "lifecycle")
		Expect(status.Code).To(Equal(int32(200)))
		Expect(terminating.Metadata.DeletionTimestamp).NotTo(BeNil())
		device, err = deviceStore.Get(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(HaveKeyWithValue("map/new-key", "map-value"))

		_, err = service.ReconcileDeviceLabels(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		device, err = deviceStore.Get(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{"manual": "preserved"}))
		finalized, err := mappingStore.FinalizeDelete(ctx, orgID, "lifecycle")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		_, status = service.GetLabelSyncMapping(ctx, orgID, "lifecycle")
		Expect(status.Code).To(Equal(int32(404)))

		deleting := newLabelSyncMapping("delete-pending", "delete/key")
		deleting.Spec.Expression = "delete-output"
		_, err = mappingStore.Create(ctx, orgID, deleting)
		Expect(err).NotTo(HaveOccurred())
		oldID := mappingID(ctx, db, orgID, "delete-pending")
		_, err = service.ReconcileDeviceLabels(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())

		status = service.DeleteLabelSyncMapping(ctx, orgID, "delete-pending")
		Expect(status.Code).To(Equal(int32(200)))
		terminating, status = service.GetLabelSyncMapping(ctx, orgID, "delete-pending")
		Expect(status.Code).To(Equal(int32(200)))
		Expect(terminating.Metadata.DeletionTimestamp).NotTo(BeNil())
		finalized, err = mappingStore.FinalizeDelete(ctx, orgID, "delete-pending")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeFalse())
		conflict := newLabelSyncMapping("delete-conflict", "delete/key")
		_, err = mappingStore.Create(ctx, orgID, conflict)
		Expect(err).To(MatchError(flterrors.ErrLabelSyncConflict))
		device, err = deviceStore.Get(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(HaveKeyWithValue("delete/key", "delete-value"))

		_, err = service.ReconcileDeviceLabels(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		device, err = deviceStore.Get(ctx, orgID, "lifecycle-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{"manual": "preserved"}))
		finalized, err = mappingStore.FinalizeDelete(ctx, orgID, "delete-pending")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		recreated := newLabelSyncMapping("delete-pending", "delete/key")
		_, err = mappingStore.Create(ctx, orgID, recreated)
		Expect(err).NotTo(HaveOccurred())
		Expect(mappingID(ctx, db, orgID, "delete-pending")).NotTo(Equal(oldID))
	})

	It("When mapping outputs collide it should resolve candidates per device", func() {
		firstMap := mapLabelSyncMapping("first-map")
		firstMap.Spec.Expression = "first-map-output"
		_, err := mappingStore.Create(ctx, orgID, firstMap)
		Expect(err).NotTo(HaveOccurred())
		secondMap := mapLabelSyncMapping("second-map")
		secondMap.Spec.Expression = "second-map-output"
		_, err = mappingStore.Create(ctx, orgID, secondMap)
		Expect(err).NotTo(HaveOccurred())
		unrelatedMap := mapLabelSyncMapping("unrelated-map")
		unrelatedMap.Spec.Expression = "unrelated-map-output"
		_, err = mappingStore.Create(ctx, orgID, unrelatedMap)
		Expect(err).NotTo(HaveOccurred())
		unrelatedID := mappingID(ctx, db, orgID, "unrelated-map")
		scalar := newLabelSyncMapping("scalar", "shared")
		scalar.Spec.Expression = "scalar-output"
		_, err = mappingStore.Create(ctx, orgID, scalar)
		Expect(err).NotTo(HaveOccurred())

		for _, name := range []string{"collision-device", "independent-device"} {
			labels := map[string]string{"manual": "preserved"}
			testutil.CreateTestDevice(ctx, deviceStore, orgID, name, nil, nil, &labels)
		}
		evaluator := &deviceScopedEvaluator{results: map[string]map[string]labelsyncmappingservice.Result{
			"collision-device": {
				"first-map-output":     labelsyncmappingservice.MapResult{"shared": "map-value", "map-map": "first", "first-only": "first"},
				"second-map-output":    labelsyncmappingservice.MapResult{"map-map": "second", "second-only": "second"},
				"unrelated-map-output": labelsyncmappingservice.MapResult{"unrelated": "unaffected"},
				"scalar-output":        labelsyncmappingservice.ScalarResult("scalar-value"),
			},
			"independent-device": {
				"first-map-output":     labelsyncmappingservice.MapResult{"shared": "device-specific-map-value", "first-only": "first"},
				"second-map-output":    labelsyncmappingservice.MapResult{"second-only": "second"},
				"unrelated-map-output": labelsyncmappingservice.MapResult{"unrelated": "unaffected"},
				"scalar-output":        labelsyncmappingservice.NoResult{},
			},
		}}
		service, _ := newReconciliationService(deviceStore, mappingStore, evaluator, log)

		collision, err := service.ReconcileDeviceLabels(ctx, orgID, "collision-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(collision.MappingOutcomes).To(HaveLen(4))
		for _, outcome := range collision.MappingOutcomes {
			if outcome.MappingID == unrelatedID {
				Expect(outcome.Err).NotTo(HaveOccurred())
			} else {
				Expect(outcome.Err).To(HaveOccurred())
			}
		}
		device, err := deviceStore.Get(ctx, orgID, "collision-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"manual": "preserved", "unrelated": "unaffected",
		}))
		Expect(lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationManagedLabels]).To(Equal(`["unrelated"]`))

		independent, err := service.ReconcileDeviceLabels(ctx, orgID, "independent-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(independent.MappingOutcomes).To(HaveLen(4))
		for _, outcome := range independent.MappingOutcomes {
			Expect(outcome.Err).NotTo(HaveOccurred())
		}
		device, err = deviceStore.Get(ctx, orgID, "independent-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"manual": "preserved", "shared": "device-specific-map-value", "first-only": "first", "second-only": "second", "unrelated": "unaffected",
		}))
		Expect(lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationManagedLabels]).To(Equal(`["first-only","second-only","shared","unrelated"]`))
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

	It("When an operator write wins before label ownership is acquired reconciliation should retry and overwrite it", func() {
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
			updated["architecture"] = "operator-update"
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
			"architecture": "aarch64", "manual": "before",
		}))
		Expect(events.snapshot()).To(HaveLen(1))
	})

	It("When reconciliation takes ownership before a stale operator update retries it should reject the write", func() {
		mapping := newLabelSyncMapping("architecture", "architecture")
		mapping.Spec.Expression = "architecture-output"
		_, err := mappingStore.Create(ctx, orgID, mapping)
		Expect(err).NotTo(HaveOccurred())
		mappingUUID := mappingID(ctx, db, orgID, "architecture")
		labels := map[string]string{"architecture": "user-value", "manual": "keep"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "mapping-wins-race", nil, nil, &labels)
		before, err := deviceStore.Get(ctx, orgID, "mapping-wins-race")
		Expect(err).NotTo(HaveOccurred())

		const callbackName = "test:pause-before-device-update-after-owner-check"
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
		operatorResult := make(chan error, 1)
		go func() {
			_, _, _, err := deviceStore.Mutate(testCtx, orgID, "mapping-wins-race", before, func(mutation *devicestore.DeviceMutation) error {
				updated := lo.FromPtr(mutation.Device.Metadata.Labels)
				updated["architecture"] = "operator-update"
				mutation.Device.Metadata.Labels = &updated
				return nil
			})
			operatorResult <- err
		}()
		select {
		case <-deviceUpdateStarted:
		case <-testCtx.Done():
			Fail(fmt.Sprintf("operator update did not reach its version check: %v", testCtx.Err()))
		}

		evaluator := newScriptedEvaluator(map[string]labelsyncmappingservice.Result{
			"architecture-output": labelsyncmappingservice.ScalarResult("mapped-value"),
		}, "", 0)
		service, events := newReconciliationService(deviceStore, mappingStore, evaluator, log)
		result, err := service.ReconcileDeviceLabels(testCtx, orgID, "mapping-wins-race")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.LabelsChanged).To(BeTrue())
		Expect(result.OwnershipChanged).To(BeTrue())

		releaseDeviceUpdate()
		select {
		case err := <-operatorResult:
			Expect(errors.Is(err, flterrors.ErrManagedLabelConflict)).To(BeTrue())
		case <-testCtx.Done():
			Fail(fmt.Sprintf("operator retry did not observe the mapping owner: %v", testCtx.Err()))
		}

		device, err := deviceStore.Get(ctx, orgID, "mapping-wins-race")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(Equal(map[string]string{
			"architecture": "mapped-value", "manual": "keep",
		}))
		var owned model.DeviceLabel
		Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "mapping-wins-race", "architecture").Take(&owned).Error).To(Succeed())
		Expect(owned.LabelSyncMappingID).To(Equal(&mappingUUID))
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

func newReconciliationService(deviceStore devicestore.Store, mappingStore labelsyncmappingstore.Store, evaluator labelsyncmappingservice.Evaluator, log *logrus.Logger) (labelsyncmappingservice.Service, *recordingEvents) {
	events := &recordingEvents{}
	service, err := labelsyncmappingservice.NewServiceHandler(mappingStore, deviceStore, evaluator, events, log)
	Expect(err).NotTo(HaveOccurred())
	return service, events
}

type deviceScopedEvaluator struct {
	results map[string]map[string]labelsyncmappingservice.Result
}

func (e *deviceScopedEvaluator) ValidateExpressionIs(string, labelsyncmappingservice.ResultKind) error {
	return nil
}

func (e *deviceScopedEvaluator) Evaluate(expression string, activation labelsyncmappingservice.Activation) (labelsyncmappingservice.Result, error) {
	roots, ok := activation.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected activation type %T", activation)
	}
	metadata, ok := roots["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected metadata type %T", roots["metadata"])
	}
	deviceName, ok := metadata["name"].(string)
	if !ok {
		return nil, fmt.Errorf("unexpected device name %T", metadata["name"])
	}
	result, ok := e.results[deviceName][expression]
	if !ok {
		return nil, fmt.Errorf("no result configured for %s on %s", expression, deviceName)
	}
	return result, nil
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
