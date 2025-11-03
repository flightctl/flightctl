package providers

import (
	"context"
	"reflect"
	"testing"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
)

// Mock TokenIdentity for testing
type mockTokenIdentity struct {
	claims map[string]interface{}
}

func (m *mockTokenIdentity) GetClaim(name string) (interface{}, bool) {
	claim, ok := m.claims[name]
	return claim, ok
}

func (m *mockTokenIdentity) GetUsername() string {
	return ""
}

func (m *mockTokenIdentity) GetUID() string {
	return ""
}

func (m *mockTokenIdentity) GetOrganizations() []common.ReportedOrganization {
	return []common.ReportedOrganization{}
}

func (m *mockTokenIdentity) GetRoles() []string {
	return []string{}
}

func (m *mockTokenIdentity) GetIssuer() *identity.Issuer {
	return nil
}

func TestClaimsProvider_GetUserOrganizations(t *testing.T) {
	tests := []struct {
		name     string
		identity common.Identity
		want     []org.ExternalOrganization
		wantErr  bool
		errMsg   string
	}{
		{
			name: "successful retrieval with multiple organizations",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{
						"org1": map[string]interface{}{
							organizationClaimID: "id1",
						},
						"org2": map[string]interface{}{
							organizationClaimID: "id2",
						},
						"org3": map[string]interface{}{
							organizationClaimID: "id3",
						},
					},
				},
			},
			want: []org.ExternalOrganization{
				{ID: "id1", Name: "org1"},
				{ID: "id2", Name: "org2"},
				{ID: "id3", Name: "org3"},
			},
			wantErr: false,
		},
		{
			name: "successful retrieval with single organization",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{
						"single-org": map[string]interface{}{
							organizationClaimID: "single-id",
						},
					},
				},
			},
			want: []org.ExternalOrganization{
				{ID: "single-id", Name: "single-org"},
			},
			wantErr: false,
		},
		{
			name: "empty organizations claim",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{},
				},
			},
			want:    []org.ExternalOrganization{},
			wantErr: false,
		},
		{
			name: "real JWT token format",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{
						"pinkcorp": map[string]interface{}{
							"id": "a6e97659-16a5-4b18-9d90-7fe88a744e2a",
						},
						"orangecorp": map[string]interface{}{
							"id": "7ca05aab-652c-46a4-aef5-1093e573865c",
						},
					},
				},
			},
			want: []org.ExternalOrganization{
				{ID: "a6e97659-16a5-4b18-9d90-7fe88a744e2a", Name: "pinkcorp"},
				{ID: "7ca05aab-652c-46a4-aef5-1093e573865c", Name: "orangecorp"},
			},
			wantErr: false,
		},
		{
			name:     "non-token identity",
			identity: &common.BaseIdentity{},
			want:     nil,
			wantErr:  true,
			errMsg:   "cannot get organizations claims from a non-token identity (got *common.BaseIdentity)",
		},
		{
			name: "missing organization claim",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{},
			},
			want:    nil,
			wantErr: true,
			errMsg:  "missing required token claims: organization claim not found",
		},
		{
			name: "invalid claim format - string",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: "invalid-format",
				},
			},
			want:    nil,
			wantErr: true,
			errMsg:  "invalid token claims: invalid organizations claim format (got string)",
		},
		{
			name: "invalid claim format - wrong map type",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[int]string{
						1: "id1",
					},
				},
			},
			want:    nil,
			wantErr: true,
			errMsg:  "invalid token claims: invalid organizations claim format (got map[int]string)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &ClaimsProvider{}
			got, err := c.GetUserOrganizations(context.Background(), tt.identity)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetUserOrganizations() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && err.Error() != tt.errMsg {
				t.Errorf("GetUserOrganizations() error message = %v, want %v", err.Error(), tt.errMsg)
				return
			}

			// Check length first
			if len(got) != len(tt.want) {
				t.Errorf("GetUserOrganizations() got %d organizations, want %d", len(got), len(tt.want))
				return
			}

			// Check that all expected organizations are present (order-independent)
			gotMap := make(map[string]org.ExternalOrganization)
			for _, org := range got {
				gotMap[org.ID] = org
			}

			for _, wantOrg := range tt.want {
				gotOrg, exists := gotMap[wantOrg.ID]
				if !exists {
					t.Errorf("GetUserOrganizations() missing organization with ID %v", wantOrg.ID)
					continue
				}
				if gotOrg.Name != wantOrg.Name {
					t.Errorf("GetUserOrganizations() organization %v: got name %v, want %v", wantOrg.ID, gotOrg.Name, wantOrg.Name)
				}
			}
		})
	}
}

func TestClaimsProvider_IsMemberOf(t *testing.T) {
	tests := []struct {
		name     string
		identity common.Identity
		orgID    string
		want     bool
		wantErr  bool
		errMsg   string
	}{
		{
			name: "member of organization - first in list",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{
						"org1": map[string]interface{}{
							organizationClaimID: "id1",
						},
						"org2": map[string]interface{}{
							organizationClaimID: "id2",
						},
						"org3": map[string]interface{}{
							organizationClaimID: "id3",
						},
					},
				},
			},
			orgID:   "id1",
			want:    true,
			wantErr: false,
		},
		{
			name: "member of organization - middle in list",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{
						"org1": map[string]interface{}{
							organizationClaimID: "id1",
						},
						"org2": map[string]interface{}{
							organizationClaimID: "id2",
						},
						"org3": map[string]interface{}{
							organizationClaimID: "id3",
						},
					},
				},
			},
			orgID:   "id2",
			want:    true,
			wantErr: false,
		},
		{
			name: "member of organization - last in list",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{
						"org1": map[string]interface{}{
							organizationClaimID: "id1",
						},
						"org2": map[string]interface{}{
							organizationClaimID: "id2",
						},
						"org3": map[string]interface{}{
							organizationClaimID: "id3",
						},
					},
				},
			},
			orgID:   "id3",
			want:    true,
			wantErr: false,
		},
		{
			name: "not member of organization",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{
						"org1": map[string]interface{}{
							organizationClaimID: "id1",
						},
						"org2": map[string]interface{}{
							organizationClaimID: "id2",
						},
					},
				},
			},
			orgID:   "id-not-exists",
			want:    false,
			wantErr: false,
		},
		{
			name: "empty organization ID check",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{
						"org1": map[string]interface{}{
							organizationClaimID: "id1",
						},
					},
				},
			},
			orgID:   "",
			want:    false,
			wantErr: false,
		},
		{
			name: "no organizations",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{},
				},
			},
			orgID:   "any-id",
			want:    false,
			wantErr: false,
		},
		{
			name:     "non-token identity",
			identity: &common.BaseIdentity{},
			orgID:    "id1",
			want:     false,
			wantErr:  true,
			errMsg:   "cannot get organizations claims from a non-token identity (got *common.BaseIdentity)",
		},
		{
			name: "missing organization claim",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{},
			},
			orgID:   "id1",
			want:    false,
			wantErr: true,
			errMsg:  "missing required token claims: organization claim not found",
		},
		{
			name: "invalid claim format",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: []string{"invalid"},
				},
			},
			orgID:   "id1",
			want:    false,
			wantErr: true,
			errMsg:  "invalid token claims: invalid organizations claim format (got []string)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &ClaimsProvider{}
			got, err := c.IsMemberOf(context.Background(), tt.identity, tt.orgID)

			if (err != nil) != tt.wantErr {
				t.Errorf("IsMemberOf() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && err.Error() != tt.errMsg {
				t.Errorf("IsMemberOf() error message = %v, want %v", err.Error(), tt.errMsg)
				return
			}

			if got != tt.want {
				t.Errorf("IsMemberOf() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClaimsFromIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity common.Identity
		want     map[orgID]orgName
		wantErr  bool
		errMsg   string
	}{
		{
			name: "valid token identity with organizations",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{
						"org1": map[string]interface{}{
							organizationClaimID: "id1",
						},
						"org2": map[string]interface{}{
							organizationClaimID: "id2",
						},
					},
				},
			},
			want: map[orgID]orgName{
				"id1": "org1",
				"id2": "org2",
			},
			wantErr: false,
		},
		{
			name: "valid token identity with empty organizations",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]interface{}{},
				},
			},
			want:    map[orgID]orgName{},
			wantErr: false,
		},
		{
			name:     "non-token identity",
			identity: &common.BaseIdentity{},
			want:     nil,
			wantErr:  true,
			errMsg:   "cannot get organizations claims from a non-token identity (got *common.BaseIdentity)",
		},
		{
			name: "token identity without organization claim",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					"other-claim": "value",
				},
			},
			want:    nil,
			wantErr: true,
			errMsg:  "missing required token claims: organization claim not found",
		},
		{
			name: "invalid claim type - string",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: "not-a-map",
				},
			},
			want:    nil,
			wantErr: true,
			errMsg:  "invalid token claims: invalid organizations claim format (got string)",
		},
		{
			name: "invalid claim type - slice",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: []string{"org1", "org2"},
				},
			},
			want:    nil,
			wantErr: true,
			errMsg:  "invalid token claims: invalid organizations claim format (got []string)",
		},
		{
			name: "invalid claim type - map with wrong value type",
			identity: &mockTokenIdentity{
				claims: map[string]interface{}{
					organizationClaimName: map[string]int{
						"org1": 1,
					},
				},
			},
			want:    nil,
			wantErr: true,
			errMsg:  "invalid token claims: invalid organizations claim format (got map[string]int)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := claimsFromIdentity(tt.identity)

			if (err != nil) != tt.wantErr {
				t.Errorf("claimsFromIdentity() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && err.Error() != tt.errMsg {
				t.Errorf("claimsFromIdentity() error message = %v, want %v", err.Error(), tt.errMsg)
				return
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("claimsFromIdentity() = %v, want %v", got, tt.want)
			}
		})
	}
}
