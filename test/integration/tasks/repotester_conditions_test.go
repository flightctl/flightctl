package tasks_test

import (
	"context"
	"errors"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

type MockRepoTester struct {
}

func (r *MockRepoTester) TestAccess(repository *model.Repository) error {
	if repository.Labels[0] == "status=OK" {
		return nil
	}
	return errors.New("fail")
}

func createRepository(ctx context.Context, repostore store.Repository, orgId uuid.UUID, name string, labels *map[string]string) error {
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url: "myrepourl",
	})
	if err != nil {
		return err
	}
	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name:   util.StrToPtr(name),
			Labels: labels,
		},
		Spec: spec,
	}

	callback := store.RepositoryStoreCallback(func(*model.Repository) {})
	_, err = repostore.Create(ctx, orgId, &resource, callback)
	return err
}

var _ = Describe("RepoTester", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		stores    store.Store
		cfg       *config.Config
		dbName    string
		repotestr *tasks.RepoTester
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		stores, cfg, dbName, _ = store.PrepareDBForUnitTests(log)
		repotestr = tasks.NewRepoTester(log, stores)
		repotestr.TypeSpecificRepoTester = &MockRepoTester{}
	})

	AfterEach(func() {
		store.DeleteTestDB(log, cfg, stores, dbName)
	})

	Context("Conditions", func() {
		It("should work when setting", func() {
			err := createRepository(ctx, stores.Repository(), orgId, "nil-to-ok", &map[string]string{"status": "OK"})
			Expect(err).ToNot(HaveOccurred())

			err = createRepository(ctx, stores.Repository(), orgId, "ok-to-ok", &map[string]string{"status": "OK"})
			Expect(err).ToNot(HaveOccurred())
			repo, err := stores.Repository().Get(ctx, orgId, "ok-to-ok")
			Expect(err).ToNot(HaveOccurred())

			repoModel, err := model.NewRepositoryFromApiResource(repo)
			Expect(err).ToNot(HaveOccurred())
			err = repotestr.SetAccessCondition(*repoModel, nil)
			Expect(err).ToNot(HaveOccurred())

			err = createRepository(ctx, stores.Repository(), orgId, "ok-to-err", &map[string]string{"status": "fail"})
			Expect(err).ToNot(HaveOccurred())
			repo, err = stores.Repository().Get(ctx, orgId, "ok-to-err")
			Expect(err).ToNot(HaveOccurred())

			repoModel, err = model.NewRepositoryFromApiResource(repo)
			Expect(err).ToNot(HaveOccurred())
			err = repotestr.SetAccessCondition(*repoModel, nil)
			Expect(err).ToNot(HaveOccurred())

			repotestr.TestRepositories()

			repo, err = stores.Repository().Get(ctx, orgId, "nil-to-ok")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(HaveLen(1))
			Expect(repo.Status.Conditions[0].Type).To(Equal(api.RepositoryAccessible))
			Expect(repo.Status.Conditions[0].Status).To(Equal(api.ConditionStatusTrue))
			Expect(repo.Status.Conditions[0].LastTransitionTime).ToNot(Equal(time.Time{}))

			repo, err = stores.Repository().Get(ctx, orgId, "ok-to-ok")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(HaveLen(1))
			Expect(repo.Status.Conditions[0].Type).To(Equal(api.RepositoryAccessible))
			Expect(repo.Status.Conditions[0].Status).To(Equal(api.ConditionStatusTrue))
			Expect(repo.Status.Conditions[0].LastTransitionTime).ToNot(Equal(time.Time{}))

			repo, err = stores.Repository().Get(ctx, orgId, "ok-to-err")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(HaveLen(1))
			Expect(repo.Status.Conditions[0].Type).To(Equal(api.RepositoryAccessible))
			Expect(repo.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))
			Expect(repo.Status.Conditions[0].LastTransitionTime).ToNot(Equal(time.Time{}))
		})
	})
})
