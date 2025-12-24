package service_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
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
	getEventsForAuthProvider := func(orgId uuid.UUID, providerName string) []api.Event {
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
			Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
			OrganizationName: "default-org",
		}
		err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment)
		Expect(err).ToNot(HaveOccurred())
		return assignment
	}

	Context("AuthProvider CRUD operations", func() {
		It("should create Auth provider successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "test-provider", "", nil)
			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("test-provider"))
			Expect(result.ApiVersion).To(Equal("flightctl.io/v1beta1"))
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
			err := provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(400)))
		})

		It("should get Auth provider successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "get-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			result, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "get-test-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("get-test-provider"))
		})

		It("should return 404 for non-existent Auth provider", func() {
			_, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "non-existent")
			Expect(status.Code).To(Equal(int32(404)))
		})

		It("should list Auth providers successfully", func() {
			// Create multiple providers
			for i := 0; i < 3; i++ {
				provider := util.ReturnTestAuthProvider(store.NullOrgId, fmt.Sprintf("list-provider-%d", i), "", nil)
				_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
				Expect(status.Code).To(Equal(int32(201)))
			}

			params := api.ListAuthProvidersParams{}
			result, status := suite.Handler.ListAuthProviders(suite.Ctx, orgId, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(len(result.Items)).To(Equal(3))
		})

		It("should list Auth providers with label selector", func() {
			// Create providers with different labels
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "labeled-provider-1", "", nil)
			provider1.Metadata.Labels = &map[string]string{"env": "test", "type": "oidc"}
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider1)
			Expect(status.Code).To(Equal(int32(201)))

			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "labeled-provider-2", "", nil)
			provider2.Metadata.Labels = &map[string]string{"env": "prod", "type": "oidc"}
			_, status = suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider2)
			Expect(status.Code).To(Equal(int32(201)))

			// Test label selector
			params := api.ListAuthProvidersParams{
				LabelSelector: lo.ToPtr("env=test"),
			}
			result, status := suite.Handler.ListAuthProviders(suite.Ctx, orgId, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(result.Items)).To(Equal(1))
			Expect(*result.Items[0].Metadata.Name).To(Equal("labeled-provider-1"))
		})

		It("should list Auth providers with field selector", func() {
			// Create providers with different issuers
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-provider-1", "https://accounts.google.com", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider1)
			Expect(status.Code).To(Equal(int32(201)))

			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-provider-2", "https://login.microsoftonline.com", nil)
			_, status = suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider2)
			Expect(status.Code).To(Equal(int32(201)))

			// Test field selector
			params := api.ListAuthProvidersParams{
				FieldSelector: lo.ToPtr("metadata.name=issuer-provider-1"),
			}
			result, status := suite.Handler.ListAuthProviders(suite.Ctx, orgId, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(result.Items)).To(Equal(1))
			Expect(*result.Items[0].Metadata.Name).To(Equal("issuer-provider-1"))
		})

		It("should replace Auth provider successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "replace-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Fetch the created provider to get the full object with ApiVersion/Kind
			fetched, err := suite.Store.AuthProvider().Get(suite.Ctx, orgId, "replace-test-provider")
			Expect(err).ToNot(HaveOccurred())

			// Update the provider - need to extract and modify the OIDC spec
			oidcSpec, err := fetched.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec.ClientId = "updated-client-id"
			err = fetched.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.ReplaceAuthProvider(suite.Ctx, orgId, "replace-test-provider", *fetched)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			updatedSpec, err := result.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedSpec.ClientId).To(Equal("updated-client-id"))
		})

		It("should reject replace with mismatched name", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "replace-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Try to replace with different name
			provider.Metadata.Name = lo.ToPtr("different-name")
			_, status = suite.Handler.ReplaceAuthProvider(suite.Ctx, orgId, "replace-test-provider", provider)
			Expect(status.Code).To(Equal(int32(400)))
		})

		It("should delete Auth provider successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "delete-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			status = suite.Handler.DeleteAuthProvider(suite.Ctx, orgId, "delete-test-provider")
			Expect(status.Code).To(Equal(int32(200)))

			// Verify it's deleted
			_, status = suite.Handler.GetAuthProvider(suite.Ctx, orgId, "delete-test-provider")
			Expect(status.Code).To(Equal(int32(404)))
		})

		It("should return 200 when deleting non-existent Auth provider", func() {
			status := suite.Handler.DeleteAuthProvider(suite.Ctx, orgId, "non-existent")
			Expect(status.Code).To(Equal(int32(200)))
		})
	})

	Context("AuthProvider by issuer operations", func() {
		It("should get Auth provider by issuer successfully", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-test-provider", "https://accounts.google.com", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			result, status := suite.Handler.GetAuthProviderByIssuerAndClientId(suite.Ctx, orgId, "https://accounts.google.com", "test-client-id-issuer-test-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("issuer-test-provider"))
			oidcSpec, err := result.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(oidcSpec.Issuer).To(Equal("https://accounts.google.com"))
		})

		It("should return 404 for non-existent issuer", func() {
			_, status := suite.Handler.GetAuthProviderByIssuerAndClientId(suite.Ctx, orgId, "https://nonexistent.com", "test-client-id")
			Expect(status.Code).To(Equal(int32(404)))
		})

		It("should handle multiple providers with same issuer", func() {
			// Create multiple providers with same issuer
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-provider-1", "https://accounts.google.com", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider1)
			Expect(status.Code).To(Equal(int32(201)))

			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "issuer-provider-2", "https://accounts.google.com", nil)
			_, status = suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider2)
			Expect(status.Code).To(Equal(int32(201)))

			// Should return the first matching provider
			result, status := suite.Handler.GetAuthProviderByIssuerAndClientId(suite.Ctx, orgId, "https://accounts.google.com", "test-client-id-issuer-provider-1")
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
			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
		})

		It("should handle dynamic organization assignment", func() {
			assignment := api.AuthOrganizationAssignment{}
			dynamicAssignment := api.AuthDynamicOrganizationAssignment{
				Type:      api.AuthDynamicOrganizationAssignmentTypeDynamic,
				ClaimPath: []string{"organization"},
			}
			err := assignment.FromAuthDynamicOrganizationAssignment(dynamicAssignment)
			Expect(err).ToNot(HaveOccurred())

			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err = roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-dynamic-org-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
				RoleAssignment:         roleAssignment,
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("dynamic-org-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
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

			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err = roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-per-user-org-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
				RoleAssignment:         roleAssignment,
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("per-user-org-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
		})
	})

	Context("AuthProvider events", func() {
		It("should generate events when creating Auth provider", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "event-test-provider", "", nil)
			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())

			// Check for events
			events := getEventsForAuthProvider(orgId, "event-test-provider")
			Expect(len(events)).To(BeNumerically(">=", 1))

			// Look for creation event
			creationEvent := findEventByReason(events, api.EventReasonResourceCreated)
			Expect(creationEvent).ToNot(BeNil())
			Expect(creationEvent.InvolvedObject.Kind).To(Equal(api.AuthProviderKind))
			Expect(creationEvent.InvolvedObject.Name).To(Equal("event-test-provider"))
		})

		It("should generate events when updating Auth provider", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "update-event-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Fetch the provider to get the latest metadata
			fetchedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "update-event-test-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(fetchedProvider).ToNot(BeNil())

			// Update the provider spec
			oidcSpec, err := fetchedProvider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec.ClientId = "updated-client-id"
			err = fetchedProvider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.ReplaceAuthProvider(suite.Ctx, orgId, "update-event-test-provider", *fetchedProvider)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())

			// Check for events with a small delay to ensure events are persisted
			time.Sleep(100 * time.Millisecond)
			events := getEventsForAuthProvider(orgId, "update-event-test-provider")
			Expect(len(events)).To(BeNumerically(">=", 2), fmt.Sprintf("events: %v", events)) // Creation + update

			// Look for update event
			updateEvent := findEventByReason(events, api.EventReasonResourceUpdated)
			Expect(updateEvent).ToNot(BeNil())
		})

		It("should generate events when deleting Auth provider", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "delete-event-test-provider", "", nil)
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			status = suite.Handler.DeleteAuthProvider(suite.Ctx, orgId, "delete-event-test-provider")
			Expect(status.Code).To(Equal(int32(200)))

			// Check for events
			events := getEventsForAuthProvider(orgId, "delete-event-test-provider")
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

			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
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
			err := provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(400)))
		})

		It("should accept valid Auth provider", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "valid-test-provider", "", nil)
			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
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
				OrgRoles:      map[string][]string{"*": {"user"}},
				SuperAdmin:    false,
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
				OrgRoles:      map[string][]string{"*": {string(api.RoleAdmin)}},
				SuperAdmin:    true, // Super admin required for creating auth providers with dynamic org assignment
			}
			return context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)
		}

		It("should reject dynamic organization assignment for non-admin users", func() {
			assignment := api.AuthOrganizationAssignment{}
			dynamicAssignment := api.AuthDynamicOrganizationAssignment{
				Type:      api.AuthDynamicOrganizationAssignmentTypeDynamic,
				ClaimPath: []string{"organization"},
			}
			err := assignment.FromAuthDynamicOrganizationAssignment(dynamicAssignment)
			Expect(err).ToNot(HaveOccurred())

			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err = roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-dynamic-non-admin-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
				RoleAssignment:         roleAssignment,
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
			_, status := suite.Handler.CreateAuthProvider(nonAdminCtx, orgId, provider)
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

			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err = roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-per-user-non-admin-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
				RoleAssignment:         roleAssignment,
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
			_, status := suite.Handler.CreateAuthProvider(nonAdminCtx, orgId, provider)
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("only flightctl-admin users are allowed to create auth providers with per-user organization mapping"))
		})

		It("should allow dynamic organization assignment for admin users", func() {
			assignment := api.AuthOrganizationAssignment{}
			dynamicAssignment := api.AuthDynamicOrganizationAssignment{
				Type:      api.AuthDynamicOrganizationAssignmentTypeDynamic,
				ClaimPath: []string{"organization"},
			}
			err := assignment.FromAuthDynamicOrganizationAssignment(dynamicAssignment)
			Expect(err).ToNot(HaveOccurred())

			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err = roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-dynamic-admin-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
				RoleAssignment:         roleAssignment,
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
			result, status := suite.Handler.CreateAuthProvider(adminCtx, orgId, provider)
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

			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err = roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-per-user-admin-provider",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
				RoleAssignment:         roleAssignment,
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
			result, status := suite.Handler.CreateAuthProvider(adminCtx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
		})

		It("should add createdBySuperAdmin annotation when created by super admin", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "super-admin-provider", "", nil)
			adminCtx := createAdminContext()
			result, status := suite.Handler.CreateAuthProvider(adminCtx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
			Expect(result.Metadata.Annotations).ToNot(BeNil())
			Expect(*result.Metadata.Annotations).To(HaveKey(api.AuthProviderAnnotationCreatedBySuperAdmin))
			Expect((*result.Metadata.Annotations)[api.AuthProviderAnnotationCreatedBySuperAdmin]).To(Equal("true"))
		})

		It("should not add createdBySuperAdmin annotation when created by non-super admin", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "non-admin-provider", "", nil)
			nonAdminCtx := createNonAdminContext()
			result, status := suite.Handler.CreateAuthProvider(nonAdminCtx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
			// Annotations should be nil or not contain the createdBySuperAdmin annotation
			if result.Metadata.Annotations != nil {
				Expect(*result.Metadata.Annotations).ToNot(HaveKey(api.AuthProviderAnnotationCreatedBySuperAdmin))
			}
		})

		It("should add createdBySuperAdmin annotation when created via ReplaceAuthProvider by super admin", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "replace-super-admin-provider", "", nil)
			adminCtx := createAdminContext()
			result, status := suite.Handler.ReplaceAuthProvider(adminCtx, orgId, "replace-super-admin-provider", provider)
			Expect(status.Code).To(Equal(int32(201))) // 201 because it's a create
			Expect(result).ToNot(BeNil())
			Expect(result.Metadata.Annotations).ToNot(BeNil())
			Expect(*result.Metadata.Annotations).To(HaveKey(api.AuthProviderAnnotationCreatedBySuperAdmin))
			Expect((*result.Metadata.Annotations)[api.AuthProviderAnnotationCreatedBySuperAdmin]).To(Equal("true"))
		})

		It("should not add createdBySuperAdmin annotation when created via ReplaceAuthProvider by non-super admin", func() {
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "replace-non-admin-provider", "", nil)
			nonAdminCtx := createNonAdminContext()
			result, status := suite.Handler.ReplaceAuthProvider(nonAdminCtx, orgId, "replace-non-admin-provider", provider)
			Expect(status.Code).To(Equal(int32(201))) // 201 because it's a create
			Expect(result).ToNot(BeNil())
			// Annotations should be nil or not contain the createdBySuperAdmin annotation
			if result.Metadata.Annotations != nil {
				Expect(*result.Metadata.Annotations).ToNot(HaveKey(api.AuthProviderAnnotationCreatedBySuperAdmin))
			}
		})

		It("should not add createdBySuperAdmin annotation when updating via ReplaceAuthProvider", func() {
			// First create a provider without the annotation
			provider := util.ReturnTestAuthProvider(store.NullOrgId, "replace-update-provider", "", nil)
			nonAdminCtx := createNonAdminContext()
			_, status := suite.Handler.CreateAuthProvider(nonAdminCtx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Fetch the created provider
			fetched, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "replace-update-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(fetched).ToNot(BeNil())

			// Update via ReplaceAuthProvider with super admin context
			oidcSpec, err := fetched.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec.ClientId = "updated-client-id"
			err = fetched.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			adminCtx := createAdminContext()
			result, status := suite.Handler.ReplaceAuthProvider(adminCtx, orgId, "replace-update-provider", *fetched)
			Expect(status.Code).To(Equal(int32(200))) // 200 because it's an update
			Expect(result).ToNot(BeNil())
			// Should not have the annotation since this is an update, not a create
			if result.Metadata.Annotations != nil {
				Expect(*result.Metadata.Annotations).ToNot(HaveKey(api.AuthProviderAnnotationCreatedBySuperAdmin))
			}
		})

		It("should reject static role mapping with flightctl-admin for non-super admin users", func() {
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleAdmin, api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-static-admin-non-admin",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
				RoleAssignment:         roleAssignment,
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("static-admin-non-admin-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			nonAdminCtx := createNonAdminContext()
			_, status := suite.Handler.CreateAuthProvider(nonAdminCtx, orgId, provider)
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("only flightctl-admin users are allowed to create static role mappings for flightctl-admin"))
		})

		It("should allow static role mapping with flightctl-admin for super admin users", func() {
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleAdmin, api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id-static-admin-admin",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
				RoleAssignment:         roleAssignment,
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("static-admin-admin-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			adminCtx := createAdminContext()
			result, status := suite.Handler.CreateAuthProvider(adminCtx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
			Expect(result.Metadata.Annotations).ToNot(BeNil())
			Expect(*result.Metadata.Annotations).To(HaveKey(api.AuthProviderAnnotationCreatedBySuperAdmin))
		})
	})

	Context("AuthProvider pagination", func() {
		It("should handle pagination correctly", func() {
			// Create multiple providers
			for i := 0; i < 5; i++ {
				provider := util.ReturnTestAuthProvider(store.NullOrgId, fmt.Sprintf("pagination-provider-%d", i), "", nil)
				_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
				Expect(status.Code).To(Equal(int32(201)))
			}

			// Test listing with limit
			params := api.ListAuthProvidersParams{
				Limit: lo.ToPtr(int32(2)),
			}
			result, status := suite.Handler.ListAuthProviders(suite.Ctx, orgId, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(result.Items)).To(Equal(2))
			Expect(result.Metadata.Continue).ToNot(BeNil())

			// Test pagination
			params.Continue = result.Metadata.Continue
			result, status = suite.Handler.ListAuthProviders(suite.Ctx, orgId, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(len(result.Items)).To(Equal(2))
		})
	})

	Context("AuthProvider duplicate validation", func() {
		It("should reject duplicate OIDC provider with same issuer and clientId", func() {
			// Create first OIDC provider
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "oidc-provider-1", "https://accounts.google.com", nil)
			result1, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider1)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result1).ToNot(BeNil())

			// Try to create second OIDC provider with same issuer and clientId but different name
			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "oidc-provider-2", "https://accounts.google.com", nil)
			// Ensure same clientId by using the same spec
			oidcSpec1, err := provider1.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			err = provider2.Spec.FromOIDCProviderSpec(oidcSpec1)
			Expect(err).ToNot(HaveOccurred())

			result2, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider2)
			Expect(status.Code).To(Equal(int32(409)))
			Expect(status.Message).To(ContainSubstring("OIDC auth provider with the same issuer and clientId already exists"))
			Expect(result2).To(BeNil())
		})

		It("should reject duplicate OIDC provider across different organizations", func() {
			// Create a second organization
			org2Id := uuid.New()
			err := util.CreateTestOrganization(suite.Ctx, suite.Store, org2Id)
			Expect(err).ToNot(HaveOccurred())

			// Create OIDC provider in first org
			provider1 := util.ReturnTestAuthProvider(orgId, "oidc-provider-org1", "https://login.microsoftonline.com", nil)
			result1, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider1)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result1).ToNot(BeNil())

			// Try to create OIDC provider with same issuer and clientId in second org
			provider2 := util.ReturnTestAuthProvider(org2Id, "oidc-provider-org2", "https://login.microsoftonline.com", nil)
			oidcSpec1, err := provider1.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			err = provider2.Spec.FromOIDCProviderSpec(oidcSpec1)
			Expect(err).ToNot(HaveOccurred())

			result2, status := suite.Handler.CreateAuthProvider(suite.Ctx, org2Id, provider2)
			Expect(status.Code).To(Equal(int32(409)))
			Expect(status.Message).To(ContainSubstring("OIDC auth provider with the same issuer and clientId already exists"))
			Expect(result2).To(BeNil())
		})

		It("should reject duplicate OAuth2 provider with same userinfoUrl and clientId", func() {
			// Create first OAuth2 provider
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oauth2Spec1 := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://oauth2.example.com/authorize",
				TokenUrl:               "https://oauth2.example.com/token",
				UserinfoUrl:            "https://oauth2.example.com/userinfo",
				ClientId:               "oauth2-client-id-1",
				ClientSecret:           lo.ToPtr("oauth2-client-secret"),
				Enabled:                lo.ToPtr(true),
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
			}

			provider1 := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-provider-1"),
				},
			}
			err = provider1.Spec.FromOAuth2ProviderSpec(oauth2Spec1)
			Expect(err).ToNot(HaveOccurred())

			result1, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider1)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result1).ToNot(BeNil())

			// Try to create second OAuth2 provider with same userinfoUrl and clientId
			provider2 := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-provider-2"),
				},
			}
			err = provider2.Spec.FromOAuth2ProviderSpec(oauth2Spec1)
			Expect(err).ToNot(HaveOccurred())

			result2, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider2)
			Expect(status.Code).To(Equal(int32(409)))
			Expect(status.Message).To(ContainSubstring("OAuth2 auth provider with the same userinfoUrl and clientId already exists"))
			Expect(result2).To(BeNil())
		})

		It("should allow duplicate OIDC provider with same issuer but different clientId", func() {
			// Create first OIDC provider
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "oidc-provider-diff-1", "https://accounts.google.com", nil)
			result1, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider1)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result1).ToNot(BeNil())

			// Create second OIDC provider with same issuer but different clientId
			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "oidc-provider-diff-2", "https://accounts.google.com", nil)
			oidcSpec2, err := provider2.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec2.ClientId = "different-client-id"
			err = provider2.Spec.FromOIDCProviderSpec(oidcSpec2)
			Expect(err).ToNot(HaveOccurred())

			result2, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider2)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result2).ToNot(BeNil())
		})

		It("should reject update that creates duplicate OIDC provider", func() {
			// Create two OIDC providers with different issuer/clientId combinations
			provider1 := util.ReturnTestAuthProvider(store.NullOrgId, "oidc-update-1", "https://issuer1.com", nil)
			oidcSpec1, err := provider1.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec1.ClientId = "client-id-1"
			err = provider1.Spec.FromOIDCProviderSpec(oidcSpec1)
			Expect(err).ToNot(HaveOccurred())
			result1, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider1)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result1).ToNot(BeNil())

			provider2 := util.ReturnTestAuthProvider(store.NullOrgId, "oidc-update-2", "https://issuer2.com", nil)
			oidcSpec2, err := provider2.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec2.ClientId = "client-id-2"
			err = provider2.Spec.FromOIDCProviderSpec(oidcSpec2)
			Expect(err).ToNot(HaveOccurred())
			result2, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider2)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result2).ToNot(BeNil())

			// Fetch provider2 and try to update it to have same issuer/clientId as provider1
			fetchedProvider2, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "oidc-update-2")
			Expect(status.Code).To(Equal(int32(200)))

			updatedSpec, err := fetchedProvider2.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			updatedSpec.Issuer = "https://issuer1.com"
			updatedSpec.ClientId = "client-id-1"
			err = fetchedProvider2.Spec.FromOIDCProviderSpec(updatedSpec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.ReplaceAuthProvider(suite.Ctx, orgId, "oidc-update-2", *fetchedProvider2)
			Expect(status.Code).To(Equal(int32(409)))
			Expect(status.Message).To(ContainSubstring("OIDC auth provider with the same issuer and clientId already exists"))
			Expect(result).To(BeNil())
		})

		It("should reject patch that creates duplicate OAuth2 provider", func() {
			// Create two OAuth2 providers
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			oauth2Spec1 := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://oauth2-1.example.com/authorize",
				TokenUrl:               "https://oauth2-1.example.com/token",
				UserinfoUrl:            "https://oauth2-1.example.com/userinfo",
				ClientId:               "oauth2-patch-client-1",
				ClientSecret:           lo.ToPtr("oauth2-client-secret"),
				Enabled:                lo.ToPtr(true),
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
			}

			provider1 := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-patch-1"),
				},
			}
			err = provider1.Spec.FromOAuth2ProviderSpec(oauth2Spec1)
			Expect(err).ToNot(HaveOccurred())
			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider1)
			Expect(status.Code).To(Equal(int32(201)))

			oauth2Spec2 := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://oauth2-2.example.com/authorize",
				TokenUrl:               "https://oauth2-2.example.com/token",
				UserinfoUrl:            "https://oauth2-2.example.com/userinfo",
				ClientId:               "oauth2-patch-client-2",
				ClientSecret:           lo.ToPtr("oauth2-client-secret"),
				Enabled:                lo.ToPtr(true),
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
			}

			provider2 := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-patch-2"),
				},
			}
			err = provider2.Spec.FromOAuth2ProviderSpec(oauth2Spec2)
			Expect(err).ToNot(HaveOccurred())
			_, status = suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider2)
			Expect(status.Code).To(Equal(int32(201)))

			// Try to patch provider2 to have same userinfoUrl and clientId as provider1
			patchRequest := api.PatchRequest{
				{
					Op:    "replace",
					Path:  "/spec/userinfoUrl",
					Value: AnyPtr("https://oauth2-1.example.com/userinfo"),
				},
				{
					Op:    "replace",
					Path:  "/spec/clientId",
					Value: AnyPtr("oauth2-patch-client-1"),
				},
			}

			result, status := suite.Handler.PatchAuthProvider(suite.Ctx, orgId, "oauth2-patch-2", patchRequest)
			Expect(status.Code).To(Equal(int32(409)))
			Expect(status.Message).To(ContainSubstring("OAuth2 auth provider with the same userinfoUrl and clientId already exists"))
			Expect(result).To(BeNil())
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
				createdProvider, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
				Expect(status.Code).To(Equal(int32(201)))
				Expect(createdProvider).ToNot(BeNil())

				// Verify event was emitted
				events := getEventsForAuthProvider(orgId, "test-provider")
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
				_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, testProvider)
				Expect(status.Code).To(Equal(int32(201)))
			})

			It("should emit ResourceUpdated event when updating Auth provider", func() {
				// Fetch the provider to get the latest metadata
				fetchedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "test-provider")
				Expect(status.Code).To(Equal(int32(200)))
				Expect(fetchedProvider).ToNot(BeNil())

				// Update Auth provider spec
				oidcSpec, err := fetchedProvider.Spec.AsOIDCProviderSpec()
				Expect(err).ToNot(HaveOccurred())
				oidcSpec.ClientId = "updated-client-id"
				err = fetchedProvider.Spec.FromOIDCProviderSpec(oidcSpec)
				Expect(err).ToNot(HaveOccurred())

				updatedProvider, status := suite.Handler.ReplaceAuthProvider(suite.Ctx, orgId, "test-provider", *fetchedProvider)
				Expect(status.Code).To(Equal(int32(200)))
				Expect(updatedProvider).ToNot(BeNil())

				// Verify event was emitted with a small delay to ensure events are persisted
				time.Sleep(100 * time.Millisecond)
				events := getEventsForAuthProvider(orgId, "test-provider")
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
				_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
				Expect(status.Code).To(Equal(int32(201)))
			})

			It("should emit ResourceDeleted event when deleting Auth provider", func() {
				// Delete Auth provider
				status := suite.Handler.DeleteAuthProvider(suite.Ctx, orgId, "test-provider")
				Expect(status.Code).To(Equal(int32(200)))

				// Verify event was emitted
				events := getEventsForAuthProvider(orgId, "test-provider")
				Expect(len(events)).To(BeNumerically(">=", 2)) // Creation + deletion

				deletionEvent := findEventByReason(events, api.EventReasonResourceDeleted)
				Expect(deletionEvent).ToNot(BeNil())
				Expect(deletionEvent.InvolvedObject.Kind).To(Equal(api.AuthProviderKind))
				Expect(deletionEvent.InvolvedObject.Name).To(Equal("test-provider"))
			})

			It("should return 200 when deleting non-existent Auth provider (idempotent)", func() {
				// Try to delete non-existent Auth provider
				status := suite.Handler.DeleteAuthProvider(suite.Ctx, orgId, "non-existent-provider")
				Expect(status.Code).To(Equal(int32(200))) // Success (idempotent)

				// No events should be emitted for non-existent resources
				events := getEventsForAuthProvider(orgId, "non-existent-provider")
				Expect(len(events)).To(Equal(0))
			})
		})
	})

	Context("OAuth2 Introspection Configuration", func() {
		It("should create OAuth2 provider with RFC 7662 introspection", func() {
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			// Create introspection spec
			introspection := &api.OAuth2Introspection{}
			rfc7662Spec := api.Rfc7662IntrospectionSpec{
				Type: api.Rfc7662,
				Url:  "https://oauth2.example.com/introspect",
			}
			err = introspection.FromRfc7662IntrospectionSpec(rfc7662Spec)
			Expect(err).ToNot(HaveOccurred())

			oauth2Spec := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://oauth2.example.com/authorize",
				TokenUrl:               "https://oauth2.example.com/token",
				UserinfoUrl:            "https://oauth2.example.com/userinfo",
				ClientId:               "rfc7662-test-client-id",
				ClientSecret:           lo.ToPtr("rfc7662-test-client-secret"),
				Enabled:                lo.ToPtr(true),
				Introspection:          introspection,
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-rfc7662-provider"),
				},
			}
			err = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())

			// Verify the introspection is stored correctly
			retrievedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "oauth2-rfc7662-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(retrievedProvider).ToNot(BeNil())

			retrievedSpec, err := retrievedProvider.Spec.AsOAuth2ProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedSpec.Introspection).ToNot(BeNil())

			retrievedIntrospection, err := retrievedSpec.Introspection.AsRfc7662IntrospectionSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedIntrospection.Type).To(Equal(api.Rfc7662))
			Expect(retrievedIntrospection.Url).To(Equal("https://oauth2.example.com/introspect"))
		})

		It("should create OAuth2 provider with GitHub introspection", func() {
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			// Create introspection spec
			introspection := &api.OAuth2Introspection{}
			githubSpec := api.GitHubIntrospectionSpec{
				Type: api.Github,
				Url:  lo.ToPtr("https://github.enterprise.com/api/v3"),
			}
			err = introspection.FromGitHubIntrospectionSpec(githubSpec)
			Expect(err).ToNot(HaveOccurred())

			oauth2Spec := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://github.com/login/oauth/authorize",
				TokenUrl:               "https://github.com/login/oauth/access_token",
				UserinfoUrl:            "https://api.github.com/user",
				ClientId:               "github-test-client-id",
				ClientSecret:           lo.ToPtr("github-test-client-secret"),
				Enabled:                lo.ToPtr(true),
				Introspection:          introspection,
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-github-provider"),
				},
			}
			err = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())

			// Verify the introspection is stored correctly
			retrievedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "oauth2-github-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(retrievedProvider).ToNot(BeNil())

			retrievedSpec, err := retrievedProvider.Spec.AsOAuth2ProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedSpec.Introspection).ToNot(BeNil())

			retrievedIntrospection, err := retrievedSpec.Introspection.AsGitHubIntrospectionSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedIntrospection.Type).To(Equal(api.Github))
			Expect(retrievedIntrospection.Url).ToNot(BeNil())
			Expect(*retrievedIntrospection.Url).To(Equal("https://github.enterprise.com/api/v3"))
		})

		It("should create OAuth2 provider with JWT introspection", func() {
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			// Create introspection spec
			introspection := &api.OAuth2Introspection{}
			jwtSpec := api.JwtIntrospectionSpec{
				Type:     api.Jwt,
				JwksUrl:  "https://oauth2.example.com/.well-known/jwks.json",
				Issuer:   lo.ToPtr("https://oauth2.example.com"),
				Audience: lo.ToPtr([]string{"jwt-test-client-id", "another-audience"}),
			}
			err = introspection.FromJwtIntrospectionSpec(jwtSpec)
			Expect(err).ToNot(HaveOccurred())

			oauth2Spec := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://oauth2.example.com/authorize",
				TokenUrl:               "https://oauth2.example.com/token",
				UserinfoUrl:            "https://oauth2.example.com/userinfo",
				ClientId:               "jwt-test-client-id",
				ClientSecret:           lo.ToPtr("jwt-test-client-secret"),
				Enabled:                lo.ToPtr(true),
				Introspection:          introspection,
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-jwt-provider"),
				},
			}
			err = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())

			// Verify the introspection is stored correctly
			retrievedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "oauth2-jwt-provider")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(retrievedProvider).ToNot(BeNil())

			retrievedSpec, err := retrievedProvider.Spec.AsOAuth2ProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedSpec.Introspection).ToNot(BeNil())

			retrievedIntrospection, err := retrievedSpec.Introspection.AsJwtIntrospectionSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedIntrospection.Type).To(Equal(api.Jwt))
			Expect(retrievedIntrospection.JwksUrl).To(Equal("https://oauth2.example.com/.well-known/jwks.json"))
			Expect(retrievedIntrospection.Issuer).ToNot(BeNil())
			Expect(*retrievedIntrospection.Issuer).To(Equal("https://oauth2.example.com"))
			Expect(retrievedIntrospection.Audience).ToNot(BeNil())
			Expect(*retrievedIntrospection.Audience).To(Equal([]string{"jwt-test-client-id", "another-audience"}))
		})

		It("should update OAuth2 provider introspection configuration", func() {
			// Create provider with RFC 7662 introspection
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			introspection := &api.OAuth2Introspection{}
			rfc7662Spec := api.Rfc7662IntrospectionSpec{
				Type: api.Rfc7662,
				Url:  "https://oauth2.example.com/introspect",
			}
			err = introspection.FromRfc7662IntrospectionSpec(rfc7662Spec)
			Expect(err).ToNot(HaveOccurred())

			oauth2Spec := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://oauth2.example.com/authorize",
				TokenUrl:               "https://oauth2.example.com/token",
				UserinfoUrl:            "https://oauth2.example.com/userinfo",
				ClientId:               "update-test-client-id",
				ClientSecret:           lo.ToPtr("update-test-client-secret"),
				Enabled:                lo.ToPtr(true),
				Introspection:          introspection,
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-update-introspection-provider"),
				},
			}
			err = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
			Expect(err).ToNot(HaveOccurred())

			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Fetch the provider
			fetchedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "oauth2-update-introspection-provider")
			Expect(status.Code).To(Equal(int32(200)))

			// Update to use JWT introspection instead
			fetchedSpec, err := fetchedProvider.Spec.AsOAuth2ProviderSpec()
			Expect(err).ToNot(HaveOccurred())

			newIntrospection := &api.OAuth2Introspection{}
			jwtSpec := api.JwtIntrospectionSpec{
				Type:    api.Jwt,
				JwksUrl: "https://oauth2.example.com/.well-known/jwks.json",
			}
			err = newIntrospection.FromJwtIntrospectionSpec(jwtSpec)
			Expect(err).ToNot(HaveOccurred())
			fetchedSpec.Introspection = newIntrospection

			err = fetchedProvider.Spec.FromOAuth2ProviderSpec(fetchedSpec)
			Expect(err).ToNot(HaveOccurred())

			updatedProvider, status := suite.Handler.ReplaceAuthProvider(suite.Ctx, orgId, "oauth2-update-introspection-provider", *fetchedProvider)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(updatedProvider).ToNot(BeNil())

			// Verify the introspection was updated
			updatedSpec, err := updatedProvider.Spec.AsOAuth2ProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedSpec.Introspection).ToNot(BeNil())

			updatedIntrospection, err := updatedSpec.Introspection.AsJwtIntrospectionSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedIntrospection.Type).To(Equal(api.Jwt))
			Expect(updatedIntrospection.JwksUrl).To(Equal("https://oauth2.example.com/.well-known/jwks.json"))
		})

		It("should infer GitHub introspection from GitHub URLs", func() {
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			// Create OAuth2 spec with GitHub URLs but no introspection
			oauth2Spec := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://github.com/login/oauth/authorize",
				TokenUrl:               "https://github.com/login/oauth/access_token",
				UserinfoUrl:            "https://api.github.com/user",
				ClientId:               "infer-github-client-id",
				ClientSecret:           lo.ToPtr("infer-github-client-secret"),
				Enabled:                lo.ToPtr(true),
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
				// Introspection is nil - should be inferred
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-infer-github-provider"),
				},
			}
			err = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
			Expect(err).ToNot(HaveOccurred())

			result, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())

			// Verify GitHub introspection was inferred
			retrievedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "oauth2-infer-github-provider")
			Expect(status.Code).To(Equal(int32(200)))

			retrievedSpec, err := retrievedProvider.Spec.AsOAuth2ProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedSpec.Introspection).ToNot(BeNil())

			inferredIntrospection, err := retrievedSpec.Introspection.AsGitHubIntrospectionSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(inferredIntrospection.Type).To(Equal(api.Github))
		})

		It("should prevent deletion of introspection field via PATCH", func() {
			// Create OAuth2 provider with explicit introspection
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			introspection := &api.OAuth2Introspection{}
			rfc7662Spec := api.Rfc7662IntrospectionSpec{
				Type: api.Rfc7662,
				Url:  "https://oauth2.example.com/introspect",
			}
			err = introspection.FromRfc7662IntrospectionSpec(rfc7662Spec)
			Expect(err).ToNot(HaveOccurred())

			oauth2Spec := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://oauth2.example.com/authorize",
				TokenUrl:               "https://oauth2.example.com/token",
				UserinfoUrl:            "https://oauth2.example.com/userinfo",
				ClientId:               "patch-delete-test-client-id",
				ClientSecret:           lo.ToPtr("patch-delete-test-client-secret"),
				Enabled:                lo.ToPtr(true),
				Introspection:          introspection,
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-patch-delete-provider"),
				},
			}
			err = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
			Expect(err).ToNot(HaveOccurred())

			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Try to remove introspection field via PATCH
			patchRequest := api.PatchRequest{
				{
					Op:   "remove",
					Path: "/spec/introspection",
				},
			}

			result, status := suite.Handler.PatchAuthProvider(suite.Ctx, orgId, "oauth2-patch-delete-provider", patchRequest)
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("introspection field cannot be removed once set"))
			Expect(result).To(BeNil())
		})

		It("should preserve user-provided introspection during replace", func() {
			// Create OAuth2 provider with explicit introspection
			assignment := createTestOrganizationAssignment()
			roleAssignment := api.AuthRoleAssignment{}
			staticRoleAssignment := api.AuthStaticRoleAssignment{
				Type:  api.AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{api.ExternalRoleViewer},
			}
			err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment)
			Expect(err).ToNot(HaveOccurred())

			introspection := &api.OAuth2Introspection{}
			rfc7662Spec := api.Rfc7662IntrospectionSpec{
				Type: api.Rfc7662,
				Url:  "https://oauth2.example.com/introspect",
			}
			err = introspection.FromRfc7662IntrospectionSpec(rfc7662Spec)
			Expect(err).ToNot(HaveOccurred())

			oauth2Spec := api.OAuth2ProviderSpec{
				ProviderType:           api.Oauth2,
				AuthorizationUrl:       "https://oauth2.example.com/authorize",
				TokenUrl:               "https://oauth2.example.com/token",
				UserinfoUrl:            "https://oauth2.example.com/userinfo",
				ClientId:               "replace-preserve-test-client-id",
				ClientSecret:           lo.ToPtr("replace-preserve-test-client-secret"),
				Enabled:                lo.ToPtr(true),
				Introspection:          introspection,
				OrganizationAssignment: assignment,
				RoleAssignment:         roleAssignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oauth2-replace-preserve-provider"),
				},
			}
			err = provider.Spec.FromOAuth2ProviderSpec(oauth2Spec)
			Expect(err).ToNot(HaveOccurred())

			_, status := suite.Handler.CreateAuthProvider(suite.Ctx, orgId, provider)
			Expect(status.Code).To(Equal(int32(201)))

			// Fetch and update the provider
			fetchedProvider, status := suite.Handler.GetAuthProvider(suite.Ctx, orgId, "oauth2-replace-preserve-provider")
			Expect(status.Code).To(Equal(int32(200)))

			// Update a different field (e.g., clientId)
			fetchedSpec, err := fetchedProvider.Spec.AsOAuth2ProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			fetchedSpec.ClientId = "updated-client-id"
			err = fetchedProvider.Spec.FromOAuth2ProviderSpec(fetchedSpec)
			Expect(err).ToNot(HaveOccurred())

			updatedProvider, status := suite.Handler.ReplaceAuthProvider(suite.Ctx, orgId, "oauth2-replace-preserve-provider", *fetchedProvider)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(updatedProvider).ToNot(BeNil())

			// Verify introspection was preserved
			updatedSpec, err := updatedProvider.Spec.AsOAuth2ProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedSpec.Introspection).ToNot(BeNil())

			updatedIntrospection, err := updatedSpec.Introspection.AsRfc7662IntrospectionSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedIntrospection.Url).To(Equal("https://oauth2.example.com/introspect"))
		})
	})
})
