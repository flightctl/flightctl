package tasks_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
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
	err = spec.FromGitRepoSpec(api.GitRepoSpec{
		Url:  "myrepourl",
		Type: api.GitRepoSpecTypeGit,
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

func createOciRepository(ctx context.Context, repostore store.Repository, orgId uuid.UUID, name string, registry string, scheme *api.OciRepoSpecScheme, username, password *string) (*api.Repository, error) {
	ociSpec := api.OciRepoSpec{
		Registry: registry,
		Type:     api.OciRepoSpecTypeOci,
		Scheme:   scheme,
	}
	if username != nil && password != nil {
		auth := &api.OciAuth{}
		_ = auth.FromDockerAuth(api.DockerAuth{
			Username: *username,
			Password: *password,
		})
		ociSpec.OciAuth = auth
	}
	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(ociSpec)
	if err != nil {
		return nil, err
	}
	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
	}

	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	return repostore.Create(ctx, orgId, &resource, callback)
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
		repotestr = tasks.NewRepoTester(log, serviceHandler, func(repository *api.Repository) tasks.TypeSpecificRepoTester {
			return &MockRepoTester{}
		})
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

			err = repotestr.SetAccessCondition(ctx, orgId, repo, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = createRepository(ctx, stores.Repository(), log, orgId, "ok-to-err", &map[string]string{"status": "fail"})
			Expect(err).ToNot(HaveOccurred())
			repo, err = stores.Repository().Get(ctx, orgId, "ok-to-err")
			Expect(err).ToNot(HaveOccurred())

			err = repotestr.SetAccessCondition(ctx, orgId, repo, nil)
			Expect(err).ToNot(HaveOccurred())

			repotestr.TestRepositories(ctx, orgId)

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

	Context("OCI Repository Conditions", func() {
		It("should set accessible condition to true when OCI registry is reachable", func() {
			// Create a mock OCI registry that succeeds with anonymous access
			mock := &MockOciRegistry{
				RequireAuth:    true,
				AnonymousToken: "test-token-12345",
			}
			server := httptest.NewServer(mock.Handler())
			defer server.Close()
			mock.AuthServerURL = server.URL

			// Create OCI repository pointing to mock server
			_, err := createOciRepository(ctx, stores.Repository(), orgId, "oci-accessible", registryHost(server.URL), lo.ToPtr(api.Http), nil, nil)
			Expect(err).ToNot(HaveOccurred())

			// Create a new repotester without mock (uses real OciRepoTester)
			ociRepotester := tasks.NewRepoTester(log, serviceHandler, nil)
			ociRepotester.TestRepositories(ctx, orgId)

			// Verify condition is set correctly
			repo, err := stores.Repository().Get(ctx, orgId, "oci-accessible")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status).ToNot(BeNil())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(HaveLen(1))
			Expect(repo.Status.Conditions[0].Type).To(Equal(api.ConditionTypeRepositoryAccessible))
			Expect(repo.Status.Conditions[0].Status).To(Equal(api.ConditionStatusTrue))
		})

		It("should set accessible condition to false when OCI registry is not reachable", func() {
			// Create a mock OCI registry that fails (no anonymous token configured)
			mock := &MockOciRegistry{
				RequireAuth: true,
				// No AnonymousToken means anonymous access will fail
			}
			server := httptest.NewServer(mock.Handler())
			defer server.Close()
			mock.AuthServerURL = server.URL

			// Create OCI repository pointing to mock server
			_, err := createOciRepository(ctx, stores.Repository(), orgId, "oci-not-accessible", registryHost(server.URL), lo.ToPtr(api.Http), nil, nil)
			Expect(err).ToNot(HaveOccurred())

			// Create a new repotester without mock (uses real OciRepoTester)
			ociRepotester := tasks.NewRepoTester(log, serviceHandler, nil)
			ociRepotester.TestRepositories(ctx, orgId)

			// Verify condition is set correctly
			repo, err := stores.Repository().Get(ctx, orgId, "oci-not-accessible")
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.Status).ToNot(BeNil())
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(HaveLen(1))
			Expect(repo.Status.Conditions[0].Type).To(Equal(api.ConditionTypeRepositoryAccessible))
			Expect(repo.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))
		})
	})
})
