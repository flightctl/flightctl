package store_test

import (
	"context"
	"sync"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
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

var _ = Describe("LabelSyncMappingStore", func() {
	var (
		ctx          context.Context
		log          *logrus.Logger
		cfg          *config.Config
		dbName       string
		db           *gorm.DB
		orgID        uuid.UUID
		otherOrgID   uuid.UUID
		mappingStore labelsyncmappingstore.Store
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
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

	It("When mappings share an organization it should persist them independently and advance the revision", func() {
		first, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(first.Metadata.Generation)).To(Equal(int64(1)))
		second, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("site", "site"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(second.Metadata.Name)).To(Equal("site"))
		revision, err := mappingStore.Revision(ctx, orgID, api.LabelSyncMappingSpecResourceTypeDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(2)))
		list, err := mappingStore.List(ctx, orgID, store.ListParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(list.Items).To(HaveLen(2))
	})

	It("When mappings are stored it should persist stable identity, lifecycle revisions, and DeviceLabel ownership", func() {
		for _, column := range []string{"id", "failure_revision", "deletion_revision", "deletion_timestamp"} {
			Expect(db.Migrator().HasColumn("label_sync_mappings", column)).To(BeTrue(), "missing label_sync_mappings.%s", column)
		}
		Expect(db.Migrator().HasColumn("device_labels", "label_sync_mapping_id")).To(BeTrue())
		Expect(db.Migrator().HasIndex("label_sync_mappings", "label_sync_mappings_org_id_id_uq")).To(BeTrue())
		Expect(db.Migrator().HasIndex("label_sync_mappings", "label_sync_mappings_org_resource_destination_key_uq")).To(BeTrue())
		Expect(db.Migrator().HasIndex("device_labels", "device_labels_label_sync_mapping_idx")).To(BeTrue())
		Expect(db.Migrator().HasIndex("device_labels", "device_labels_label_sync_key_idx")).To(BeTrue())
		Expect(db.Migrator().HasConstraint(&model.DeviceLabel{}, "device_labels_label_sync_mapping_fk")).To(BeTrue())

		first, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("scalar", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
		mapMapping := newLabelSyncMapping("map", "")
		mapMapping.Spec.Key = nil
		_, err = mappingStore.Create(ctx, orgID, mapMapping)
		Expect(err).NotTo(HaveOccurred())

		var firstIdentity struct {
			ID               uuid.UUID
			FailureRevision  int64
			DeletionRevision *int64
		}
		Expect(db.Raw("SELECT id, failure_revision, deletion_revision FROM label_sync_mappings WHERE org_id = ? AND name = ?", orgID, "scalar").Scan(&firstIdentity).Error).To(Succeed())
		Expect(firstIdentity.ID).NotTo(Equal(uuid.Nil))
		Expect(firstIdentity.FailureRevision).To(Equal(int64(0)))
		Expect(firstIdentity.DeletionRevision).To(BeNil())
		Expect(lo.FromPtr(first.Metadata.Name)).To(Equal("scalar"))
	})

	It("When mapping names or keys belong to another organization it should isolate them", func() {
		_, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, otherOrgID, newLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		list, err := mappingStore.List(ctx, otherOrgID, store.ListParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(list.Items).To(HaveLen(1))
	})

	It("When a scalar key is reserved in an organization it should reject a second mapping", func() {
		_, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("first", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, orgID, newLabelSyncMapping("second", "systeminfo/architecture"))
		Expect(err).To(MatchError(flterrors.ErrLabelSyncConflict))
	})

	It("When a mapping changes and begins deletion it should retain the tombstone and advance the organization revision", func() {
		mapping, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		mapping.Spec.Expression = "status.systemInfo.architecture + '-v2'"
		updated, _, err := mappingStore.Update(ctx, orgID, mapping)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(updated.Metadata.Generation)).To(Equal(int64(2)))
		deleted, err := mappingStore.Delete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		terminating, err := mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(terminating.Metadata.DeletionTimestamp).NotTo(BeNil())
		Expect(lo.FromPtr(terminating.Status.Conditions)[0].Reason).To(Equal("Pending"))
		var tombstone model.LabelSyncMapping
		Expect(db.Select("id", "resource_version", "deletion_revision").Where("org_id = ? AND name = ?", orgID, "architecture").Take(&tombstone).Error).To(Succeed())
		Expect(tombstone.DeletionRevision).NotTo(BeNil())
		Expect(*tombstone.DeletionRevision).To(Equal(lo.FromPtr(tombstone.ResourceVersion)))
		_, err = mappingStore.Create(ctx, orgID, newLabelSyncMapping("replacement", "architecture"))
		Expect(err).To(MatchError(flterrors.ErrLabelSyncConflict))
		revision, err := mappingStore.Revision(ctx, orgID, api.LabelSyncMappingSpecResourceTypeDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(3)))
		finalized, err := mappingStore.FinalizeDelete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		_, err = mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).To(MatchError(ContainSubstring("resource not found")))
		Expect(db.Unscoped().Where("org_id = ? AND name = ?", orgID, "architecture").Take(&tombstone).Error).To(MatchError(gorm.ErrRecordNotFound))
		revision, err = mappingStore.Revision(ctx, orgID, api.LabelSyncMappingSpecResourceTypeDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(4)))
		recreated, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(recreated.Metadata.Generation)).To(Equal(int64(1)))
		var recreatedMapping model.LabelSyncMapping
		Expect(db.Select("id").Where("org_id = ? AND name = ?", orgID, "architecture").Take(&recreatedMapping).Error).To(Succeed())
		Expect(recreatedMapping.ID).NotTo(Equal(tombstone.ID))
		revision, err = mappingStore.Revision(ctx, orgID, api.LabelSyncMappingSpecResourceTypeDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(5)))
	})

	It("When concurrent mappings claim a scalar key it should admit only one reservation", func() {
		start := make(chan struct{})
		results := make(chan error, 2)
		var wait sync.WaitGroup
		for _, name := range []string{"first", "second"} {
			wait.Add(1)
			go func(name string) {
				defer wait.Done()
				<-start
				_, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping(name, "systeminfo/architecture"))
				results <- err
			}(name)
		}
		close(start)
		wait.Wait()
		close(results)

		created, conflicts := 0, 0
		for err := range results {
			if err == nil {
				created++
				continue
			}
			Expect(err).To(MatchError(flterrors.ErrLabelSyncConflict))
			conflicts++
		}
		Expect(created).To(Equal(1))
		Expect(conflicts).To(Equal(1))
	})

	It("When an unmanaged device label exists it should reject a scalar key claim", func() {
		deviceStore := devicestore.NewDeviceStore(db, log)
		labels := map[string]string{"systeminfo/architecture": "operator-value"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "unmanaged-label", nil, nil, &labels)
		_, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).To(MatchError(flterrors.ErrLabelSyncConflict))
	})

	It("When a mapping already owns a key it should allow an atomic map-to-scalar change", func() {
		mapping := newLabelSyncMapping("mapping", "")
		mapping.Spec.Key = nil
		created, err := mappingStore.Create(ctx, orgID, mapping)
		Expect(err).NotTo(HaveOccurred())
		var storedMapping model.LabelSyncMapping
		Expect(db.Select("id").Where("org_id = ? AND name = ?", orgID, "mapping").Take(&storedMapping).Error).To(Succeed())

		deviceStore := devicestore.NewDeviceStore(db, log)
		labels := map[string]string{"systeminfo/architecture": "x86_64"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "mapped-device", nil, nil, &labels)
		Expect(db.Model(&model.DeviceLabel{}).
			Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "mapped-device", "systeminfo/architecture").
			Update("label_sync_mapping_id", storedMapping.ID).Error).To(Succeed())

		created.Spec.Key = lo.ToPtr("systeminfo/architecture")
		updated, _, err := mappingStore.Update(ctx, orgID, created)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(updated.Metadata.Generation)).To(Equal(int64(2)))
	})

	It("When a mapping owns a DeviceLabel it should preserve ownership on value changes and delete it with the label", func() {
		_, err := mappingStore.Create(ctx, orgID, newLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
		var storedMapping model.LabelSyncMapping
		Expect(db.Select("id").Where("org_id = ? AND name = ?", orgID, "architecture").Take(&storedMapping).Error).To(Succeed())
		mappingID := storedMapping.ID

		deviceStore := devicestore.NewDeviceStore(db, log)
		labels := map[string]string{"systeminfo/architecture": "x86_64"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "mapped-device", nil, nil, &labels)
		Expect(db.Model(&model.DeviceLabel{}).
			Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "mapped-device", "systeminfo/architecture").
			Update("label_sync_mapping_id", mappingID).Error).To(Succeed())

		updatedLabels := model.JSONMap[string, string]{"systeminfo/architecture": "aarch64"}
		Expect(db.Model(&model.Device{}).Where("org_id = ? AND name = ?", orgID, "mapped-device").Update("labels", updatedLabels).Error).To(Succeed())

		var label model.DeviceLabel
		Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "mapped-device", "systeminfo/architecture").Take(&label).Error).To(Succeed())
		Expect(label.LabelValue).To(Equal("aarch64"))
		Expect(label.LabelSyncMappingID).To(Equal(&mappingID))

		Expect(mappingStore.Delete(ctx, orgID, "architecture")).To(BeTrue())
		finalized, err := mappingStore.FinalizeDelete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeFalse())

		emptyLabels := model.JSONMap[string, string]{}
		Expect(db.Model(&model.Device{}).Where("org_id = ? AND name = ?", orgID, "mapped-device").Update("labels", emptyLabels).Error).To(Succeed())
		Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "mapped-device", "systeminfo/architecture").Take(&label).Error).To(HaveOccurred())
		finalized, err = mappingStore.FinalizeDelete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
	})
})

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
