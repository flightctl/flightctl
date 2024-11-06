package tasks

import (
	"context"
	"fmt"
	"io"
	"net/http"

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
		log:       log,
		repoStore: store.Repository(),
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

		repoSpec, _ := repository.Spec.Data.GetGenericRepoSpec()
		if repoSpec.Type == "http" {
			log.Info("Detected HTTP repository type")
			r.TypeSpecificRepoTester = &HttpRepoTester{}
		} else {
			log.Info("Defaulting to Git repository type")
			r.TypeSpecificRepoTester = &GitRepoTester{}
		}

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

type HttpRepoTester struct {
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

func (r *HttpRepoTester) TestAccess(repository *model.Repository) error {
	if repository.Spec == nil {
		return fmt.Errorf("repository has no spec")
	}

	repoHttpSpec, err := repository.Spec.Data.GetHttpRepoSpec()
	if err != nil {
		return fmt.Errorf("failed to get HTTP repo spec: %w", err)
	}

	repoURL := repoHttpSpec.Url
	// Append the validationSuffix if it exists
	if repoHttpSpec.ValidationSuffix != nil {
		repoURL += *repoHttpSpec.ValidationSuffix
	}

	req, err := http.NewRequest("GET", repoURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req, tlsConfig, err := buildHttpRepoRequestAuth(repoHttpSpec, req)
	if err != nil {
		return fmt.Errorf("error building request authentication: %w", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	_, err = io.ReadAll(resp.Body)
	return err
}

func (r *RepoTester) SetAccessCondition(repository model.Repository, err error) error {
	if repository.Status == nil {
		repository.Status = model.MakeJSONField(api.RepositoryStatus{Conditions: []api.Condition{}})
	}
	if repository.Status.Data.Conditions == nil {
		repository.Status.Data.Conditions = []api.Condition{}
	}
	changed := api.SetStatusConditionByError(&repository.Status.Data.Conditions, api.RepositoryAccessible, "Accessible", "Inaccessible", err)
	if changed {
		return r.repoStore.UpdateStatusIgnoreOrg(&repository)
	}
	return nil
}
