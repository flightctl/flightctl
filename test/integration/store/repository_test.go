package store_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("RepositoryStore create", func() {
	var (
		log             *logrus.Logger
		ctx             context.Context
		orgId           uuid.UUID
		storeInst       store.Store
		cfg             *config.Config
		dbName          string
		numRepositories int
		callbackCalled  bool
		callback        store.RepositoryStoreCallback
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numRepositories = 3
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
		callbackCalled = false
		callback = store.RepositoryStoreCallback(func(*model.Repository) { callbackCalled = true })

		err := testutil.CreateRepositories(ctx, 3, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	Context("Repository store", func() {
		It("Get repository success", func() {
			repo, err := storeInst.Repository().Get(ctx, orgId, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*repo.Metadata.Name).To(Equal("myrepository-1"))
		})

		It("Get repository - not found error", func() {
			_, err := storeInst.Repository().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get repository - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := storeInst.Repository().Get(ctx, badOrgId, "myrepository-1")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Delete repository success", func() {
			err := storeInst.Repository().Delete(ctx, orgId, "myrepository-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
		})

		It("Delete repository success when not found", func() {
			err := storeInst.Repository().Delete(ctx, orgId, "nonexistent", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeFalse())
		})

		It("Delete all repositories in org", func() {
			otherOrgId, _ := uuid.NewUUID()
			deleteAllCallback := store.RepositoryStoreAllDeletedCallback(func(uuid.UUID) { callbackCalled = true })
			err := storeInst.Repository().DeleteAll(ctx, otherOrgId, deleteAllCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())

			listParams := store.ListParams{Limit: 1000}
			repositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(numRepositories))

			callbackCalled = false
			err = storeInst.Repository().DeleteAll(ctx, orgId, deleteAllCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())

			repositories, err = storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(0))
		})

		It("List with paging", func() {
			listParams := store.ListParams{Limit: 1000}
			allRepositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allRepositories.Items)).To(Equal(numRepositories))
			allRepoNames := make([]string, len(allRepositories.Items))
			for i, repo := range allRepositories.Items {
				allRepoNames[i] = *repo.Metadata.Name
			}

			foundRepoNames := make([]string, len(allRepositories.Items))
			listParams.Limit = 1
			repositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundRepoNames[0] = *repositories.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(repositories.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			repositories, err = storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundRepoNames[1] = *repositories.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(repositories.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			repositories, err = storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(repositories.Metadata.RemainingItemCount).To(BeNil())
			Expect(repositories.Metadata.Continue).To(BeNil())
			foundRepoNames[2] = *repositories.Items[0].Metadata.Name

			for i := range allRepoNames {
				Expect(allRepoNames[i]).To(Equal(foundRepoNames[i]))
			}
		})

		It("List with paging", func() {
			listParams := store.ListParams{
				Limit:  1000,
				Labels: map[string]string{"key": "value-1"}}
			repositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("myrepository-1"))
		})

		It("CreateOrUpdateRepository create mode", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGitGenericRepoSpec(api.GitGenericRepoSpec{
				Repo: "myrepo",
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, err := storeInst.Repository().CreateOrUpdate(ctx, orgId, &repository, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
			Expect(created).To(Equal(true))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPI))
			Expect(repo.Kind).To(Equal(model.RepositoryKind))
			repoSpec, err := repo.Spec.AsGitGenericRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Repo).To(Equal("myrepo"))
			Expect(repo.Status.Conditions).To(BeNil())
		})

		It("CreateOrUpdateRepository update mode", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGitGenericRepoSpec(api.GitGenericRepoSpec{
				Repo: "myotherrepo",
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("myrepository-1"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, err := storeInst.Repository().CreateOrUpdate(ctx, orgId, &repository, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
			Expect(created).To(Equal(false))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPI))
			Expect(repo.Kind).To(Equal(model.RepositoryKind))
			repoSpec, err := repo.Spec.AsGitGenericRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Repo).To(Equal("myotherrepo"))
			Expect(repo.Status.Conditions).To(BeNil())
		})
	})
})
