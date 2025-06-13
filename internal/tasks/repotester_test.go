package tasks

import (
	"context"
	"errors"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type mockRepoTesterService struct {
	orgsToReturn  []uuid.UUID
	orgsStatus    api.Status
	reposToReturn *api.RepositoryList
	reposStatus   api.Status
	replaceStatus api.Status
	repoToReturn  *api.Repository

	listReposCallCount     int
	listReposContexts      []context.Context
	replaceStatusCallCount int
}

func (m *mockRepoTesterService) ListAllOrganizationIDs(ctx context.Context) ([]uuid.UUID, api.Status) {
	return m.orgsToReturn, m.orgsStatus
}

func (m *mockRepoTesterService) ListRepositories(ctx context.Context, params api.ListRepositoriesParams) (*api.RepositoryList, api.Status) {
	m.listReposCallCount = m.listReposCallCount + 1
	m.listReposContexts = append(m.listReposContexts, ctx)
	return m.reposToReturn, m.reposStatus
}

func (m *mockRepoTesterService) ReplaceRepositoryStatus(ctx context.Context, name string, repository api.Repository) (*api.Repository, api.Status) {
	m.replaceStatusCallCount = m.replaceStatusCallCount + 1
	return m.repoToReturn, m.replaceStatus
}

type mockTypeSpecificRepoTester struct {
}

func (m *mockTypeSpecificRepoTester) TestAccess(repository *api.Repository) error {
	return nil
}

func TestTestRepositories(t *testing.T) {
	log := log.NewPrefixLogger("test")
	orgID := uuid.New()
	orgIDTwo := uuid.New()
	repoName := "my-repo"
	repoURL := "https://github.com/flightctl/test-repo"

	testCases := []struct {
		name                      string
		repoType                  api.RepoSpecType
		orgsStatus                api.Status
		orgsToReturn              []uuid.UUID
		testAccessError           error
		expectedConditionMsg      string
		expectedListReposCalls    int
		expectedStatusUpdateCalls int
	}{
		{
			name:                      "Org fetch failure",
			orgsStatus:                api.Status{Code: 500, Message: "Broken"},
			orgsToReturn:              []uuid.UUID{},
			repoType:                  api.Git,
			testAccessError:           nil,
			expectedConditionMsg:      "",
			expectedListReposCalls:    0,
			expectedStatusUpdateCalls: 0,
		},
		{
			name:                      "Git repo accessible",
			orgsStatus:                api.Status{Code: 200},
			orgsToReturn:              []uuid.UUID{orgID},
			repoType:                  api.Git,
			testAccessError:           nil,
			expectedConditionMsg:      "Accessible",
			expectedListReposCalls:    1,
			expectedStatusUpdateCalls: 1,
		},
		{
			name:                      "Git repo inaccessible",
			orgsStatus:                api.Status{Code: 200},
			orgsToReturn:              []uuid.UUID{orgID},
			repoType:                  api.Git,
			testAccessError:           errors.New("auth failed"),
			expectedConditionMsg:      "Inaccessible: auth failed",
			expectedListReposCalls:    1,
			expectedStatusUpdateCalls: 1,
		},
		{
			name:                      "HTTP repo accessible",
			orgsStatus:                api.Status{Code: 200},
			orgsToReturn:              []uuid.UUID{orgID},
			repoType:                  api.Http,
			testAccessError:           nil,
			expectedConditionMsg:      "Accessible",
			expectedListReposCalls:    1,
			expectedStatusUpdateCalls: 1,
		},
		{
			name:                      "HTTP repo inaccessible",
			orgsStatus:                api.Status{Code: 200},
			orgsToReturn:              []uuid.UUID{orgID},
			repoType:                  api.Http,
			testAccessError:           errors.New("404 not found"),
			expectedConditionMsg:      "Inaccessible: 404 not found",
			expectedListReposCalls:    1,
			expectedStatusUpdateCalls: 1,
		},
		{
			name:                      "No orgs",
			orgsStatus:                api.Status{Code: 200},
			orgsToReturn:              []uuid.UUID{},
			repoType:                  api.Git,
			testAccessError:           nil,
			expectedConditionMsg:      "",
			expectedListReposCalls:    0,
			expectedStatusUpdateCalls: 0,
		},
		{
			name:                      "Two orgs",
			orgsStatus:                api.Status{Code: 200},
			orgsToReturn:              []uuid.UUID{orgID, orgIDTwo},
			repoType:                  api.Git,
			testAccessError:           nil,
			expectedConditionMsg:      "Accessible",
			expectedListReposCalls:    2,
			expectedStatusUpdateCalls: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctx := context.Background()

			spec := api.RepositorySpec{}
			spec.FromGenericRepoSpec(api.GenericRepoSpec{
				Url:  repoURL,
				Type: tc.repoType,
			})
			repo := api.Repository{
				Metadata: api.ObjectMeta{Name: &repoName},
				Spec:     spec,
			}
			mockService := &mockRepoTesterService{
				orgsToReturn:      tc.orgsToReturn,
				orgsStatus:        tc.orgsStatus,
				reposToReturn:     &api.RepositoryList{Items: []api.Repository{repo}},
				reposStatus:       api.Status{Code: 200},
				replaceStatus:     api.Status{Code: 200},
				repoToReturn:      &repo,
				listReposContexts: []context.Context{},
			}
			mockTypeTester := &mockTypeSpecificRepoTester{}
			repoTester := &RepoTester{
				log:                    log,
				serviceHandler:         mockService,
				TypeSpecificRepoTester: mockTypeTester,
			}

			repoTester.TestRepositories(ctx)

			if tc.orgsStatus.Code != 200 {
				require.Equal(0, mockService.listReposCallCount)
				require.Equal(0, mockService.replaceStatusCallCount)
				return
			}

			require.Equal(tc.expectedListReposCalls, mockService.listReposCallCount)
			require.Equal(tc.expectedStatusUpdateCalls, mockService.replaceStatusCallCount)

			// Verify organization ids were added to context
			for i, ctx := range mockService.listReposContexts {
				orgIDFromCtx, ok := util.OrganizationIDValue(ctx)
				require.True(ok)
				require.Equal(tc.orgsToReturn[i], orgIDFromCtx)
			}
		})
	}
}
