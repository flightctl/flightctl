package authn

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func newTestK8sAuthN(t *testing.T, k8sClient k8sclient.K8SClient) *K8sAuthN {
	t.Helper()
	auth, err := NewK8sAuthN(
		api.ObjectMeta{Name: lo.ToPtr("test-k8s-provider")},
		api.K8sProviderSpec{
			Enabled:    lo.ToPtr(true),
			ApiUrl:     "https://api.k8s.example.com:6443",
			RbacNs:     lo.ToPtr("flightctl-ext"),
			RoleSuffix: lo.ToPtr("flightctl"),
		},
		k8sClient,
	)
	require.NoError(t, err)
	return auth
}

func TestK8sGetIdentity_GroupRolesResolved(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestK8sAuthN(t, mock)

	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/tokenreviews"), gomock.Any()).
		Return(tokenReviewJSON("user01", "uid-001", []string{"developers", "system:authenticated"}), nil)

	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "flightctl-ext", "user01", []string{"developers", "system:authenticated"}).
		Return([]string{"flightctl-viewer-flightctl"}, nil)

	identity, err := auth.GetIdentity(context.Background(), "test-token")
	require.NoError(t, err)

	assert.Equal(t, "user01", identity.GetUsername())
	assert.Equal(t, "uid-001", identity.GetUID())

	orgs := identity.GetOrganizations()
	require.Len(t, orgs, 1)
	assert.Equal(t, "default", orgs[0].Name)
	assert.Contains(t, orgs[0].Roles, "flightctl-viewer")
}

func TestK8sGetIdentity_NoGroupsNoRoles(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestK8sAuthN(t, mock)

	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/tokenreviews"), gomock.Any()).
		Return(tokenReviewJSON("user04", "uid-004", []string{"system:authenticated"}), nil)

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

func TestK8sGetIdentity_AdminViaGroup(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestK8sAuthN(t, mock)

	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/tokenreviews"), gomock.Any()).
		Return(tokenReviewJSON("admin-user", "uid-admin", []string{"platform-admins", "system:authenticated"}), nil)

	// Group platform-admins is bound to flightctl-admin role
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "flightctl-ext", "admin-user", []string{"platform-admins", "system:authenticated"}).
		Return([]string{"flightctl-admin-flightctl"}, nil)

	identity, err := auth.GetIdentity(context.Background(), "test-token-3")
	require.NoError(t, err)

	assert.True(t, identity.IsSuperAdmin())
}

func TestK8sGetIdentity_UserAndGroupRolesCombined(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	auth := newTestK8sAuthN(t, mock)

	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/tokenreviews"), gomock.Any()).
		Return(tokenReviewJSON("user05", "uid-005", []string{"ops-team", "system:authenticated"}), nil)

	// User gets viewer directly + operator through group
	mock.EXPECT().
		ListRoleBindingsForUser(gomock.Any(), "flightctl-ext", "user05", []string{"ops-team", "system:authenticated"}).
		Return([]string{"flightctl-viewer-flightctl", "flightctl-operator-flightctl"}, nil)

	identity, err := auth.GetIdentity(context.Background(), "test-token-4")
	require.NoError(t, err)

	orgs := identity.GetOrganizations()
	require.Len(t, orgs, 1)
	assert.Contains(t, orgs[0].Roles, "flightctl-viewer")
	assert.Contains(t, orgs[0].Roles, "flightctl-operator")
}

func TestK8sGetIdentity_NoRbacNs(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)

	auth, err := NewK8sAuthN(
		api.ObjectMeta{Name: lo.ToPtr("test-k8s-no-rbac")},
		api.K8sProviderSpec{
			Enabled: lo.ToPtr(true),
			ApiUrl:  "https://api.k8s.example.com:6443",
		},
		mock,
	)
	require.NoError(t, err)

	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authentication.k8s.io/v1/tokenreviews"), gomock.Any()).
		Return(tokenReviewJSON("user06", "uid-006", []string{"developers"}), nil)

	// No ListRoleBindingsForUser call expected since RbacNs is nil

	identity, err := auth.GetIdentity(context.Background(), "test-token-5")
	require.NoError(t, err)

	assert.Equal(t, "user06", identity.GetUsername())
	orgs := identity.GetOrganizations()
	require.Len(t, orgs, 1)
	assert.Empty(t, orgs[0].Roles)
}
