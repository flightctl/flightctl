package store_test

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store"
	checkpointstore "github.com/flightctl/flightctl/internal/store/checkpoint"
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
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("Encryption migration", func() {
	var (
		log    *logrus.Logger
		ctx    context.Context
		orgId  uuid.UUID
		cfg    *config.Config
		dbName string
		db     *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())

		orgId = uuid.New()
		organizationStore := organizationstore.NewOrganizationStore(db)
		Expect(testutil.CreateTestOrganization(ctx, organizationStore, orgId)).To(Succeed())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	seedPlaintextRepository := func(name, password string) {
		var spec domain.RepositorySpec
		Expect(spec.FromGitRepoSpec(domain.GitRepoSpec{
			Url: "https://example.com/" + name + ".git",
			HttpConfig: &domain.HttpConfig{
				Username: lo.ToPtr("user"),
				Password: lo.ToPtr(password),
			},
		})).To(Succeed())
		raw, err := json.Marshal(spec)
		Expect(err).ToNot(HaveOccurred())

		Expect(db.WithContext(ctx).Exec(
			`INSERT INTO repositories (org_id, name, generation, resource_version, created_at, updated_at, spec)
			 VALUES (?, ?, 1, 1, NOW(), NOW(), ?::jsonb)`,
			orgId, name, string(raw),
		).Error).To(Succeed())
	}

	seedPlaintextAuthProvider := func(name, secret string) {
		var assignment domain.AuthOrganizationAssignment
		Expect(assignment.FromAuthStaticOrganizationAssignment(domain.AuthStaticOrganizationAssignment{
			Type:             domain.AuthStaticOrganizationAssignmentTypeStatic,
			OrganizationName: "default-org",
		})).To(Succeed())
		var roleAssignment domain.AuthRoleAssignment
		Expect(roleAssignment.FromAuthStaticRoleAssignment(domain.AuthStaticRoleAssignment{
			Type:  domain.AuthStaticRoleAssignmentTypeStatic,
			Roles: []string{domain.ExternalRoleViewer},
		})).To(Succeed())

		var spec domain.AuthProviderSpec
		Expect(spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
			ProviderType:           domain.Oidc,
			Issuer:                 "https://issuer.example.com/" + name,
			ClientId:               "client-" + name,
			ClientSecret:           secret,
			OrganizationAssignment: assignment,
			RoleAssignment:         roleAssignment,
		})).To(Succeed())
		raw, err := json.Marshal(spec)
		Expect(err).ToNot(HaveOccurred())

		Expect(db.WithContext(ctx).Exec(
			`INSERT INTO auth_providers (org_id, name, generation, resource_version, created_at, updated_at, spec)
			 VALUES (?, ?, 1, 1, NOW(), NOW(), ?::jsonb)`,
			orgId, name, string(raw),
		).Error).To(Succeed())
	}

	newMigrator := func() *tasks.EncryptionMigrator {
		cp := checkpointstore.NewCheckpointStore(db, log.WithField("pkg", "checkpoint-store"))
		migrator := tasks.NewEncryptionMigrator(db, encryption.GlobalManager(), cp, nil, log)
		migrator.SetBatchSize(1)
		return migrator
	}

	runUntilComplete := func(migrator *tasks.EncryptionMigrator, kind string, org uuid.UUID) {
		Eventually(func(g Gomega) {
			report, err := migrator.RunBatch(ctx, kind, org)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(report.Complete).To(BeTrue())
		}).WithPolling(50 * time.Millisecond).WithTimeout(5 * time.Second).Should(Succeed())
	}

	It("When Repository and AuthProvider specs are plaintext it should migrate them to the active key", func() {
		seedPlaintextRepository("repo-plain", "repo-secret")
		seedPlaintextAuthProvider("ap-plain", "auth-secret")
		migrator := newMigrator()

		runUntilComplete(migrator, domain.RepositoryKind, orgId)
		runUntilComplete(migrator, domain.AuthProviderKind, orgId)

		var repo model.Repository
		Expect(db.WithContext(ctx).First(&repo, "org_id = ? AND name = ?", orgId, "repo-plain").Error).To(Succeed())
		repoJSON, err := json.Marshal(repo.Spec.Data)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(repoJSON)).ToNot(ContainSubstring("repo-secret"))
		Expect(string(repoJSON)).To(ContainSubstring("enc:v1:default:"))

		var ap model.AuthProvider
		Expect(db.WithContext(ctx).First(&ap, "org_id = ? AND name = ?", orgId, "ap-plain").Error).To(Succeed())
		apJSON, err := json.Marshal(ap.Spec.Data)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(apJSON)).ToNot(ContainSubstring("auth-secret"))
		Expect(string(apJSON)).To(ContainSubstring("enc:v1:default:"))

		work, err := migrator.IncompleteWork(ctx)
		Expect(err).ToNot(HaveOccurred())
		for _, item := range work {
			Expect(item.OrgID).ToNot(Equal(orgId), "migrated org should not remain incomplete")
		}
	})

	It("When interrupted mid-scan it should resume from the checkpoint", func() {
		seedPlaintextRepository("repo-a", "secret-a")
		seedPlaintextRepository("repo-b", "secret-b")
		migrator := newMigrator()

		report, err := migrator.RunBatch(ctx, domain.RepositoryKind, orgId)
		Expect(err).ToNot(HaveOccurred())
		Expect(report.Complete).To(BeFalse())
		Expect(report.Updated).To(Equal(1))

		migrator = newMigrator()
		runUntilComplete(migrator, domain.RepositoryKind, orgId)

		for _, name := range []string{"repo-a", "repo-b"} {
			var repo model.Repository
			Expect(db.WithContext(ctx).First(&repo, "org_id = ? AND name = ?", orgId, name).Error).To(Succeed())
			raw, err := json.Marshal(repo.Spec.Data)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring("enc:v1:default:"))
		}
	})

	It("When the active key target changes it should reset a completed checkpoint", func() {
		seedPlaintextRepository("repo-reset", "reset-secret")
		migrator := newMigrator()
		runUntilComplete(migrator, domain.RepositoryKind, orgId)

		cp := checkpointstore.NewCheckpointStore(db, log.WithField("pkg", "checkpoint-store"))
		stale, err := json.Marshal(tasks.EncryptionMigrationCheckpoint{
			TargetActiveKeyID: "stale-key",
			Complete:          true,
			LastName:          "repo-reset",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(cp.Set(ctx, "encryption-migration", "Repository:"+orgId.String(), stale)).To(Succeed())

		needs, err := migrator.NeedsMigration(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(needs).To(BeTrue())

		report, err := migrator.RunBatch(ctx, domain.RepositoryKind, orgId)
		Expect(err).ToNot(HaveOccurred())
		Expect(report.ActiveKeyID).To(Equal("default"))
		Expect(report.Complete).To(BeFalse())
	})

	It("When two workers contend for an org lease only one acquires it", func() {
		locker := tasks.NewPostgresEncryptionMigrationLocker(db)
		key := domain.RepositoryKind + ":" + orgId.String()
		unlock1, ok1, err := locker.TryLock(ctx, key)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok1).To(BeTrue())

		_, ok2, err := locker.TryLock(ctx, key)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok2).To(BeFalse())

		Expect(unlock1()).To(Succeed())

		unlock3, ok3, err := locker.TryLock(ctx, key)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok3).To(BeTrue())
		Expect(unlock3()).To(Succeed())
	})

	It("When multiple orgs exist it should migrate each independently", func() {
		orgB := uuid.New()
		organizationStore := organizationstore.NewOrganizationStore(db)
		Expect(testutil.CreateTestOrganization(ctx, organizationStore, orgB)).To(Succeed())

		seedPlaintextRepository("repo-a", "secret-a")

		var spec domain.RepositorySpec
		Expect(spec.FromGitRepoSpec(domain.GitRepoSpec{
			Url: "https://example.com/repo-b.git",
			HttpConfig: &domain.HttpConfig{
				Username: lo.ToPtr("user"),
				Password: lo.ToPtr("secret-b"),
			},
		})).To(Succeed())
		raw, err := json.Marshal(spec)
		Expect(err).ToNot(HaveOccurred())
		Expect(db.WithContext(ctx).Exec(
			`INSERT INTO repositories (org_id, name, generation, resource_version, created_at, updated_at, spec)
			 VALUES (?, ?, 1, 1, NOW(), NOW(), ?::jsonb)`,
			orgB, "repo-b", string(raw),
		).Error).To(Succeed())

		migrator := newMigrator()
		runUntilComplete(migrator, domain.RepositoryKind, orgId)
		runUntilComplete(migrator, domain.RepositoryKind, orgB)

		for _, item := range []struct {
			org  uuid.UUID
			name string
		}{
			{orgId, "repo-a"},
			{orgB, "repo-b"},
		} {
			var repo model.Repository
			Expect(db.WithContext(ctx).First(&repo, "org_id = ? AND name = ?", item.org, item.name).Error).To(Succeed())
			body, err := json.Marshal(repo.Spec.Data)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("enc:v1:default:"))
		}
	})
})
