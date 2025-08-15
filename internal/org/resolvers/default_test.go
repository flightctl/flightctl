package resolvers

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultResolver_getDefaultOrg(t *testing.T) {
	ctx := context.Background()

	defaultOrg := &model.Organization{
		ID:          org.DefaultID,
		DisplayName: "Default",
	}

	tests := []struct {
		name                   string
		orgID                  uuid.UUID
		setOrgInCache          bool
		mockOrgStore           *mockOrgStore
		expectedStoreCallCount int
		expectedResult         *model.Organization
		expectedErrorString    string
	}{
		{
			name:          "default org not set, default org in store",
			orgID:         org.DefaultID,
			setOrgInCache: false,
			mockOrgStore: &mockOrgStore{
				getByIDResult: defaultOrg,
			},
			expectedStoreCallCount: 1,
			expectedResult:         defaultOrg,
			expectedErrorString:    "",
		},
		{
			name:                   "default org set",
			orgID:                  org.DefaultID,
			setOrgInCache:          true,
			mockOrgStore:           &mockOrgStore{},
			expectedStoreCallCount: 0,
			expectedResult:         defaultOrg,
			expectedErrorString:    "",
		},
		{
			name:          "default org not set, default org not in store",
			orgID:         org.DefaultID,
			setOrgInCache: false,
			mockOrgStore: &mockOrgStore{
				getByIDError: errors.New("organization not found"),
			},
			expectedStoreCallCount: 1,
			expectedResult:         nil,
			expectedErrorString:    "organization not found",
		},
		{
			name:                   "requested org is not default",
			orgID:                  uuid.New(),
			setOrgInCache:          false,
			mockOrgStore:           &mockOrgStore{},
			expectedStoreCallCount: 0,
			expectedResult:         nil,
			expectedErrorString:    "organization not found",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := NewDefaultResolver(test.mockOrgStore)
			if test.setOrgInCache {
				resolver.defaultOrg = defaultOrg
			}
			org, err := resolver.getDefaultOrg(ctx, test.orgID)

			if test.expectedErrorString != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, test.expectedErrorString)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, test.expectedResult, org)
			assert.Equal(t, test.expectedStoreCallCount, test.mockOrgStore.getByIDCallCount)
		})
	}
}

func TestDefaultResolver_GetUserOrganizations(t *testing.T) {
	ctx := context.Background()
	identity := common.NewBaseIdentity("test-user", "test", []string{})
	resolver := NewDefaultResolver(nil)
	resolver.defaultOrg = &model.Organization{
		ID:          org.DefaultID,
		DisplayName: "Default",
	}
	orgs, err := resolver.GetUserOrganizations(ctx, identity)
	assert.NoError(t, err)
	assert.Equal(t, []*model.Organization{resolver.defaultOrg}, orgs)
}
