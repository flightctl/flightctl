package repotester

import (
	"context"
	"errors"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RepoTester Suite")
}

func createRepository(ctx context.Context, repostore service.RepositoryStoreInterface, orgId uuid.UUID) error {
	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name: util.StrToPtr("myrepo"),
		},
		Spec: api.RepositorySpec{
			Repo: util.StrToPtr("myrepourl"),
		},
	}

	_, err := repostore.CreateRepository(ctx, orgId, &resource)
	return err
}

var _ = Describe("RepoTester", func() {
	var (
		log        *logrus.Logger
		ctx        context.Context
		orgId      uuid.UUID
		db         *gorm.DB
		cfg        *config.Config
		dbName     string
		repotester *RepoTester
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		var stores *store.Store
		db, stores, cfg, dbName = store.PrepareDBForUnitTests(log)
		repotester = NewRepoTester(log, db, stores)
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, db, dbName)
	})

	Context("RepoTester", func() {
		It("Set conditions", func() {
			err := createRepository(ctx, repotester.repoStore, orgId)
			Expect(err).ToNot(HaveOccurred())

			repo, err := repotester.repoStore.GetRepository(ctx, orgId, "myrepo")
			Expect(err).ToNot(HaveOccurred())
			repoModel := model.NewRepositoryFromApiResource(repo)

			// Nil -> OK
			repotester.setAccessCondition(log, repoModel, nil)
			repo, err = repotester.repoStore.GetRepository(ctx, orgId, "myrepo")
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
			(*repoModel.Status.Data.Conditions)[0].LastHeartbeatTime = oldTime
			(*repoModel.Status.Data.Conditions)[0].LastTransitionTime = oldTime
			repotester.setAccessCondition(log, repoModel, nil)
			repo, err = repotester.repoStore.GetRepository(ctx, orgId, "myrepo")
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
			(*repoModel.Status.Data.Conditions)[0].LastHeartbeatTime = oldTime
			(*repoModel.Status.Data.Conditions)[0].LastTransitionTime = oldTime
			repotester.setAccessCondition(log, repoModel, errors.New("something bad"))
			repo, err = repotester.repoStore.GetRepository(ctx, orgId, "myrepo")
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
