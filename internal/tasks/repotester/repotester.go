package repotester

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type API interface {
	Test()
}

type RepoTester struct {
	log       logrus.FieldLogger
	repoStore store.Repository
}

func NewRepoTester(log logrus.FieldLogger, store store.Store) *RepoTester {
	return &RepoTester{
		log:       log,
		repoStore: store.Repository(),
	}
}

func (r *RepoTester) TestRepo() {
	reqid.OverridePrefix("repotester")
	requestID := reqid.NextRequestID()
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, r.log)

	log.Info("Running RepoTester")

	repositories, err := r.repoStore.ListIgnoreOrg()
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

		listOps := &git.ListOptions{}
		if repository.Spec.Data.Username != nil && repository.Spec.Data.Password != nil {
			listOps.Auth = &http.BasicAuth{
				Username: *repository.Spec.Data.Username,
				Password: *repository.Spec.Data.Password,
			}
		}

		_, err = remote.List(listOps)

		if repository.Status == nil {
			repository.Status = model.MakeJSONField(api.RepositoryStatus{})
		}
		err = r.setAccessCondition(log, repository.Name, repository.OrgID, repository.Status.Data, err)
		if err != nil {
			log.Errorf("Failed to update repository status for %s: %v", repository.Name, err)
		}
	}
}

func (r *RepoTester) setAccessCondition(log logrus.FieldLogger, name string, orgId uuid.UUID, repoStatus api.RepositoryStatus, err error) error {
	timestamp := util.TimeStampStringPtr()
	var lastTransitionTime *string
	previousStatus := api.Unknown
	if repoStatus.Conditions != nil && len(*repoStatus.Conditions) > 0 {
		previousStatus = (*repoStatus.Conditions)[0].Status
		lastTransitionTime = (*repoStatus.Conditions)[0].LastTransitionTime
	}
	condition := api.RepositoryCondition{
		Type:               api.Accessible,
		LastHeartbeatTime:  timestamp,
		LastTransitionTime: lastTransitionTime,
	}

	if err == nil {
		condition.Status = api.True
		condition.Reason = util.StrToPtr("Accessible")
		condition.Message = util.StrToPtr("Accessible")
	} else {
		condition.Status = api.False
		condition.Reason = util.StrToPtr("Inaccessible")
		condition.Message = util.StrToPtr(err.Error())
	}
	if previousStatus != condition.Status {
		condition.LastTransitionTime = timestamp
	}

	repo := model.Repository{
		Resource: model.Resource{OrgID: orgId, Name: name},
		Status:   model.MakeJSONField(api.RepositoryStatus{Conditions: &[]api.RepositoryCondition{condition}})}
	return r.repoStore.UpdateStatusIgnoreOrg(&repo)
}
