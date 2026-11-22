package authz

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestPolicyAdministrationPermissions(t *testing.T) {
	orgID := uuid.New()
	organization := &model.Organization{ID: orgID}
	authorizer := NewStaticAuthZ(logrus.New())
	for _, role := range []string{api.RoleAdmin, api.RoleOperator, api.RoleViewer, api.RoleInstaller} {
		t.Run("When the role is "+role+" it should enforce policy administration permissions", func(t *testing.T) {
			mapped := identity.NewMappedIdentity("test", "test", []*model.Organization{organization}, map[string][]string{orgID.String(): {role}}, false, nil)
			ctx := util.WithOrganizationID(context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mapped), orgID)
			for _, resource := range []string{"enrollmentapprovalpolicies", "enrollmentapprovalpolicies/approvallimit"} {
				allowed, err := authorizer.CheckPermission(ctx, resource, "update")
				require.NoError(t, err)
				require.Equal(t, role == api.RoleAdmin, allowed)
			}
		})
	}
}
