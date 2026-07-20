package v1beta1

import (
	"context"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildValidPerUserOrgAssignment returns an AuthOrganizationAssignment in the PerUser form.
func buildValidPerUserOrgAssignment() AuthOrganizationAssignment {
	a := AuthOrganizationAssignment{}
	_ = a.FromAuthPerUserOrganizationAssignment(AuthPerUserOrganizationAssignment{
		Type: PerUser,
	})
	return a
}

// buildValidDynamicRoleAssignment returns an AuthRoleAssignment in the Dynamic form.
func buildValidDynamicRoleAssignment() AuthRoleAssignment {
	r := AuthRoleAssignment{}
	_ = r.FromAuthDynamicRoleAssignment(AuthDynamicRoleAssignment{
		Type:      AuthDynamicRoleAssignmentTypeDynamic,
		ClaimPath: []string{"groups"},
	})
	return r
}

// validOIDCSpec builds a minimal OIDC spec that passes validation under a super-admin ctx.
func validOIDCSpec(issuer string) OIDCProviderSpec {
	return OIDCProviderSpec{
		ProviderType:           Oidc,
		Issuer:                 issuer,
		ClientId:               "my-client",
		OrganizationAssignment: buildValidPerUserOrgAssignment(),
		RoleAssignment:         buildValidDynamicRoleAssignment(),
	}
}

func TestValidateAbsoluteURL(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantError bool
	}{
		{"When valid HTTPS URL it should pass", "https://example.com", false},
		{"When valid HTTP URL it should pass", "http://example.com", false},
		{"When URL with path it should pass", "https://example.com/realm/master", false},
		{"When URL with port it should pass", "https://example.com:8443", false},
		{"When URL missing scheme it should fail", "example.com", true},
		{"When URL missing host it should fail", "https://", true},
		{"When relative path it should fail", "/just/a/path", true},
		{"When empty string it should fail", "", true},
		{"When plain word it should fail", "notaurl", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAbsoluteURL("field", tt.value)
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOIDCProviderSpec_Validate_URLFields(t *testing.T) {
	ctx := contextWithSuperAdmin(context.Background())

	tests := []struct {
		name        string
		spec        OIDCProviderSpec
		wantErrMsgs []string
	}{
		{
			name: "When valid spec it should pass",
			spec: validOIDCSpec("https://idp.example.com"),
		},
		{
			name:        "When issuer is empty it should fail",
			spec:        validOIDCSpec(""),
			wantErrMsgs: []string{"issuer is required"},
		},
		{
			name:        "When issuer has no scheme it should fail",
			spec:        validOIDCSpec("idp.example.com"),
			wantErrMsgs: []string{"issuer must be a valid URL"},
		},
		{
			name:        "When issuer has no host it should fail",
			spec:        validOIDCSpec("https://"),
			wantErrMsgs: []string{"issuer must be a valid URL"},
		},
		{
			name:        "When issuer is a bare word it should fail",
			spec:        validOIDCSpec("not-a-url"),
			wantErrMsgs: []string{"issuer must be a valid URL"},
		},
		{
			name: "When clientId is empty it should fail",
			spec: OIDCProviderSpec{
				ProviderType:           Oidc,
				Issuer:                 "https://idp.example.com",
				OrganizationAssignment: buildValidPerUserOrgAssignment(),
				RoleAssignment:         buildValidDynamicRoleAssignment(),
			},
			wantErrMsgs: []string{"clientId is required"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.spec.Validate(ctx, false)
			allErrs := ""
			for _, e := range errs {
				allErrs += e.Error() + "; "
			}
			if len(tt.wantErrMsgs) == 0 {
				assert.Empty(t, errs, "unexpected errors: %s", allErrs)
				return
			}
			for _, want := range tt.wantErrMsgs {
				assert.Contains(t, allErrs, want)
			}
		})
	}
}

func TestOAuth2ProviderSpec_Validate_URLFields(t *testing.T) {
	ctx := contextWithSuperAdmin(context.Background())

	validRfc7662Introspection := func() *OAuth2Introspection {
		i := &OAuth2Introspection{}
		require.NoError(t, i.FromRfc7662IntrospectionSpec(Rfc7662IntrospectionSpec{
			Type: Rfc7662,
			Url:  "https://idp.example.com/introspect",
		}))
		return i
	}

	baseSpec := func() OAuth2ProviderSpec {
		return OAuth2ProviderSpec{
			ProviderType:           Oauth2,
			AuthorizationUrl:       "https://idp.example.com/authorize",
			TokenUrl:               "https://idp.example.com/token",
			UserinfoUrl:            "https://idp.example.com/userinfo",
			ClientId:               "my-client",
			Introspection:          validRfc7662Introspection(),
			OrganizationAssignment: buildValidPerUserOrgAssignment(),
			RoleAssignment:         buildValidDynamicRoleAssignment(),
		}
	}

	tests := []struct {
		name        string
		mutate      func(s *OAuth2ProviderSpec)
		wantErrMsgs []string
	}{
		{
			name:   "When valid spec it should pass",
			mutate: func(s *OAuth2ProviderSpec) {},
		},
		{
			name:        "When authorizationUrl is empty it should fail",
			mutate:      func(s *OAuth2ProviderSpec) { s.AuthorizationUrl = "" },
			wantErrMsgs: []string{"authorizationUrl is required"},
		},
		{
			name:        "When authorizationUrl has no scheme it should fail",
			mutate:      func(s *OAuth2ProviderSpec) { s.AuthorizationUrl = "idp.example.com/authorize" },
			wantErrMsgs: []string{"authorizationUrl must be a valid URL"},
		},
		{
			name:        "When tokenUrl has no scheme it should fail",
			mutate:      func(s *OAuth2ProviderSpec) { s.TokenUrl = "idp.example.com/token" },
			wantErrMsgs: []string{"tokenUrl must be a valid URL"},
		},
		{
			name:        "When userinfoUrl has no scheme it should fail",
			mutate:      func(s *OAuth2ProviderSpec) { s.UserinfoUrl = "idp.example.com/userinfo" },
			wantErrMsgs: []string{"userinfoUrl must be a valid URL"},
		},
		{
			name:        "When optional issuer is set to invalid URL it should fail",
			mutate:      func(s *OAuth2ProviderSpec) { s.Issuer = lo.ToPtr("not-a-url") },
			wantErrMsgs: []string{"issuer must be a valid URL"},
		},
		{
			name:        "When introspection is nil it should fail",
			mutate:      func(s *OAuth2ProviderSpec) { s.Introspection = nil },
			wantErrMsgs: []string{"introspection field is required"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := baseSpec()
			tt.mutate(&spec)
			errs := spec.Validate(ctx, false)
			allErrs := ""
			for _, e := range errs {
				allErrs += e.Error() + "; "
			}
			if len(tt.wantErrMsgs) == 0 {
				assert.Empty(t, errs, "unexpected errors: %s", allErrs)
				return
			}
			for _, want := range tt.wantErrMsgs {
				assert.Contains(t, allErrs, want)
			}
		})
	}
}

func TestAuthProviderSpec_Validate_ConfigOnlyTypes(t *testing.T) {
	ctx := contextWithSuperAdmin(context.Background())

	t.Run("When K8s provider type it should be rejected via API", func(t *testing.T) {
		spec := AuthProviderSpec{}
		_ = spec.FromK8sProviderSpec(K8sProviderSpec{ProviderType: K8s, ApiUrl: "https://k8s.example.com"})
		errs := spec.Validate(ctx, false)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "configuration")
	})

	t.Run("When AAP provider type it should be rejected via API", func(t *testing.T) {
		spec := AuthProviderSpec{}
		_ = spec.FromAapProviderSpec(AapProviderSpec{
			ProviderType:     Aap,
			ApiUrl:           "https://aap.example.com",
			AuthorizationUrl: "https://aap.example.com/authorize",
			TokenUrl:         "https://aap.example.com/token",
			ClientId:         "client",
			ClientSecret:     "secret",
			Scopes:           []string{"api"},
		})
		errs := spec.Validate(ctx, false)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "configuration")
	})
}

func TestAuthProvider_Validate_E2E(t *testing.T) {
	ctx := contextWithSuperAdmin(context.Background())

	t.Run("When valid OIDC AuthProvider it should pass", func(t *testing.T) {
		spec := AuthProviderSpec{}
		_ = spec.FromOIDCProviderSpec(validOIDCSpec("https://idp.example.com"))
		ap := AuthProvider{
			Metadata: ObjectMeta{Name: lo.ToPtr("my-oidc")},
			Spec:     spec,
		}
		errs := ap.Validate(ctx)
		assert.Empty(t, errs)
	})

	t.Run("When OIDC AuthProvider with invalid issuer URL it should fail", func(t *testing.T) {
		spec := AuthProviderSpec{}
		_ = spec.FromOIDCProviderSpec(validOIDCSpec("not-a-url"))
		ap := AuthProvider{
			Metadata: ObjectMeta{Name: lo.ToPtr("my-oidc")},
			Spec:     spec,
		}
		errs := ap.Validate(ctx)
		require.NotEmpty(t, errs)
		allErrs := ""
		for _, e := range errs {
			allErrs += e.Error() + "; "
		}
		assert.Contains(t, allErrs, "issuer must be a valid URL")
	})

	t.Run("When OIDC AuthProvider with missing name it should fail", func(t *testing.T) {
		spec := AuthProviderSpec{}
		_ = spec.FromOIDCProviderSpec(validOIDCSpec("https://idp.example.com"))
		ap := AuthProvider{
			Spec: spec,
		}
		errs := ap.Validate(ctx)
		require.NotEmpty(t, errs)
	})
}
