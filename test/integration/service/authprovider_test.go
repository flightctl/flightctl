package service_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("AuthProvider Service Integration Tests", func() {
	var (
		suite *ServiceTestSuite
		orgId uuid.UUID
	)

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		orgId = store.NullOrgId // Use the same orgId as the service
	})

	AfterEach(func() {
		suite.Teardown()
	})

	// Helper function to get events for a specific Auth provider
	getEventsForAuthProvider := func(providerName string) []api.Event {
		// Use field selector to filter events by involved object name and kind
		fieldSelector, err := selector.NewFieldSelectorFromMap(map[string]string{
			"involvedObject.name": providerName,
			"involvedObject.kind": string(api.AuthProviderKind),
		})
		Expect(err).ToNot(HaveOccurred())

		listParams := store.ListParams{
			Limit:         100,
			FieldSelector: fieldSelector,
			SortColumns:   []store.SortColumn{store.SortByCreatedAt, store.SortByName},
			SortOrder:     lo.ToPtr(store.SortDesc),
		}
		eventList, err := suite.Store.Event().List(suite.Ctx, orgId, listParams)
		Expect(err).ToNot(HaveOccurred())

		return eventList.Items
	}

	// Helper function to check for specific event reason
	findEventByReason := func(events []api.Event, reason api.EventReason) *api.Event {
		for _, event := range events {
			if event.Reason == reason {
				return &event
			}
		}
		return nil
	}

	// Helper function to create test organization assignment
	createTestOrganizationAssignment := func() api.AuthOrganizationAssignment {
		assignment := api.AuthOrganizationAssignment{}
		staticAssignment := api.AuthStaticOrganizationAssignment{
			Type:             api.Static,
			OrganizationName: "default-org",
		}
		_ = assignment.FromAuthStaticOrganizationAssignment(staticAssignment)
		return assignment
	}

	Context("AuthProvider CRUD operations", func() {
		It("should create Auth provider successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "test-provider", "", nil)
			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("test-provider"))
			Expect(result.ApiVersion).To(Equal("flightctl.io/v1alpha1"))
			Expect(result.Kind).To(Equal(api.AuthProviderKind))
		})

		It("should reject Auth provider with missing required fields", func() {
			// Create an auth provider spec with missing required fields
			oidcSpec := api.OIDCProviderSpec{
				// Missing required fields like Issuer, ClientId, etc.
				OrganizationAssignment: createTestOrganizationAssignment(),
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("invalid-provider"),
				},
			}
			_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)

			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(400)))
		})

		It("should get Auth provider successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "get-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))

			result, status := suite.Handler.GetAuthProvider(suite.Ctx, "get-test-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("get-test-provider"))
		})

		It("should return 404 for non-existent Auth provider", func() {
			_, status := suite.Handler.GetAuthProvider(suite.Ctx, "non-existent")
			Expect(status.Code).To(Equal(int32(404)))
		})

		It("should list Auth providers successfully", func() {
			// Create multiple providers
			for i := 0; i < 3; i++ {
				provider := util.ReturnTestAuthProvider(store.NullOrgId, fmt.Sprintf("list-provider-%d", i), "", nil)
				_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
				Expect(status.Code).To(Equal(int32(201)))
			}

			params := api.ListAuthProvidersParams{}
			result, status := suite.Handler.ListAuthProviders(suite.Ctx, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(len(result.Items)).To(Equal(3))
		})

		It("should list Auth providers with label selector", func() {
			// Create providers with different labels
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "labeled-provider-1", "", nil)
			provider1.Metadata.Labels = &map[string]string{"env": "test", "type": "oidc"}
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider1)
			Expect(status.Code).To(Equal(int32(201)))

			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "labeled-provider-2", "", nil)
			provider2.Metadata.Labels = &map[string]string{"env": "prod", "type": "oidc"}
			_, status = suite.Handler.CreateAuthProvider(suite.Ctx, provider2)
			Expect(status.Code).To(Equal(int32(201)))

			// Test label selector
			params := api.ListAuthProvidersParams{
				LabelSelector: lo.ToPtr("env=test"),
			}
			result, status := suite.Handler.ListAuthProviders(suite.Ctx, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(result.Items)).To(Equal(1))
			Expect(*result.Items[0].Metadata.Name).To(Equal("labeled-provider-1"))
		})

		It("should list Auth providers with field selector", func() {
			// Create providers with different issuers
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-provider-1", "https://accounts.google.com", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider1)
			Expect(status.Code).To(Equal(int32(201)))

			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-provider-2", "https://login.microsoftonline.com", nil)
			_, status = suite.Handler.CreateAuthProvider(suite.Ctx, provider2)
			Expect(status.Code).To(Equal(int32(201)))

			// Test field selector
			params := api.ListAuthProvidersParams{
				FieldSelector: lo.ToPtr("metadata.name=issuer-provider-1"),
			}
			result, status := suite.Handler.ListAuthProviders(suite.Ctx, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(result.Items)).To(Equal(1))
			Expect(*result.Items[0].Metadata.Name).To(Equal("issuer-provider-1"))
		})

		It("should replace Auth provider successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "replace-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Update the provider - need to extract and modify the OIDC spec
			oidcSpec, err := provider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec.ClientId = "updated-client-id"
			_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)

			result, status := suite.Handler.ReplaceAuthProvider(suite.Ctx, "replace-test-provider", provider)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			updatedSpec, err := result.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedSpec.ClientId).To(Equal("updated-client-id"))
		})

		It("should reject replace with mismatched name", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "replace-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Try to replace with different name
			provider.Metadata.Name = lo.ToPtr("different-name")
			_, status = suite.Handler.ReplaceAuthProvider(suite.Ctx, "replace-test-provider", provider)
			Expect(status.Code).To(Equal(int32(400)))
		})

		It("should delete Auth provider successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "delete-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))

			status = suite.Handler.DeleteAuthProvider(suite.Ctx, "delete-test-provider")
			Expect(status.Code).To(Equal(int32(200)))

			// Verify it's deleted
			_, status = suite.Handler.GetAuthProvider(suite.Ctx, "delete-test-provider")
			Expect(status.Code).To(Equal(int32(404)))
		})

		It("should return 200 when deleting non-existent Auth provider", func() {
			status := suite.Handler.DeleteAuthProvider(suite.Ctx, "non-existent")
			Expect(status.Code).To(Equal(int32(200)))
		})
	})

	Context("AuthProvider by issuer operations", func() {
		It("should get Auth provider by issuer successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-test-provider", "https://accounts.google.com", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))

			result, status := suite.Handler.GetAuthProviderByIssuerAndClientId(suite.Ctx, "https://accounts.google.com", "test-client-id-issuer-test-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("issuer-test-provider"))
			oidcSpec, err := result.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(oidcSpec.Issuer).To(Equal("https://accounts.google.com"))
		})

		It("should return 404 for non-existent issuer", func() {
			_, status := suite.Handler.GetAuthProviderByIssuerAndClientId(suite.Ctx, "https://nonexistent.com", "test-client-id")
			Expect(status.Code).To(Equal(int32(404)))
		})

		It("should handle multiple providers with same issuer", func() {
			// Create multiple providers with same issuer
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-provider-1", "https://accounts.google.com", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider1)
			Expect(status.Code).To(Equal(int32(201)))

			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-provider-2", "https://accounts.google.com", nil)
			_, status = suite.Handler.CreateAuthProvider(suite.Ctx, provider2)
			Expect(status.Code).To(Equal(int32(201)))

			// Should return the first matching provider
			result, status := suite.Handler.GetAuthProviderByIssuerAndClientId(suite.Ctx, "https://accounts.google.com", "test-client-id-issuer-provider-1")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			oidcSpec, err := result.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(oidcSpec.Issuer).To(Equal("https://accounts.google.com"))
		})
	})

	Context("AuthProvider with different organization assignments", func() {
		It("should handle static organization assignment", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "static-org-provider", "", nil)
			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
		})

		It("should handle dynamic organization assignment", func() {
			assignment := api.AuthOrganizationAssignment{}
			dynamicAssignment := api.AuthDynamicOrganizationAssignment{
				Type:      api.Dynamic,
				ClaimPath: "organization",
			}
			err := assignment.FromAuthDynamicOrganizationAssignment(dynamicAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-dynamic-org-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr("preferred_username"),
				RoleClaim:              lo.ToPtr("groups"),
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("dynamic-org-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)), fmt.Sprintf("status: %v result: %v", status, result))
			Expect(result).ToNot(BeNil(), fmt.Sprintf("result: %v", result))
		})

		It("should handle per-user organization assignment", func() {
			assignment := api.AuthOrganizationAssignment{}
			perUserAssignment := api.AuthPerUserOrganizationAssignment{
				Type:                   api.PerUser,
				OrganizationNamePrefix: lo.ToPtr("user-org-"),
				OrganizationNameSuffix: lo.ToPtr("-org"),
			}
			err := assignment.FromAuthPerUserOrganizationAssignment(perUserAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-per-user-org-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr("preferred_username"),
				RoleClaim:              lo.ToPtr("groups"),
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("per-user-org-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
		})
	})

	Context("AuthProvider events", func() {
		It("should generate events when creating Auth provider", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "event-test-provider", "", nil)
			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())

			// Check for events
			events := getEventsForAuthProvider("event-test-provider")
			Expect(len(events)).To(BeNumerically(">=", 1))

			// Look for creation event
			creationEvent := findEventByReason(events, api.EventReasonResourceCreated)
			Expect(creationEvent).ToNot(BeNil())
			Expect(creationEvent.InvolvedObject.Kind).To(Equal(api.AuthProviderKind))
			Expect(creationEvent.InvolvedObject.Name).To(Equal("event-test-provider"))
		})

		It("should generate events when updating Auth provider", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "update-event-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Fetch the provider to get the latest metadata
			fetchedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, "update-event-test-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(fetchedProvider).ToNot(BeNil())

			// Update the provider spec
			oidcSpec, err := fetchedProvider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec.ClientId = "updated-client-id"
			_ = fetchedProvider.Spec.FromOIDCProviderSpec(oidcSpec)

			result, status := suite.Handler.ReplaceAuthProvider(suite.Ctx, "update-event-test-provider", *fetchedProvider)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())

			// Check for events with a small delay to ensure events are persisted
			time.Sleep(100 * time.Millisecond)
			events := getEventsForAuthProvider("update-event-test-provider")
			Expect(len(events)).To(BeNumerically(">=", 2), fmt.Sprintf("events: %v", events)) // Creation + update

			// Look for update event
			updateEvent := findEventByReason(events, api.EventReasonResourceUpdated)
			Expect(updateEvent).ToNot(BeNil())
		})

		It("should generate events when deleting Auth provider", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "delete-event-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))

			status = suite.Handler.DeleteAuthProvider(suite.Ctx, "delete-event-test-provider")
			Expect(status.Code).To(Equal(int32(200)))

			// Check for events
			events := getEventsForAuthProvider("delete-event-test-provider")
			Expect(len(events)).To(BeNumerically(">=", 2)) // Creation + deletion

			// Look for deletion event
			deletionEvent := findEventByReason(events, api.EventReasonResourceDeleted)
			Expect(deletionEvent).ToNot(BeNil())
		})
	})

	Context("AuthProvider validation", func() {
		It("should validate required fields", func() {
			oidcSpec := api.OIDCProviderSpec{
				// Missing required fields: issuer, clientId, clientSecret
				OrganizationAssignment: createTestOrganizationAssignment(),
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("validation-test-provider"),
				},
			}
			_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)

			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(400)))
		})

		It("should validate organization assignment", func() {
			// Create invalid organization assignment (empty)
			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				OrganizationAssignment: api.AuthOrganizationAssignment{}, // Invalid/empty
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("org-assignment-test-provider"),
				},
			}
			_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)

			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(400)))
		})

		It("should accept valid Auth provider", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "valid-test-provider", "", nil)
			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
		})
	})

	Context("AuthProvider authorization validation", func() {
		// Helper function to create context with mapped identity for non-admin user
		createNonAdminContext := func() context.Context {
			// Create a mapped identity for a non-admin user
			testOrg := &model.Organization{
				ID:          store.NullOrgId,
				ExternalID:  "test-org",
				DisplayName: "Test Organization",
			}
			mappedIdentity := &identity.MappedIdentity{
				Username:      "testuser",
				UID:           "testuser-id",
				Organizations: []*model.Organization{testOrg},
				Roles:         []string{"user"},
			}
			return context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)
		}

		// Helper function to create context with mapped identity for admin user
		createAdminContext := func() context.Context {
			// Create a mapped identity for an admin user
			testOrg := &model.Organization{
				ID:          store.NullOrgId,
				ExternalID:  "test-org",
				DisplayName: "Test Organization",
			}
			mappedIdentity := &identity.MappedIdentity{
				Username:      "adminuser",
				UID:           "adminuser-id",
				Organizations: []*model.Organization{testOrg},
				Roles:         []string{string(api.RoleAdmin)},
			}
			return context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)
		}

		It("should reject dynamic organization assignment for non-admin users", func() {
			assignment := api.AuthOrganizationAssignment{}
			dynamicAssignment := api.AuthDynamicOrganizationAssignment{
				Type:      api.Dynamic,
				ClaimPath: "organization",
			}
			err := assignment.FromAuthDynamicOrganizationAssignment(dynamicAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-dynamic-non-admin-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr("preferred_username"),
				RoleClaim:              lo.ToPtr("groups"),
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("dynamic-non-admin-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			// Create context with non-admin user
			nonAdminCtx := createNonAdminContext()
			_, status := suite.Handler.CreateAuthProvider(nonAdminCtx, provider)
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("only flightctl-admin users are allowed to create auth providers with dynamic organization mapping"))
		})

		It("should reject per-user organization assignment for non-admin users", func() {
			assignment := api.AuthOrganizationAssignment{}
			perUserAssignment := api.AuthPerUserOrganizationAssignment{
				Type:                   api.PerUser,
				OrganizationNamePrefix: lo.ToPtr("user-org-"),
				OrganizationNameSuffix: lo.ToPtr("-org"),
			}
			err := assignment.FromAuthPerUserOrganizationAssignment(perUserAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-per-user-non-admin-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr("preferred_username"),
				RoleClaim:              lo.ToPtr("groups"),
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("per-user-non-admin-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			// Create context with non-admin user
			nonAdminCtx := createNonAdminContext()
			_, status := suite.Handler.CreateAuthProvider(nonAdminCtx, provider)
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("only flightctl-admin users are allowed to create auth providers with per-user organization mapping"))
		})

		It("should allow dynamic organization assignment for admin users", func() {
			assignment := api.AuthOrganizationAssignment{}
			dynamicAssignment := api.AuthDynamicOrganizationAssignment{
				Type:      api.Dynamic,
				ClaimPath: "organization",
			}
			err := assignment.FromAuthDynamicOrganizationAssignment(dynamicAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-dynamic-admin-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr("preferred_username"),
				RoleClaim:              lo.ToPtr("groups"),
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("dynamic-admin-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			// Create context with admin user
			adminCtx := createAdminContext()
			result, status := suite.Handler.CreateAuthProvider(adminCtx, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
		})

		It("should allow per-user organization assignment for admin users", func() {
			assignment := api.AuthOrganizationAssignment{}
			perUserAssignment := api.AuthPerUserOrganizationAssignment{
				Type:                   api.PerUser,
				OrganizationNamePrefix: lo.ToPtr("user-org-"),
				OrganizationNameSuffix: lo.ToPtr("-org"),
			}
			err := assignment.FromAuthPerUserOrganizationAssignment(perUserAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-per-user-admin-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr("preferred_username"),
				RoleClaim:              lo.ToPtr("groups"),
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("per-user-admin-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			// Create context with admin user
			adminCtx := createAdminContext()
			result, status := suite.Handler.CreateAuthProvider(adminCtx, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
		})
	})

	Context("AuthProvider pagination", func() {
		It("should handle pagination correctly", func() {
			// Create multiple providers
			for i := 0; i < 5; i++ {
				provider := util.ReturnTestAuthProvider(store.NullOrgId, fmt.Sprintf("pagination-provider-%d", i), "", nil)
				_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
				Expect(status.Code).To(Equal(int32(201)))
			}

			// Test listing with limit
			params := api.ListAuthProvidersParams{
				Limit: lo.ToPtr(int32(2)),
			}
			result, status := suite.Handler.ListAuthProviders(suite.Ctx, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(result.Items)).To(Equal(2))
			Expect(result.Metadata.Continue).ToNot(BeNil())

			// Test pagination
			params.Continue = result.Metadata.Continue
			result, status = suite.Handler.ListAuthProviders(suite.Ctx, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(result.Items)).To(Equal(2))
		})
	})

	Context("Auth Provider Events", func() {
		var (
			orgId uuid.UUID
		)

		BeforeEach(func() {
			orgId = uuid.New()
			err := util.CreateTestOrganization(suite.Ctx, suite.Store, orgId)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("Create Auth Provider Events", func() {
			It("should emit ResourceCreated event when creating Auth provider", func() {
				// Create Auth provider
				provider := util.ReturnTestAuthProvider(orgId, "test-provider", "", nil)
				createdProvider, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
				Expect(status.Code).To(Equal(int32(201)))
				Expect(createdProvider).ToNot(BeNil())

				// Verify event was emitted
				events := getEventsForAuthProvider("test-provider")
				Expect(len(events)).To(BeNumerically(">=", 1))

				creationEvent := findEventByReason(events, api.EventReasonResourceCreated)
				Expect(creationEvent).ToNot(BeNil())
				Expect(creationEvent.InvolvedObject.Kind).To(Equal(api.AuthProviderKind))
				Expect(creationEvent.InvolvedObject.Name).To(Equal("test-provider"))
			})
		})

		Context("Update Auth Provider Events", func() {
			BeforeEach(func() {
				// Create initial Auth provider
				testProvider := util.ReturnTestAuthProvider(orgId, "test-provider", "", nil)
				_, status := suite.Handler.CreateAuthProvider(suite.Ctx, testProvider)
				Expect(status.Code).To(Equal(int32(201)))
			})

			It("should emit ResourceUpdated event when updating Auth provider", func() {
				// Fetch the provider to get the latest metadata
				fetchedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, "test-provider")
				Expect(status.Code).To(Equal(int32(200)))
				Expect(fetchedProvider).ToNot(BeNil())

				// Update Auth provider spec
				oidcSpec, err := fetchedProvider.Spec.AsOIDCProviderSpec()
				Expect(err).ToNot(HaveOccurred())
				oidcSpec.ClientId = "updated-client-id"
				_ = fetchedProvider.Spec.FromOIDCProviderSpec(oidcSpec)

				updatedProvider, status := suite.Handler.ReplaceAuthProvider(suite.Ctx, "test-provider", *fetchedProvider)
				Expect(status.Code).To(Equal(int32(200)))
				Expect(updatedProvider).ToNot(BeNil())

				// Verify event was emitted with a small delay to ensure events are persisted
				time.Sleep(100 * time.Millisecond)
				events := getEventsForAuthProvider("test-provider")
				Expect(len(events)).To(BeNumerically(">=", 2)) // Creation + update

				updateEvent := findEventByReason(events, api.EventReasonResourceUpdated)
				Expect(updateEvent).ToNot(BeNil())
				Expect(updateEvent.InvolvedObject.Kind).To(Equal(api.AuthProviderKind))
				Expect(updateEvent.InvolvedObject.Name).To(Equal("test-provider"))
			})
		})

		Context("Delete Auth Provider Events", func() {
			BeforeEach(func() {
				// Create Auth provider
				provider := util.ReturnTestAuthProvider(orgId, "test-provider", "", nil)
				_, status := suite.Handler.CreateAuthProvider(suite.Ctx, provider)
				Expect(status.Code).To(Equal(int32(201)))
			})

			It("should emit ResourceDeleted event when deleting Auth provider", func() {
				// Delete Auth provider
				status := suite.Handler.DeleteAuthProvider(suite.Ctx, "test-provider")
				Expect(status.Code).To(Equal(int32(200)))

				// Verify event was emitted
				events := getEventsForAuthProvider("test-provider")
				Expect(len(events)).To(BeNumerically(">=", 2)) // Creation + deletion

				deletionEvent := findEventByReason(events, api.EventReasonResourceDeleted)
				Expect(deletionEvent).ToNot(BeNil())
				Expect(deletionEvent.InvolvedObject.Kind).To(Equal(api.AuthProviderKind))
				Expect(deletionEvent.InvolvedObject.Name).To(Equal("test-provider"))
			})

			It("should return 200 when deleting non-existent Auth provider (idempotent)", func() {
				// Try to delete non-existent Auth provider
				status := suite.Handler.DeleteAuthProvider(suite.Ctx, "non-existent-provider")
				Expect(status.Code).To(Equal(int32(200))) // Success (idempotent)

				// No events should be emitted for non-existent resources
				events := getEventsForAuthProvider("non-existent-provider")
				Expect(len(events)).To(Equal(0))
			})
		})
	})
})
