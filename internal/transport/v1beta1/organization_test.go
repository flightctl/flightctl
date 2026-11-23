package transportv1beta1

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/api_server/versioning"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/service"
	organizationservice "github.com/flightctl/flightctl/internal/service/organization"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Unimplemented store methods panic, detecting any unexpected writes or lookups.
type organizationReadStore struct {
	organizationstore.Store
	orgs []*model.Organization
}

func (s *organizationReadStore) List(context.Context, store.ListParams) ([]*model.Organization, error) {
	return s.orgs, nil
}

func (s *organizationReadStore) ListByExternalIDs(_ context.Context, ids []string) ([]*model.Organization, error) {
	var result []*model.Organization
	for _, org := range s.orgs {
		if lo.Contains(ids, org.ExternalID) {
			result = append(result, org)
		}
	}
	return result, nil
}

func TestOrganizations_AAPGatewayMetadata(t *testing.T) {
	tests := []struct {
		name      string
		superuser bool
		auditor   bool
		nonAAP    bool
		prefix    string
		gatewayID int
	}{
		{name: "When a superuser lists organizations it should enrich only Gateway organizations", superuser: true, prefix: "aap-", gatewayID: 42},
		{name: "When an auditor lists organizations it should use the Gateway all-organizations list", auditor: true, prefix: "aap-", gatewayID: 42},
		{name: "When membership is direct or team-inherited it should retain IDs and roles", prefix: "aap-", gatewayID: 42},
		{name: "When the prefix is empty it should retain the existing external key", gatewayID: 42},
		{name: "When another Gateway reports the same names it should use that identity's IDs", prefix: "aap-", gatewayID: 142},
		{name: "When Gateway IDs exceed 32 bits it should serialize the full integer", prefix: "aap-", gatewayID: 1099511627776},
		{name: "When authentication is not AAP it should omit Gateway IDs", nonAAP: true, prefix: "aap-", gatewayID: 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			gatewayOrgs := []*aap.AAPOrganization{
				{ID: tt.gatewayID, Name: "Example Org"},
				{Name: "Missing"}, {ID: -2, Name: "Negative"},
				{ID: 90, Name: "Conflicting"},
				{ID: 74, Name: "Incomplete"}, {Name: "Incomplete"}, {ID: 74, Name: "Incomplete"},
				{ID: 82, Name: "Duplicate One"}, {ID: 82, Name: "Duplicate Two"},
			}
			teamOrgs := []*aap.AAPOrganization{
				{ID: 73, Name: "Team Org"}, {ID: tt.gatewayID, Name: "Example Org"},
				{ID: 92, Name: "Cross-list conflict"},
			}
			gatewayOrgs = append(gatewayOrgs, &aap.AAPOrganization{ID: 93, Name: "Cross-list conflict"}, &aap.AAPOrganization{ID: 91, Name: "Conflicting"})
			calls := map[string]int{}
			var callsMu sync.Mutex
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callsMu.Lock()
				calls[r.URL.Path]++
				callsMu.Unlock()
				require.Equal(t, "Bearer synthetic-token", r.Header.Get("Authorization"))
				var results any
				switch r.URL.Path {
				case "/api/gateway/v1/me/":
					results = []aap.AAPUser{{ID: 7, Username: "example-user", IsSuperuser: tt.superuser, IsPlatformAuditor: tt.auditor,
						SummaryFields: aap.AAPUserSummaryFields{Resource: aap.AAPResourceSummary{AnsibleID: "example-user-resource"}}}}
				case "/api/gateway/v1/organizations":
					all := append([]*aap.AAPOrganization{}, gatewayOrgs...)
					results = append(all, teamOrgs...)
				case "/api/gateway/v1/users/7/organizations":
					results = gatewayOrgs
				case "/api/gateway/v1/users/7/teams":
					teams := make([]aap.AAPTeam, 0, len(teamOrgs))
					for _, org := range teamOrgs {
						teams = append(teams, aap.AAPTeam{ID: 8, SummaryFields: aap.AAPTeamSummaryFields{Organization: *org,
							Resource: aap.AAPResourceSummary{AnsibleID: "example-team-resource"}}})
					}
					results = teams
				case "/api/controller/v2/role_user_assignments/":
					roles := make([]aap.AAPRoleUserAssignment, 0, len(gatewayOrgs))
					for _, org := range gatewayOrgs {
						roles = append(roles, aap.AAPRoleUserAssignment{ContentType: "shared.organization",
							SummaryFields: aap.AAPRoleUserAssignmentSummaryFields{
								ContentObject:  aap.AAPContentObject{ID: 999, Name: org.Name},
								RoleDefinition: aap.AAPRoleDefinition{Name: api.ExternalRoleViewer}}})
					}
					results = roles
				case "/api/controller/v2/role_team_assignments/":
					results = []aap.AAPRoleTeamAssignment{{ContentType: "shared.organization",
						SummaryFields: aap.AAPRoleTeamAssignmentSummaryFields{
							ContentObject:  aap.AAPContentObject{ID: 888, Name: "Team Org"},
							RoleDefinition: aap.AAPRoleDefinition{Name: api.ExternalRoleOperator}}}}
				default:
					t.Errorf("unexpected Gateway request: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				var next *string
				switch items := results.(type) {
				case []*aap.AAPOrganization:
					results, next = aapOrganizationTestPage(r, items)
				case []aap.AAPTeam:
					results, next = aapOrganizationTestPage(r, items)
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"results": results, "next": next}))
			}))
			defer gateway.Close()
			provider, err := authn.NewAapGatewayAuth(api.ObjectMeta{}, api.AapProviderSpec{
				ApiUrl: gateway.URL, OrganizationNamePrefix: &tt.prefix,
			}, &tls.Config{MinVersion: tls.VersionTLS12}, 0)
			require.NoError(t, err)
			var authIdentity common.Identity
			if tt.nonAAP {
				authIdentity = common.NewBaseIdentityWithIssuer("example-user", "7", []common.ReportedOrganization{
					{Name: tt.prefix + "Example Org", ID: tt.prefix + "Example Org"},
				}, identity.NewIssuer(identity.AuthTypeOIDC, "https://issuer.example.com"))
			} else {
				authIdentity, err = provider.GetIdentity(ctx, "synthetic-token")
				require.NoError(t, err)
				cached, cacheErr := provider.GetIdentity(ctx, "synthetic-token")
				require.NoError(t, cacheErr)
				require.Same(t, authIdentity, cached)
			}

			orgNames := []string{"Example Org", "Team Org", "Missing", "Negative", "Conflicting", "Duplicate One", "Duplicate Two", "Cross-list conflict", "Incomplete"}
			orgs := make([]*model.Organization, 0, len(orgNames)+1)
			for _, name := range orgNames {
				orgs = append(orgs, &model.Organization{ID: uuid.New(), ExternalID: tt.prefix + name, DisplayName: tt.prefix + name})
			}
			// Same display name and a numeric external key must never supply metadata.
			unknown := &model.Organization{ID: uuid.New(), ExternalID: "42", DisplayName: tt.prefix + "Example Org"}
			orgs = append(orgs, unknown)
			orgService := organizationservice.NewServiceHandler(&organizationReadStore{orgs: orgs})
			log := logrus.New()
			mapper := service.NewIdentityMapper(orgService, nil, log)
			mapping := middleware.NewIdentityMappingMiddleware(mapper, log)
			transport := &TransportHandler{organization: orgService, converter: convert.NewConverter()}
			multi := common.NewMockMultiAuthNMiddleware(gomock.NewController(t))
			multi.EXPECT().GetAuthToken(gomock.Any()).Return("synthetic-token", nil).Times(2)
			var authProvider common.AuthNMiddleware = provider
			if tt.nonAAP {
				otherProvider := common.NewMockAuthNMiddleware(gomock.NewController(t))
				otherProvider.EXPECT().GetIdentity(gomock.Any(), "synthetic-token").Return(authIdentity, nil).Times(2)
				authProvider = otherProvider
			}
			multi.EXPECT().ValidateTokenAndGetProvider(gomock.Any(), "synthetic-token").Return(authProvider, nil).Times(2)
			checkRoles := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mapped, ok := contextutil.GetMappedIdentityFromContext(r.Context())
					require.True(t, ok)
					if !tt.nonAAP {
						require.Contains(t, mapped.OrgRoles[orgs[2].ID.String()], api.RoleViewer)
						require.Contains(t, mapped.OrgRoles[orgs[4].ID.String()], api.RoleViewer)
						require.Contains(t, mapped.OrgRoles[orgs[1].ID.String()], api.RoleOperator)
					}
					next.ServeHTTP(w, r)
				})
			}
			endpoint := newAAPOrganizationTestRoute(t, multi, mapping, transport, log, checkRoles)
			for _, filtered := range []bool{false, true} {
				url := "/api/v1/organizations"
				if filtered {
					url += "?fieldSelector=" + orgs[0].ID.String() + "," + unknown.ID.String()
				}
				response := httptest.NewRecorder()
				endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				var list api.OrganizationList
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &list))
				expectedCount := len(orgNames)
				if tt.nonAAP || filtered {
					expectedCount = 1
				}
				if tt.superuser {
					expectedCount++
				}
				require.Len(t, list.Items, expectedCount)
				wantIDs := map[string]int64{}
				if !tt.nonAAP {
					wantIDs[tt.prefix+"Example Org"] = int64(tt.gatewayID)
					wantIDs[tt.prefix+"Team Org"] = 73
				}
				assertOrganizationMetadata(t, response.Body.String(), orgs, wantIDs)
				t.Logf("filtered=%t response=%s", filtered, strings.TrimSpace(response.Body.String()))
			}
			callsMu.Lock()
			defer callsMu.Unlock()
			if !tt.nonAAP {
				if tt.superuser || tt.auditor {
					require.Equal(t, 2, calls["/api/gateway/v1/organizations"])
					require.Zero(t, calls["/api/gateway/v1/users/7/organizations"])
					require.Equal(t, 2, calls["/api/gateway/v1/users/7/teams"])
				} else {
					require.Zero(t, calls["/api/gateway/v1/organizations"])
					require.Equal(t, 2, calls["/api/gateway/v1/users/7/organizations"])
					require.Equal(t, 4, calls["/api/gateway/v1/users/7/teams"])
				}
			}
		})
	}
}

// Decode raw JSON as well as API types to distinguish an absent field from null
// and to verify that the wire value is a JSON integer rather than a string.
func assertOrganizationMetadata(t *testing.T, body string, orgs []*model.Organization, wantIDs map[string]int64) {
	t.Helper()
	var result struct {
		Items []struct {
			Metadata api.ObjectMeta             `json:"metadata"`
			Spec     map[string]json.RawMessage `json:"spec"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	for _, item := range result.Items {
		var externalID, displayName string
		require.NoError(t, json.Unmarshal(item.Spec["externalId"], &externalID))
		require.NoError(t, json.Unmarshal(item.Spec["displayName"], &displayName))
		var original *model.Organization
		for _, candidate := range orgs {
			if candidate.ExternalID == externalID {
				original = candidate
				break
			}
		}
		require.NotNil(t, original)
		require.Equal(t, original.ID.String(), *item.Metadata.Name)
		require.Equal(t, original.DisplayName, displayName)
		idJSON, present := item.Spec["aapOrganizationId"]
		if wantID, expected := wantIDs[externalID]; expected {
			require.True(t, present)
			var actualID int64
			require.NoError(t, json.Unmarshal(idJSON, &actualID))
			require.Equal(t, wantID, actualID)
		} else {
			require.False(t, present, "unexpected AAP ID for %s: %s", externalID, idJSON)
		}
	}
}

func aapOrganizationTestPage[T any](r *http.Request, items []T) ([]T, *string) {
	split := (len(items) + 1) / 2
	if r.URL.Query().Get("page") == "2" {
		return items[split:], nil
	}
	next := r.URL.Path + "?page=2"
	return items[:split], &next
}

func newAAPOrganizationTestRoute(t *testing.T, multi common.MultiAuthNMiddleware, mapping *middleware.IdentityMappingMiddleware, transport *TransportHandler, log logrus.FieldLogger, checkRoles func(http.Handler) http.Handler) http.Handler {
	t.Helper()
	authZ, err := auth.InitMultiAuthZ(config.NewDefault(), log)
	require.NoError(t, err)
	spec, err := api.GetSpec()
	require.NoError(t, err)
	apiRouter := versioning.NewRouter(versioning.RouterConfig{
		Middlewares: []versioning.Middleware{oapimiddleware.OapiRequestValidatorWithOptions(spec, &oapimiddleware.Options{SilenceServersWarning: true})},
		RegisterRoutes: func(r chi.Router) {
			server.HandlerWithOptions(transport, server.ChiServerOptions{BaseRouter: r})
		},
	})
	negotiator := versioning.NewNegotiator(versioning.V1Beta1, server.MetadataResolver)
	negotiated, err := versioning.NewNegotiatedRouter(negotiator.NegotiateMiddleware, map[versioning.Version]chi.Router{versioning.V1Beta1: apiRouter}, versioning.V1Beta1)
	require.NoError(t, err)
	router := chi.NewRouter()
	router.Route(server.ServerUrlApiv1, func(r chi.Router) {
		r.Use(auth.CreateAuthNMiddleware(multi, log), mapping.MapIdentityToDB,
			middleware.ExtractAndValidateOrg(middleware.QueryOrgIDExtractor, log),
			auth.CreateAuthZMiddleware(authZ, log), checkRoles)
		r.Mount("/", negotiated)
	})
	return router
}
