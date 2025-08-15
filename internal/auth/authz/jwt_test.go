package authz

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

type mockMembershipChecker struct {
	membership bool
	err        error
}

func (m *mockMembershipChecker) IsMemberOf(ctx context.Context, identity common.Identity, orgID uuid.UUID) (bool, error) {
	return m.membership, m.err
}

func TestJWTAuthZ_CheckPermission(t *testing.T) {
	backgroundCtx := context.Background()

	identity := common.NewBaseIdentity("test-username", "test-uid", []string{})
	ctxWithIdentity := context.WithValue(backgroundCtx, consts.IdentityCtxKey, identity)

	orgID := uuid.New()
	ctxWithIdentityAndOrgID := context.WithValue(ctxWithIdentity, consts.OrganizationIDCtxKey, orgID)

	tests := []struct {
		name              string
		membershipChecker mockMembershipChecker
		ctx               context.Context
		want              bool
		wantErr           bool
		resource          string
		op                string
	}{
		{
			name: "route is organizations list",
			membershipChecker: mockMembershipChecker{
				membership: false,
				err:        nil,
			},
			ctx:      backgroundCtx,
			want:     true,
			wantErr:  false,
			resource: "organizations",
			op:       "list",
		},
		{
			name: "identity is not in context",
			membershipChecker: mockMembershipChecker{
				membership: false,
				err:        nil,
			},
			ctx:      backgroundCtx,
			want:     false,
			wantErr:  true,
			resource: "devices",
			op:       "get",
		},
		{
			name: "org id is not in context",
			membershipChecker: mockMembershipChecker{
				membership: false,
				err:        nil,
			},
			ctx:      ctxWithIdentity,
			want:     false,
			wantErr:  true,
			resource: "devices",
			op:       "get",
		},
		{
			name: "user is not member of org",
			membershipChecker: mockMembershipChecker{
				membership: false,
				err:        nil,
			},
			ctx:      ctxWithIdentityAndOrgID,
			want:     false,
			wantErr:  false,
			resource: "devices",
			op:       "get",
		},
		{
			name: "user is member of org",
			membershipChecker: mockMembershipChecker{
				membership: true,
				err:        nil,
			},
			ctx:      ctxWithIdentityAndOrgID,
			want:     true,
			wantErr:  false,
			resource: "devices",
			op:       "get",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jwtAuthZ := NewJWTAuthZ(&tt.membershipChecker)
			got, err := jwtAuthZ.CheckPermission(tt.ctx, tt.resource, tt.op)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}
