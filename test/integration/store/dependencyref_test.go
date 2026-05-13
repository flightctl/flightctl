package store_test

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var _ = Describe("DependencyRefStore", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		cfg       *config.Config
		dbName    string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)

		orgId = uuid.New()
		err := testutil.CreateTestOrganization(ctx, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
	})

	Context("When running initial migration", func() {
		It("should create the dependency_refs table without error", func() {
			Expect(storeInst.DependencyRef()).ToNot(BeNil())
		})
	})

	Context("When upserting and listing by ref type", func() {
		It("should return only refs matching the requested type", func() {
			gitRef := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "git:my-repo/main",
				FleetName:      lo.ToPtr("fleet-1"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "git",
				RepositoryName: lo.ToPtr("my-repo"),
				Revision:       lo.ToPtr("main"),
			}
			httpRef := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "http:my-http-repo//config.json",
				FleetName:      lo.ToPtr("fleet-1"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "http",
				RepositoryName: lo.ToPtr("my-http-repo"),
				HTTPSuffix:     lo.ToPtr("/config.json"),
			}

			err := storeInst.DependencyRef().Upsert(ctx, orgId, gitRef)
			Expect(err).ToNot(HaveOccurred())
			err = storeInst.DependencyRef().Upsert(ctx, orgId, httpRef)
			Expect(err).ToNot(HaveOccurred())

			gitRefs, err := storeInst.DependencyRef().ListByRefType(ctx, orgId, "git")
			Expect(err).ToNot(HaveOccurred())
			Expect(gitRefs).To(HaveLen(1))
			Expect(*gitRefs[0].RepositoryName).To(Equal("my-repo"))

			httpRefs, err := storeInst.DependencyRef().ListByRefType(ctx, orgId, "http")
			Expect(err).ToNot(HaveOccurred())
			Expect(httpRefs).To(HaveLen(1))
			Expect(*httpRefs[0].RepositoryName).To(Equal("my-http-repo"))
		})
	})

	Context("When listing an empty result set", func() {
		It("should return an empty slice without error", func() {
			refs, err := storeInst.DependencyRef().ListByRefType(ctx, orgId, "git")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(BeEmpty())
		})
	})

	Context("When upserting an existing ref", func() {
		It("should update the existing row", func() {
			ref := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "git:my-repo/main",
				FleetName:      lo.ToPtr("fleet-1"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "git",
				RepositoryName: lo.ToPtr("my-repo"),
				Revision:       lo.ToPtr("main"),
			}
			err := storeInst.DependencyRef().Upsert(ctx, orgId, ref)
			Expect(err).ToNot(HaveOccurred())

			ref.Revision = lo.ToPtr("develop")
			err = storeInst.DependencyRef().Upsert(ctx, orgId, ref)
			Expect(err).ToNot(HaveOccurred())

			refs, err := storeInst.DependencyRef().ListByRefType(ctx, orgId, "git")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(HaveLen(1))
			Expect(*refs[0].Revision).To(Equal("develop"))
		})
	})

	Context("When deleting refs by fleet", func() {
		It("should remove all refs for that fleet and leave others", func() {
			ref1 := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "git:repo-a/main",
				FleetName:      lo.ToPtr("fleet-1"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "git",
				RepositoryName: lo.ToPtr("repo-a"),
				Revision:       lo.ToPtr("main"),
			}
			ref2 := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "http:repo-b//data",
				FleetName:      lo.ToPtr("fleet-1"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "http",
				RepositoryName: lo.ToPtr("repo-b"),
				HTTPSuffix:     lo.ToPtr("/data"),
			}
			ref3 := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "git:repo-c/main",
				FleetName:      lo.ToPtr("fleet-2"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "git",
				RepositoryName: lo.ToPtr("repo-c"),
				Revision:       lo.ToPtr("main"),
			}

			Expect(storeInst.DependencyRef().Upsert(ctx, orgId, ref1)).To(Succeed())
			Expect(storeInst.DependencyRef().Upsert(ctx, orgId, ref2)).To(Succeed())
			Expect(storeInst.DependencyRef().Upsert(ctx, orgId, ref3)).To(Succeed())

			err := storeInst.DependencyRef().DeleteByFleet(ctx, orgId, "fleet-1")
			Expect(err).ToNot(HaveOccurred())

			gitRefs, err := storeInst.DependencyRef().ListByRefType(ctx, orgId, "git")
			Expect(err).ToNot(HaveOccurred())
			Expect(gitRefs).To(HaveLen(1))
			Expect(*gitRefs[0].FleetName).To(Equal("fleet-2"))

			httpRefs, err := storeInst.DependencyRef().ListByRefType(ctx, orgId, "http")
			Expect(err).ToNot(HaveOccurred())
			Expect(httpRefs).To(BeEmpty())
		})
	})

	Context("When querying with org isolation", func() {
		It("should not return refs from a different org", func() {
			ref := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "git:my-repo/main",
				FleetName:      lo.ToPtr("fleet-1"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "git",
				RepositoryName: lo.ToPtr("my-repo"),
				Revision:       lo.ToPtr("main"),
			}
			Expect(storeInst.DependencyRef().Upsert(ctx, orgId, ref)).To(Succeed())

			otherOrg := uuid.New()
			err := testutil.CreateTestOrganization(ctx, storeInst, otherOrg)
			Expect(err).ToNot(HaveOccurred())

			refs, err := storeInst.DependencyRef().ListByRefType(ctx, otherOrg, "git")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(BeEmpty())
		})
	})

	Context("When deleting by fleet for a non-existent fleet", func() {
		It("should succeed without error", func() {
			err := storeInst.DependencyRef().DeleteByFleet(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When listing secret dependency targets", func() {
		It("should return matching fleet and device refs", func() {
			fleetRef := &model.DependencyRef{
				OrgID:           orgId,
				ResourceKey:     "secret:prod/db-creds",
				FleetName:       lo.ToPtr("fleet-a"),
				DeviceName:      lo.ToPtr(""),
				RefType:         "secret",
				SecretName:      lo.ToPtr("db-creds"),
				SecretNamespace: lo.ToPtr("prod"),
			}
			deviceRef := &model.DependencyRef{
				OrgID:           orgId,
				ResourceKey:     "secret:prod/db-creds",
				FleetName:       lo.ToPtr("fleet-a"),
				DeviceName:      lo.ToPtr("device-x"),
				RefType:         "secret",
				SecretName:      lo.ToPtr("db-creds"),
				SecretNamespace: lo.ToPtr("prod"),
			}
			Expect(storeInst.DependencyRef().Upsert(ctx, orgId, fleetRef)).To(Succeed())
			Expect(storeInst.DependencyRef().Upsert(ctx, orgId, deviceRef)).To(Succeed())

			refs, err := storeInst.DependencyRef().ListSecretDependencyTargets(ctx, "prod", "db-creds", "sha256:new")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(HaveLen(2))
		})

		It("should return refs from multiple orgs for the same secret", func() {
			otherOrg := uuid.New()
			Expect(testutil.CreateTestOrganization(ctx, storeInst, otherOrg)).To(Succeed())

			ref1 := &model.DependencyRef{
				OrgID:           orgId,
				ResourceKey:     "secret:prod/db-creds",
				FleetName:       lo.ToPtr("fleet-a"),
				DeviceName:      lo.ToPtr(""),
				RefType:         "secret",
				SecretName:      lo.ToPtr("db-creds"),
				SecretNamespace: lo.ToPtr("prod"),
			}
			ref2 := &model.DependencyRef{
				OrgID:           otherOrg,
				ResourceKey:     "secret:prod/db-creds",
				FleetName:       lo.ToPtr("fleet-b"),
				DeviceName:      lo.ToPtr(""),
				RefType:         "secret",
				SecretName:      lo.ToPtr("db-creds"),
				SecretNamespace: lo.ToPtr("prod"),
			}
			Expect(storeInst.DependencyRef().Upsert(ctx, orgId, ref1)).To(Succeed())
			Expect(storeInst.DependencyRef().Upsert(ctx, otherOrg, ref2)).To(Succeed())

			refs, err := storeInst.DependencyRef().ListSecretDependencyTargets(ctx, "prod", "db-creds", "sha256:new")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(HaveLen(2))

			orgIDs := []uuid.UUID{refs[0].OrgID, refs[1].OrgID}
			Expect(orgIDs).To(ContainElements(orgId, otherOrg))
		})

		It("should filter out refs whose stored fingerprint matches newFingerprint", func() {
			ref := &model.DependencyRef{
				OrgID:           orgId,
				ResourceKey:     "secret:prod/db-creds",
				FleetName:       lo.ToPtr("fleet-a"),
				DeviceName:      lo.ToPtr(""),
				RefType:         "secret",
				SecretName:      lo.ToPtr("db-creds"),
				SecretNamespace: lo.ToPtr("prod"),
			}
			Expect(storeInst.DependencyRef().Upsert(ctx, orgId, ref)).To(Succeed())

			// Secret sync_state uses uuid.Nil as the sentinel org_id
			syncState := &model.SyncState{
				OrgID:       uuid.Nil,
				ResourceKey: "secret:prod/db-creds",
				Fingerprint: "rv1000",
			}
			Expect(storeInst.SyncState().Set(ctx, uuid.Nil, syncState)).To(Succeed())

			// Same fingerprint — should be filtered out
			refs, err := storeInst.DependencyRef().ListSecretDependencyTargets(ctx, "prod", "db-creds", "rv1000")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(BeEmpty())

			// Different fingerprint — should return the ref
			refs, err = storeInst.DependencyRef().ListSecretDependencyTargets(ctx, "prod", "db-creds", "rv1001")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(HaveLen(1))
			Expect(refs[0].Fingerprint).ToNot(BeNil())
			Expect(*refs[0].Fingerprint).To(Equal("rv1000"))
		})

		It("should return refs with nil fingerprint when no sync_state exists (first seen)", func() {
			ref := &model.DependencyRef{
				OrgID:           orgId,
				ResourceKey:     "secret:prod/db-creds",
				FleetName:       lo.ToPtr("fleet-a"),
				DeviceName:      lo.ToPtr(""),
				RefType:         "secret",
				SecretName:      lo.ToPtr("db-creds"),
				SecretNamespace: lo.ToPtr("prod"),
			}
			Expect(storeInst.DependencyRef().Upsert(ctx, orgId, ref)).To(Succeed())

			refs, err := storeInst.DependencyRef().ListSecretDependencyTargets(ctx, "prod", "db-creds", "sha256:any")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(HaveLen(1))
			Expect(refs[0].Fingerprint).To(BeNil())
		})

		It("should return empty when no refs match", func() {
			refs, err := storeInst.DependencyRef().ListSecretDependencyTargets(ctx, "prod", "nonexistent", "sha256:any")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(BeEmpty())
		})
	})
})
