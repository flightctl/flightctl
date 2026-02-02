package service_test

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Catalog Integration Tests", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	Context("CatalogItem version validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept version with tag only", func() {
			item := createValidCatalogItem("item-with-tag")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:  "1.0.0",
					Tag:      lo.ToPtr("v1.0.0"),
					Channels: []string{"stable"},
				},
			}

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept version with digest only", func() {
			item := createValidCatalogItem("item-with-digest")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:  "1.0.0",
					Digest:   lo.ToPtr("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"),
					Channels: []string{"stable"},
				},
			}

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject version with neither tag nor digest", func() {
			item := createValidCatalogItem("item-missing-both")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:  "1.0.0",
					Channels: []string{"stable"},
				},
			}

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("exactly one of tag or digest must be specified"))
		})

		It("should reject version with both tag and digest", func() {
			item := createValidCatalogItem("item-has-both")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:  "1.0.0",
					Tag:      lo.ToPtr("v1.0.0"),
					Digest:   lo.ToPtr("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"),
					Channels: []string{"stable"},
				},
			}

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("tag and digest are mutually exclusive"))
		})

		It("should reject when any version in the list has neither tag nor digest", func() {
			item := createValidCatalogItem("item-mixed-invalid")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:  "2.0.0",
					Tag:      lo.ToPtr("v2.0.0"),
					Channels: []string{"fast"},
				},
				{
					Version:  "1.0.0",
					Channels: []string{"stable"},
				},
			}

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("exactly one of tag or digest must be specified"))
		})
	})

	Context("CatalogItem category and type validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog-types"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog Types"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept system category with os type", func() {
			item := createValidCatalogItem("system-os")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeOS

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject system category with container type", func() {
			item := createValidCatalogItem("system-container-invalid")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeContainer

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("not valid for category"))
		})

		It("should accept application category with container type", func() {
			item := createValidCatalogItem("app-container")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategoryApplication)
			item.Spec.Type = api.CatalogItemTypeContainer

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject application category with os type", func() {
			item := createValidCatalogItem("app-os-invalid")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategoryApplication)
			item.Spec.Type = api.CatalogItemTypeOS

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("not valid for category"))
		})

		It("should accept application category with data type", func() {
			item := createValidCatalogItem("app-data")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategoryApplication)
			item.Spec.Type = api.CatalogItemTypeData

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject system category with data type", func() {
			item := createValidCatalogItem("system-data-invalid")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeData

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("not valid for category"))
		})
	})

	Context("CatalogItem required fields validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog-required"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog Required"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject item missing reference.uri", func() {
			item := api.CatalogItem{
				Metadata: api.CatalogItemMeta{
					Name: lo.ToPtr("missing-uri"),
				},
				Spec: api.CatalogItemSpec{
					Category: lo.ToPtr(api.CatalogItemCategoryApplication),
					Type:     api.CatalogItemTypeContainer,
					Reference: api.CatalogItemReference{
						Uri: "",
					},
					Versions: []api.CatalogItemVersion{
						{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}},
					},
				},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("spec.reference.uri"))
		})

		It("should reject item with no versions", func() {
			item := api.CatalogItem{
				Metadata: api.CatalogItemMeta{
					Name: lo.ToPtr("no-versions"),
				},
				Spec: api.CatalogItemSpec{
					Category: lo.ToPtr(api.CatalogItemCategoryApplication),
					Type:     api.CatalogItemTypeContainer,
					Reference: api.CatalogItemReference{
						Uri: "quay.io/test/image",
					},
					Versions: []api.CatalogItemVersion{},
				},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("at least one entry"))
		})

		It("should reject version with empty channels", func() {
			item := createValidCatalogItem("empty-channels")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{}},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("channels"))
		})

		It("should use default category when not specified", func() {
			item := createValidCatalogItem("default-category")
			item.Spec.Category = nil
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject empty type", func() {
			item := createValidCatalogItem("empty-type")
			item.Spec.Type = ""
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("spec.type"))
		})
	})

	Context("CatalogItem semver validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog-semver"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog Semver"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept valid semver versions", func() {
			item := createValidCatalogItem("valid-semver")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}},
				{Version: "2.0.0-beta.1", Tag: lo.ToPtr("v2.0.0-beta.1"), Channels: []string{"fast"}},
				{Version: "3.0.0+build.123", Tag: lo.ToPtr("v3.0.0"), Channels: []string{"fast"}},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject invalid semver version", func() {
			item := createValidCatalogItem("invalid-semver")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "not-semver", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("semver"))
		})

		It("should reject version with v prefix", func() {
			item := createValidCatalogItem("v-prefix")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "v1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("must not have 'v' prefix"))
		})
	})

	Context("CatalogItem digest format validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog-digest"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog Digest"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept valid sha256 digest", func() {
			item := createValidCatalogItem("valid-digest")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:  "1.0.0",
					Digest:   lo.ToPtr("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"),
					Channels: []string{"stable"},
				},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject invalid digest format", func() {
			item := createValidCatalogItem("invalid-digest")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:  "1.0.0",
					Digest:   lo.ToPtr("not-a-valid-digest"),
					Channels: []string{"stable"},
				},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("digest"))
		})
	})

	Context("CatalogItem duplicate version validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog-dups"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog Duplicates"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject duplicate version numbers", func() {
			item := createValidCatalogItem("dup-versions")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}},
				{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0-alt"), Channels: []string{"fast"}},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("duplicate"))
		})
	})

	Context("CatalogItem replaces/skips validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog-edges"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog Edges"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept valid replaces reference", func() {
			item := createValidCatalogItem("valid-replaces")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "2.0.0", Tag: lo.ToPtr("v2.0.0"), Channels: []string{"stable"}, Replaces: lo.ToPtr("1.0.0")},
				{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept replaces referencing any valid semver (not validated against versions list)", func() {
			item := createValidCatalogItem("replaces-any-semver")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "2.0.0", Tag: lo.ToPtr("v2.0.0"), Channels: []string{"stable"}, Replaces: lo.ToPtr("0.9.0")},
				{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept valid skips references", func() {
			item := createValidCatalogItem("valid-skips")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "3.0.0", Tag: lo.ToPtr("v3.0.0"), Channels: []string{"stable"}, Skips: &[]string{"2.0.0", "1.0.0"}},
				{Version: "2.0.0", Tag: lo.ToPtr("v2.0.0"), Channels: []string{"fast"}},
				{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept skips referencing any valid semver (not validated against versions list)", func() {
			item := createValidCatalogItem("skips-any-semver")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "2.0.0", Tag: lo.ToPtr("v2.0.0"), Channels: []string{"stable"}, Skips: &[]string{"0.9.0"}},
				{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})
	})

	Context("CatalogItem invalid type validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog-invalid-type"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog Invalid Type"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject invalid type value", func() {
			item := createValidCatalogItem("invalid-type")
			item.Spec.Type = api.CatalogItemType("invalid")

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("spec.type must be one of"))
		})

		It("should reject invalid category value", func() {
			item := createValidCatalogItem("invalid-category")
			invalidCategory := api.CatalogItemCategory("invalid")
			item.Spec.Category = &invalidCategory

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("spec.category must be"))
		})

		It("should reject invalid visibility value", func() {
			item := createValidCatalogItem("invalid-visibility")
			invalidVisibility := api.CatalogItemVisibility("invalid")
			item.Spec.Visibility = &invalidVisibility

			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("spec.visibility must be"))
		})
	})

	Context("CatalogItem related artifacts validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog-related"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog Related"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept valid artifacts", func() {
			item := createValidCatalogItem("valid-artifacts")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeOS
			item.Spec.Reference = api.CatalogItemReference{
				Uri: "quay.io/redhat/rhel-bootc",
				Artifacts: &[]api.CatalogItemArtifact{
					{Type: lo.ToPtr(api.CatalogItemArtifactTypeQcow2), Uri: "quay.io/redhat/rhel-bootc-qcow2"},
					{Type: lo.ToPtr(api.CatalogItemArtifactTypeIso), Uri: "quay.io/redhat/rhel-bootc-iso"},
				},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject artifact missing uri", func() {
			item := createValidCatalogItem("artifact-missing-uri")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeOS
			item.Spec.Reference = api.CatalogItemReference{
				Uri: "quay.io/redhat/rhel-bootc",
				Artifacts: &[]api.CatalogItemArtifact{
					{Type: lo.ToPtr(api.CatalogItemArtifactTypeQcow2), Uri: ""},
				},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("uri is required"))
		})

		It("should reject multiple artifacts missing type", func() {
			item := createValidCatalogItem("artifact-missing-type")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeOS
			item.Spec.Reference = api.CatalogItemReference{
				Uri: "quay.io/redhat/rhel-bootc",
				Artifacts: &[]api.CatalogItemArtifact{
					{Uri: "quay.io/redhat/rhel-bootc-qcow2"},
					{Uri: "quay.io/redhat/rhel-bootc-iso"},
				},
			}
			_, status := suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("type is required"))
		})
	})

	Context("Catalog deletion with items", func() {
		It("should prevent deletion of non-empty catalog", func() {
			catalogName := "catalog-with-items"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Catalog With Items"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			item := createValidCatalogItem("test-item")
			_, status = suite.Handler.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			status = suite.Handler.DeleteCatalog(suite.Ctx, suite.OrgID, catalogName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusConflict))
		})

		It("should allow deletion of empty catalog", func() {
			catalogName := "empty-catalog"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Empty Catalog"),
				},
			}
			_, status := suite.Handler.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			status = suite.Handler.DeleteCatalog(suite.Ctx, suite.OrgID, catalogName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		})
	})
})

func createValidCatalogItem(name string) api.CatalogItem {
	return api.CatalogItem{
		Metadata: api.CatalogItemMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.CatalogItemSpec{
			DisplayName: lo.ToPtr("Test Item"),
			Category:    lo.ToPtr(api.CatalogItemCategoryApplication),
			Type:        api.CatalogItemTypeContainer,
			Reference: api.CatalogItemReference{
				Uri: "quay.io/test/image",
			},
			Versions: []api.CatalogItemVersion{
				{
					Version:  "1.0.0",
					Tag:      lo.ToPtr("v1.0.0"),
					Channels: []string{"stable"},
				},
			},
		},
	}
}
