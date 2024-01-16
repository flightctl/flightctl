package repotester

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type API interface {
	Test()
}

type RepoTester struct {
	log       logrus.FieldLogger
	db        *gorm.DB
	repoStore service.RepositoryStoreInterface
}

func NewRepoTester(log logrus.FieldLogger, db *gorm.DB, store *store.Store) *RepoTester {
	return &RepoTester{
		log:       log,
		db:        db,
		repoStore: store.GetRepositoryStore(),
	}
}

func (r *RepoTester) TestRepo() {
	reqid.OverridePrefix("repotester")
	requestID := reqid.NextRequestID()
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, r.log)

	log.Info("Running RepoTester")

	repositories, err := r.repoStore.ListAllRepositoriesInternal()
	if err != nil {
		log.Errorf("error fetching repositories: %s", err)
		return
	}

	for _, repository := range repositories {
		remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
			Name:  repository.Name,
			URLs:  []string{*repository.Spec.Data.Repo},
			Fetch: []config.RefSpec{"HEAD"},
		})

		_, err = remote.List(&git.ListOptions{
			Auth: &http.BasicAuth{
				Username: *repository.Spec.Data.Username,
				Password: *repository.Spec.Data.Password,
			},
		})

		r.setAccessCondition(log, &repository, err)
	}
}

func (r *RepoTester) setAccessCondition(log logrus.FieldLogger, repository *model.Repository, err error) {
	var condition api.RepositoryCondition
	if err == nil {
		condition = api.RepositoryCondition{
			Type:               api.Accessible,
			LastTransitionTime: util.TimeStampStringPtr(),
			Status:             api.True,
			Reason:             util.StrToPtr("Accessible"),
			Message:            util.StrToPtr("Accessible"),
		}
	} else {
		condition = api.RepositoryCondition{
			Type:               api.Accessible,
			LastTransitionTime: util.TimeStampStringPtr(),
			Status:             api.False,
			Reason:             util.StrToPtr("Inaccessible"),
			Message:            util.StrToPtr(err.Error()),
		}
		log.Debugf("repository %s inaccessible: %s", repository.Name, err)
	}

	status := api.RepositoryStatus{
		Conditions: &[]api.RepositoryCondition{condition},
	}
	repository.Status = model.MakeJSONField(status)

	r.repoStore.UpdateRepositoryStatusInternal(repository)
}
