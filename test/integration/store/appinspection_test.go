package store_test

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	appinspectionservice "github.com/flightctl/flightctl/internal/service/appinspection"
	"github.com/flightctl/flightctl/internal/store"
	appinspectionstore "github.com/flightctl/flightctl/internal/store/appinspection"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

var _ = Describe("ApplicationInspectionStore", func() {
	var (
		ctx             context.Context
		cfg             *config.Config
		db              *gorm.DB
		inspections     *appinspectionstore.Store
		orgID, otherOrg uuid.UUID
		attempt         appinspectionstore.Attempt
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log := flightlog.InitLogs()
		var dbName string
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed()) })
		inspections, err = appinspectionstore.NewStore(db)
		Expect(err).NotTo(HaveOccurred())
		orgID, otherOrg = uuid.New(), uuid.New()
		orgs := organizationstore.NewOrganizationStore(db)
		Expect(testutil.CreateTestOrganization(ctx, orgs, orgID)).To(Succeed())
		Expect(testutil.CreateTestOrganization(ctx, orgs, otherOrg)).To(Succeed())
		attempt = appinspectionstore.Attempt{
			Input: model.ApplicationInspectionInput{
				Provider: model.InspectionProviderCompose, Format: model.InspectionFormatInline,
				InlineFingerprint: model.InspectionFingerprint{1},
			},
			State: model.InspectionComplete,
			References: []appinspectionstore.ReferenceInput{{
				Reference: "registry.example:5000/team/tool:stable", Role: model.InspectionRoleWorkload, Mechanism: model.InspectionPullContainer,
			}},
		}
	})

	listReferences := func(ctx context.Context, org uuid.UUID, scope appinspectionstore.ReferenceScope) ([]model.ApplicationInspectionRef, error) {
		page, err := inspections.ListReferences(ctx, org, scope, 100, nil)
		Expect(page.Next).To(BeNil(), "fixture should fit in one page")
		return page.Items, err
	}
	listUsages := func(ctx context.Context, org uuid.UUID, owner appinspectionstore.Owner) ([]model.ApplicationInspectionUsage, error) {
		page, err := inspections.ListUsages(ctx, org, owner, 100, "")
		Expect(page.Next).To(BeEmpty(), "fixture should fit in one page")
		return page.Items, err
	}

	It("When the schema already exists it should be possible to migrate again", func() {
		migrationDB, err := store.InitMigrationDB(cfg, flightlog.InitLogs())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(testdb.CloseDB, migrationDB)
		migrator, err := appinspectionstore.NewStore(migrationDB)
		Expect(err).NotTo(HaveOccurred())
		migrationCtx := store.WithBypassSpanCheck(ctx)
		Expect(migrator.InitialMigration(migrationCtx)).To(Succeed())
		schema := migrationDB.WithContext(migrationCtx).Migrator()
		Expect(schema.HasIndex(&model.ApplicationInspection{}, "inspection_cleanup_age")).To(BeTrue())
		Expect(schema.HasIndex(&model.ApplicationInspectionRef{}, "inspection_ref_scope")).To(BeTrue())
		Expect(schema.HasIndex(&model.ApplicationInspectionUsage{}, "inspection_usage_pending")).To(BeTrue())
		Expect(schema.HasIndex(&model.ApplicationInspectionUsage{}, "inspection_usage_result")).To(BeTrue())
	})

	It("When attempts finish incomplete it should retain partial facts and allow a reusable completion", func() {
		key, err := appinspectionstore.KeyForInput(orgID, attempt.Input)
		Expect(err).NotTo(HaveOccurred())
		incomplete := attempt
		incomplete.State = model.InspectionFinishedIncomplete
		incomplete.DiagnosticCodes = []model.InspectionDiagnostic{model.InspectionContentUnavailable}
		incomplete.Revision = model.InspectionFingerprint{3}
		partialID, err := inspections.RecordAttempt(ctx, orgID, incomplete)
		Expect(err).NotTo(HaveOccurred())
		otherPartialID, err := inspections.RecordAttempt(ctx, orgID, incomplete)
		Expect(err).NotTo(HaveOccurred())
		Expect(otherPartialID).NotTo(Equal(partialID))
		_, err = inspections.FindComplete(ctx, orgID, key)
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))

		completeID, err := inspections.RecordAttempt(ctx, orgID, attempt)
		Expect(err).NotTo(HaveOccurred())
		rotated := attempt
		rotated.Revision = model.InspectionFingerprint{4}
		rotated.References = nil
		Expect(inspections.RecordAttempt(ctx, orgID, rotated)).To(Equal(completeID))
		reopened, err := appinspectionstore.NewStore(db)
		Expect(err).NotTo(HaveOccurred())
		result, err := reopened.FindComplete(ctx, orgID, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Inspection.ID).To(Equal(completeID))
		Expect(result.Inspection.State).To(Equal(model.InspectionComplete))
		Expect(result.References).To(HaveLen(1))
		partial, err := reopened.GetAttempt(ctx, orgID, partialID)
		Expect(err).NotTo(HaveOccurred())
		Expect(partial.Inspection.State).To(Equal(model.InspectionFinishedIncomplete))
		Expect(partial.Inspection.Revision).To(Equal(incomplete.Revision))
		Expect(partial.Inspection.DiagnosticCodes).To(Equal(incomplete.DiagnosticCodes))
		Expect(partial.References).To(HaveLen(1))

		foreignID, err := inspections.RecordAttempt(ctx, otherOrg, rotated)
		Expect(err).NotTo(HaveOccurred())
		Expect(foreignID).NotTo(Equal(completeID))
		_, err = reopened.FindComplete(ctx, otherOrg, key)
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		foreignKey, err := appinspectionstore.KeyForInput(otherOrg, attempt.Input)
		Expect(err).NotTo(HaveOccurred())
		foreign, err := reopened.FindComplete(ctx, otherOrg, foreignKey)
		Expect(err).NotTo(HaveOccurred())
		Expect(foreign.Inspection.ID).To(Equal(foreignID))
		Expect(foreign.References).To(BeEmpty())
	})

	It("When completions race it should return one canonical result with one writer's reference set", func() {
		other := attempt
		other.References = []appinspectionstore.ReferenceInput{{Reference: "other.example/tool", Role: model.InspectionRoleVolume, Mechanism: model.InspectionPullContainer}}
		type completion struct {
			id  uuid.UUID
			err error
		}
		start := make(chan struct{})
		finished := make(chan completion, 2)
		for _, candidate := range []appinspectionstore.Attempt{attempt, other} {
			go func() {
				<-start
				id, err := inspections.RecordAttempt(ctx, orgID, candidate)
				finished <- completion{id: id, err: err}
			}()
		}
		close(start)
		first, second := <-finished, <-finished
		Expect(first.err).NotTo(HaveOccurred())
		Expect(second.err).NotTo(HaveOccurred())
		Expect(first.id).To(Equal(second.id))
		result, err := inspections.GetAttempt(ctx, orgID, first.id)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.References).To(HaveLen(1))
		Expect(result.References[0].Reference).To(BeElementOf(attempt.References[0].Reference, other.References[0].Reference))
	})

	It("When listing by registry, port and path it should include partial facts and preserve distinct roles and mechanisms", func() {
		attempt.State = model.InspectionFinishedIncomplete
		attempt.DiagnosticCodes = []model.InspectionDiagnostic{model.InspectionBudgetExceeded}
		base := attempt.References[0]
		attempt.References = append(attempt.References, base,
			appinspectionstore.ReferenceInput{Reference: base.Reference, Role: model.InspectionRoleVolume, Mechanism: base.Mechanism},
			appinspectionstore.ReferenceInput{Reference: base.Reference, Role: base.Role, Mechanism: model.InspectionPullHelmImage},
			appinspectionstore.ReferenceInput{Reference: "registry.example:6000/team/tool", Role: base.Role, Mechanism: base.Mechanism},
			appinspectionstore.ReferenceInput{Reference: "registry.example:5000/team/tool/child", Role: base.Role, Mechanism: base.Mechanism},
		)
		_, err := inspections.RecordAttempt(ctx, orgID, attempt)
		Expect(err).NotTo(HaveOccurred())
		otherID, err := inspections.RecordAttempt(ctx, otherOrg, attempt)
		Expect(err).NotTo(HaveOccurred())
		_, err = inspections.GetAttempt(ctx, orgID, otherID)
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		port := uint16(5000)
		scope := appinspectionstore.ReferenceScope{Registry: "registry.example", Port: &port, Path: []string{"team", "tool"}}
		refs, err := listReferences(ctx, orgID, scope)
		Expect(err).NotTo(HaveOccurred())
		Expect(refs).To(HaveLen(3))
		for _, ref := range refs {
			Expect(ref.OrgID).To(Equal(orgID))
			Expect(ref.Reference).To(Equal(base.Reference))
		}
		var after *appinspectionstore.ReferenceCursor
		for i, ref := range refs {
			page, err := inspections.ListReferences(ctx, orgID, scope, 1, after)
			Expect(err).NotTo(HaveOccurred())
			Expect(page.Items).To(Equal([]model.ApplicationInspectionRef{ref}))
			after = page.Next
			if i < len(refs)-1 {
				Expect(after).NotTo(BeNil())
			}
		}
		Expect(after).To(BeNil())
		for _, limit := range []int{-1, 0, appinspectionstore.MaxPageSize + 1} {
			Expect(inspections.ListReferences(ctx, orgID, scope, limit, nil)).Error().To(HaveOccurred())
		}
		scope.Port = nil
		Expect(listReferences(ctx, orgID, scope)).To(HaveLen(4))
		scope.Path = nil
		Expect(listReferences(ctx, orgID, scope)).To(HaveLen(5))
		Expect(listReferences(ctx, orgID, appinspectionstore.ReferenceScope{})).Error().To(HaveOccurred())

		secondAttempt, err := inspections.RecordAttempt(ctx, orgID, attempt)
		Expect(err).NotTo(HaveOccurred())
		firstPage, err := inspections.ListReferences(ctx, orgID, scope, 5, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(firstPage.Items).To(HaveLen(5))
		Expect(firstPage.Next).NotTo(BeNil())
		secondPage, err := inspections.ListReferences(ctx, orgID, scope, 5, firstPage.Next)
		Expect(err).NotTo(HaveOccurred())
		Expect(secondPage.Items).To(HaveLen(5))
		Expect(secondPage.Next).To(BeNil())
		Expect(firstPage.Items[0].InspectionID).NotTo(Equal(secondPage.Items[0].InspectionID))
		Expect(secondAttempt).To(BeElementOf(firstPage.Items[0].InspectionID, secondPage.Items[0].InspectionID))
	})

	createDevice := func(org uuid.UUID, name string) appinspectionstore.OwnerVersion {
		devices := devicestore.NewDeviceStore(db, flightlog.InitLogs())
		testutil.CreateTestDevice(ctx, devices, org, name, nil, nil, nil)
		device, err := devices.Get(ctx, org, name)
		Expect(err).NotTo(HaveOccurred())
		return appinspectionstore.OwnerVersion{
			Owner:      appinspectionstore.Owner{Kind: appinspectionstore.OwnerDevice, Name: name},
			Generation: lo.FromPtr(device.Metadata.Generation), CreatedAt: lo.FromPtr(device.Metadata.CreationTimestamp),
		}
	}

	makeBinding := func(application string, id uuid.UUID) appinspectionstore.UsageBinding {
		return appinspectionstore.UsageBinding{
			ApplicationContext: application, InspectionID: id, Mechanism: model.InspectionPullContainer,
		}
	}

	It("When an earlier usage is removed it should resume after the last context within the same owner and organization", func() {
		owner := createDevice(orgID, "device")
		otherOwner := createDevice(orgID, "other")
		foreignOwner := createDevice(otherOrg, "device")
		local, err := inspections.RecordAttempt(ctx, orgID, attempt)
		Expect(err).NotTo(HaveOccurred())
		foreign, err := inspections.RecordAttempt(ctx, otherOrg, attempt)
		Expect(err).NotTo(HaveOccurred())
		bindings := []appinspectionstore.UsageBinding{makeBinding("a", local), makeBinding("c", local), makeBinding("e", local)}
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, bindings)).To(BeTrue())
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, otherOwner, []appinspectionstore.UsageBinding{makeBinding("d", local)})).To(BeTrue())
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, otherOrg, foreignOwner, []appinspectionstore.UsageBinding{makeBinding("b", foreign)})).To(BeTrue())

		for _, limit := range []int{-1, 0, appinspectionstore.MaxPageSize + 1} {
			Expect(inspections.ListUsages(ctx, orgID, owner.Owner, limit, "")).Error().To(HaveOccurred())
		}
		first, err := inspections.ListUsages(ctx, orgID, owner.Owner, 1, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Items).To(HaveLen(1))
		Expect(first.Items[0].ApplicationContext).To(Equal("a"))
		Expect(first.Next).To(Equal("a"))
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, bindings[1:])).To(BeTrue())
		second, err := inspections.ListUsages(ctx, orgID, owner.Owner, 1, first.Next)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Items).To(HaveLen(1))
		Expect(second.Items[0].ApplicationContext).To(Equal("c"))
		Expect(second.Next).To(Equal("c"))
		last, err := inspections.ListUsages(ctx, orgID, owner.Owner, 1, second.Next)
		Expect(err).NotTo(HaveOccurred())
		Expect(last.Items).To(HaveLen(1))
		Expect(last.Items[0].ApplicationContext).To(Equal("e"))
		Expect(last.Next).To(BeEmpty())
	})

	It("When a device generation advances it should replace the application usages and reject stale writes even after clearing them", func() {
		owner := createDevice(orgID, "device")
		oldID, err := inspections.RecordAttempt(ctx, orgID, attempt)
		Expect(err).NotTo(HaveOccurred())
		old := []appinspectionstore.UsageBinding{makeBinding("kept", oldID), makeBinding("removed", oldID)}
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, old)).To(BeTrue())
		shared := createDevice(orgID, "shared")
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, shared, old[:1])).To(BeTrue())

		devices := devicestore.NewDeviceStore(db, flightlog.InitLogs())
		changed, _, _, err := devices.Mutate(ctx, orgID, owner.Name, nil, func(m *devicestore.DeviceMutation) error {
			if err := m.RequireExisting(); err != nil {
				return err
			}
			m.Device.Spec.Os = &domain.DeviceOsSpec{Image: "registry.example/os:next"}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		current := owner
		current.Generation = lo.FromPtr(changed.Metadata.Generation)
		Expect(current.Generation).To(BeNumerically(">", owner.Generation))

		attempt.Input.InlineFingerprint[0]++
		newID, err := inspections.RecordAttempt(ctx, orgID, attempt)
		Expect(err).NotTo(HaveOccurred())
		currentBindings := []appinspectionstore.UsageBinding{makeBinding("kept", newID)}
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, current, currentBindings)).To(BeTrue())
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, nil)).To(BeFalse())
		usages, err := listUsages(ctx, orgID, owner.Owner)
		Expect(err).NotTo(HaveOccurred())
		Expect(usages).To(HaveLen(1))
		Expect(usages[0].InspectionID).To(Equal(newID))
		Expect(usages[0].ApplicationContext).To(Equal("kept"))
		Expect(usages[0].Generation).To(Equal(current.Generation))
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, current, nil)).To(BeTrue())
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, old)).To(BeFalse())
		Expect(listUsages(ctx, orgID, owner.Owner)).To(BeEmpty())
		sharedUsages, err := listUsages(ctx, orgID, shared.Owner)
		Expect(err).NotTo(HaveOccurred())
		Expect(sharedUsages).To(HaveLen(1))
		Expect(sharedUsages[0].InspectionID).To(Equal(oldID))
	})

	It("When device names match across organizations it should scope usage links and preserve the old set on a failed replacement", func() {
		owner := createDevice(orgID, "device")
		otherOwner := createDevice(otherOrg, "device")
		local, err := inspections.RecordAttempt(ctx, orgID, attempt)
		Expect(err).NotTo(HaveOccurred())
		foreign, err := inspections.RecordAttempt(ctx, otherOrg, attempt)
		Expect(err).NotTo(HaveOccurred())
		binding := makeBinding("application", local)
		binding.User, binding.Pending, binding.ContinuationID = "app-user", true, lo.ToPtr(uuid.New())
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, []appinspectionstore.UsageBinding{binding})).To(BeTrue())
		_, err = inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, []appinspectionstore.UsageBinding{binding, binding})
		Expect(err).To(MatchError(flterrors.ErrDuplicateName))
		usages, err := listUsages(ctx, orgID, owner.Owner)
		Expect(err).NotTo(HaveOccurred())
		Expect(usages).To(HaveLen(1))
		Expect(usages[0].User).To(Equal(binding.User))
		Expect(usages[0].Mechanism).To(Equal(binding.Mechanism))
		Expect(usages[0].Pending).To(BeTrue())
		Expect(usages[0].ContinuationID).To(Equal(binding.ContinuationID))
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, otherOrg, otherOwner, []appinspectionstore.UsageBinding{makeBinding("application", foreign)})).To(BeTrue())
		Expect(store.WithTransaction(ctx, db, func(txCtx context.Context) error {
			_, err := inspections.ReplaceUsagesIfCurrent(txCtx, orgID, owner, []appinspectionstore.UsageBinding{binding, makeBinding("foreign", foreign)})
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
			Expect(listUsages(txCtx, orgID, owner.Owner)).To(HaveLen(1))
			return nil
		})).To(Succeed())
		for org, id := range map[uuid.UUID]uuid.UUID{orgID: local, otherOrg: foreign} {
			usages, err := listUsages(ctx, org, owner.Owner)
			Expect(err).NotTo(HaveOccurred())
			Expect(usages).To(HaveLen(1))
			Expect(usages[0].InspectionID).To(Equal(id))
		}
	})

	It("When a usage update contains invalid identities it should leave the current set intact", func() {
		owner := createDevice(orgID, "device")
		result, err := inspections.RecordAttempt(ctx, orgID, attempt)
		Expect(err).NotTo(HaveOccurred())
		binding := makeBinding("application", result)
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, []appinspectionstore.UsageBinding{binding})).To(BeTrue())

		invalidOwner := owner
		invalidOwner.Kind = "unknown"
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, invalidOwner, nil)).Error().To(HaveOccurred())
		Expect(listUsages(ctx, orgID, invalidOwner.Owner)).Error().To(HaveOccurred())
		Expect(listUsages(ctx, orgID, appinspectionstore.Owner{Kind: owner.Kind})).Error().To(HaveOccurred())
		invalidOwner = owner
		invalidOwner.CreatedAt = time.Time{}
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, invalidOwner, nil)).Error().To(HaveOccurred())
		invalidBinding := makeBinding("unexpected configuration text", result)
		_, err = inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, []appinspectionstore.UsageBinding{invalidBinding})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring(invalidBinding.ApplicationContext))
		usages, err := listUsages(ctx, orgID, owner.Owner)
		Expect(err).NotTo(HaveOccurred())
		Expect(usages).To(HaveLen(1))
		Expect(usages[0].ApplicationContext).To(Equal(binding.ApplicationContext))
		Expect(usages[0].InspectionID).To(Equal(result))
	})

	It("When owners are removed it should cascade usages and reject a deleted device incarnation", func() {
		devices := devicestore.NewDeviceStore(db, flightlog.InitLogs())
		fleets := fleetstore.NewFleetStore(db, flightlog.InitLogs())
		device := createDevice(orgID, "device")
		testutil.CreateTestFleet(ctx, fleets, orgID, "fleet", nil, nil)
		fleetRecord, err := fleets.Get(ctx, orgID, "fleet")
		Expect(err).NotTo(HaveOccurred())
		fleet := appinspectionstore.OwnerVersion{
			Owner:      appinspectionstore.Owner{Kind: appinspectionstore.OwnerFleet, Name: "fleet"},
			Generation: lo.FromPtr(fleetRecord.Metadata.Generation), CreatedAt: lo.FromPtr(fleetRecord.Metadata.CreationTimestamp), TemplateIdentity: "template-1",
		}
		result, err := inspections.RecordAttempt(ctx, orgID, attempt)
		Expect(err).NotTo(HaveOccurred())
		bindings := []appinspectionstore.UsageBinding{makeBinding("application", result)}
		for _, owner := range []appinspectionstore.OwnerVersion{device, fleet} {
			Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, bindings)).To(BeTrue())
		}
		usages, err := listUsages(ctx, orgID, fleet.Owner)
		Expect(err).NotTo(HaveOccurred())
		Expect(usages).To(HaveLen(1))
		Expect(usages[0].TemplateIdentity).To(Equal(fleet.TemplateIdentity))
		Expect(devices.Delete(ctx, orgID, device.Name, nil)).To(BeTrue())
		Expect(listUsages(ctx, orgID, device.Owner)).To(BeEmpty())
		_, err = inspections.ReplaceUsagesIfCurrent(ctx, orgID, device, bindings)
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		recreated := createDevice(orgID, device.Name)
		Expect(recreated.Generation).To(Equal(device.Generation))
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, device, bindings)).To(BeFalse())
		Expect(listUsages(ctx, orgID, device.Owner)).To(BeEmpty())
		Expect(listUsages(ctx, orgID, fleet.Owner)).To(HaveLen(1))
		Expect(fleets.Delete(ctx, orgID, fleet.Name, nil)).To(Succeed())
		Expect(listUsages(ctx, orgID, fleet.Owner)).To(BeEmpty())
		Expect(devices.Delete(ctx, orgID, recreated.Name, nil)).To(BeTrue())
		Expect(db.WithContext(ctx).Delete(&model.Organization{ID: orgID}).Error).To(Succeed())
		_, err = inspections.GetAttempt(ctx, orgID, result)
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		Expect(listReferences(ctx, orgID, appinspectionstore.ReferenceScope{Registry: "registry.example"})).To(BeEmpty())
	})

	recordAt := func(org uuid.UUID, fingerprint byte, createdAt time.Time) uuid.UUID {
		creator, err := appinspectionstore.NewStore(db.Session(&gorm.Session{NowFunc: func() time.Time { return createdAt }}))
		Expect(err).NotTo(HaveOccurred())
		input := attempt
		input.Input.InlineFingerprint[0] = fingerprint
		id, err := creator.RecordAttempt(ctx, org, input)
		Expect(err).NotTo(HaveOccurred())
		return id
	}

	It("When results expire it should reclaim bounded orphan batches and retain live, young and foreign results", func() {
		now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
		before := now.Add(-7 * 24 * time.Hour)
		older := before.Add(-time.Hour)
		orphan := recordAt(orgID, 1, older.Add(-time.Hour))
		secondOrphan := recordAt(orgID, 2, older)
		live := recordAt(orgID, 3, older)
		boundary := recordAt(orgID, 4, before)
		foreign := recordAt(otherOrg, 4, older)
		owner := createDevice(orgID, "device")
		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, []appinspectionstore.UsageBinding{makeBinding("application", live)})).To(BeTrue())
		_, err := inspections.DeleteExpiredUnreferenced(ctx, orgID, before, 0)
		Expect(err).To(HaveOccurred())
		Expect(inspections.DeleteExpiredUnreferenced(ctx, orgID, before, 1)).To(Equal(int64(1)))
		_, err = inspections.GetAttempt(ctx, orgID, orphan)
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		Expect(inspections.GetAttempt(ctx, orgID, secondOrphan)).Error().NotTo(HaveOccurred())

		cleaner, err := appinspectionservice.NewCleaner(inspections)
		Expect(err).NotTo(HaveOccurred())
		Expect(cleaner.Cleanup(ctx, orgID, now)).To(Equal(int64(1)))
		Expect(cleaner.Cleanup(ctx, orgID, now)).To(BeZero())
		refs, err := listReferences(ctx, orgID, appinspectionstore.ReferenceScope{Registry: "registry.example"})
		Expect(err).NotTo(HaveOccurred())
		Expect(refs).To(HaveLen(2))
		Expect([]uuid.UUID{refs[0].InspectionID, refs[1].InspectionID}).To(ConsistOf(live, boundary))
		Expect(inspections.GetAttempt(ctx, otherOrg, foreign)).Error().NotTo(HaveOccurred())

		Expect(inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, nil)).To(BeTrue())
		Expect(cleaner.Cleanup(ctx, orgID, now)).To(Equal(int64(1)))
		Expect(inspections.GetAttempt(ctx, orgID, live)).Error().To(MatchError(flterrors.ErrResourceNotFound))
	})

	It("When cleanup holds an uncommitted deletion it should read the complete previous snapshot without waiting", func() {
		cutoff := time.Now()
		id := recordAt(orgID, 1, cutoff.Add(-time.Hour))
		key, err := appinspectionstore.KeyForInput(orgID, attempt.Input)
		Expect(err).NotTo(HaveOccurred())
		expected, err := appinspectionstore.NormalizeReference(attempt.References[0])
		Expect(err).NotTo(HaveOccurred())
		expected.OrgID, expected.InspectionID = orgID, id
		Expect(store.WithTransaction(ctx, db, func(txCtx context.Context) error {
			Expect(inspections.DeleteExpiredUnreferenced(txCtx, orgID, cutoff, 1)).To(Equal(int64(1)))
			readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			// Read on a separate connection while cleanup's deletion is uncommitted.
			result, err := inspections.FindComplete(readCtx, orgID, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Inspection.ID).To(Equal(id))
			Expect(result.References).To(Equal([]model.ApplicationInspectionRef{expected}))
			return nil
		})).To(Succeed())
		Expect(inspections.GetAttempt(ctx, orgID, id)).Error().To(MatchError(flterrors.ErrResourceNotFound))
	})

	It("When cleanup and an application link overlap it should preserve a successful link and reject a reclaimed target", func() {
		cutoff := time.Now()
		attached := recordAt(orgID, 1, cutoff.Add(-time.Hour))
		reclaimed := recordAt(orgID, 2, cutoff.Add(-time.Hour))
		owner := createDevice(orgID, "device")
		Expect(store.WithTransaction(ctx, db, func(txCtx context.Context) error {
			bindings := []appinspectionstore.UsageBinding{makeBinding("current", attached)}
			Expect(inspections.ReplaceUsagesIfCurrent(txCtx, orgID, owner, bindings)).To(BeTrue())
			cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			// The independent connection overlaps the uncommitted link.
			Expect(inspections.DeleteExpiredUnreferenced(cleanupCtx, orgID, cutoff, 2)).To(Equal(int64(1)))
			return nil
		})).To(Succeed())
		_, err := inspections.ReplaceUsagesIfCurrent(ctx, orgID, owner, []appinspectionstore.UsageBinding{makeBinding("replacement", reclaimed)})
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		usages, err := listUsages(ctx, orgID, owner.Owner)
		Expect(err).NotTo(HaveOccurred())
		Expect(usages).To(HaveLen(1))
		Expect(usages[0].InspectionID).To(Equal(attached))
		Expect(inspections.DeleteExpiredUnreferenced(ctx, orgID, cutoff, 2)).To(BeZero())
	})

	It("When invalid input includes sensitive text it should reject the attempt and persist no facts", func() {
		invalid := attempt
		invalid.References = append(invalid.References, appinspectionstore.ReferenceInput{
			Reference: "https://user:secret-value@registry.example/image", Role: model.InspectionRoleWorkload, Mechanism: model.InspectionPullContainer,
		})
		_, err := inspections.RecordAttempt(ctx, orgID, invalid)
		Expect(err).To(MatchError(ContainSubstring("inspection reference 2: invalid inspection OCI reference")))
		Expect(err.Error()).NotTo(ContainSubstring("secret-value"))
		invalid = attempt
		invalid.State = "queued"
		Expect(inspections.RecordAttempt(ctx, orgID, invalid)).Error().To(HaveOccurred())
		invalid.State = model.InspectionComplete
		invalid.DiagnosticCodes = []model.InspectionDiagnostic{model.InspectionParseFailed}
		Expect(inspections.RecordAttempt(ctx, orgID, invalid)).Error().To(HaveOccurred())
		invalid.State = model.InspectionFinishedIncomplete
		invalid.DiagnosticCodes = nil
		_, err = inspections.RecordAttempt(ctx, orgID, invalid)
		Expect(err).To(HaveOccurred())
		invalid.DiagnosticCodes = []model.InspectionDiagnostic{"secret-value"}
		_, err = inspections.RecordAttempt(ctx, orgID, invalid)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("secret-value"))
		key, err := appinspectionstore.KeyForInput(orgID, attempt.Input)
		Expect(err).NotTo(HaveOccurred())
		_, err = inspections.FindComplete(ctx, orgID, key)
		Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		Expect(listReferences(ctx, orgID, appinspectionstore.ReferenceScope{Registry: "registry.example"})).To(BeEmpty())
		Expect(inspections.RecordAttempt(ctx, uuid.New(), attempt)).Error().To(MatchError(flterrors.ErrResourceNotFound))
	})
})
