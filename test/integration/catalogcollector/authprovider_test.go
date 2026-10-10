package catalogcollector_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	// authClientID is the client ID the API server is configured with. The
	// userinfo validation path never sends it anywhere; the provider spec
	// simply requires one.
	authClientID = "flightctl-catalog-collector-tests"

	// authOrganization is the organization every authenticated caller is
	// assigned to. The API server provisions it on first use.
	authOrganization = "catalog-collector-integration"

	// authUsername identifies the caller the collector and the harness share.
	// It is the value the userinfo endpoint reports.
	authUsername = "catalog-collector"

	// authAccessToken is the one credential the fake provider accepts. It is
	// deliberately opaque: a JWT carrying an issuer would be routed to the
	// OIDC branch of the server's provider lookup, which has no provider
	// configured in this suite.
	authAccessToken = "catalog-collector-opaque-access-token" //nolint:gosec

	// userinfoPath is the OAuth2 userinfo endpoint the API server validates
	// bearer tokens against.
	userinfoPath = "/userinfo"

	// usernameClaim is the userinfo field the API server reads the caller's
	// username from.
	usernameClaim = "preferred_username"
)

// testAuthProvider is a minimal OAuth2 provider: a single /userinfo endpoint
// that accepts one opaque bearer token.
//
// The Flight Control API server authenticates every request, so the suite
// needs a provider rather than a middleware stub. The server's OAuth2
// provider validates a token by calling GET /userinfo with it and treating a
// 200 plus a JSON body as proof of a valid token, so this is the whole
// protocol surface the suite has to implement. Every server-side auth path -
// token extraction, validation, identity construction, role and organization
// assignment, and authorization - runs unchanged.
type testAuthProvider struct {
	// URL is the base URL of the provider.
	URL string

	// Token is the bearer token the suite's caller presents.
	Token string

	server *httptest.Server
}

// startTestAuthProvider serves the userinfo endpoint over plain HTTP on an
// ephemeral loopback port.
func startTestAuthProvider() *testAuthProvider {
	provider := &testAuthProvider{Token: authAccessToken}

	mux := http.NewServeMux()
	mux.HandleFunc(userinfoPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+authAccessToken {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"sub":         authUsername,
			usernameClaim: authUsername,
		})
	})

	provider.server = httptest.NewServer(mux)
	provider.URL = provider.server.URL

	return provider
}

// Close shuts the provider down.
func (p *testAuthProvider) Close() {
	if p.server != nil {
		p.server.Close()
	}
}

// UserinfoURL returns the endpoint the API server validates tokens against.
func (p *testAuthProvider) UserinfoURL() string { return p.URL + userinfoPath }

// ProviderSpec returns the static provider configuration the API server is
// started with so that it trusts this provider.
//
// Enabled is set explicitly: the server applies its enabled-by-default rule
// while completing the configuration, which happens before the harness
// attaches this provider, so a nil value would leave the provider disabled and
// silently excluded from token validation.
//
// Introspection is deliberately left unset so that the server validates tokens
// through the userinfo endpoint.
//
// Every authenticated caller is granted the organization administrator role in
// a single organization: the collector needs to create, update, and delete
// catalogs and catalog items, and the suite asserts on resources from the same
// organization through the same API. The role is named the way an identity
// provider would report it, so the server's own role mapping runs unchanged.
func (p *testAuthProvider) ProviderSpec() (apiv1beta1.OAuth2ProviderSpec, error) {
	var roles apiv1beta1.AuthRoleAssignment
	if err := roles.FromAuthStaticRoleAssignment(apiv1beta1.AuthStaticRoleAssignment{
		Type:  apiv1beta1.AuthStaticRoleAssignmentTypeStatic,
		Roles: []string{apiv1beta1.ExternalRoleOrgAdmin},
	}); err != nil {
		return apiv1beta1.OAuth2ProviderSpec{}, fmt.Errorf("building role assignment: %w", err)
	}

	var organizations apiv1beta1.AuthOrganizationAssignment
	if err := organizations.FromAuthStaticOrganizationAssignment(
		apiv1beta1.AuthStaticOrganizationAssignment{
			Type:             apiv1beta1.AuthStaticOrganizationAssignmentTypeStatic,
			OrganizationName: authOrganization,
		}); err != nil {
		return apiv1beta1.OAuth2ProviderSpec{}, fmt.Errorf("building organization assignment: %w", err)
	}

	return apiv1beta1.OAuth2ProviderSpec{
		ProviderType:           apiv1beta1.Oauth2,
		AuthorizationUrl:       p.URL + "/authorize",
		TokenUrl:               p.URL + "/token",
		UserinfoUrl:            p.UserinfoURL(),
		Issuer:                 lo.ToPtr(p.URL),
		ClientId:               authClientID,
		ClientSecret:           "unused-by-userinfo-validation",
		Enabled:                lo.ToPtr(true),
		UsernameClaim:          lo.ToPtr([]string{usernameClaim}),
		RoleAssignment:         roles,
		OrganizationAssignment: organizations,
	}, nil
}

// getUserinfo issues a GET against the fake provider's userinfo endpoint,
// optionally presenting a bearer token, and returns the status code and body.
func getUserinfo(authorization string) (int, string) {
	GinkgoHelper()

	req, err := http.NewRequestWithContext(
		suiteCtx, http.MethodGet, suiteAuthProvider.UserinfoURL(), nil)
	Expect(err).ToNot(HaveOccurred())
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	resp, err := newBoundedHTTPClient(probeTimeout).Do(req)
	Expect(err).ToNot(HaveOccurred())
	defer func() { Expect(resp.Body.Close()).To(Succeed()) }()

	body, err := io.ReadAll(resp.Body)
	Expect(err).ToNot(HaveOccurred())

	return resp.StatusCode, string(body)
}

var _ = Describe("Test OAuth2 userinfo provider", func() {
	Context("when a request carries no Authorization header", func() {
		It("should reject it as unauthenticated", func() {
			status, _ := getUserinfo("")
			Expect(status).To(Equal(http.StatusUnauthorized))
		})
	})

	Context("when a request carries the wrong bearer token", func() {
		It("should reject it as unauthenticated", func() {
			status, _ := getUserinfo("Bearer not-" + authAccessToken)
			Expect(status).To(Equal(http.StatusUnauthorized))
		})
	})

	Context("when a request carries the expected bearer token", func() {
		It("should report the suite's username", func() {
			status, body := getUserinfo("Bearer " + suiteAuthProvider.Token)
			Expect(status).To(Equal(http.StatusOK))

			var claims map[string]string
			Expect(json.Unmarshal([]byte(body), &claims)).To(Succeed())
			Expect(claims).To(HaveKeyWithValue(usernameClaim, authUsername))
		})
	})
})
