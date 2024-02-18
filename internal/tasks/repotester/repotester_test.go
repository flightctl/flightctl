package repotester

import (
	"context"
	"errors"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RepoTester Suite")
}

type UnitTestRepoTester struct {
}

func (r *UnitTestRepoTester) testAccess(repository *model.Repository) error {
	if repository.Labels[0] == "status=OK" {
		return nil
	}
	return errors.New("fail")
}

func createRepository(ctx context.Context, repostore store.Repository, orgId uuid.UUID, name string, labels *map[string]string) error {
	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name:   util.StrToPtr(name),
			Labels: labels,
		},
		Spec: api.RepositorySpec{
			Repo: util.StrToPtr("myrepourl"),
		},
	}

	_, err := repostore.Create(ctx, orgId, &resource)
	return err
}

var _ = Describe("RepoTester", func() {
	var (
		log        *logrus.Logger
		ctx        context.Context
		orgId      uuid.UUID
		stores     store.Store
		cfg        *config.Config
		dbName     string
		repotester *RepoTester
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		stores, cfg, dbName = store.PrepareDBForUnitTests(log)
		repotester = NewRepoTester(log, stores)
		repotester.typeSpecificRepoTester = &UnitTestRepoTester{}
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, stores, dbName)
	})

	Context("RepoTester", func() {
		It("Set conditions", func() {
			err := createRepository(ctx, repotester.repoStore, orgId, "nil-to-ok", &map[string]string{"status": "OK"})
			Expect(err).ToNot(HaveOccurred())

			err = createRepository(ctx, repotester.repoStore, orgId, "ok-to-ok", &map[string]string{"status": "OK"})
			Expect(err).ToNot(HaveOccurred())
			repo, err := stores.Repository().Get(ctx, orgId, "ok-to-ok")
			Expect(err).ToNot(HaveOccurred())
			repoModel := model.NewRepositoryFromApiResource(repo)
			err = repotester.setAccessCondition(log, *repoModel, nil)
			Expect(err).ToNot(HaveOccurred())

			err = createRepository(ctx, repotester.repoStore, orgId, "ok-to-err", &map[string]string{"status": "fail"})
			Expect(err).ToNot(HaveOccurred())
			repo, err = stores.Repository().Get(ctx, orgId, "ok-to-err")
			Expect(err).ToNot(HaveOccurred())
			repoModel = model.NewRepositoryFromApiResource(repo)
			err = repotester.setAccessCondition(log, *repoModel, nil)
			Expect(err).ToNot(HaveOccurred())

			repotester.TestRepositories()

			repo, err = repotester.repoStore.Get(ctx, orgId, "nil-to-ok")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(len(*(repo.Status.Conditions))).To(Equal(1))
			cond := (*repo.Status.Conditions)[0]
			Expect(cond.Type).To(Equal(api.Accessible))
			Expect(cond.Status).To(Equal(api.True))
			Expect(cond.LastTransitionTime).ToNot(BeNil())

			repo, err = repotester.repoStore.Get(ctx, orgId, "ok-to-ok")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(len(*(repo.Status.Conditions))).To(Equal(1))
			cond = (*repo.Status.Conditions)[0]
			Expect(cond.Type).To(Equal(api.Accessible))
			Expect(cond.Status).To(Equal(api.True))
			Expect(cond.LastTransitionTime).ToNot(BeNil())

			repo, err = repotester.repoStore.Get(ctx, orgId, "ok-to-err")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(len(*(repo.Status.Conditions))).To(Equal(1))
			cond = (*repo.Status.Conditions)[0]
			Expect(cond.Type).To(Equal(api.Accessible))
			Expect(cond.Status).To(Equal(api.False))
			Expect(cond.LastTransitionTime).ToNot(BeNil())
		})
	})
})
