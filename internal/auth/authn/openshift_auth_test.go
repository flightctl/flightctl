package authn

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	k8sAuthenticationV1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func newTestOpenShiftAuth(t *testing.T, k8sClient k8sclient.K8SClient) *OpenShiftAuth {
	t.Helper()
	encryptedSecret, err := encryption.Encrypt(t.Context(), []byte("secret"))
	require.NoError(t, err)
	encryptedStr := string(encryptedSecret)

	auth, err := NewOpenShiftAuth(
		api.ObjectMeta{Name: lo.ToPtr("test-provider")},
		api.OpenShiftProviderSpec{
			Enabled:                lo.ToPtr(true),
			AuthorizationUrl:       lo.ToPtr("https://oauth.example.com/authorize"),
			TokenUrl:               lo.ToPtr("https://oauth.example.com/token"),
			ClientId:               lo.ToPtr("flightctl"),
			ClientSecret:           &encryptedStr,
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

func selfSubjectReviewJSON(username, uid string, groups []string) []byte {
	review := k8sAuthenticationV1.SelfSubjectReview{
		Status: k8sAuthenticationV1.SelfSubjectReviewStatus{
			UserInfo: k8sAuthenticationV1.UserInfo{
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

func currentUserJSON(username, uid string, groups []string) []byte {
	user := struct {
		Metadata struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"metadata"`
		Groups []string `json:"groups"`
	}{Groups: groups}
	user.Metadata.Name = username
	user.Metadata.UID = uid
	data, _ := json.Marshal(user)
	return data
}

func newTestOpenShiftAuthWithPrefix(t *testing.T, k8sClient k8sclient.K8SClient, prefix string) *OpenShiftAuth {
	t.Helper()
	encryptedSecret, err := encryption.Encrypt(t.Context(), []byte("secret"))
	require.NoError(t, err)
	encryptedStr := string(encryptedSecret)

	auth, err := NewOpenShiftAuth(
		api.ObjectMeta{Name: lo.ToPtr("test-provider")},
		api.OpenShiftProviderSpec{
			Enabled:                lo.ToPtr(true),
			AuthorizationUrl:       lo.ToPtr("https://oauth.example.com/authorize"),
			TokenUrl:               lo.ToPtr("https://oauth.example.com/token"),
			ClientId:               lo.ToPtr("flightctl"),
			ClientSecret:           &encryptedStr,
			ClusterControlPlaneUrl: lo.ToPtr("https://api.example.com:6443"),
			Issuer:                 lo.ToPtr("https://oauth.example.com"),
			RoleSuffix:             lo.ToPtr("flightctl"),
			OrganizationNamePrefix: lo.ToPtr(prefix),
		},
		k8sClient,
		nil,
		logrus.New(),
	)
	require.NoError(t, err)
	return auth
}

func newServiceAccountOpenShiftAuth(t *testing.T, k8sClient k8sclient.K8SClient, secretFile string) *OpenShiftAuth {
	t.Helper()

	auth, err := NewOpenShiftAuth(
		api.ObjectMeta{Name: lo.ToPtr("test-provider")},
		api.OpenShiftProviderSpec{
			Enabled:                lo.ToPtr(true),
			AuthorizationUrl:       lo.ToPtr("https://oauth.example.com/authorize"),
			TokenUrl:               lo.ToPtr("https://oauth.example.com/token"),
			ClientId:               lo.ToPtr("system:serviceaccount:flightctl:flightctl-api"),
			ClientType:             lo.ToPtr(api.Serviceaccount),
			ClientSecretFile:       lo.ToPtr(secretFile),
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

func TestGetIdentity_OrganizationNamePrefix(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestOpenShiftAuthWithPrefix(t, mock, "ocp-")

	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/selfsubjectreviews"), gomock.Any(), gomock.Any()).
		Return(selfSubjectReviewJSON("user01", "uid-001", []string{"system:authenticated"}), nil)
	mock.EXPECT().
		ListProjects(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(projectListJSON("my-project"), nil)
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "my-project", "user01", []string{"system:authenticated"}).
		Return([]string{"flightctl-viewer-flightctl"}, nil)

	identity, err := auth.GetIdentity(context.Background(), "test-token")
	require.NoError(t, err)

	assert.Equal(t, "user01", identity.GetUsername())
	orgs := identity.GetOrganizations()
	require.Len(t, orgs, 1)
	assert.Equal(t, "ocp-my-project", orgs[0].Name)
}

func TestGetIdentity_GroupRolesResolved(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestOpenShiftAuth(t, mock)

	// SelfSubjectReview: user01 is in groups RedHat and system:authenticated
	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/selfsubjectreviews"), gomock.Any(), gomock.Any()).
		Return(selfSubjectReviewJSON("user01", "uid-001", []string{"RedHat", "system:authenticated"}), nil)

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

	// SelfSubjectReview: user04 has no custom groups
	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/selfsubjectreviews"), gomock.Any(), gomock.Any()).
		Return(selfSubjectReviewJSON("user04", "uid-004", []string{"system:authenticated"}), nil)

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

	// SelfSubjectReview: user is in system:cluster-admins
	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/selfsubjectreviews"), gomock.Any(), gomock.Any()).
		Return(selfSubjectReviewJSON("admin-user", "uid-admin", []string{"system:cluster-admins", "system:authenticated"}), nil)

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
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/selfsubjectreviews"), gomock.Any(), gomock.Any()).
		Return(selfSubjectReviewJSON("user02", "uid-002", groups), nil)

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

func TestGetIdentity_FallsBackToCurrentUserWhenSelfSubjectReviewIsForbidden(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestOpenShiftAuth(t, mock)

	groups := []string{"system:authenticated", "team-a"}
	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/selfsubjectreviews"), gomock.Any(), gomock.Any()).
		Return(nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "authentication.k8s.io", Resource: "selfsubjectreviews"},
			"",
			errors.New("forbidden by token scope"),
		))
	mock.EXPECT().
		GetCurrentUser(gomock.Any(), "test-token").
		Return(currentUserJSON("user01", "uid-001", groups), nil)
	mock.EXPECT().
		ListProjects(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(projectListJSON("flightctl-ext"), nil)
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "flightctl-ext", "user01", groups).
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

func TestGetIdentity_ServiceAccountClientUsesCurrentUserEndpoint(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("client-token"), 0o600))
	auth := newServiceAccountOpenShiftAuth(t, mock, tokenFile)

	groups := []string{"system:authenticated", "team-a"}
	mock.EXPECT().
		GetCurrentUser(gomock.Any(), "user-token").
		Return(currentUserJSON("user01", "uid-001", groups), nil)
	mock.EXPECT().
		ListProjects(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(projectListJSON("flightctl-ext"), nil)
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "flightctl-ext", "user01", groups).
		Return([]string{"flightctl-viewer-flightctl"}, nil)

	identity, err := auth.GetIdentity(context.Background(), "user-token")
	require.NoError(t, err)
	assert.Equal(t, "user01", identity.GetUsername())
	assert.Equal(t, "uid-001", identity.GetUID())
}

func TestGetIdentity_ServiceAccountClientClusterAdminViaCurrentUserGroups(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("client-token"), 0o600))
	auth := newServiceAccountOpenShiftAuth(t, mock, tokenFile)

	groups := []string{"system:cluster-admins", "system:authenticated"}
	mock.EXPECT().
		GetCurrentUser(gomock.Any(), "user-token").
		Return(currentUserJSON("cluster-admin", "uid-admin", groups), nil)
	mock.EXPECT().
		ListProjects(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(projectListJSON("flightctl-ext"), nil)
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "flightctl-ext", "cluster-admin", groups).
		Return([]string{}, nil)

	identity, err := auth.GetIdentity(context.Background(), "user-token")
	require.NoError(t, err)
	assert.True(t, identity.IsSuperAdmin(), "system:cluster-admins from users/~ must still grant global admin")
}

func TestOpenShiftServiceAccountClient_UsesRotatingTokenFile(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("client-token-1"), 0o600))
	auth := newServiceAccountOpenShiftAuth(t, k8sclient.NewMockK8SClient(ctrl), tokenFile)

	spec := auth.GetOpenShiftSpec()
	require.NotNil(t, spec.ClientSecret)
	require.Equal(t, "client-token-1", *spec.ClientSecret)

	require.NoError(t, os.WriteFile(tokenFile, []byte("client-token-2"), 0o600))
	spec = auth.GetOpenShiftSpec()
	require.NotNil(t, spec.ClientSecret)
	require.Equal(t, "client-token-2", *spec.ClientSecret)

	config, err := auth.GetAuthConfig()
	require.NoError(t, err)
	require.NotNil(t, config.Providers)
	providerSpec, err := (*config.Providers)[0].Spec.AsOpenShiftProviderSpec()
	require.NoError(t, err)
	require.Nil(t, providerSpec.ClientSecret, "client credential must not be exposed in public auth config")
	require.NotNil(t, providerSpec.ClientType)
	require.Equal(t, api.Serviceaccount, *providerSpec.ClientType)
	require.NotNil(t, providerSpec.Scopes)
	require.ElementsMatch(t, []string{"user:info", "user:check-access", "user:list-projects"}, *providerSpec.Scopes)
}

func TestOpenShiftValidateToken(t *testing.T) {
	t.Run("When SelfSubjectReview succeeds it should accept the token", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mock := k8sclient.NewMockK8SClient(ctrl)
		auth := newTestOpenShiftAuth(t, mock)
		mock.EXPECT().
			PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/selfsubjectreviews"), gomock.Any(), gomock.Any()).
			Return(selfSubjectReviewJSON("user01", "uid-001", nil), nil)

		require.NoError(t, auth.ValidateToken(context.Background(), "test-token"))
	})

	t.Run("When SelfSubjectReview is rejected it should reject the token", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mock := k8sclient.NewMockK8SClient(ctrl)
		auth := newTestOpenShiftAuth(t, mock)
		mock.EXPECT().
			PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/selfsubjectreviews"), gomock.Any(), gomock.Any()).
			Return(nil, errors.New("Unauthorized"))

		require.Error(t, auth.ValidateToken(context.Background(), "bad-token"))
	})
}
