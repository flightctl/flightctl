package v1beta1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func promptTestProviderSpec(t *testing.T, providerType string, prompt *string) AuthProviderSpec {
	t.Helper()
	oidc := validOIDCSpec("https://idp.example.com")
	oidc.Prompt = prompt
	spec := AuthProviderSpec{}
	if providerType == "oidc" {
		require.NoError(t, spec.FromOIDCProviderSpec(oidc))
		return spec
	}
	introspection := OAuth2Introspection{}
	require.NoError(t, introspection.FromRfc7662IntrospectionSpec(Rfc7662IntrospectionSpec{
		Type: Rfc7662,
		Url:  "https://idp.example.com/introspect",
	}))
	require.NoError(t, spec.FromOAuth2ProviderSpec(OAuth2ProviderSpec{
		ProviderType:           Oauth2,
		AuthorizationUrl:       "https://idp.example.com/authorize",
		TokenUrl:               "https://idp.example.com/token",
		UserinfoUrl:            "https://idp.example.com/userinfo",
		ClientId:               oidc.ClientId,
		OrganizationAssignment: oidc.OrganizationAssignment,
		RoleAssignment:         oidc.RoleAssignment,
		Introspection:          &introspection,
		Prompt:                 prompt,
	}))
	return spec
}

func TestAuthProviderSpec_Validate_Prompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt *string
		err    string
	}{
		{"When prompt is omitted it should pass", nil, ""},
		{"When prompt is empty it should pass", lo.ToPtr(""), ""},
		{"When prompt is login it should pass", lo.ToPtr("login"), ""},
		{"When prompt is consent it should pass", lo.ToPtr("consent"), ""},
		{"When prompt is select_account it should pass", lo.ToPtr("select_account"), ""},
		{"When prompt is none it should pass", lo.ToPtr("none"), ""},
		{"When prompt combines values it should pass", lo.ToPtr("login consent"), ""},
		{"When prompt has extra spaces it should preserve them", lo.ToPtr("  login  consent "), ""},
		{"When prompt repeats values it should pass", lo.ToPtr("login login"), ""},
		{"When prompt repeats none it should pass", lo.ToPtr(" none  none "), ""},
		{"When prompt uses extensions it should pass", lo.ToPtr("custom:prompt custom-prompt"), ""},
		{"When prompt uses different casing it should treat it as an extension", lo.ToPtr("NONE login"), ""},
		{"When none precedes another value it should fail", lo.ToPtr("none login"), "none cannot be combined"},
		{"When none follows another value it should fail", lo.ToPtr("consent  none"), "none cannot be combined"},
		{"When none accompanies an extension it should fail", lo.ToPtr("none custom"), "none cannot be combined"},
		{"When prompt contains only spaces it should fail", lo.ToPtr("   "), "only spaces"},
		{"When prompt contains a tab it should fail", lo.ToPtr("login\tconsent"), "printable ASCII"},
		{"When prompt contains a newline it should fail", lo.ToPtr("login\n"), "printable ASCII"},
		{"When prompt contains a carriage return it should fail", lo.ToPtr("login\r"), "printable ASCII"},
		{"When prompt contains a control character it should fail", lo.ToPtr("login\x00"), "printable ASCII"},
		{"When prompt contains DEL it should fail", lo.ToPtr("login\x7f"), "printable ASCII"},
		{"When prompt contains non-ASCII text it should fail", lo.ToPtr("lögin"), "printable ASCII"},
	}
	ctx := contextWithSuperAdmin(context.Background())
	for _, providerType := range []string{"oidc", "oauth2"} {
		t.Run(providerType, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					spec := promptTestProviderSpec(t, providerType, tt.prompt)
					oldSpec := promptTestProviderSpec(t, providerType, nil)
					before, err := json.Marshal(spec)
					require.NoError(t, err)
					for _, errs := range [][]error{spec.Validate(ctx, false), spec.ValidateUpdate(ctx, &oldSpec)} {
						if tt.err == "" {
							require.Empty(t, errs)
						} else {
							require.Len(t, errs, 1)
							require.Contains(t, errs[0].Error(), tt.err)
						}
					}
					after, err := json.Marshal(spec)
					require.NoError(t, err)
					require.JSONEq(t, string(before), string(after))
				})
			}
		})
	}
}

func TestAuthProviderPrompt_HTTPPreservesOmittedPrompt(t *testing.T) {
	swagger, err := GetSpec()
	require.NoError(t, err)
	router, err := gorillamux.NewRouter(swagger)
	require.NoError(t, err)
	tests := []struct {
		name    string
		include bool
		value   any
		want    any
	}{
		{"When prompt is omitted it should preserve the omission", false, nil, nil},
		{"When prompt is null it should preserve null", true, nil, nil},
		{"When prompt is empty it should remain empty", true, "", ""},
		{"When prompt is configured it should preserve its value", true, " login  consent ", " login  consent "},
	}
	for _, providerType := range []string{"oidc", "oauth2"} {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			t.Run(providerType+"/"+method, func(t *testing.T) {
				for _, tt := range tests {
					t.Run(tt.name, func(t *testing.T) {
						provider := AuthProvider{
							ApiVersion: AuthProviderAPIVersion,
							Kind:       AuthProviderKind,
							Metadata:   ObjectMeta{Name: lo.ToPtr("prompt-test")},
							Spec:       promptTestProviderSpec(t, providerType, nil),
						}
						body, err := json.Marshal(provider)
						require.NoError(t, err)
						var document map[string]any
						require.NoError(t, json.Unmarshal(body, &document))
						if tt.include {
							document["spec"].(map[string]any)["prompt"] = tt.value
						}
						body, err = json.Marshal(document)
						require.NoError(t, err)
						path := "/api/v1/authproviders"
						if method == http.MethodPut {
							path += "/prompt-test"
						}
						request := httptest.NewRequest(method, path, bytes.NewReader(body))
						request.Header.Set("Content-Type", "application/json")
						route, params, err := router.FindRoute(request)
						require.NoError(t, err)
						require.NoError(t, openapi3filter.ValidateRequest(t.Context(), &openapi3filter.RequestValidationInput{
							Request: request, PathParams: params, Route: route,
							Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
						}))
						require.NoError(t, json.NewDecoder(request.Body).Decode(&document))
						require.Equal(t, tt.want, document["spec"].(map[string]any)["prompt"])
					})
				}
			})
		}
	}
}
