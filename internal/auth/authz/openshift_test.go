package authz

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type emptyScopesMatcher struct{}

func (emptyScopesMatcher) Matches(x any) bool {
	body, ok := x.([]byte)
	if !ok {
		return false
	}
	var review struct {
		Spec struct {
			Scopes []string `json:"scopes"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &review); err != nil {
		return false
	}
	return review.Spec.Scopes != nil && len(review.Spec.Scopes) == 0
}

func (emptyScopesMatcher) String() string {
	return "contains spec.scopes: []"
}

func TestOpenShiftAuthZ_GetUserPermissions_FallsBackToOpenShiftRulesReview(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := k8sclient.NewMockK8SClient(ctrl)
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	orgID := uuid.New()
	org := &model.Organization{ID: orgID, ExternalID: "project-a", DisplayName: "Project A"}
	mappedIdentity := identity.NewMappedIdentity(
		"user01",
		"uid-001",
		[]*model.Organization{org},
		map[string][]string{orgID.String(): {"flightctl-viewer"}},
		false,
		nil,
	)
	ctx, cancel := context.WithCancel(
		util.WithOrganizationID(
			context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity),
			orgID,
		),
	)
	defer cancel()
	authZ := NewOpenShiftAuthZ(ctx, mock, log)

	mock.EXPECT().
		PostCRD(gomock.Any(), gomock.Eq("authorization.k8s.io/v1/selfsubjectrulesreviews"), gomock.Any(), gomock.Any()).
		Return(nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "authorization.k8s.io", Resource: "selfsubjectrulesreviews"},
			"",
			errors.New("forbidden by token scope"),
		))
	mock.EXPECT().
		PostCRD(
			gomock.Any(),
			gomock.Eq("authorization.openshift.io/v1/namespaces/project-a/selfsubjectrulesreviews"),
			emptyScopesMatcher{},
			gomock.Any(),
		).
		Return([]byte(`{"status":{"rules":[{"verbs":["get","list"],"apiGroups":["flightctl.io"],"resources":["devices"]},{"verbs":["*"],"apiGroups":["apps"],"resources":["deployments"]}]}}`), nil)

	permissions, err := authZ.GetUserPermissions(ctx, "test-token")
	require.NoError(t, err)
	require.Len(t, permissions.Permissions, 1)
	assert.Equal(t, "devices", permissions.Permissions[0].Resource)
	assert.ElementsMatch(t, []string{"get", "list"}, permissions.Permissions[0].Operations)
}
