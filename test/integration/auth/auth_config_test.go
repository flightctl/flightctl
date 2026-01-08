package auth_test

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

func TestAuthConfigIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Auth Integration Suite")
}

var _ = Describe("Auth Config Integration Tests", func() {
	var (
		ctx            context.Context
		serviceHandler service.Service
		multiAuth      *authn.MultiAuth
	)

	BeforeEach(func() {
		ctx = context.Background()
		log := util.InitLogsWithDebug()

		// Setup test database and service handler
		testStore, _, _, _ := store.PrepareDBForUnitTests(ctx, log)

		// Add admin identity to context for auth provider operations
		testOrg := &model.Organization{
			ID:          store.NullOrgId,
			ExternalID:  "test-org",
			DisplayName: "Test Organization",
		}
		adminIdentity := &identity.MappedIdentity{
			Username:      "test-admin",
			UID:           uuid.New().String(),
			Organizations: []*model.Organization{testOrg},
			OrgRoles:      map[string][]string{"*": {string(api.RoleAdmin)}},
			SuperAdmin:    true, // Super admin required for creating auth providers with dynamic org assignment
		}
		ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, adminIdentity)

		// Create service handler (it implements AuthProviderService interface)
		serviceHandler = service.NewServiceHandler(testStore, nil, nil, nil, log, "", "", []string{})

		// Create an auth config with static OIDC provider to test production initialization path
		authCfg := common.NewAuthWithOIDC("https://static-oidc.example.com", "", true)

		// Initialize MultiAuth using production code path
		var err error
		authN, err := auth.InitMultiAuth(authCfg, log, serviceHandler)
		Expect(err).ToNot(HaveOccurred())
		Expect(authN).ToNot(BeNil(), "Expected auth instance")
		multiAuth = authN
	})

	AfterEach(func() {
		// MultiAuth cleanup handled by context cancellation
	})

	Context("GetAuthConfig with only static providers", func() {
		It("should return static OIDC provider from config via service handler", func() {
			// Get auth config from multiAuth (simulates what transport layer does)
			authConfig := multiAuth.GetAuthConfig()

			// Call service handler GetAuthConfig (simulates the API endpoint)
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config).ToNot(BeNil())
			Expect(config.DefaultProvider).ToNot(BeNil())
			Expect(*config.DefaultProvider).To(Equal("oidc"))
			Expect(config.Providers).ToNot(BeNil())
			Expect(len(*config.Providers)).To(Equal(1))

			// Verify the static provider
			staticProvider := (*config.Providers)[0]
			Expect(staticProvider.Metadata.Name).ToNot(BeNil())
			Expect(*staticProvider.Metadata.Name).To(Equal("oidc"))

			// Verify the spec is OIDC type
			discriminator, err := staticProvider.Spec.Discriminator()
			Expect(err).ToNot(HaveOccurred())
			Expect(discriminator).To(Equal("oidc"))

			// Get OIDC spec and verify issuer
			oidcSpec, err := staticProvider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(oidcSpec.Issuer).To(Equal("https://static-oidc.example.com"))

			// Verify default provider is set correctly
			Expect(config.DefaultProvider).ToNot(BeNil())
			Expect(*config.DefaultProvider).To(Equal("oidc"), "First provider should be default")
		})

		It("should support multiple static providers of different types via service handler", func() {
			// Re-initialize with OIDC + AAP static provider types
			// Note: K8s auth requires in-cluster env vars or token file, so we test only OIDC + AAP
			authCfg := common.NewAuthWithOIDC("https://static-oidc.example.com", "", true).
				WithAAP("https://aap-gateway.example.com", true)

			log := util.InitLogsWithDebug()
			authN, err := auth.InitMultiAuth(authCfg, log, serviceHandler)
			Expect(err).ToNot(HaveOccurred())
			Expect(authN).ToNot(BeNil())
			multiAuthWithAll := authN

			// Get auth config via service handler
			authConfig := multiAuthWithAll.GetAuthConfig()
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config).ToNot(BeNil())
			Expect(config.Providers).ToNot(BeNil())
			Expect(len(*config.Providers)).To(Equal(2), "Should have OIDC + AAP")

			// Count providers by type
			providersByType := make(map[string]int)
			for _, p := range *config.Providers {
				discriminator, err := p.Spec.Discriminator()
				if err == nil {
					providersByType[discriminator]++
				}
			}

			Expect(providersByType["oidc"]).To(Equal(1))
			Expect(providersByType["aap"]).To(Equal(1))
		})
	})

	Context("GetAuthConfig with dynamic providers", func() {
		It("should return both static and dynamic AuthProviders after creation via service handler", func() {
			// Create an AuthProvider resource via service
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "test-dynamic-provider", "https://accounts.google.com", nil)

			_, createStatus := serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, provider)
			Expect(createStatus.Code).To(Equal(int32(201)))

			// Trigger sync to load dynamic providers
			err := multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Get auth config via service handler (simulates /auth/config endpoint)
			authConfig := multiAuth.GetAuthConfig()
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config).ToNot(BeNil())
			Expect(config.Providers).ToNot(BeNil())
			Expect(len(*config.Providers)).To(Equal(2), "Should have 1 static + 1 dynamic provider")

			// Find the static and dynamic providers
			var staticFound *api.AuthProvider
			var dynamicFound *api.AuthProvider
			for i := range *config.Providers {
				p := (*config.Providers)[i]
				if p.Metadata.Name != nil && *p.Metadata.Name == "oidc" {
					staticFound = &p
				}
				if p.Metadata.Name != nil && *p.Metadata.Name == "test-dynamic-provider" {
					dynamicFound = &p
				}
			}

			// Verify static provider
			Expect(staticFound).ToNot(BeNil(), "Static provider should be in auth config")

			// Verify dynamic provider
			Expect(dynamicFound).ToNot(BeNil(), "Dynamic provider should be in auth config")
			dynamicSpec, err := dynamicFound.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(dynamicSpec.Issuer).To(Equal("https://accounts.google.com"))

			discriminator, err := dynamicFound.Spec.Discriminator()
			Expect(err).ToNot(HaveOccurred())
			Expect(discriminator).To(Equal("oidc"))

			// Verify default provider is set to static provider
			Expect(authConfig.DefaultProvider).ToNot(BeNil())
			Expect(*authConfig.DefaultProvider).To(Equal("oidc"), "Static provider should be default")
		})

		It("should return multiple AuthProviders after creation via service handler", func() {
			// Create multiple AuthProvider resources via service
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "provider-1", "https://accounts.google.com", nil)
			_, createStatus := serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, provider1)
			Expect(createStatus.Code).To(Equal(int32(201)))

			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "provider-2", "https://login.microsoftonline.com", nil)
			_, createStatus = serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, provider2)
			Expect(createStatus.Code).To(Equal(int32(201)))

			provider3 := util.ReturnTestAuthProvider(store.NullOrgId, "provider-3", "https://auth.example.com", nil)
			_, createStatus = serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, provider3)
			Expect(createStatus.Code).To(Equal(int32(201)))

			// Trigger sync
			err := multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Get auth config via service handler
			authConfig := multiAuth.GetAuthConfig()
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config).ToNot(BeNil())
			Expect(config.Providers).ToNot(BeNil())
			Expect(len(*config.Providers)).To(Equal(4), "Should have 1 static + 3 dynamic providers")

			// Verify all dynamic providers are present
			providerNames := []string{}
			for _, p := range *config.Providers {
				if p.Metadata.Name != nil {
					providerNames = append(providerNames, *p.Metadata.Name)
				}
			}

			Expect(providerNames).To(ContainElement("oidc"), "Should have static oidc provider")
			Expect(providerNames).To(ContainElement("provider-1"))
			Expect(providerNames).To(ContainElement("provider-2"))
			Expect(providerNames).To(ContainElement("provider-3"))
		})

		It("should support both OIDC and OAuth2 dynamic providers via service handler", func() {
			// Create an OIDC provider
			oidcProvider := util.ReturnTestAuthProvider(store.NullOrgId, "oidc-provider", "https://oidc.example.com", nil)
			_, createStatus := serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, oidcProvider)
			Expect(createStatus.Code).To(Equal(int32(201)))

			// Create an OAuth2 provider
			oauth2Provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-provider"),
				},
			}
			oauth2Spec := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				Issuer:                 lo.ToPtr("https://oauth2.example.com"),
				ClientId:               "oauth2-client-id",
				ClientSecret:           lo.ToPtr("oauth2-client-secret"),
				AuthorizationUrl:       "https://oauth2.example.com/authorize",
				TokenUrl:               "https://oauth2.example.com/token",
				UserinfoUrl:            "https://oauth2.example.com/userinfo",
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
				OrganizationAssignment: api.AuthOrganizationAssignment{},
			}
			staticAssignment := api.AuthStaticOrganizationAssignment{
				Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
				OrganizationName: "default-org",
			}
			err := oauth2Spec.OrganizationAssignment.FromAuthStaticOrganizationAssignment(staticAssignment)
			Expect(err).ToNot(HaveOccurred())

			roleAssignment := api.AuthRoleAssignment{}
			dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
				Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
				ClaimPath: []string{"groups"},
			}
			err = roleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)
			Expect(err).ToNot(HaveOccurred())
			oauth2Spec.RoleAssignment = roleAssignment

			err = oauth2Provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
			Expect(err).ToNot(HaveOccurred())

			_, createStatus = serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, oauth2Provider)
			Expect(createStatus.Code).To(Equal(int32(201)))

			// Trigger sync
			err = multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Get auth config via service handler
			authConfig := multiAuth.GetAuthConfig()
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config).ToNot(BeNil())
			Expect(config.Providers).ToNot(BeNil())
			Expect(len(*config.Providers)).To(Equal(3), "Should have 1 static + 1 OIDC + 1 OAuth2")

			// Find both dynamic providers
			var oidcFound, oauth2Found *api.AuthProvider
			for i := range *config.Providers {
				p := (*config.Providers)[i]
				if p.Metadata.Name != nil {
					if *p.Metadata.Name == "oidc-provider" {
						oidcFound = &p
					}
					if *p.Metadata.Name == "oauth2-provider" {
						oauth2Found = &p
					}
				}
			}

			// Verify OIDC provider
			Expect(oidcFound).ToNot(BeNil(), "OIDC provider should be in config")
			oidcDiscriminator, err := oidcFound.Spec.Discriminator()
			Expect(err).ToNot(HaveOccurred())
			Expect(oidcDiscriminator).To(Equal("oidc"))
			oidcSpec, err := oidcFound.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(oidcSpec.Issuer).To(Equal("https://oidc.example.com"))

			// Verify OAuth2 provider
			Expect(oauth2Found).ToNot(BeNil(), "OAuth2 provider should be in config")
			oauth2Discriminator, err := oauth2Found.Spec.Discriminator()
			Expect(err).ToNot(HaveOccurred())
			Expect(oauth2Discriminator).To(Equal("oauth2"))
			oauth2Spec, err = oauth2Found.Spec.AsOAuth2ProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(oauth2Spec.Issuer).ToNot(BeNil())
			Expect(*oauth2Spec.Issuer).To(Equal("https://oauth2.example.com"))
			Expect(oauth2Spec.AuthorizationUrl).To(Equal("https://oauth2.example.com/authorize"))
			Expect(oauth2Spec.TokenUrl).To(Equal("https://oauth2.example.com/token"))
			Expect(oauth2Spec.UserinfoUrl).To(Equal("https://oauth2.example.com/userinfo"))
			Expect(oauth2Spec.Scopes).ToNot(BeNil())
			Expect(*oauth2Spec.Scopes).To(ContainElement("openid"))
		})

		It("should update config when AuthProvider is modified via service handler", func() {
			// Create initial provider
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "update-test-provider", "https://initial.example.com", nil)
			_, createStatus := serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, provider)
			Expect(createStatus.Code).To(Equal(int32(201)))

			// Sync and verify initial state
			err := multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			authConfig := multiAuth.GetAuthConfig()
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config.Providers).ToNot(BeNil())
			Expect(len(*config.Providers)).To(Equal(2), "Should have 1 static + 1 dynamic")

			// Find the dynamic provider
			var dynamicProvider *api.AuthProvider
			for i := range *config.Providers {
				p := (*config.Providers)[i]
				if p.Metadata.Name != nil && *p.Metadata.Name == "update-test-provider" {
					dynamicProvider = &p
					break
				}
			}
			Expect(dynamicProvider).ToNot(BeNil())
			dynamicSpec, err := dynamicProvider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(dynamicSpec.Issuer).To(Equal("https://initial.example.com"))

			// Update the provider with new issuer
			updatedProvider, getStatus := serviceHandler.GetAuthProvider(ctx, store.NullOrgId, "update-test-provider")
			Expect(getStatus.Code).To(Equal(int32(200)))

			oidcSpec, err := updatedProvider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec.Issuer = "https://updated.example.com"
			err = updatedProvider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			_, replaceStatus := serviceHandler.ReplaceAuthProvider(ctx, store.NullOrgId, "update-test-provider", *updatedProvider)
			Expect(replaceStatus.Code).To(Equal(int32(200)))

			// Sync again and verify updated state
			err = multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			authConfig = multiAuth.GetAuthConfig()
			config, status = serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config.Providers).ToNot(BeNil())
			Expect(len(*config.Providers)).To(Equal(2), "Should still have 1 static + 1 dynamic")

			// Find the updated dynamic provider
			dynamicProvider = nil
			for i := range *config.Providers {
				p := (*config.Providers)[i]
				if p.Metadata.Name != nil && *p.Metadata.Name == "update-test-provider" {
					dynamicProvider = &p
					break
				}
			}
			Expect(dynamicProvider).ToNot(BeNil())
			updatedSpec, err := dynamicProvider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedSpec.Issuer).To(Equal("https://updated.example.com"))
		})

		It("should remove provider from config when AuthProvider is deleted via service handler", func() {
			// Create two providers
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "delete-test-1", "https://provider1.example.com", nil)
			_, createStatus := serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, provider1)
			Expect(createStatus.Code).To(Equal(int32(201)))

			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "delete-test-2", "https://provider2.example.com", nil)
			_, createStatus = serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, provider2)
			Expect(createStatus.Code).To(Equal(int32(201)))

			// Sync and verify both present
			err := multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			authConfig := multiAuth.GetAuthConfig()
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(*config.Providers)).To(Equal(3), "Should have 1 static + 2 dynamic")

			// Delete one provider
			deleteStatus := serviceHandler.DeleteAuthProvider(ctx, store.NullOrgId, "delete-test-1")
			Expect(deleteStatus.Code).To(Equal(int32(200)))

			// Sync and verify only one dynamic remains
			err = multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			authConfig = multiAuth.GetAuthConfig()
			config, status = serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(*config.Providers)).To(Equal(2), "Should have 1 static + 1 dynamic")

			// Verify the remaining providers
			providerNames := []string{}
			for _, p := range *config.Providers {
				if p.Metadata.Name != nil {
					providerNames = append(providerNames, *p.Metadata.Name)
				}
			}
			Expect(providerNames).To(ContainElement("delete-test-2"))
			Expect(providerNames).ToNot(ContainElement("delete-test-1"))
		})

		It("should include provider client ID and all fields via service handler", func() {
			// Create provider with all fields
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "full-provider", "https://full.example.com", nil)

			_, createStatus := serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, provider)
			Expect(createStatus.Code).To(Equal(int32(201)))

			err := multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			authConfig := multiAuth.GetAuthConfig()
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config.Providers).ToNot(BeNil())
			Expect(len(*config.Providers)).To(Equal(2), "Should have 1 static + 1 dynamic")

			// Find the dynamic provider
			var dynamicProvider *api.AuthProvider
			for i := range *config.Providers {
				p := (*config.Providers)[i]
				if p.Metadata.Name != nil && *p.Metadata.Name == "full-provider" {
					dynamicProvider = &p
					break
				}
			}

			Expect(dynamicProvider).ToNot(BeNil())
			Expect(*dynamicProvider.Metadata.Name).To(Equal("full-provider"))
			spec, err := dynamicProvider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(spec.ClientId).To(Equal("test-client-id-full-provider"))
			Expect(spec.Issuer).To(Equal("https://full.example.com"))
		})

		It("should handle disabled AuthProviders via service handler", func() {
			// Create enabled provider
			enabledProvider := util.ReturnTestAuthProvider(store.NullOrgId, "enabled-provider", "https://enabled.example.com", nil)
			_, createStatus := serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, enabledProvider)
			Expect(createStatus.Code).To(Equal(int32(201)))

			// Create disabled provider
			disabledProvider := util.ReturnTestAuthProvider(store.NullOrgId, "disabled-provider", "https://disabled.example.com", nil)
			oidcSpec, err := disabledProvider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec.Enabled = lo.ToPtr(false)
			err = disabledProvider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			_, createStatus = serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, disabledProvider)
			Expect(createStatus.Code).To(Equal(int32(201)))

			// Sync - disabled providers should be filtered out from auth config
			err = multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Get auth config - should only contain enabled providers
			authConfig := multiAuth.GetAuthConfig()
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config.Providers).ToNot(BeNil())

			// Verify that enabled provider is present and disabled provider is NOT present
			var foundEnabled, foundDisabled bool
			for _, p := range *config.Providers {
				if p.Metadata.Name != nil {
					if *p.Metadata.Name == "enabled-provider" {
						foundEnabled = true
					}
					if *p.Metadata.Name == "disabled-provider" {
						foundDisabled = true
					}
				}
			}

			Expect(foundEnabled).To(BeTrue(), "Enabled provider should be in auth config")
			Expect(foundDisabled).To(BeFalse(), "Disabled provider should NOT be in auth config")

			// For authproviders list API, both should be present
			providerList, listStatus := serviceHandler.ListAuthProviders(ctx, store.NullOrgId, api.ListAuthProvidersParams{})
			Expect(listStatus.Code).To(Equal(int32(200)))
			Expect(providerList).ToNot(BeNil())
			Expect(len(providerList.Items)).To(Equal(2), "Both enabled and disabled providers should be in list API")
		})
	})

	Context("GetAuthConfig verifies DefaultProvider", func() {
		It("should correctly return static and dynamic providers with default provider set via service handler", func() {
			// Add a dynamic provider
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "dynamic-test", "https://dynamic.example.com", nil)
			_, createStatus := serviceHandler.CreateAuthProvider(ctx, store.NullOrgId, provider)
			Expect(createStatus.Code).To(Equal(int32(201)))

			err := multiAuth.LoadAllAuthProviders(ctx)
			Expect(err).ToNot(HaveOccurred())

			authConfig := multiAuth.GetAuthConfig()
			config, status := serviceHandler.GetAuthConfig(ctx, authConfig)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(config).ToNot(BeNil())
			Expect(config.Providers).ToNot(BeNil())
			Expect(len(*config.Providers)).To(Equal(2))

			// Find static and dynamic providers
			var staticProvider, dynamicProvider *api.AuthProvider
			for i := range *config.Providers {
				p := (*config.Providers)[i]
				if p.Metadata.Name != nil && *p.Metadata.Name == "oidc" {
					staticProvider = &p
				}
				if p.Metadata.Name != nil && *p.Metadata.Name == "dynamic-test" {
					dynamicProvider = &p
				}
			}

			// Verify static provider exists
			Expect(staticProvider).ToNot(BeNil(), "Static provider should be in config")

			// Verify dynamic provider exists
			Expect(dynamicProvider).ToNot(BeNil(), "Dynamic provider should be in config")

			// Default provider should be set to the first static provider
			Expect(config.DefaultProvider).ToNot(BeNil())
			Expect(*config.DefaultProvider).To(Equal("oidc"), "Default provider should be oidc (first static provider)")
		})
	})
})
