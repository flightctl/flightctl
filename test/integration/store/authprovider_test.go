package store_test

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var _ = Describe("AuthProviderStore", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		authStore store.AuthProvider
		cfg       *config.Config
		dbName    string
		called    bool
		callback  store.EventCallback
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		authStore = storeInst.AuthProvider()
		called = false
		callback = store.EventCallback(func(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
			called = true
		})

		orgId = uuid.New()
		err := testutil.CreateTestOrganization(ctx, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
	})

	// Helper function to create a test auth provider
	createTestAuthProvider := func(name string) api.AuthProvider {
		assignment := api.AuthOrganizationAssignment{}
		staticAssignment := api.AuthStaticOrganizationAssignment{
			Type:             api.Static,
			OrganizationName: "default-org",
		}
		_ = assignment.FromAuthStaticOrganizationAssignment(staticAssignment)

		oidcSpec := api.OIDCProviderSpec{
			ProviderType:           api.Oidc,
			Issuer:                 "https://accounts.google.com",
			ClientId:               "test-client-id",
			ClientSecret:           lo.ToPtr("test-client-secret"),
			Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
			Enabled:                lo.ToPtr(true),
			UsernameClaim:          lo.ToPtr("preferred_username"),
			RoleClaim:              lo.ToPtr("groups"),
			OrganizationAssignment: assignment,
		}

		provider := api.AuthProvider{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr(name),
			},
		}
		_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)

		return provider
	}

	Context("AuthProvider store operations", func() {
		It("CreateAuthProvider success", func() {
			provider := createTestAuthProvider("test-provider")
			result, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("test-provider"))
			Expect(result.ApiVersion).To(Equal("flightctl.io/v1alpha1"))
			Expect(result.Kind).To(Equal(api.AuthProviderKind))
			Expect(called).To(BeTrue())
		})

		It("CreateAuthProvider - duplicate name error", func() {
			provider1 := createTestAuthProvider("duplicate-provider")
			provider2 := createTestAuthProvider("duplicate-provider")

			_, err := authStore.Create(ctx, orgId, &provider1, callback)
			Expect(err).ToNot(HaveOccurred())

			_, err = authStore.Create(ctx, orgId, &provider2, callback)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrDuplicateName))
		})

		It("GetAuthProvider success", func() {
			provider := createTestAuthProvider("get-test-provider")
			_, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())

			result, err := authStore.Get(ctx, orgId, "get-test-provider")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("get-test-provider"))
		})

		It("GetAuthProvider - not found error", func() {
			_, err := authStore.Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("GetAuthProvider - wrong org - not found error", func() {
			provider := createTestAuthProvider("wrong-org-provider")
			_, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())

			badOrgId := uuid.New()
			_, err = authStore.Get(ctx, badOrgId, "wrong-org-provider")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("UpdateAuthProvider success", func() {
			provider := createTestAuthProvider("update-test-provider")
			created, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())

			// Update the provider
			oidcSpec, err := created.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec.ClientId = "updated-client-id"
			_ = created.Spec.FromOIDCProviderSpec(oidcSpec)

			result, err := authStore.Update(ctx, orgId, created, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			updatedSpec, err := result.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedSpec.ClientId).To(Equal("updated-client-id"))
		})

		It("CreateOrUpdateAuthProvider create mode", func() {
			provider := createTestAuthProvider("create-or-update-provider")
			result, created, err := authStore.CreateOrUpdate(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("create-or-update-provider"))
		})

		It("CreateOrUpdateAuthProvider update mode", func() {
			provider := createTestAuthProvider("create-or-update-provider")
			_, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())

			// Update the provider
			oidcSpec, err := provider.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			oidcSpec.ClientId = "updated-client-id"
			_ = provider.Spec.FromOIDCProviderSpec(oidcSpec)

			result, created, err := authStore.CreateOrUpdate(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(result).ToNot(BeNil())
			updatedSpec, err := result.Spec.AsOIDCProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedSpec.ClientId).To(Equal("updated-client-id"))
		})

		It("DeleteAuthProvider success", func() {
			provider := createTestAuthProvider("delete-test-provider")
			_, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())

			err = authStore.Delete(ctx, orgId, "delete-test-provider", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			// Verify it's deleted
			_, err = authStore.Get(ctx, orgId, "delete-test-provider")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("DeleteAuthProvider - not found", func() {
			err := authStore.Delete(ctx, orgId, "nonexistent", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeFalse())
		})

		It("ListAuthProviders with paging", func() {
			// Create multiple providers
			for i := 0; i < 5; i++ {
				provider := createTestAuthProvider(fmt.Sprintf("provider-%d", i))
				_, err := authStore.Create(ctx, orgId, &provider, callback)
				Expect(err).ToNot(HaveOccurred())
			}

			// Test listing with limit
			listParams := store.ListParams{Limit: 2}
			result, err := authStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(result.Items)).To(Equal(2))
			Expect(result.Metadata.Continue).ToNot(BeNil())

			// Test pagination
			cont, err := store.ParseContinueString(result.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			result, err = authStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(result.Items)).To(Equal(2))
		})

		It("ListAuthProviders with label selector", func() {
			// Create providers with different labels
			provider1 := createTestAuthProvider("labeled-provider-1")
			provider1.Metadata.Labels = &map[string]string{"env": "test", "type": "oidc"}
			_, err := authStore.Create(ctx, orgId, &provider1, callback)
			Expect(err).ToNot(HaveOccurred())

			provider2 := createTestAuthProvider("labeled-provider-2")
			provider2.Metadata.Labels = &map[string]string{"env": "prod", "type": "oidc"}
			_, err = authStore.Create(ctx, orgId, &provider2, callback)
			Expect(err).ToNot(HaveOccurred())

			// Test label selector
			labelSelector, err := selector.NewLabelSelector("env=test")
			Expect(err).ToNot(HaveOccurred())
			listParams := store.ListParams{
				Limit:         1000,
				LabelSelector: labelSelector,
			}
			result, err := authStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(result.Items)).To(Equal(1))
			Expect(*result.Items[0].Metadata.Name).To(Equal("labeled-provider-1"))
		})

		It("ListAuthProviders with field selector", func() {
			// Create providers with different names
			provider1 := createTestAuthProvider("field-provider-1")
			_, err := authStore.Create(ctx, orgId, &provider1, callback)
			Expect(err).ToNot(HaveOccurred())

			provider2 := createTestAuthProvider("field-provider-2")
			_, err = authStore.Create(ctx, orgId, &provider2, callback)
			Expect(err).ToNot(HaveOccurred())

			// Test field selector with supported field (metadata.name)
			fieldSelector, err := selector.NewFieldSelectorFromMap(map[string]string{"metadata.name": "field-provider-1"})
			Expect(err).ToNot(HaveOccurred())
			listParams := store.ListParams{
				Limit:         1000,
				FieldSelector: fieldSelector,
			}
			result, err := authStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(result.Items)).To(Equal(1))
			Expect(*result.Items[0].Metadata.Name).To(Equal("field-provider-1"))
		})

		It("CountAuthProviders", func() {
			// Create some providers
			for i := 0; i < 3; i++ {
				provider := createTestAuthProvider(fmt.Sprintf("count-provider-%d", i))
				_, err := authStore.Create(ctx, orgId, &provider, callback)
				Expect(err).ToNot(HaveOccurred())
			}

			count, err := authStore.Count(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(int64(3)))
		})

		It("CountByOrg", func() {
			// Create providers in current org
			for i := 0; i < 2; i++ {
				provider := createTestAuthProvider(fmt.Sprintf("org-provider-%d", i))
				_, err := authStore.Create(ctx, orgId, &provider, callback)
				Expect(err).ToNot(HaveOccurred())
			}

			// Create providers in another org
			otherOrgId := uuid.New()
			err := testutil.CreateTestOrganization(ctx, storeInst, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			for i := 0; i < 3; i++ {
				provider := createTestAuthProvider(fmt.Sprintf("other-org-provider-%d", i))
				_, err := authStore.Create(ctx, otherOrgId, &provider, callback)
				Expect(err).ToNot(HaveOccurred())
			}

			// Test CountByOrg with specific org
			results, err := authStore.CountByOrg(ctx, &orgId)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(Equal(1))
			Expect(results[0].OrgID).To(Equal(orgId.String()))
			Expect(results[0].Count).To(Equal(int64(2)))

			// Test CountByOrg with nil org (all orgs)
			results, err = authStore.CountByOrg(ctx, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(Equal(2)) // Two orgs
		})
	})

	Context("AuthProvider validation", func() {

		It("should accept provider with all required fields", func() {
			provider := createTestAuthProvider("valid-provider")
			result, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
		})
	})

	Context("AuthProvider with different organization assignments", func() {
		It("should handle static organization assignment", func() {
			assignment := api.AuthOrganizationAssignment{}
			staticAssignment := api.AuthStaticOrganizationAssignment{
				Type:             api.Static,
				OrganizationName: "test-org",
			}
			err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment)
			Expect(err).ToNot(HaveOccurred())

			oidcSpec := api.OIDCProviderSpec{
				ProviderType:           api.Oidc,
				Issuer:                 "https://accounts.google.com",
				ClientId:               "test-client-id",
				ClientSecret:           lo.ToPtr("test-client-secret"),
				Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
				Enabled:                lo.ToPtr(true),
				UsernameClaim:          lo.ToPtr("preferred_username"),
				RoleClaim:              lo.ToPtr("groups"),
				OrganizationAssignment: assignment,
			}

			provider := api.AuthProvider{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("static-org-provider"),
				},
			}
			err = provider.Spec.FromOIDCProviderSpec(oidcSpec)
			Expect(err).ToNot(HaveOccurred())

			result, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())
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
				ClientId:               "test-client-id",
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

			result, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
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
				ClientId:               "test-client-id",
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

			result, err := authStore.Create(ctx, orgId, &provider, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
		})
	})
})
