package service

import (
	"testing"

	"github.com/flightctl/flightctl/internal/identity"
	orgmodel "github.com/flightctl/flightctl/internal/org/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMappedIdentityToUserInfoResponse(t *testing.T) {
	orgID := uuid.New()
	mapped := &identity.MappedIdentity{
		Username: "alice",
		UID:      "uid-alice",
		Organizations: []*orgmodel.Organization{
			{
				ID:          orgID,
				ExternalID:  "project-a",
				DisplayName: "Project A",
			},
		},
		OrgRoles: map[string][]string{
			orgID.String(): {"flightctl-org-admin"},
		},
	}

	response := mappedIdentityToUserInfoResponse(mapped)

	require.Nil(t, response.Error)
	require.NotNil(t, response.Sub)
	require.Equal(t, "uid-alice", *response.Sub)
	require.NotNil(t, response.PreferredUsername)
	require.Equal(t, "alice", *response.PreferredUsername)
	require.NotNil(t, response.Name)
	require.Equal(t, "alice", *response.Name)
	require.NotNil(t, response.Organizations)
	require.Len(t, *response.Organizations, 1)

	org := (*response.Organizations)[0]
	require.NotNil(t, org.Metadata.Name)
	require.Equal(t, orgID.String(), *org.Metadata.Name)
	require.NotNil(t, org.Spec)
	require.NotNil(t, org.Spec.ExternalId)
	require.Equal(t, "project-a", *org.Spec.ExternalId)
	require.NotNil(t, org.Spec.DisplayName)
	require.Equal(t, "Project A", *org.Spec.DisplayName)

	// Roles are intentionally not part of the OIDC userinfo response. They stay
	// in MappedIdentity.OrgRoles for authorization.
}
