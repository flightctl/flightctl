package tasks_test

import (
	"context"
	"errors"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

type MockRepoTester struct {
}

func (r *MockRepoTester) TestAccess(repository *api.Repository) error {
	if repository.Metadata.Labels == nil {
		return errors.New("fail")
	}
	if v, ok := (*repository.Metadata.Labels)["status"]; ok && strings.EqualFold(v, "OK") {
		return nil
	}
	return errors.New("fail")
}

func createRepository(ctx context.Context, repostore store.Repository, log *logrus.Logger, orgId uuid.UUID, name string, labels *map[string]string) (*api.Repository, error) {
	var (
		repo *api.Repository
		err  error
	)
	spec := api.RepositorySpec{}
	err = spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url: "myrepourl",
	})
	if err != nil {
		return nil, err
	}
	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: labels,
		},
		Spec: spec,
	}

	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	repo, err = repostore.Create(ctx, orgId, &resource, callback)
	return repo, err
}

var _ = Describe("RepoTester", func() {
	var (
		log            *logrus.Logger
		ctx            context.Context
		orgId          uuid.UUID
		stores         store.Store
		serviceHandler service.Service
		cfg            *config.Config
		dbName         string
		repotestr      *tasks.RepoTester
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		stores, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		ctrl := gomock.NewController(GinkgoT())
		publisher := queues.NewMockQueueProducer(ctrl)
		publisher.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		workerClient := worker_client.NewWorkerClient(publisher, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(stores, workerClient, kvStore, nil, log, "", "", []string{})
		repotestr = tasks.NewRepoTester(log, serviceHandler)
		repotestr.TypeSpecificRepoTester = &MockRepoTester{}
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, stores, dbName)
	})

	Context("Conditions", func() {
		It("should work when setting", func() {
			var (
				err  error
				repo *api.Repository
			)
			_, err = createRepository(ctx, stores.Repository(), log, orgId, "nil-to-ok", &map[string]string{"status": "OK"})
			Expect(err).ToNot(HaveOccurred())

			_, err = createRepository(ctx, stores.Repository(), log, orgId, "ok-to-ok", &map[string]string{"status": "OK"})
			Expect(err).ToNot(HaveOccurred())
			repo, err = stores.Repository().Get(ctx, orgId, "ok-to-ok")
			Expect(err).ToNot(HaveOccurred())

			err = repotestr.SetAccessCondition(ctx, repo, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = createRepository(ctx, stores.Repository(), log, orgId, "ok-to-err", &map[string]string{"status": "fail"})
			Expect(err).ToNot(HaveOccurred())
			repo, err = stores.Repository().Get(ctx, orgId, "ok-to-err")
			Expect(err).ToNot(HaveOccurred())

			err = repotestr.SetAccessCondition(ctx, repo, nil)
			Expect(err).ToNot(HaveOccurred())

			repotestr.TestRepositories(ctx)

			repo, err = stores.Repository().Get(ctx, orgId, "nil-to-ok")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(HaveLen(1))
			Expect(repo.Status.Conditions[0].Type).To(Equal(api.ConditionTypeRepositoryAccessible))
			Expect(repo.Status.Conditions[0].Status).To(Equal(api.ConditionStatusTrue))
			Expect(repo.Status.Conditions[0].LastTransitionTime).ToNot(Equal(time.Time{}))

			repo, err = stores.Repository().Get(ctx, orgId, "ok-to-ok")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(HaveLen(1))
			Expect(repo.Status.Conditions[0].Type).To(Equal(api.ConditionTypeRepositoryAccessible))
			Expect(repo.Status.Conditions[0].Status).To(Equal(api.ConditionStatusTrue))
			Expect(repo.Status.Conditions[0].LastTransitionTime).ToNot(Equal(time.Time{}))

			repo, err = stores.Repository().Get(ctx, orgId, "ok-to-err")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(HaveLen(1))
			Expect(repo.Status.Conditions[0].Type).To(Equal(api.ConditionTypeRepositoryAccessible))
			Expect(repo.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))
			Expect(repo.Status.Conditions[0].LastTransitionTime).ToNot(Equal(time.Time{}))
		})
	})
})
