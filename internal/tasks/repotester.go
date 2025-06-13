package tasks

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type RepoTester struct {
	log            logrus.FieldLogger
	serviceHandler RepoTesterService
}

type RepoTesterService interface {
	ListAllOrganizationIDs(ctx context.Context) ([]uuid.UUID, api.Status)
	ListRepositories(ctx context.Context, params api.ListRepositoriesParams) (*api.RepositoryList, api.Status)
	ReplaceRepositoryStatus(ctx context.Context, name string, repository api.Repository) (*api.Repository, api.Status)
}

func NewRepoTester(log logrus.FieldLogger, serviceHandler service.Service) *RepoTester {
	return &RepoTester{
		log:            log,
		serviceHandler: serviceHandler,
	}
}

func (r *RepoTester) TestRepositories(ctx context.Context) {
	reqid.OverridePrefix("repotester")
	requestID := reqid.NextRequestID()
	ctx = context.WithValue(ctx, middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, r.log)

	log.Info("Running RepoTester")

	organizationIDs, status := r.serviceHandler.ListAllOrganizationIDs(ctx)
	if status.Code != 200 {
		log.Errorf("error fetching organizations: %s", status.Message)
		return
	}

	for _, orgID := range organizationIDs {
		r.TestRepositoriesForOrganization(ctx, log, orgID)
	}
}

func (r *RepoTester) TestRepositoriesForOrganization(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	ctx = util.WithOrganizationID(ctx, orgID)
	log = log.WithField("organization", orgID)

	limit := int32(ItemsPerPage)
	continueToken := (*string)(nil)
	for {
		repositories, status := r.serviceHandler.ListRepositories(ctx, api.ListRepositoriesParams{
			Limit:    &limit,
			Continue: continueToken,
		})
		if status.Code != 200 {
			log.Errorf("error fetching repositories: %s", status.Message)
			return
		}

		for i := range repositories.Items {
			repository := repositories.Items[i]

			repoSpec, err := repository.Spec.GetGenericRepoSpec()
			if err != nil {
				log.Errorf("failed to get generic repo spec for %s: %v", *repository.Metadata.Name, err)
				continue
			}

			typeSpecificRepoTester, err := GetRepoTesterForType(log, repoSpec.Type)
			if err != nil {
				log.Errorf("failed to get repo tester for type %s: %v", repoSpec.Type, err)
				continue
			}

			accessErr := typeSpecificRepoTester.TestAccess(&repository)

			err = r.SetAccessCondition(ctx, &repository, accessErr)
			if err != nil {
				log.Errorf("failed to update repository status for %s: %v", *repository.Metadata.Name, err)
			}
		}

		continueToken = repositories.Metadata.Continue
		if continueToken == nil {
			break
		}
	}
}

// Assigned as a var to allow for easy mocking in tests
var GetRepoTesterForType = func(log logrus.FieldLogger, repoType api.RepoSpecType) (TypeSpecificRepoTester, error) {
	switch repoType {
	case api.Http:
		log.Info("detected HTTP repository type")
		return &HttpRepoTester{}, nil
	case api.Git:
		log.Info("detected Git repository type")
		return &GitRepoTester{}, nil
	default:
		log.Errorf("unsupported repository type: %s", repoType)
		return nil, fmt.Errorf("unsupported repository type: %s", repoType)
	}
}

type TypeSpecificRepoTester interface {
	TestAccess(repository *api.Repository) error
}

type GitRepoTester struct {
}

type HttpRepoTester struct {
}

func (r *GitRepoTester) TestAccess(repository *api.Repository) error {
	repoURL, err := repository.Spec.GetRepoURL()
	if err != nil {
		return err
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name:  *repository.Metadata.Name,
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

func (r *HttpRepoTester) TestAccess(repository *api.Repository) error {
	repoHttpSpec, err := repository.Spec.GetHttpRepoSpec()
	if err != nil {
		return fmt.Errorf("failed to get HTTP repo spec: %w", err)
	}

	repoURL := repoHttpSpec.Url
	// Append the validationSuffix if it exists
	if repoHttpSpec.ValidationSuffix != nil {
		repoURL += *repoHttpSpec.ValidationSuffix
	}

	repoSpec := repository.Spec
	_, err = sendHTTPrequest(repoSpec, repoURL)
	return err
}

func (r *RepoTester) SetAccessCondition(ctx context.Context, repository *api.Repository, err error) error {
	if repository.Status == nil {
		repository.Status = &api.RepositoryStatus{Conditions: []api.Condition{}}
	}
	if repository.Status.Conditions == nil {
		repository.Status.Conditions = []api.Condition{}
	}
	changed := api.SetStatusConditionByError(&repository.Status.Conditions, api.RepositoryAccessible, "Accessible", "Inaccessible", err)
	if !changed {
		// Nothing to do
		return nil
	}
	_, status := r.serviceHandler.ReplaceRepositoryStatus(ctx, *repository.Metadata.Name, *repository)
	return service.ApiStatusToErr(status)
}
