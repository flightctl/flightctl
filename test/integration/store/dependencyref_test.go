package store_test

import (
	"context"
	"strings"
	"time"

	domain "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	syncstatestore "github.com/flightctl/flightctl/internal/store/syncstate"
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

var _ = Describe("DependencyRefStore", func() {
	var (
		log                *logrus.Logger
		ctx                context.Context
		orgId              uuid.UUID
		dependencyRefStore dependencyrefstore.Store
		deviceStore        devicestore.Store
		repositoryStore    repositorystore.Store
		syncStateStore     syncstatestore.Store
		organizationStore  organizationstore.Store
		cfg                *config.Config
		dbName             string
		db                 *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		dependencyRefStore = dependencyrefstore.NewDependencyRefStore(db, log.WithField("pkg", "dependencyref-store"))
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		repositoryStore = repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		syncStateStore = syncstatestore.NewSyncStateStore(db, log.WithField("pkg", "syncstate-store"))
		organizationStore = organizationstore.NewOrganizationStore(db)

		orgId = uuid.New()
		err = testutil.CreateTestOrganization(ctx, organizationStore, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	Context("When running initial migration", func() {
		It("should create the dependency_refs table without error", func() {
			Expect(dependencyRefStore).ToNot(BeNil())
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
				ResourceKey:    "http:my-http-repo/config.json",
				FleetName:      lo.ToPtr("fleet-1"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "http",
				RepositoryName: lo.ToPtr("my-http-repo"),
				HTTPSuffix:     lo.ToPtr("/config.json"),
			}

			err := dependencyRefStore.Upsert(ctx, orgId, gitRef)
			Expect(err).ToNot(HaveOccurred())
			err = dependencyRefStore.Upsert(ctx, orgId, httpRef)
			Expect(err).ToNot(HaveOccurred())

			gitRefs, err := dependencyRefStore.ListByRefType(ctx, orgId, "git")
			Expect(err).ToNot(HaveOccurred())
			Expect(gitRefs).To(HaveLen(1))
			Expect(*gitRefs[0].RepositoryName).To(Equal("my-repo"))

			httpRefs, err := dependencyRefStore.ListByRefType(ctx, orgId, "http")
			Expect(err).ToNot(HaveOccurred())
			Expect(httpRefs).To(HaveLen(1))
			Expect(*httpRefs[0].RepositoryName).To(Equal("my-http-repo"))
		})
	})

	Context("When listing an empty result set", func() {
		It("should return an empty slice without error", func() {
			refs, err := dependencyRefStore.ListByRefType(ctx, orgId, "git")
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
			err := dependencyRefStore.Upsert(ctx, orgId, ref)
			Expect(err).ToNot(HaveOccurred())

			ref.Revision = lo.ToPtr("develop")
			err = dependencyRefStore.Upsert(ctx, orgId, ref)
			Expect(err).ToNot(HaveOccurred())

			refs, err := dependencyRefStore.ListByRefType(ctx, orgId, "git")
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
				ResourceKey:    "http:repo-b/data",
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

			Expect(dependencyRefStore.Upsert(ctx, orgId, ref1)).To(Succeed())
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref2)).To(Succeed())
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref3)).To(Succeed())

			err := dependencyRefStore.DeleteByFleet(ctx, orgId, "fleet-1")
			Expect(err).ToNot(HaveOccurred())

			gitRefs, err := dependencyRefStore.ListByRefType(ctx, orgId, "git")
			Expect(err).ToNot(HaveOccurred())
			Expect(gitRefs).To(HaveLen(1))
			Expect(*gitRefs[0].FleetName).To(Equal("fleet-2"))

			httpRefs, err := dependencyRefStore.ListByRefType(ctx, orgId, "http")
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
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())

			otherOrg := uuid.New()
			err := testutil.CreateTestOrganization(ctx, organizationStore, otherOrg)
			Expect(err).ToNot(HaveOccurred())

			refs, err := dependencyRefStore.ListByRefType(ctx, otherOrg, "git")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(BeEmpty())
		})
	})

	Context("When deleting by fleet for a non-existent fleet", func() {
		It("should succeed without error", func() {
			err := dependencyRefStore.DeleteByFleet(ctx, orgId, "nonexistent")
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
			Expect(dependencyRefStore.Upsert(ctx, orgId, fleetRef)).To(Succeed())
			Expect(dependencyRefStore.Upsert(ctx, orgId, deviceRef)).To(Succeed())

			refs, err := dependencyRefStore.ListSecretDependencyTargets(ctx, "prod", "db-creds", "sha256:new")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(HaveLen(2))
		})

		It("should return refs from multiple orgs for the same secret", func() {
			otherOrg := uuid.New()
			Expect(testutil.CreateTestOrganization(ctx, organizationStore, otherOrg)).To(Succeed())

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
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref1)).To(Succeed())
			Expect(dependencyRefStore.Upsert(ctx, otherOrg, ref2)).To(Succeed())

			refs, err := dependencyRefStore.ListSecretDependencyTargets(ctx, "prod", "db-creds", "sha256:new")
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
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())

			// Secret sync_state uses uuid.Nil as the sentinel org_id
			syncState := &model.SyncState{
				OrgID:       uuid.Nil,
				ResourceKey: "secret:prod/db-creds",
				Fingerprint: "rv1000",
			}
			Expect(syncStateStore.Set(ctx, uuid.Nil, syncState)).To(Succeed())

			// Same fingerprint — should be filtered out
			refs, err := dependencyRefStore.ListSecretDependencyTargets(ctx, "prod", "db-creds", "rv1000")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(BeEmpty())

			// Different fingerprint — should return the ref
			refs, err = dependencyRefStore.ListSecretDependencyTargets(ctx, "prod", "db-creds", "rv1001")
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
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())

			refs, err := dependencyRefStore.ListSecretDependencyTargets(ctx, "prod", "db-creds", "sha256:any")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(HaveLen(1))
			Expect(refs[0].Fingerprint).To(BeNil())
		})

		It("should return empty when no refs match", func() {
			refs, err := dependencyRefStore.ListSecretDependencyTargets(ctx, "prod", "nonexistent", "sha256:any")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(BeEmpty())
		})
	})

	Context("When listing due HTTP dependencies", func() {
		var httpRepoName string

		createHttpRepo := func(name, url string) {
			spec := domain.RepositorySpec{}
			Expect(spec.FromHttpRepoSpec(domain.HttpRepoSpec{
				Url:  url,
				Type: domain.HttpRepoSpecTypeHttp,
			})).To(Succeed())
			repo := &domain.Repository{
				ApiVersion: "v1beta1",
				Kind:       domain.RepositoryKind,
				Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name)},
				Spec:       spec,
			}
			_, err := repositoryStore.Create(ctx, orgId, repo)
			Expect(err).ToNot(HaveOccurred())
		}

		BeforeEach(func() {
			httpRepoName = "http-repo-1"
			createHttpRepo(httpRepoName, "https://example.com/config")
		})

		It("should return probes for HTTP refs that have never been checked", func() {
			ref := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "http:http-repo-1/config.json",
				FleetName:      lo.ToPtr("fleet-a"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "http",
				RepositoryName: lo.ToPtr(httpRepoName),
				HTTPSuffix:     lo.ToPtr("/config.json"),
			}
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())

			probes, err := dependencyRefStore.ListDueHttpDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(HaveLen(1))
			Expect(probes[0].RepositoryName).To(Equal(httpRepoName))
			Expect(probes[0].HTTPSuffix).To(Equal("/config.json"))
			Expect(probes[0].Fingerprint).To(BeNil())
			Expect(probes[0].FleetNames).To(ContainElement("fleet-a"))
			Expect(probes[0].DeviceNames).To(BeNil())
			Expect(probes[0].RepoSpec).ToNot(BeNil())
		})

		It("should not return probes that were recently checked", func() {
			ref := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "http:http-repo-1/config.json",
				FleetName:      lo.ToPtr("fleet-a"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "http",
				RepositoryName: lo.ToPtr(httpRepoName),
				HTTPSuffix:     lo.ToPtr("/config.json"),
			}
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())

			syncState := &model.SyncState{
				OrgID:         orgId,
				ResourceKey:   "http:http-repo-1/config.json",
				Fingerprint:   `"etag-abc"`,
				LastCheckedAt: time.Now(),
			}
			Expect(syncStateStore.Set(ctx, orgId, syncState)).To(Succeed())

			probes, err := dependencyRefStore.ListDueHttpDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(BeEmpty())
		})

		It("should return probes whose last check exceeds the poll interval", func() {
			ref := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "http:http-repo-1/config.json",
				FleetName:      lo.ToPtr("fleet-a"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "http",
				RepositoryName: lo.ToPtr(httpRepoName),
				HTTPSuffix:     lo.ToPtr("/config.json"),
			}
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())

			syncState := &model.SyncState{
				OrgID:         orgId,
				ResourceKey:   "http:http-repo-1/config.json",
				Fingerprint:   `"etag-abc"`,
				LastCheckedAt: time.Now().Add(-20 * time.Minute),
			}
			Expect(syncStateStore.Set(ctx, orgId, syncState)).To(Succeed())

			probes, err := dependencyRefStore.ListDueHttpDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(HaveLen(1))
			Expect(probes[0].Fingerprint).ToNot(BeNil())
			Expect(*probes[0].Fingerprint).To(Equal(`"etag-abc"`))
		})

		It("should aggregate multiple fleets and devices referencing the same repo+suffix", func() {
			for _, fleet := range []string{"fleet-a", "fleet-b"} {
				ref := &model.DependencyRef{
					OrgID:          orgId,
					ResourceKey:    "http:http-repo-1/config.json",
					FleetName:      lo.ToPtr(fleet),
					DeviceName:     lo.ToPtr(""),
					RefType:        "http",
					RepositoryName: lo.ToPtr(httpRepoName),
					HTTPSuffix:     lo.ToPtr("/config.json"),
				}
				Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())
			}
			deviceRef := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "http:http-repo-1/config.json",
				FleetName:      lo.ToPtr("fleet-a"),
				DeviceName:     lo.ToPtr("device-x"),
				RefType:        "http",
				RepositoryName: lo.ToPtr(httpRepoName),
				HTTPSuffix:     lo.ToPtr("/config.json"),
			}
			Expect(dependencyRefStore.Upsert(ctx, orgId, deviceRef)).To(Succeed())

			probes, err := dependencyRefStore.ListDueHttpDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(HaveLen(1))
			Expect(probes[0].FleetNames).To(ContainElements("fleet-a", "fleet-b"))
			Expect(probes[0].DeviceNames).To(ContainElement("device-x"))
		})

		It("should return separate probes for different suffixes on the same repo", func() {
			for _, suffix := range []string{"/config.json", "/data.yaml"} {
				ref := &model.DependencyRef{
					OrgID:          orgId,
					ResourceKey:    "http:http-repo-1/" + strings.TrimPrefix(suffix, "/"),
					FleetName:      lo.ToPtr("fleet-a"),
					DeviceName:     lo.ToPtr(""),
					RefType:        "http",
					RepositoryName: lo.ToPtr(httpRepoName),
					HTTPSuffix:     lo.ToPtr(suffix),
				}
				Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())
			}

			probes, err := dependencyRefStore.ListDueHttpDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(HaveLen(2))
		})

		It("should carry the repository spec for URL and auth extraction", func() {
			ref := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "http:http-repo-1/config.json",
				FleetName:      lo.ToPtr("fleet-a"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "http",
				RepositoryName: lo.ToPtr(httpRepoName),
				HTTPSuffix:     lo.ToPtr("/config.json"),
			}
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())

			probes, err := dependencyRefStore.ListDueHttpDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(HaveLen(1))
			Expect(probes[0].RepoSpec).ToNot(BeNil())

			httpSpec, err := probes[0].RepoSpec.Data.AsHttpRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(httpSpec.Url).To(Equal("https://example.com/config"))
		})

		It("should not return git refs", func() {
			gitRef := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "git:http-repo-1/main",
				FleetName:      lo.ToPtr("fleet-a"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "git",
				RepositoryName: lo.ToPtr(httpRepoName),
				Revision:       lo.ToPtr("main"),
			}
			Expect(dependencyRefStore.Upsert(ctx, orgId, gitRef)).To(Succeed())

			probes, err := dependencyRefStore.ListDueHttpDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(BeEmpty())
		})

		It("should enforce org isolation", func() {
			ref := &model.DependencyRef{
				OrgID:          orgId,
				ResourceKey:    "http:http-repo-1/config.json",
				FleetName:      lo.ToPtr("fleet-a"),
				DeviceName:     lo.ToPtr(""),
				RefType:        "http",
				RepositoryName: lo.ToPtr(httpRepoName),
				HTTPSuffix:     lo.ToPtr("/config.json"),
			}
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())

			otherOrg := uuid.New()
			Expect(testutil.CreateTestOrganization(ctx, organizationStore, otherOrg)).To(Succeed())

			probes, err := dependencyRefStore.ListDueHttpDependencies(ctx, otherOrg, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(BeEmpty())
		})

		It("should return empty when no HTTP refs exist", func() {
			probes, err := dependencyRefStore.ListDueHttpDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(BeEmpty())
		})
	})

	Context("When listing due Git dependencies", func() {
		It("should include the matching fingerprint rendered by a device", func() {
			repoName := "git-repo-1"
			spec := domain.RepositorySpec{}
			Expect(spec.FromGitRepoSpec(domain.GitRepoSpec{
				Url:  "https://example.com/repository.git",
				Type: domain.GitRepoSpecTypeGit,
			})).To(Succeed())
			_, err := repositoryStore.Create(ctx, orgId, &domain.Repository{
				ApiVersion: "v1beta1",
				Kind:       domain.RepositoryKind,
				Metadata:   domain.ObjectMeta{Name: lo.ToPtr(repoName)},
				Spec:       spec,
			})
			Expect(err).ToNot(HaveOccurred())

			oldFingerprint := "old-commit"
			oldCommaFingerprint := "old-comma-commit"
			otherFingerprint := "unrelated-fingerprint"
			deviceStatus := domain.DeviceStatus{
				DependencySync: &domain.DependencySyncStatus{ConfigRefs: &[]domain.DependencySyncConfigRefStatus{
					{ConfigProviderName: "git-config", Fingerprint: &oldFingerprint},
					{ConfigProviderName: "team,blue", Fingerprint: &oldCommaFingerprint},
					{ConfigProviderName: "other-config", Fingerprint: &otherFingerprint},
				}},
			}
			_, err = deviceStore.Create(ctx, orgId, &domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("device-x"), Owner: lo.ToPtr("Fleet/fleet-a")},
				Status:   &deviceStatus,
			}, nil)
			Expect(err).ToNot(HaveOccurred())

			currentFingerprint := "current-commit"
			currentCommaFingerprint := "current-comma-commit"
			_, err = deviceStore.Create(ctx, orgId, &domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("device-y"), Owner: lo.ToPtr("Fleet/fleet-a")},
				Status: &domain.DeviceStatus{DependencySync: &domain.DependencySyncStatus{ConfigRefs: &[]domain.DependencySyncConfigRefStatus{
					{ConfigProviderName: "git-config", Fingerprint: &currentFingerprint},
					{ConfigProviderName: "team,blue", Fingerprint: &currentCommaFingerprint},
				}}},
			}, nil)
			Expect(err).ToNot(HaveOccurred())

			Expect(dependencyRefStore.Upsert(ctx, orgId, &model.DependencyRef{
				OrgID:               orgId,
				ResourceKey:         "git:" + repoName + "/main",
				FleetName:           lo.ToPtr("fleet-a"),
				DeviceName:          lo.ToPtr(""),
				RefType:             "git",
				RepositoryName:      lo.ToPtr(repoName),
				Revision:            lo.ToPtr("main"),
				ConfigProviderName:  "git-config",
				ConfigProviderNames: model.StringArray{"git-config", "team,blue"},
			})).To(Succeed())

			probes, err := dependencyRefStore.ListDueGitDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(HaveLen(1))
			Expect(probes[0].RepositoryName).To(Equal(repoName))
			Expect(probes[0].Revision).To(Equal("main"))
			Expect(probes[0].FleetNames).To(ConsistOf("fleet-a"))
			Expect(probes[0].DeviceNames).To(BeEmpty())
			Expect(probes[0].RenderedTargets).NotTo(BeNil())
			Expect(probes[0].RenderedTargets.Data).To(ConsistOf(
				model.GitDependencyRenderedTarget{FleetName: "fleet-a", DeviceName: "device-x", Fingerprint: "old-commit", DeviceLevel: false},
				model.GitDependencyRenderedTarget{FleetName: "fleet-a", DeviceName: "device-x", Fingerprint: "old-comma-commit", DeviceLevel: false},
				model.GitDependencyRenderedTarget{FleetName: "fleet-a", DeviceName: "device-y", Fingerprint: "current-commit", DeviceLevel: false},
				model.GitDependencyRenderedTarget{FleetName: "fleet-a", DeviceName: "device-y", Fingerprint: "current-comma-commit", DeviceLevel: false},
			))
		})

		It("should include the matching fingerprint rendered by a direct device ref", func() {
			repoName := "direct-git-repo"
			spec := domain.RepositorySpec{}
			Expect(spec.FromGitRepoSpec(domain.GitRepoSpec{
				Url:  "https://example.com/direct-repository.git",
				Type: domain.GitRepoSpecTypeGit,
			})).To(Succeed())
			_, err := repositoryStore.Create(ctx, orgId, &domain.Repository{
				ApiVersion: "v1beta1",
				Kind:       domain.RepositoryKind,
				Metadata:   domain.ObjectMeta{Name: lo.ToPtr(repoName)},
				Spec:       spec,
			})
			Expect(err).ToNot(HaveOccurred())

			fingerprint := "direct-old-commit"
			_, err = deviceStore.Create(ctx, orgId, &domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("standalone-device")},
				Status: &domain.DeviceStatus{DependencySync: &domain.DependencySyncStatus{ConfigRefs: &[]domain.DependencySyncConfigRefStatus{
					{ConfigProviderName: "direct-git-config", Fingerprint: &fingerprint},
				}}},
			}, nil)
			Expect(err).ToNot(HaveOccurred())

			Expect(dependencyRefStore.Upsert(ctx, orgId, &model.DependencyRef{
				OrgID:               orgId,
				ResourceKey:         "git:" + repoName + "/main",
				FleetName:           lo.ToPtr(""),
				DeviceName:          lo.ToPtr("standalone-device"),
				RefType:             "git",
				RepositoryName:      lo.ToPtr(repoName),
				Revision:            lo.ToPtr("main"),
				ConfigProviderName:  "direct-git-config",
				ConfigProviderNames: model.StringArray{"direct-git-config"},
			})).To(Succeed())

			probes, err := dependencyRefStore.ListDueGitDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(HaveLen(1))
			Expect(probes[0].DeviceNames).To(ConsistOf("standalone-device"))
			Expect(probes[0].RenderedTargets.Data).To(ConsistOf(model.GitDependencyRenderedTarget{
				DeviceName: "standalone-device", Fingerprint: "direct-old-commit", DeviceLevel: true,
			}))
		})

		It("should skip rendered targets after a fingerprint has been stored", func() {
			repoName := "stored-fingerprint-repo"
			spec := domain.RepositorySpec{}
			Expect(spec.FromGitRepoSpec(domain.GitRepoSpec{
				Url:  "https://example.com/stored-fingerprint-repository.git",
				Type: domain.GitRepoSpecTypeGit,
			})).To(Succeed())
			_, err := repositoryStore.Create(ctx, orgId, &domain.Repository{
				ApiVersion: "v1beta1",
				Kind:       domain.RepositoryKind,
				Metadata:   domain.ObjectMeta{Name: lo.ToPtr(repoName)},
				Spec:       spec,
			})
			Expect(err).ToNot(HaveOccurred())

			fingerprint := "stored-commit"
			_, err = deviceStore.Create(ctx, orgId, &domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("stored-fingerprint-device")},
				Status: &domain.DeviceStatus{DependencySync: &domain.DependencySyncStatus{ConfigRefs: &[]domain.DependencySyncConfigRefStatus{
					{ConfigProviderName: "stored-config", Fingerprint: &fingerprint},
				}}},
			}, nil)
			Expect(err).ToNot(HaveOccurred())

			resourceKey := "git:" + repoName + "/main"
			Expect(dependencyRefStore.Upsert(ctx, orgId, &model.DependencyRef{
				OrgID:               orgId,
				ResourceKey:         resourceKey,
				FleetName:           lo.ToPtr(""),
				DeviceName:          lo.ToPtr("stored-fingerprint-device"),
				RefType:             "git",
				RepositoryName:      lo.ToPtr(repoName),
				Revision:            lo.ToPtr("main"),
				ConfigProviderName:  "stored-config",
				ConfigProviderNames: model.StringArray{"stored-config"},
			})).To(Succeed())
			Expect(syncStateStore.Set(ctx, orgId, &model.SyncState{
				ResourceKey:   resourceKey,
				Fingerprint:   fingerprint,
				LastCheckedAt: time.Now().Add(-time.Hour),
			})).To(Succeed())

			probes, err := dependencyRefStore.ListDueGitDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(HaveLen(1))
			Expect(probes[0].RenderedTargets).NotTo(BeNil())
			Expect(probes[0].RenderedTargets.Data).To(BeEmpty())
		})

		It("should exclude recently checked first-seen refs from due probes", func() {
			repoName := "recently-checked-repo"
			resourceKey := "git:" + repoName + "/main"
			fingerprint := ""
			_, err := deviceStore.Create(ctx, orgId, &domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("recently-checked-device")},
				Status: &domain.DeviceStatus{DependencySync: &domain.DependencySyncStatus{ConfigRefs: &[]domain.DependencySyncConfigRefStatus{
					{ConfigProviderName: "recent-config", Fingerprint: &fingerprint},
				}}},
			}, nil)
			Expect(err).ToNot(HaveOccurred())

			Expect(dependencyRefStore.Upsert(ctx, orgId, &model.DependencyRef{
				OrgID:               orgId,
				ResourceKey:         resourceKey,
				FleetName:           lo.ToPtr(""),
				DeviceName:          lo.ToPtr("recently-checked-device"),
				RefType:             "git",
				RepositoryName:      lo.ToPtr(repoName),
				Revision:            lo.ToPtr("main"),
				ConfigProviderName:  "recent-config",
				ConfigProviderNames: model.StringArray{"recent-config"},
			})).To(Succeed())
			Expect(syncStateStore.Set(ctx, orgId, &model.SyncState{
				ResourceKey:   resourceKey,
				Fingerprint:   "",
				LastCheckedAt: time.Now(),
			})).To(Succeed())

			probes, err := dependencyRefStore.ListDueGitDependencies(ctx, orgId, 15*time.Minute)
			Expect(err).ToNot(HaveOccurred())
			Expect(probes).To(BeEmpty())
		})
	})

	Context("When storing ConfigProviderName", func() {
		It("should round-trip the config provider name", func() {
			ref := &model.DependencyRef{
				OrgID:               orgId,
				ResourceKey:         "git:my-repo/main",
				FleetName:           lo.ToPtr("fleet-1"),
				DeviceName:          lo.ToPtr(""),
				RefType:             "git",
				RepositoryName:      lo.ToPtr("my-repo"),
				Revision:            lo.ToPtr("main"),
				ConfigProviderName:  "nginx-config",
				ConfigProviderNames: model.StringArray{"nginx-config"},
			}
			Expect(dependencyRefStore.Upsert(ctx, orgId, ref)).To(Succeed())

			refs, err := dependencyRefStore.ListByRefType(ctx, orgId, "git")
			Expect(err).ToNot(HaveOccurred())
			Expect(refs).To(HaveLen(1))
			Expect(refs[0].ConfigProviderName).To(Equal("nginx-config"))
			Expect(refs[0].ConfigProviderNames).To(ConsistOf("nginx-config"))
		})
	})

})
