package store_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("RepositoryStore create", func() {
	var (
		log             *logrus.Logger
		ctx             context.Context
		orgId           uuid.UUID
		storeInst       store.Store
		cfg             *config.Config
		dbName          string
		db              *gorm.DB
		numRepositories int
		callbackCalled  bool
		callback        store.RepositoryStoreCallback
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numRepositories = 3
		storeInst, cfg, dbName, db = store.PrepareDBForUnitTests(log)
		callbackCalled = false
		callback = store.RepositoryStoreCallback(func(uuid.UUID, *api.Repository, *api.Repository) { callbackCalled = true })

		err := testutil.CreateRepositories(ctx, 3, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())

		nilrepo := model.Repository{Resource: model.Resource{OrgID: orgId, Name: "nilspec"}}
		result := db.Create(&nilrepo)
		Expect(result.Error).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
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

		It("Get repository - nil spec - not found error", func() {
			_, err := storeInst.Repository().Get(ctx, orgId, "nilspec")
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

		It("Delete repository success when nil spec", func() {
			err := storeInst.Repository().Delete(ctx, orgId, "nilspec", callback)
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
			// Delete the repo with nilspec so it doesn't interfere with the counts
			nilrepo := model.Repository{Resource: model.Resource{OrgID: orgId, Name: "nilspec"}}
			result := db.Delete(&nilrepo)
			Expect(result.Error).ToNot(HaveOccurred())

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

		It("List with labels", func() {
			listParams := store.ListParams{
				Limit:         1000,
				LabelSelector: selector.NewLabelSelectorFromMapOrDie(map[string]string{"key": "value-1"})}
			repositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("myrepository-1"))
		})

		It("CreateOrUpdateRepository create mode", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
				Url:  "myrepo",
				Type: "git",
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("newresourcename"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, _, err := storeInst.Repository().CreateOrUpdate(ctx, orgId, &repository, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
			Expect(created).To(Equal(true))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))
			repoSpec, err := repo.Spec.AsGenericRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Url).To(Equal("myrepo"))
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(BeEmpty())
		})

		It("CreateOrUpdateRepository update mode", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
				Url:  "myotherrepo",
				Type: "git",
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("myrepository-1"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, _, err := storeInst.Repository().CreateOrUpdate(ctx, orgId, &repository, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
			Expect(created).To(Equal(false))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))
			repoSpec, err := repo.Spec.AsGenericRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Url).To(Equal("myotherrepo"))
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(BeEmpty())
		})

		It("CreateOrUpdateRepository create nilspec", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
				Url:  "myotherrepo",
				Type: "git",
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("nilspec"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, _, err := storeInst.Repository().CreateOrUpdate(ctx, orgId, &repository, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
			Expect(created).To(Equal(true))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))
			repoSpec, err := repo.Spec.AsGenericRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Url).To(Equal("myotherrepo"))
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(BeEmpty())
		})

		It("Delete repo with fleet association", func() {
			testutil.CreateTestFleets(ctx, 1, storeInst.Fleet(), orgId, "myfleet", false, nil)

			err := storeInst.Fleet().OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = storeInst.Repository().Delete(ctx, orgId, "myrepository-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
		})

		It("Delete repo with device association", func() {
			testutil.CreateTestDevices(ctx, 1, storeInst.Device(), orgId, nil, false)

			err := storeInst.Device().OverwriteRepositoryRefs(ctx, orgId, "mydevice-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Device().GetRepositoryRefs(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = storeInst.Repository().Delete(ctx, orgId, "myrepository-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
		})

		It("Delete all repos with associations", func() {
			testutil.CreateTestFleets(ctx, 1, storeInst.Fleet(), orgId, "myfleet", false, nil)
			testutil.CreateTestDevices(ctx, 1, storeInst.Device(), orgId, nil, false)

			err := storeInst.Fleet().OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = storeInst.Device().OverwriteRepositoryRefs(ctx, orgId, "mydevice-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err = storeInst.Device().GetRepositoryRefs(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			deleteAllCallback := store.RepositoryStoreAllDeletedCallback(func(uuid.UUID) { callbackCalled = true })
			err = storeInst.Repository().DeleteAll(ctx, orgId, deleteAllCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
		})
	})
})
