package store_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

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

var _ = Describe("LabelSyncMapping store CRUD", func() {
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
		first, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(first.Metadata.Generation)).To(Equal(int64(1)))
		second, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("site", "site"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(second.Metadata.Name)).To(Equal("site"))
		revision, err := labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(2)))
		list, err := mappingStore.List(ctx, orgID, store.ListParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(list.Items).To(HaveLen(2))
	})

	It("When a mapping is created in an active transaction it should be visible through revision reads", func() {
		var revision int64
		err := store.WithTransaction(ctx, db, func(txCtx context.Context) error {
			if _, err := mappingStore.Create(txCtx, orgID, newStoreCoverageLabelSyncMapping("transactional", "architecture")); err != nil {
				return err
			}
			var err error
			revision, err = labelSyncMappingRevision(txCtx, mappingStore, orgID)
			return err
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(1)))
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

		first, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("scalar", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
		mapMapping := newStoreCoverageLabelSyncMapping("map", "")
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
		_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, otherOrgID, newStoreCoverageLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		list, err := mappingStore.List(ctx, otherOrgID, store.ListParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(list.Items).To(HaveLen(1))
	})

	It("When a scalar key is reserved in an organization it should reject a second mapping", func() {
		_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("first", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
		_, err = mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("second", "systeminfo/architecture"))
		Expect(err).To(MatchError(flterrors.ErrLabelSyncConflict))
	})

	It("When a mapping changes and begins deletion it should retain the tombstone and advance the organization revision", func() {
		mapping, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "architecture"))
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
		_, err = mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("replacement", "architecture"))
		Expect(err).To(MatchError(flterrors.ErrLabelSyncConflict))
		revision, err := labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(3)))
		finalized, err := mappingStore.FinalizeDelete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		_, err = mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).To(MatchError(ContainSubstring("resource not found")))
		Expect(db.Unscoped().Where("org_id = ? AND name = ?", orgID, "architecture").Take(&tombstone).Error).To(MatchError(gorm.ErrRecordNotFound))
		revision, err = labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(4)))
		recreated, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "architecture"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(recreated.Metadata.Generation)).To(Equal(int64(1)))
		var recreatedMapping model.LabelSyncMapping
		Expect(db.Select("id").Where("org_id = ? AND name = ?", orgID, "architecture").Take(&recreatedMapping).Error).To(Succeed())
		Expect(recreatedMapping.ID).NotTo(Equal(tombstone.ID))
		revision, err = labelSyncMappingRevision(ctx, mappingStore, orgID)
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
				_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping(name, "systeminfo/architecture"))
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
		revision, err := labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(1)))
	})

	It("When distinct mappings are created concurrently it should advance the revision for each mapping", func() {
		names := []string{"first", "second", "third", "fourth"}
		start := make(chan struct{})
		results := make(chan error, len(names))
		var wait sync.WaitGroup
		for _, name := range names {
			wait.Add(1)
			go func(name string) {
				defer wait.Done()
				<-start
				_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping(name, "systeminfo/"+name))
				results <- err
			}(name)
		}
		close(start)
		wait.Wait()
		close(results)

		for err := range results {
			Expect(err).NotTo(HaveOccurred())
		}
		revision, err := labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(len(names))))
	})

	It("When concurrent updates use the same resource version it should accept only one update", func() {
		created, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())

		first, err := mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		second, err := mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		first.Spec.Expression = "device.status.systemInfo.architecture + '-first'"
		second.Spec.Expression = "device.status.systemInfo.architecture + '-second'"

		start := make(chan struct{})
		results := make(chan error, 2)
		var wait sync.WaitGroup
		for _, mapping := range []*api.LabelSyncMapping{first, second} {
			wait.Add(1)
			go func(mapping *api.LabelSyncMapping) {
				defer wait.Done()
				<-start
				_, _, err := mappingStore.Update(ctx, orgID, mapping)
				results <- err
			}(mapping)
		}
		close(start)
		wait.Wait()
		close(results)

		succeeded, conflicts := 0, 0
		for err := range results {
			if err == nil {
				succeeded++
				continue
			}
			Expect(err).To(MatchError(flterrors.ErrResourceVersionConflict))
			conflicts++
		}
		Expect(succeeded).To(Equal(1))
		Expect(conflicts).To(Equal(1))

		updated, err := mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(updated.Metadata.ResourceVersion)).NotTo(Equal(lo.FromPtr(created.Metadata.ResourceVersion)))
		revision, err := labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(2)))
	})

	It("When an update claims a reserved scalar key it should reject the update", func() {
		_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
		update, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("hostname", "systeminfo/hostname"))
		Expect(err).NotTo(HaveOccurred())

		update.Spec.Key = lo.ToPtr("systeminfo/architecture")
		_, _, err = mappingStore.Update(ctx, orgID, update)
		Expect(err).To(MatchError(flterrors.ErrLabelSyncConflict))

		architecture, err := mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(architecture.Spec.Key)).To(Equal("systeminfo/architecture"))
		hostname, err := mappingStore.Get(ctx, orgID, "hostname")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(hostname.Spec.Key)).To(Equal("systeminfo/hostname"))
		Expect(lo.FromPtr(hostname.Metadata.ResourceVersion)).To(Equal("1"))
		Expect(lo.FromPtr(hostname.Metadata.Generation)).To(Equal(int64(1)))

		revision, err := labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(2)))
	})

	It("When an update races with same-name recreation it should not modify the replacement mapping", func() {
		stale, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
		stale.Spec.Expression = "device.status.systemInfo.architecture + '-stale'"

		writeReached, resumeWrite, cleanupPause, err := pauseBeforeNextLabelSyncMappingWrite(db)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = cleanupPause() }()

		updateResult := make(chan error, 1)
		go func() {
			_, _, err := mappingStore.Update(ctx, orgID, stale)
			updateResult <- err
		}()
		Eventually(writeReached).Should(BeClosed())

		deleted, err := mappingStore.Delete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		finalized, err := mappingStore.FinalizeDelete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		replacement, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())

		resumeWrite()
		Expect(<-updateResult).To(MatchError(flterrors.ErrResourceVersionConflict))
		Expect(cleanupPause()).To(Succeed())

		current, err := mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Spec.Expression).To(Equal(replacement.Spec.Expression))
		Expect(lo.FromPtr(current.Metadata.ResourceVersion)).To(Equal("1"))
		Expect(lo.FromPtr(current.Metadata.Generation)).To(Equal(int64(1)))
		revision, err := labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(4)))
	})

	It("When concurrent deletes target the same mapping it should return idempotently", func() {
		_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())

		start := make(chan struct{})
		results := make(chan error, 2)
		var wait sync.WaitGroup
		for range 2 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				deleted, err := mappingStore.Delete(ctx, orgID, "architecture")
				if err == nil && !deleted {
					err = errors.New("delete did not report the existing mapping")
				}
				results <- err
			}()
		}
		close(start)
		wait.Wait()
		close(results)
		for err := range results {
			Expect(err).NotTo(HaveOccurred())
		}

		mapping, err := mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(mapping.Metadata.DeletionTimestamp).NotTo(BeNil())
		revision, err := labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(2)))
	})

	It("When a delete races with same-name recreation it should leave the replacement mapping active", func() {
		_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())

		writeReached, resumeWrite, cleanupPause, err := pauseBeforeNextLabelSyncMappingWrite(db)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = cleanupPause() }()

		deleteResult := make(chan struct {
			deleted bool
			err     error
		}, 1)
		go func() {
			deleted, err := mappingStore.Delete(ctx, orgID, "architecture")
			deleteResult <- struct {
				deleted bool
				err     error
			}{deleted, err}
		}()
		Eventually(writeReached).Should(BeClosed())

		deleted, err := mappingStore.Delete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		finalized, err := mappingStore.FinalizeDelete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		replacement, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())

		resumeWrite()
		staleDelete := <-deleteResult
		Expect(staleDelete.err).NotTo(HaveOccurred())
		Expect(staleDelete.deleted).To(BeFalse())
		Expect(cleanupPause()).To(Succeed())

		current, err := mappingStore.Get(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Spec.Expression).To(Equal(replacement.Spec.Expression))
		Expect(current.Metadata.DeletionTimestamp).To(BeNil())
		Expect(lo.FromPtr(current.Metadata.ResourceVersion)).To(Equal("1"))
		Expect(lo.FromPtr(current.Metadata.Generation)).To(Equal(int64(1)))
		revision, err := labelSyncMappingRevision(ctx, mappingStore, orgID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(int64(4)))
	})

	It("When an unmanaged device label exists it should allow a scalar key claim", func() {
		deviceStore := devicestore.NewDeviceStore(db, log)
		labels := map[string]string{"systeminfo/architecture": "operator-value"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "unmanaged-label", nil, nil, &labels)
		_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("When another mapping owns a device label it should allow a scalar key claim", func() {
		owner := newStoreCoverageLabelSyncMapping("map-owner", "")
		owner.Spec.Key = nil
		_, err := mappingStore.Create(ctx, orgID, owner)
		Expect(err).NotTo(HaveOccurred())
		var storedOwner model.LabelSyncMapping
		Expect(db.Select("id").Where("org_id = ? AND name = ?", orgID, "map-owner").Take(&storedOwner).Error).To(Succeed())

		deviceStore := devicestore.NewDeviceStore(db, log)
		labels := map[string]string{"systeminfo/architecture": "mapped-value"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "mapped-label", nil, nil, &labels)
		Expect(db.Model(&model.DeviceLabel{}).
			Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "mapped-label", "systeminfo/architecture").
			Update("label_sync_mapping_id", storedOwner.ID).Error).To(Succeed())

		_, err = mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("scalar", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("When a mapping already owns a key it should allow an atomic map-to-scalar change", func() {
		mapping := newStoreCoverageLabelSyncMapping("mapping", "")
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
		_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
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
		finalized := false
		var remaining int64
		err = store.WithTransaction(ctx, db, func(txCtx context.Context) error {
			var err error
			finalized, err = mappingStore.FinalizeDelete(txCtx, orgID, "architecture")
			if err != nil {
				return err
			}
			return store.DB(txCtx, db).Model(&model.LabelSyncMapping{}).
				Where("org_id = ? AND name = ?", orgID, "architecture").Count(&remaining).Error
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeFalse())
		Expect(remaining).To(Equal(int64(1)))

		emptyLabels := model.JSONMap[string, string]{}
		Expect(db.Model(&model.Device{}).Where("org_id = ? AND name = ?", orgID, "mapped-device").Update("labels", emptyLabels).Error).To(Succeed())
		Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "mapped-device", "systeminfo/architecture").Take(&label).Error).To(HaveOccurred())
		finalized, err = mappingStore.FinalizeDelete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
	})

	It("When finalization races with label ownership it should preserve referential integrity", func() {
		_, err := mappingStore.Create(ctx, orgID, newStoreCoverageLabelSyncMapping("architecture", "systeminfo/architecture"))
		Expect(err).NotTo(HaveOccurred())
		deleted, err := mappingStore.Delete(ctx, orgID, "architecture")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())

		var storedMapping model.LabelSyncMapping
		Expect(db.Select("id").Where("org_id = ? AND name = ?", orgID, "architecture").Take(&storedMapping).Error).To(Succeed())
		deviceStore := devicestore.NewDeviceStore(db, log)
		labels := map[string]string{"systeminfo/architecture": "x86_64"}
		testutil.CreateTestDevice(ctx, deviceStore, orgID, "mapped-device", nil, nil, &labels)

		start := make(chan struct{})
		finalizeResult := make(chan struct {
			finalized bool
			err       error
		}, 1)
		ownershipResult := make(chan error, 1)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			finalized, err := mappingStore.FinalizeDelete(ctx, orgID, "architecture")
			finalizeResult <- struct {
				finalized bool
				err       error
			}{finalized, err}
		}()
		go func() {
			defer wait.Done()
			<-start
			err := db.Model(&model.DeviceLabel{}).
				Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "mapped-device", "systeminfo/architecture").
				Update("label_sync_mapping_id", storedMapping.ID).Error
			ownershipResult <- err
		}()
		close(start)
		wait.Wait()
		finalize := <-finalizeResult
		ownershipErr := <-ownershipResult
		Expect(finalize.err).NotTo(HaveOccurred())
		if ownershipErr == nil {
			Expect(finalize.finalized).To(BeFalse())
			var label model.DeviceLabel
			Expect(db.Where("org_id = ? AND device_name = ? AND label_key = ?", orgID, "mapped-device", "systeminfo/architecture").Take(&label).Error).To(Succeed())
			Expect(label.LabelSyncMappingID).To(Equal(&storedMapping.ID))
			_, err := mappingStore.Get(ctx, orgID, "architecture")
			Expect(err).NotTo(HaveOccurred())
		} else {
			Expect(errors.Is(ownershipErr, gorm.ErrForeignKeyViolated)).To(BeTrue())
			Expect(finalize.finalized).To(BeTrue())
			_, err := mappingStore.Get(ctx, orgID, "architecture")
			Expect(err).To(MatchError(ContainSubstring("resource not found")))
		}
	})
})

func newStoreCoverageLabelSyncMapping(name, key string) *api.LabelSyncMapping {
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

func pauseBeforeNextLabelSyncMappingWrite(db *gorm.DB) (<-chan struct{}, func(), func() error, error) {
	const callbackName = "test:pause-before-label-sync-mapping-write"
	writeReached := make(chan struct{})
	resume := make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	var resumeOnce sync.Once
	resumeWrite := func() {
		resumeOnce.Do(func() { close(resume) })
	}
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "label_sync_mappings" || !armed.CompareAndSwap(true, false) {
			return
		}
		close(writeReached)
		<-resume
	}); err != nil {
		return nil, nil, nil, err
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			resumeWrite()
			cleanupErr = db.Callback().Update().Remove(callbackName)
		})
		return cleanupErr
	}
	return writeReached, resumeWrite, cleanup, nil
}

func labelSyncMappingRevision(ctx context.Context, mappingStore labelsyncmappingstore.Store, orgID uuid.UUID) (int64, error) {
	snapshot, err := mappingStore.GetDeviceMappingsSnapshot(ctx, orgID)
	return snapshot.Revision, err
}
