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

func createRepository(ctx context.Context, repostore store.Repository, orgId uuid.UUID) error {
	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name: util.StrToPtr("myrepo"),
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
		stores, cfg, dbName, _ = store.PrepareDBForUnitTests(log)
		repotester = NewRepoTester(log, stores)
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, stores, dbName)
	})

	Context("RepoTester", func() {
		It("Set conditions", func() {
			err := createRepository(ctx, repotester.repoStore, orgId)
			Expect(err).ToNot(HaveOccurred())

			repo, err := repotester.repoStore.Get(ctx, orgId, "myrepo")
			Expect(err).ToNot(HaveOccurred())
			repoModel := model.NewRepositoryFromApiResource(repo)

			// Nil -> OK
			err = repotester.setAccessCondition(log, "myrepo", orgId, repoModel.Status.Data, nil)
			Expect(err).ToNot(HaveOccurred())
			repo, err = repotester.repoStore.Get(ctx, orgId, "myrepo")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(len(*(repo.Status.Conditions))).To(Equal(1))
			cond := (*repo.Status.Conditions)[0]
			Expect(cond.Type).To(Equal(api.Accessible))
			Expect(cond.Status).To(Equal(api.True))
			Expect(cond.LastHeartbeatTime).ToNot(BeNil())
			Expect(cond.LastTransitionTime).ToNot(BeNil())

			// OK -> OK
			oldTime := util.StrToPtr("some old time")
			repoModel.Status = model.MakeJSONField(*repo.Status)
			(*repoModel.Status.Data.Conditions)[0].LastHeartbeatTime = oldTime
			(*repoModel.Status.Data.Conditions)[0].LastTransitionTime = oldTime
			err = repotester.setAccessCondition(log, "myrepo", orgId, repoModel.Status.Data, nil)
			Expect(err).ToNot(HaveOccurred())
			repo, err = repotester.repoStore.Get(ctx, orgId, "myrepo")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(len(*(repo.Status.Conditions))).To(Equal(1))
			cond = (*repo.Status.Conditions)[0]
			Expect(cond.Type).To(Equal(api.Accessible))
			Expect(cond.Status).To(Equal(api.True))
			Expect(cond.LastHeartbeatTime).ToNot(BeNil())
			Expect(*cond.LastHeartbeatTime).ToNot(Equal(*oldTime))
			Expect(cond.LastTransitionTime).ToNot(BeNil())
			Expect(*cond.LastTransitionTime).To(Equal(*oldTime))

			// OK -> Not OK
			err = repotester.setAccessCondition(log, "myrepo", orgId, repoModel.Status.Data, errors.New("something bad"))
			Expect(err).ToNot(HaveOccurred())
			repo, err = repotester.repoStore.Get(ctx, orgId, "myrepo")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(len(*(repo.Status.Conditions))).To(Equal(1))
			cond = (*repo.Status.Conditions)[0]
			Expect(cond.Type).To(Equal(api.Accessible))
			Expect(cond.Status).To(Equal(api.False))
			Expect(*cond.Message).To(Equal("something bad"))
			Expect(cond.LastHeartbeatTime).ToNot(BeNil())
			Expect(*cond.LastHeartbeatTime).ToNot(Equal(*oldTime))
			Expect(cond.LastTransitionTime).ToNot(BeNil())
			Expect(*cond.LastTransitionTime).ToNot(Equal(*oldTime))
		})
	})
})
