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
})
