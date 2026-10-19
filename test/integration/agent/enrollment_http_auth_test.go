package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

type enrollmentManagementHTTPClient struct {
	client apiclient.HttpRequestDoer
	token  string
}

func (client enrollmentManagementHTTPClient) Do(request *http.Request) (*http.Response, error) {
	request.Header.Set("Authorization", "Bearer "+client.token)
	return client.client.Do(request)
}

func newEnrollmentHTTPAuth() (api.OIDCProviderSpec, string) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	signingKey, err := jwk.FromRaw(privateKey)
	Expect(err).NotTo(HaveOccurred())
	Expect(signingKey.Set(jwk.KeyIDKey, "enrollment-http")).To(Succeed())
	Expect(signingKey.Set(jwk.AlgorithmKey, jwa.ES256)).To(Succeed())
	publicKey, err := signingKey.PublicKey()
	Expect(err).NotTo(HaveOccurred())
	keys := jwk.NewSet()
	Expect(keys.AddKey(publicKey)).To(Succeed())
	jwks, err := json.Marshal(keys)
	Expect(err).NotTo(HaveOccurred())

	mux := http.NewServeMux()
	issuer := httptest.NewServer(mux)
	DeferCleanup(issuer.Close)
	discovery, err := json.Marshal(map[string]string{"issuer": issuer.URL, "jwks_uri": issuer.URL + "/jwks"})
	Expect(err).NotTo(HaveOccurred())
	for path, payload := range map[string][]byte{"/.well-known/openid-configuration": discovery, "/jwks": jwks} {
		mux.HandleFunc(path, func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write(payload)
		})
	}

	provider := api.OIDCProviderSpec{
		ProviderType: api.Oidc, Issuer: issuer.URL, ClientId: "enrollment-http", Enabled: lo.ToPtr(true),
		UsernameClaim: lo.ToPtr([]string{"preferred_username"}),
	}
	Expect(provider.OrganizationAssignment.FromAuthStaticOrganizationAssignment(api.AuthStaticOrganizationAssignment{
		Type: api.AuthStaticOrganizationAssignmentTypeStatic, OrganizationName: "default",
	})).To(Succeed())
	Expect(provider.RoleAssignment.FromAuthStaticRoleAssignment(api.AuthStaticRoleAssignment{
		Type: api.AuthStaticRoleAssignmentTypeStatic, Roles: []string{api.ExternalRoleAdmin},
	})).To(Succeed())
	claims := jwt.New()
	for name, value := range map[string]any{
		"iss": issuer.URL, "aud": []string{provider.ClientId}, "sub": "enrollment-http-admin",
		"preferred_username": "enrollment-http-admin", "iat": time.Now(), "exp": time.Now().Add(5 * time.Minute),
	} {
		Expect(claims.Set(name, value)).To(Succeed())
	}
	token, err := jwt.Sign(claims, jwt.WithKey(jwa.ES256, signingKey))
	Expect(err).NotTo(HaveOccurred())
	return provider, string(token)
}
