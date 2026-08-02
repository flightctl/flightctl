package store_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	canaryservice "github.com/flightctl/flightctl/internal/service/canary"
	"github.com/flightctl/flightctl/internal/store"
	canarystore "github.com/flightctl/flightctl/internal/store/canary"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
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

var _ = Describe("Encryption key rotation", func() {
	var (
		log             *logrus.Logger
		ctx             context.Context
		orgId           uuid.UUID
		cfg             *config.Config
		dbName          string
		db              *gorm.DB
		repositoryStore repositorystore.Store
		v1Strategy      *encryption.V1Strategy
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())

		repositoryStore = repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		organizationStore := organizationstore.NewOrganizationStore(db)
		orgId = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationStore, orgId)).To(Succeed())

		v1Strategy = addEncryptionKey("rot-rotated")
	})

	AfterEach(func() {
		if v1Strategy != nil {
			Expect(v1Strategy.SetActiveKey("default")).To(Succeed())
		}
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	createRepoWithPassword := func(name, password string) *api.Repository {
		GinkgoHelper()
		spec := api.RepositorySpec{}
		Expect(spec.FromHttpRepoSpec(api.HttpRepoSpec{
			Url:  "https://example.com/" + name + ".git",
			Type: api.HttpRepoSpecTypeHttp,
			HttpConfig: &api.HttpConfig{
				Username: lo.ToPtr("user"),
				Password: &password,
			},
		})).To(Succeed())

		repo := &api.Repository{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr(name),
			},
			Spec: spec,
		}
		callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
		created, err := repositoryStore.Create(ctx, orgId, repo, callback)
		Expect(err).ToNot(HaveOccurred())
		return created
	}

	readRawSpec := func(name string) string {
		GinkgoHelper()
		var raw struct {
			Spec string
		}
		Expect(db.WithContext(ctx).
			Table("repositories").
			Select("spec").
			Where("org_id = ? AND name = ?", orgId, name).
			Scan(&raw).Error).To(Succeed())
		return raw.Spec
	}

	It("When key rotates and repository is updated it should re-encrypt with the new key", func() {
		createRepoWithPassword("rotation-test", "my-secret-password")

		rawBefore := readRawSpec("rotation-test")
		Expect(rawBefore).To(ContainSubstring("enc:v1:default:"))

		Expect(v1Strategy.SetActiveKey("rot-rotated")).To(Succeed())

		repo, err := repositoryStore.Get(ctx, orgId, "rotation-test")
		Expect(err).ToNot(HaveOccurred())
		callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
		_, err = repositoryStore.Update(ctx, orgId, repo, callback)
		Expect(err).ToNot(HaveOccurred())

		rawAfter := readRawSpec("rotation-test")
		Expect(rawAfter).To(ContainSubstring("enc:v1:rot-rotated:"))
		Expect(rawAfter).ToNot(ContainSubstring("enc:v1:default:"))

		updatedRepo, err := repositoryStore.Get(ctx, orgId, "rotation-test")
		Expect(err).ToNot(HaveOccurred())
		httpSpec, err := updatedRepo.Spec.AsHttpRepoSpec()
		Expect(err).ToNot(HaveOccurred())
		Expect(decryptString(*httpSpec.HttpConfig.Password)).To(Equal("my-secret-password"))
	})

	It("When key rotates old-key data should still decrypt", func() {
		createRepoWithPassword("old-key-test", "old-key-secret")

		Expect(v1Strategy.SetActiveKey("rot-rotated")).To(Succeed())

		repo, err := repositoryStore.Get(ctx, orgId, "old-key-test")
		Expect(err).ToNot(HaveOccurred())
		httpSpec, err := repo.Spec.AsHttpRepoSpec()
		Expect(err).ToNot(HaveOccurred())
		Expect(decryptString(*httpSpec.HttpConfig.Password)).To(Equal("old-key-secret"))
	})

	It("When ProcessEncryption is called on old-key data it should re-encrypt with new key", func() {
		createRepoWithPassword("process-test", "process-secret")

		Expect(v1Strategy.SetActiveKey("rot-rotated")).To(Succeed())

		oldEncrypted := readStoredRepoPassword(db, ctx, orgId, "process-test")
		Expect(oldEncrypted).To(ContainSubstring("enc:v1:default:"))

		mgr := encryption.GlobalManager()
		reencrypted, err := mgr.ProcessEncryption(ctx, []byte(oldEncrypted))
		Expect(err).ToNot(HaveOccurred())
		Expect(string(reencrypted)).To(ContainSubstring("enc:v1:rot-rotated:"))

		decrypted, err := mgr.Decrypt(ctx, reencrypted)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(decrypted)).To(Equal("process-secret"))
	})

	It("When key rotates canaries should exist for both old and new keys", func() {
		mgr := encryption.GlobalManager()
		cs := canarystore.NewCanaryStore(db, log.WithField("pkg", "canary-store"))
		svc := canaryservice.NewServiceHandler(cs)
		mgr.SetCanaryStore(canaryservice.AsEncryptionStore(svc))
		defer mgr.SetCanaryStore(nil)

		createRepoWithPassword("canary-test", "canary-secret")

		oldCanary, err := cs.Get(ctx, "v1", "default")
		Expect(err).ToNot(HaveOccurred())
		Expect(oldCanary).ToNot(BeNil())

		Expect(v1Strategy.SetActiveKey("rot-rotated")).To(Succeed())

		repo, err := repositoryStore.Get(ctx, orgId, "canary-test")
		Expect(err).ToNot(HaveOccurred())
		callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
		_, err = repositoryStore.Update(ctx, orgId, repo, callback)
		Expect(err).ToNot(HaveOccurred())

		newCanary, err := cs.Get(ctx, "v1", "rot-rotated")
		Expect(err).ToNot(HaveOccurred())
		Expect(newCanary).ToNot(BeNil())

		allCanaries, err := cs.List(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(allCanaries).To(HaveLen(2))
	})

	It("When data is already on active key ProcessEncryption should return it unchanged", func() {
		createRepoWithPassword("idempotent-test", "same-key-secret")

		encrypted := readStoredRepoPassword(db, ctx, orgId, "idempotent-test")

		mgr := encryption.GlobalManager()
		result, err := mgr.ProcessEncryption(ctx, []byte(encrypted))
		Expect(err).ToNot(HaveOccurred())
		Expect(string(result)).To(Equal(encrypted))
	})
})
