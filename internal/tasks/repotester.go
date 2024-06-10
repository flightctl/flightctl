package tasks

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/sirupsen/logrus"
)

type API interface {
	Test()
}

type RepoTester struct {
	log                    logrus.FieldLogger
	repoStore              store.Repository
	TypeSpecificRepoTester TypeSpecificRepoTester
}

func NewRepoTester(log logrus.FieldLogger, store store.Store) *RepoTester {
	return &RepoTester{
		log:                    log,
		repoStore:              store.Repository(),
		TypeSpecificRepoTester: &GitRepoTester{},
	}
}

func (r *RepoTester) TestRepositories() {
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

	for i := range repositories {
		repository := repositories[i]
		accessErr := r.TypeSpecificRepoTester.TestAccess(&repository)

		err := r.SetAccessCondition(repository, accessErr)
		if err != nil {
			log.Errorf("Failed to update repository status for %s: %v", repository.Name, err)
		}
	}
}

type TypeSpecificRepoTester interface {
	TestAccess(repository *model.Repository) error
}

type GitRepoTester struct {
}

func (r *GitRepoTester) TestAccess(repository *model.Repository) error {
	if repository.Spec == nil {
		return fmt.Errorf("repository has no spec")
	}
	repoURL, err := repository.Spec.Data.GetRepoURL()
	if err != nil {
		return err
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name:  repository.Name,
		URLs:  []string{repoURL},
		Fetch: []config.RefSpec{"HEAD"},
	})

	listOps := &git.ListOptions{}
	auth, err := GetAuth(repository)
	if err != nil {
		return err
	}

	listOps.Auth = auth
	_, err = remote.List(listOps)
	return err
}

func (r *RepoTester) SetAccessCondition(repository model.Repository, err error) error {
	if repository.Status == nil {
		repository.Status = model.MakeJSONField(api.RepositoryStatus{Conditions: &[]api.Condition{}})
	}
	if repository.Status.Data.Conditions == nil {
		repository.Status.Data.Conditions = &[]api.Condition{}
	}
	changed := api.SetStatusConditionByError(repository.Status.Data.Conditions, api.RepositoryAccessible, "Accessible", "Inaccessible", err)
	if changed {
		return r.repoStore.UpdateStatusIgnoreOrg(&repository)
	}
	return nil
}
