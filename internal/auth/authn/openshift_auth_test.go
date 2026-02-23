package authn

import (
	"context"
	"encoding/json"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	k8sAuthenticationV1 "k8s.io/api/authentication/v1"
)

func newTestOpenShiftAuth(t *testing.T, k8sClient k8sclient.K8SClient) *OpenShiftAuth {
	t.Helper()
	auth, err := NewOpenShiftAuth(
		api.ObjectMeta{Name: lo.ToPtr("test-provider")},
		api.OpenShiftProviderSpec{
			Enabled:                lo.ToPtr(true),
			AuthorizationUrl:       lo.ToPtr("https://oauth.example.com/authorize"),
			TokenUrl:               lo.ToPtr("https://oauth.example.com/token"),
			ClientId:               lo.ToPtr("flightctl"),
			ClientSecret:           lo.ToPtr("secret"),
			ClusterControlPlaneUrl: lo.ToPtr("https://api.example.com:6443"),
			Issuer:                 lo.ToPtr("https://oauth.example.com"),
			RoleSuffix:             lo.ToPtr("flightctl"),
		},
		k8sClient,
		nil,
		logrus.New(),
	)
	require.NoError(t, err)
	return auth
}

func tokenReviewJSON(username, uid string, groups []string) []byte {
	review := k8sAuthenticationV1.TokenReview{
		Status: k8sAuthenticationV1.TokenReviewStatus{
			Authenticated: true,
			User: k8sAuthenticationV1.UserInfo{
				Username: username,
				UID:      uid,
				Groups:   groups,
			},
		},
	}
	data, _ := json.Marshal(review)
	return data
}

func projectListJSON(names ...string) []byte {
	type item struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	list := struct {
		Items []item `json:"items"`
	}{}
	for _, n := range names {
		it := item{}
		it.Metadata.Name = n
		list.Items = append(list.Items, it)
	}
	data, _ := json.Marshal(list)
	return data
}

func TestGetIdentity_GroupRolesResolved(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestOpenShiftAuth(t, mock)

	// TokenReview: user01 is in groups RedHat and system:authenticated
	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/tokenreviews"), gomock.Any()).
		Return(tokenReviewJSON("user01", "uid-001", []string{"RedHat", "system:authenticated"}), nil)

	// ListProjects: user has access to flightctl-ext
	mock.EXPECT().
		ListProjects(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(projectListJSON("flightctl-ext"), nil)

	// ListRoleBindingsForUser: called with username AND groups; returns viewer role via group
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "flightctl-ext", "user01", []string{"RedHat", "system:authenticated"}).
		Return([]string{"flightctl-viewer-flightctl"}, nil)

	identity, err := auth.GetIdentity(context.Background(), "test-token")
	require.NoError(t, err)

	assert.Equal(t, "user01", identity.GetUsername())
	assert.Equal(t, "uid-001", identity.GetUID())

	orgs := identity.GetOrganizations()
	require.Len(t, orgs, 1)
	assert.Equal(t, "flightctl-ext", orgs[0].Name)
	assert.Contains(t, orgs[0].Roles, "flightctl-viewer")
}

func TestGetIdentity_NoGroupsNoRoles(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestOpenShiftAuth(t, mock)

	// TokenReview: user04 has no custom groups
	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/tokenreviews"), gomock.Any()).
		Return(tokenReviewJSON("user04", "uid-004", []string{"system:authenticated"}), nil)

	mock.EXPECT().
		ListProjects(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(projectListJSON("flightctl-ext"), nil)

	// No role bindings match this user or their groups
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "flightctl-ext", "user04", []string{"system:authenticated"}).
		Return([]string{}, nil)

	identity, err := auth.GetIdentity(context.Background(), "test-token-2")
	require.NoError(t, err)

	assert.Equal(t, "user04", identity.GetUsername())
	orgs := identity.GetOrganizations()
	require.Len(t, orgs, 1)
	assert.Empty(t, orgs[0].Roles)
}

func TestGetIdentity_ClusterAdminViaGroup(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestOpenShiftAuth(t, mock)

	// TokenReview: user is in system:cluster-admins
	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/tokenreviews"), gomock.Any()).
		Return(tokenReviewJSON("admin-user", "uid-admin", []string{"system:cluster-admins", "system:authenticated"}), nil)

	mock.EXPECT().
		ListProjects(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(projectListJSON("flightctl-ext"), nil)

	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "flightctl-ext", "admin-user", []string{"system:cluster-admins", "system:authenticated"}).
		Return([]string{}, nil)

	identity, err := auth.GetIdentity(context.Background(), "test-token-3")
	require.NoError(t, err)

	assert.True(t, identity.IsSuperAdmin())
}

func TestGetIdentity_MultipleProjectsWithGroupRoles(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestOpenShiftAuth(t, mock)

	groups := []string{"RedHat", "Engineering", "system:authenticated"}

	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/tokenreviews"), gomock.Any()).
		Return(tokenReviewJSON("user02", "uid-002", groups), nil)

	mock.EXPECT().
		ListProjects(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(projectListJSON("project-a", "project-b"), nil)

	// project-a: group RedHat has operator
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "project-a", "user02", groups).
		Return([]string{"flightctl-operator-flightctl"}, nil)

	// project-b: group Engineering has viewer
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "project-b", "user02", groups).
		Return([]string{"flightctl-viewer-flightctl"}, nil)

	identity, err := auth.GetIdentity(context.Background(), "test-token-4")
	require.NoError(t, err)

	orgs := identity.GetOrganizations()
	require.Len(t, orgs, 2)

	orgMap := map[string][]string{}
	for _, org := range orgs {
		orgMap[org.Name] = org.Roles
	}
	assert.Contains(t, orgMap["project-a"], "flightctl-operator")
	assert.Contains(t, orgMap["project-b"], "flightctl-viewer")
}
