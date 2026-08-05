package service_test

import (
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	apiversioning "github.com/flightctl/flightctl/api/versioning"
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
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept version with tag reference", func() {
			item := createValidCatalogItem("item-with-tag")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"},
					Channels:   []string{"stable"},
				},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept version with digest reference", func() {
			item := createValidCatalogItem("item-with-digest")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"container": "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"},
					Channels:   []string{"stable"},
				},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject version with missing references", func() {
			item := createValidCatalogItem("item-missing-refs")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:  "1.0.0",
					Channels: []string{"stable"},
				},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("references: required"))
		})

		It("should reject version with empty references map", func() {
			item := createValidCatalogItem("item-empty-refs")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{},
					Channels:   []string{"stable"},
				},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("references: required"))
		})

		It("should reject when any version in the list has missing references", func() {
			item := createValidCatalogItem("item-mixed-invalid")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "2.0.0",
					References: map[api.CatalogItemArtifactType]string{"container": "v2.0.0"},
					Channels:   []string{"fast"},
				},
				{
					Version:  "1.0.0",
					Channels: []string{"stable"},
				},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("references: required"))
		})

		It("should reject references key not matching artifact type", func() {
			item := createValidCatalogItem("item-bad-ref-key")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"nonexistent": "v1.0.0"},
					Channels:   []string{"stable"},
				},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("does not match any artifact type"))
		})

		It("should accept mixed tag and digest references", func() {
			item := createValidCatalogItem("item-mixed-refs")
			item.Spec.Artifacts = []api.CatalogItemArtifact{
				{Type: api.CatalogItemArtifactTypeContainer, Uri: "quay.io/test/image"},
				{Type: api.CatalogItemArtifactTypeQcow2, Uri: "quay.io/test/image-qcow2"},
			}
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version: "1.0.0",
					References: map[api.CatalogItemArtifactType]string{
						"container": "v1.0.0",
						"qcow2":     "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
					},
					Channels: []string{"stable"},
				},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept evolving artifacts across versions", func() {
			item := createValidCatalogItem("item-evolving")
			item.Spec.Artifacts = []api.CatalogItemArtifact{
				{Type: api.CatalogItemArtifactTypeContainer, Uri: "quay.io/test/gateway"},
				{Type: api.CatalogItemArtifactTypeQcow2, Uri: "quay.io/test/gateway-appliance"},
				{Type: api.CatalogItemArtifactTypeIso, Uri: "quay.io/test/gateway-iso"},
			}
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "3.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v3.0", "qcow2": "v3.0", "iso": "v3.0"}, Channels: []string{"fast"}, Replaces: lo.ToPtr("2.0.0")},
				{Version: "2.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v2.0", "qcow2": "v2.0"}, Channels: []string{"stable"}, Replaces: lo.ToPtr("1.0.0")},
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0"}, Channels: []string{"stable"}},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
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
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept system category with os type", func() {
			item := createValidCatalogItem("system-os")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeOS

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject system category with container type", func() {
			item := createValidCatalogItem("system-container-invalid")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeContainer

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("not valid for category"))
		})

		It("should accept application category with container type", func() {
			item := createValidCatalogItem("app-container")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategoryApplication)
			item.Spec.Type = api.CatalogItemTypeContainer

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject application category with os type", func() {
			item := createValidCatalogItem("app-os-invalid")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategoryApplication)
			item.Spec.Type = api.CatalogItemTypeOS

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("not valid for category"))
		})

		It("should accept application category with data type", func() {
			item := createValidCatalogItem("app-data")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategoryApplication)
			item.Spec.Type = api.CatalogItemTypeData

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject system category with data type", func() {
			item := createValidCatalogItem("system-data-invalid")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeData

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
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
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject item with artifact missing uri", func() {
			item := api.CatalogItem{
				Metadata: api.CatalogItemMeta{
					Name: lo.ToPtr("missing-uri"),
				},
				Spec: api.CatalogItemSpec{
					Category: lo.ToPtr(api.CatalogItemCategoryApplication),
					Type:     api.CatalogItemTypeContainer,
					Artifacts: []api.CatalogItemArtifact{
						{Type: api.CatalogItemArtifactTypeContainer, Uri: ""},
					},
					Versions: []api.CatalogItemVersion{
						{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
					},
				},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("uri is required"))
		})

		It("should reject item with no versions", func() {
			item := api.CatalogItem{
				Metadata: api.CatalogItemMeta{
					Name: lo.ToPtr("no-versions"),
				},
				Spec: api.CatalogItemSpec{
					Category: lo.ToPtr(api.CatalogItemCategoryApplication),
					Type:     api.CatalogItemTypeContainer,
					Artifacts: []api.CatalogItemArtifact{
						{Type: api.CatalogItemArtifactTypeContainer, Uri: "quay.io/test/image"},
					},
					Versions: []api.CatalogItemVersion{},
				},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("at least one entry"))
		})

		It("should reject version with empty channels", func() {
			item := createValidCatalogItem("empty-channels")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("channels"))
		})

		It("should use default category when not specified", func() {
			item := createValidCatalogItem("default-category")
			item.Spec.Category = nil
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject empty type", func() {
			item := createValidCatalogItem("empty-type")
			item.Spec.Type = ""
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
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
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept valid semver versions", func() {
			item := createValidCatalogItem("valid-semver")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
				{Version: "2.0.0-beta.1", References: map[api.CatalogItemArtifactType]string{"container": "v2.0.0-beta.1"}, Channels: []string{"fast"}},
				{Version: "3.0.0+build.123", References: map[api.CatalogItemArtifactType]string{"container": "v3.0.0"}, Channels: []string{"fast"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject invalid semver version", func() {
			item := createValidCatalogItem("invalid-semver")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "not-semver", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("semver"))
		})

		It("should reject version with v prefix", func() {
			item := createValidCatalogItem("v-prefix")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "v1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("must not have 'v' prefix"))
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
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject duplicate version numbers", func() {
			item := createValidCatalogItem("dup-versions")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0-alt"}, Channels: []string{"fast"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
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
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept valid replaces reference", func() {
			item := createValidCatalogItem("valid-replaces")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "2.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v2.0.0"}, Channels: []string{"stable"}, Replaces: lo.ToPtr("1.0.0")},
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept replaces referencing any valid semver (not validated against versions list)", func() {
			item := createValidCatalogItem("replaces-any-semver")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "2.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v2.0.0"}, Channels: []string{"stable"}, Replaces: lo.ToPtr("0.9.0")},
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept valid skips references", func() {
			item := createValidCatalogItem("valid-skips")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "3.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v3.0.0"}, Channels: []string{"stable"}, Skips: &[]string{"2.0.0", "1.0.0"}},
				{Version: "2.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v2.0.0"}, Channels: []string{"fast"}},
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept skips referencing any valid semver (not validated against versions list)", func() {
			item := createValidCatalogItem("skips-any-semver")
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "2.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v2.0.0"}, Channels: []string{"stable"}, Skips: &[]string{"0.9.0"}},
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
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
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject invalid type value", func() {
			item := createValidCatalogItem("invalid-type")
			item.Spec.Type = api.CatalogItemType("invalid")

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("spec.type must be one of"))
		})

		It("should reject invalid category value", func() {
			item := createValidCatalogItem("invalid-category")
			invalidCategory := api.CatalogItemCategory("invalid")
			item.Spec.Category = &invalidCategory

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("spec.category must be"))
		})
	})

	Context("CatalogItem artifacts validation", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "test-catalog-artifacts"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Test Catalog Artifacts"),
				},
			}
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept multi-artifact item", func() {
			item := createValidCatalogItem("valid-artifacts")
			item.Spec.Category = lo.ToPtr(api.CatalogItemCategorySystem)
			item.Spec.Type = api.CatalogItemTypeOS
			item.Spec.Artifacts = []api.CatalogItemArtifact{
				{Type: api.CatalogItemArtifactTypeContainer, Uri: "quay.io/redhat/rhel-bootc"},
				{Type: api.CatalogItemArtifactTypeQcow2, Uri: "quay.io/redhat/rhel-bootc-qcow2"},
				{Type: api.CatalogItemArtifactTypeIso, Uri: "quay.io/redhat/rhel-bootc-iso"},
			}
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "9.4.0", References: map[api.CatalogItemArtifactType]string{"container": "9.4", "qcow2": "9.4", "iso": "9.4"}, Channels: []string{"stable"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject artifact missing uri", func() {
			item := createValidCatalogItem("artifact-missing-uri")
			item.Spec.Artifacts = []api.CatalogItemArtifact{
				{Type: api.CatalogItemArtifactTypeContainer, Uri: "quay.io/test/image"},
				{Type: api.CatalogItemArtifactTypeQcow2, Uri: ""},
			}
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("uri is required"))
		})

		It("should reject artifact missing type", func() {
			item := createValidCatalogItem("artifact-missing-type")
			item.Spec.Artifacts = []api.CatalogItemArtifact{
				{Type: api.CatalogItemArtifactTypeContainer, Uri: "quay.io/test/image"},
				{Uri: "quay.io/test/image-qcow2"},
			}
			item.Spec.Versions = []api.CatalogItemVersion{
				{Version: "1.0.0", References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"}, Channels: []string{"stable"}},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(status.Message).To(ContainSubstring("type is required"))
		})
	})

	Context("Cross-catalog CatalogItem listing", func() {
		It("should list items across all catalogs", func() {
			// Create two catalogs
			for _, name := range []string{"alpha-catalog", "beta-catalog"} {
				catalog := api.Catalog{
					Metadata: apiv1beta1.ObjectMeta{
						Name: lo.ToPtr(name),
					},
					Spec: api.CatalogSpec{
						DisplayName: lo.ToPtr(name),
					},
				}
				_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
				Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			}

			// Create items in each catalog
			for _, catalogName := range []string{"alpha-catalog", "beta-catalog"} {
				for _, itemName := range []string{"app-one", "app-two"} {
					item := createValidCatalogItem(itemName)
					_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
					Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
				}
			}

			// List all items across catalogs
			result, status := suite.Catalog.ListAllCatalogItems(suite.Ctx, suite.OrgID, api.ListAllCatalogItemsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(4))

			// Verify order: (catalog_name, app_name) ASC
			Expect(*result.Items[0].Metadata.Name).To(Equal("app-one"))
			Expect(result.Items[0].Metadata.Catalog).To(Equal("alpha-catalog"))
			Expect(*result.Items[1].Metadata.Name).To(Equal("app-two"))
			Expect(result.Items[1].Metadata.Catalog).To(Equal("alpha-catalog"))
			Expect(*result.Items[2].Metadata.Name).To(Equal("app-one"))
			Expect(result.Items[2].Metadata.Catalog).To(Equal("beta-catalog"))
			Expect(*result.Items[3].Metadata.Name).To(Equal("app-two"))
			Expect(result.Items[3].Metadata.Catalog).To(Equal("beta-catalog"))
		})

		It("should paginate across catalogs", func() {
			// Create two catalogs with items
			for _, name := range []string{"cat-a", "cat-b"} {
				catalog := api.Catalog{
					Metadata: apiv1beta1.ObjectMeta{
						Name: lo.ToPtr(name),
					},
					Spec: api.CatalogSpec{
						DisplayName: lo.ToPtr(name),
					},
				}
				_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
				Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

				item := createValidCatalogItem("item-1")
				_, status = suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, name, item)
				Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			}

			// Page 1: limit=1
			limit := int32(1)
			result, status := suite.Catalog.ListAllCatalogItems(suite.Ctx, suite.OrgID, api.ListAllCatalogItemsParams{
				Limit: &limit,
			})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].Metadata.Catalog).To(Equal("cat-a"))
			Expect(result.Metadata.Continue).NotTo(BeNil())
			Expect(result.Metadata.RemainingItemCount).NotTo(BeNil())

			// Page 2: use continue token
			result, status = suite.Catalog.ListAllCatalogItems(suite.Ctx, suite.OrgID, api.ListAllCatalogItemsParams{
				Limit:    &limit,
				Continue: result.Metadata.Continue,
			})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].Metadata.Catalog).To(Equal("cat-b"))
			Expect(result.Metadata.Continue).To(BeNil())
		})

		It("should filter by label selector across catalogs", func() {
			// Create two catalogs
			for _, name := range []string{"label-cat-a", "label-cat-b"} {
				catalog := api.Catalog{
					Metadata: apiv1beta1.ObjectMeta{
						Name: lo.ToPtr(name),
					},
					Spec: api.CatalogSpec{
						DisplayName: lo.ToPtr(name),
					},
				}
				_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
				Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			}

			// Create items with different labels
			itemWithLabel := createValidCatalogItem("labeled-app")
			itemWithLabel.Metadata.Labels = &map[string]string{"env": "prod"}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, "label-cat-a", itemWithLabel)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			itemWithoutLabel := createValidCatalogItem("unlabeled-app")
			_, status = suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, "label-cat-b", itemWithoutLabel)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			// Filter by label
			labelSelector := "env=prod"
			result, status := suite.Catalog.ListAllCatalogItems(suite.Ctx, suite.OrgID, api.ListAllCatalogItemsParams{
				LabelSelector: &labelSelector,
			})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(*result.Items[0].Metadata.Name).To(Equal("labeled-app"))
			Expect(result.Items[0].Metadata.Catalog).To(Equal("label-cat-a"))
		})

		It("should return empty list when no catalogs exist", func() {
			result, status := suite.Catalog.ListAllCatalogItems(suite.Ctx, suite.OrgID, api.ListAllCatalogItemsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(0))
		})
	})

	Context("CatalogItem deployments", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "deploy-catalog"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Deploy Catalog"),
				},
			}
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			// OS-type items (referenced via spec.Os.CatalogItemRef)
			for _, itemDef := range []struct {
				name     string
				versions []string
			}{
				{"os-image", []string{"9.4.0"}},
				{"multi-os-item", []string{"1.0.0"}},
				{"triple-os-item", []string{"1.0.0"}},
				{"different-item", []string{"1.0.0"}},
			} {
				item := createValidOSCatalogItem(itemDef.name)
				item.Spec.Versions = nil
				for _, v := range itemDef.versions {
					item.Spec.Versions = append(item.Spec.Versions, api.CatalogItemVersion{
						Version:    v,
						References: map[api.CatalogItemArtifactType]string{"qcow2": "v" + v},
						Channels:   []string{"stable"},
					})
				}
				_, itemStatus := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
				Expect(itemStatus.Code).To(BeEquivalentTo(http.StatusCreated))
			}

			// Application-type items (referenced via app spec.Applications)
			for _, itemDef := range []struct {
				name     string
				versions []string
			}{
				{"web-server", []string{"2.1.0"}},
				{"multi-app-item", []string{"1.0.0"}},
				{"triple-app-item", []string{"2.0.0"}},
			} {
				item := createValidCatalogItem(itemDef.name)
				item.Spec.Versions = nil
				for _, v := range itemDef.versions {
					item.Spec.Versions = append(item.Spec.Versions, api.CatalogItemVersion{
						Version:    v,
						References: map[api.CatalogItemArtifactType]string{"container": "v" + v},
						Channels:   []string{"stable"},
					})
				}
				_, itemStatus := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
				Expect(itemStatus.Code).To(BeEquivalentTo(http.StatusCreated))
			}

			// Data-type items (referenced via volume catalog item refs)
			for _, itemDef := range []struct {
				name     string
				versions []string
			}{
				{"data-image", []string{"1.2.0"}},
				{"mount-data", []string{"3.0.0"}},
				{"other-vol-item", []string{"1.0.0"}},
				{"triple-vol-item", []string{"3.0.0"}},
			} {
				item := createValidDataCatalogItem(itemDef.name)
				item.Spec.Versions = nil
				for _, v := range itemDef.versions {
					item.Spec.Versions = append(item.Spec.Versions, api.CatalogItemVersion{
						Version:    v,
						References: map[api.CatalogItemArtifactType]string{"container": "v" + v},
						Channels:   []string{"stable"},
					})
				}
				_, itemStatus := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
				Expect(itemStatus.Code).To(BeEquivalentTo(http.StatusCreated))
			}
		})

		It("should return an empty list when no devices reference the catalog item", func() {
			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "nonexistent-item", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result).ToNot(BeNil())
			Expect(result.ApiVersion).To(Equal(apiversioning.QualifiedV1Alpha1))
			Expect(result.Kind).To(Equal(api.CatalogItemDeploymentListKind))
			Expect(result.Items).To(BeEmpty())
		})

		It("should return a deployment when a device has an OS catalog item ref", func() {
			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("os-ref-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Os: &apiv1beta1.DeviceOsSpec{
						CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
							Catalog: catalogName,
							Item:    "os-image",
							Version: "9.4.0",
							Channel: lo.ToPtr("stable"),
						},
					},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "os-image", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].Catalog).To(Equal(catalogName))
			Expect(result.Items[0].CatalogItem).To(Equal("os-image"))
			Expect(result.Items[0].Version).To(Equal("9.4.0"))
			Expect(result.Items[0].Channel).To(HaveValue(Equal("stable")))
			Expect(result.Items[0].ApplicationName).To(BeNil())
		})

		It("should return a deployment when a device has an app catalog item ref", func() {
			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("my-web-app"),
			}
			err := container.FromCatalogItemRefApplicationProviderSpec(apiv1beta1.CatalogItemRefApplicationProviderSpec{
				CatalogItemRef: apiv1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    "web-server",
					Version: "2.1.0",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("app-ref-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "web-server", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].Catalog).To(Equal(catalogName))
			Expect(result.Items[0].CatalogItem).To(Equal("web-server"))
			Expect(result.Items[0].Version).To(Equal("2.1.0"))
			Expect(result.Items[0].ApplicationName).To(HaveValue(Equal("my-web-app")))
		})

		It("should return deployments from both OS and app refs across devices", func() {
			// Device 1: OS catalog ref
			osDevice := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("combined-os-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Os: &apiv1beta1.DeviceOsSpec{
						CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
							Catalog: catalogName,
							Item:    "multi-os-item",
							Version: "1.0.0",
						},
					},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, osDevice)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "multi-os-item", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].Catalog).To(Equal(catalogName))
			Expect(result.Items[0].CatalogItem).To(Equal("multi-os-item"))
			Expect(result.Items[0].ApplicationName).To(BeNil())

			// Device 2: app catalog ref to a different item
			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("sidecar"),
			}
			err := container.FromCatalogItemRefApplicationProviderSpec(apiv1beta1.CatalogItemRefApplicationProviderSpec{
				CatalogItemRef: apiv1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    "multi-app-item",
					Version: "1.0.0",
				},
			})
			Expect(err).ToNot(HaveOccurred())
			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			appDevice := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("combined-app-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus = suite.Device.CreateDevice(suite.Ctx, suite.OrgID, appDevice)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			result, status = suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "multi-app-item", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].Catalog).To(Equal(catalogName))
			Expect(result.Items[0].CatalogItem).To(Equal("multi-app-item"))
			Expect(result.Items[0].ApplicationName).To(HaveValue(Equal("sidecar")))
		})

		It("should return a deployment when a device has a volume catalog item ref (image type)", func() {
			vol := apiv1beta1.ApplicationVolume{Name: "data-vol"}
			err := vol.FromImageVolumeProviderSpec(apiv1beta1.ImageVolumeProviderSpec{
				Image: apiv1beta1.ImageVolumeSource{
					CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
						Catalog: catalogName,
						Item:    "data-image",
						Version: "1.2.0",
						Channel: lo.ToPtr("stable"),
					},
				},
			})
			Expect(err).ToNot(HaveOccurred())

			quadlet := apiv1beta1.QuadletApplication{
				AppType: apiv1beta1.AppTypeQuadlet,
				Name:    lo.ToPtr("vol-app"),
				Volumes: &[]apiv1beta1.ApplicationVolume{vol},
			}
			err = quadlet.FromImageApplicationProviderSpec(apiv1beta1.ImageApplicationProviderSpec{
				Image: "quay.io/example/app:latest",
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromQuadletApplication(quadlet)
			Expect(err).ToNot(HaveOccurred())

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("vol-ref-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "data-image", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].Catalog).To(Equal(catalogName))
			Expect(result.Items[0].CatalogItem).To(Equal("data-image"))
			Expect(result.Items[0].Version).To(Equal("1.2.0"))
			Expect(result.Items[0].Channel).To(HaveValue(Equal("stable")))
			Expect(result.Items[0].ApplicationName).To(HaveValue(Equal("vol-app")))
		})

		It("should return a deployment when a device has a volume catalog item ref (image_mount type)", func() {
			vol := apiv1beta1.ApplicationVolume{Name: "mount-vol"}
			err := vol.FromImageMountVolumeProviderSpec(apiv1beta1.ImageMountVolumeProviderSpec{
				Image: apiv1beta1.ImageVolumeSource{
					CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
						Catalog: catalogName,
						Item:    "mount-data",
						Version: "3.0.0",
					},
				},
				Mount: apiv1beta1.VolumeMount{
					Path: "/data",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("mount-app"),
				Volumes: &[]apiv1beta1.ApplicationVolume{vol},
			}
			err = container.FromImageApplicationProviderSpec(apiv1beta1.ImageApplicationProviderSpec{
				Image: "quay.io/example/container:latest",
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("mount-vol-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "mount-data", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].Catalog).To(Equal(catalogName))
			Expect(result.Items[0].CatalogItem).To(Equal("mount-data"))
			Expect(result.Items[0].Version).To(Equal("3.0.0"))
			Expect(result.Items[0].ApplicationName).To(HaveValue(Equal("mount-app")))
		})

		It("should return deployments from OS, app, and volume refs across devices", func() {
			// Device 1: OS catalog ref
			osDevice := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("triple-os-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Os: &apiv1beta1.DeviceOsSpec{
						CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
							Catalog: catalogName,
							Item:    "triple-os-item",
							Version: "1.0.0",
						},
					},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, osDevice)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			// Device 2: app catalog ref
			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("triple-app"),
			}
			err := container.FromCatalogItemRefApplicationProviderSpec(apiv1beta1.CatalogItemRefApplicationProviderSpec{
				CatalogItemRef: apiv1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    "triple-app-item",
					Version: "2.0.0",
				},
			})
			Expect(err).ToNot(HaveOccurred())
			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			appDevice := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("triple-app-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus = suite.Device.CreateDevice(suite.Ctx, suite.OrgID, appDevice)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			// Device 3: volume catalog ref
			vol := apiv1beta1.ApplicationVolume{Name: "triple-vol"}
			err = vol.FromImageVolumeProviderSpec(apiv1beta1.ImageVolumeProviderSpec{
				Image: apiv1beta1.ImageVolumeSource{
					CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
						Catalog: catalogName,
						Item:    "triple-vol-item",
						Version: "3.0.0",
					},
				},
			})
			Expect(err).ToNot(HaveOccurred())

			volQuadlet := apiv1beta1.QuadletApplication{
				AppType: apiv1beta1.AppTypeQuadlet,
				Name:    lo.ToPtr("triple-vol-app"),
				Volumes: &[]apiv1beta1.ApplicationVolume{vol},
			}
			err = volQuadlet.FromImageApplicationProviderSpec(apiv1beta1.ImageApplicationProviderSpec{
				Image: "quay.io/example/vol:latest",
			})
			Expect(err).ToNot(HaveOccurred())
			var volAppSpec apiv1beta1.ApplicationProviderSpec
			err = volAppSpec.FromQuadletApplication(volQuadlet)
			Expect(err).ToNot(HaveOccurred())

			volDevice := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("triple-vol-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{volAppSpec},
				},
			}
			_, devStatus = suite.Device.CreateDevice(suite.Ctx, suite.OrgID, volDevice)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "triple-os-item", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].ApplicationName).To(BeNil())

			result, status = suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "triple-app-item", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].ApplicationName).To(HaveValue(Equal("triple-app")))

			result, status = suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "triple-vol-item", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(1))
			Expect(result.Items[0].ApplicationName).To(HaveValue(Equal("triple-vol-app")))
		})

		It("should not return devices whose volumes reference a different catalog item", func() {
			vol := apiv1beta1.ApplicationVolume{Name: "other-vol"}
			err := vol.FromImageVolumeProviderSpec(apiv1beta1.ImageVolumeProviderSpec{
				Image: apiv1beta1.ImageVolumeSource{
					CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
						Catalog: catalogName,
						Item:    "other-vol-item",
						Version: "1.0.0",
					},
				},
			})
			Expect(err).ToNot(HaveOccurred())

			quadlet := apiv1beta1.QuadletApplication{
				AppType: apiv1beta1.AppTypeQuadlet,
				Name:    lo.ToPtr("other-vol-app"),
				Volumes: &[]apiv1beta1.ApplicationVolume{vol},
			}
			err = quadlet.FromImageApplicationProviderSpec(apiv1beta1.ImageApplicationProviderSpec{
				Image: "quay.io/example/app:latest",
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromQuadletApplication(quadlet)
			Expect(err).ToNot(HaveOccurred())

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("other-vol-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "target-vol-item", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(BeEmpty())
		})

		It("should not return devices that reference a different catalog item", func() {
			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("other-item-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Os: &apiv1beta1.DeviceOsSpec{
						CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
							Catalog: catalogName,
							Item:    "different-item",
							Version: "1.0.0",
						},
					},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "target-item", api.GetCatalogItemDeploymentsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(BeEmpty())
		})

		It("should paginate deployments when limit is set", func() {
			for i := 0; i < 3; i++ {
				device := apiv1beta1.Device{
					Metadata: apiv1beta1.ObjectMeta{
						Name: lo.ToPtr(fmt.Sprintf("paginate-dev-%d", i)),
					},
					Spec: &apiv1beta1.DeviceSpec{
						Os: &apiv1beta1.DeviceOsSpec{
							CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
								Catalog: catalogName,
								Item:    "os-image",
								Version: "9.4.0",
								Channel: lo.ToPtr("stable"),
							},
						},
					},
				}
				_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
				Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))
			}

			limit := int32(2)
			result, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "os-image", api.GetCatalogItemDeploymentsParams{
				Limit: &limit,
			})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result.Items).To(HaveLen(2))
			Expect(result.Metadata.Continue).ToNot(BeNil())
			Expect(result.Metadata.RemainingItemCount).ToNot(BeNil())

			result2, status := suite.Catalog.GetCatalogItemDeployments(suite.Ctx, suite.OrgID, catalogName, "os-image", api.GetCatalogItemDeploymentsParams{
				Limit:    &limit,
				Continue: result.Metadata.Continue,
			})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(result2.Items).To(HaveLen(1))
			Expect(result2.Metadata.Continue).To(BeNil())
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
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			item := createValidCatalogItem("test-item")
			_, status = suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			status = suite.Catalog.DeleteCatalog(suite.Ctx, suite.OrgID, catalogName, true)
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
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			status = suite.Catalog.DeleteCatalog(suite.Ctx, suite.OrgID, catalogName, true)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		})
	})

	Context("CatalogItem deletion and edit with in-use versions", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "inuse-catalog"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("In-Use Catalog"),
				},
			}
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should prevent deletion of a catalog item with versions in use by devices", func() {
			item := createValidOSCatalogItem("inuse-item")
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("inuse-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Os: &apiv1beta1.DeviceOsSpec{
						CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
							Catalog: catalogName,
							Item:    "inuse-item",
							Version: "1.0.0",
						},
					},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			status = suite.Catalog.DeleteCatalogItem(suite.Ctx, suite.OrgID, catalogName, "inuse-item", true)
			Expect(status.Code).To(BeEquivalentTo(http.StatusConflict))
			Expect(status.Message).To(ContainSubstring("in use by devices"))
		})

		It("should prevent replacing a catalog item when an in-use version is removed", func() {
			item := createValidOSCatalogItem("replace-inuse-item")
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("replace-inuse-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Os: &apiv1beta1.DeviceOsSpec{
						CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{
							Catalog: catalogName,
							Item:    "replace-inuse-item",
							Version: "1.0.0",
						},
					},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))

			updated := createValidOSCatalogItem("replace-inuse-item")
			updated.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "2.0.0",
					References: map[api.CatalogItemArtifactType]string{"qcow2": "v2.0.0"},
					Channels:   []string{"stable"},
				},
			}
			_, status = suite.Catalog.ReplaceCatalogItem(suite.Ctx, suite.OrgID, catalogName, "replace-inuse-item", updated, true)
			Expect(status.Code).To(BeEquivalentTo(http.StatusConflict))
			Expect(status.Message).To(ContainSubstring("in use by devices"))
			Expect(status.Message).To(ContainSubstring("1.0.0"))
		})

		It("should allow replacing a catalog item when only non-deployed versions change", func() {
			item := createValidCatalogItem("replace-ok-item")
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			updated := createValidCatalogItem("replace-ok-item")
			updated.Spec.Versions = append(updated.Spec.Versions, api.CatalogItemVersion{
				Version:    "2.0.0",
				References: map[api.CatalogItemArtifactType]string{"container": "v2.0.0"},
				Channels:   []string{"fast"},
			})
			_, status = suite.Catalog.ReplaceCatalogItem(suite.Ctx, suite.OrgID, catalogName, "replace-ok-item", updated, true)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		})

		It("should allow deletion of a catalog item with no devices referencing it", func() {
			item := createValidCatalogItem("unreferenced-item")
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			status = suite.Catalog.DeleteCatalogItem(suite.Ctx, suite.OrgID, catalogName, "unreferenced-item", true)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		})
	})

	Context("ConfigSchema validation for devices", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "schema-catalog"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Schema Catalog"),
				},
			}
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should accept a device whose app conforms to the catalog item configSchema", func() {
			item := createValidCatalogItem("schema-app")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"},
					Channels:   []string{"stable"},
					ConfigSchema: &map[string]interface{}{
						"type":     "object",
						"required": []interface{}{"envVars"},
						"properties": map[string]interface{}{
							"envVars": map[string]interface{}{
								"type":     "object",
								"required": []interface{}{"environment"},
								"properties": map[string]interface{}{
									"environment": map[string]interface{}{"type": "string"},
								},
							},
						},
					},
				},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("my-app"),
				EnvVars: &map[string]string{"environment": "production"},
			}
			err := container.FromCatalogItemRefApplicationProviderSpec(apiv1beta1.CatalogItemRefApplicationProviderSpec{
				CatalogItemRef: apiv1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    "schema-app",
					Version: "1.0.0",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("schema-ok-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject a device whose app is missing a required envvar", func() {
			item := createValidCatalogItem("strict-app")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"},
					Channels:   []string{"stable"},
					ConfigSchema: &map[string]interface{}{
						"type":     "object",
						"required": []interface{}{"envVars"},
						"properties": map[string]interface{}{
							"envVars": map[string]interface{}{
								"type":     "object",
								"required": []interface{}{"environment"},
								"properties": map[string]interface{}{
									"environment": map[string]interface{}{"type": "string"},
								},
							},
						},
					},
				},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("my-app"),
			}
			err := container.FromCatalogItemRefApplicationProviderSpec(apiv1beta1.CatalogItemRefApplicationProviderSpec{
				CatalogItemRef: apiv1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    "strict-app",
					Version: "1.0.0",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("schema-fail-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(devStatus.Message).To(ContainSubstring("configSchema"))
		})

		It("should accept a device when the catalog item has no configSchema", func() {
			item := createValidCatalogItem("no-schema-app")
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("my-app"),
			}
			err := container.FromCatalogItemRefApplicationProviderSpec(apiv1beta1.CatalogItemRefApplicationProviderSpec{
				CatalogItemRef: apiv1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    "no-schema-app",
					Version: "1.0.0",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("no-schema-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should use defaults configSchema when version has none", func() {
			item := createValidCatalogItem("defaults-schema-app")
			item.Spec.Defaults = &api.CatalogItemConfigurable{
				ConfigSchema: &map[string]interface{}{
					"type":     "object",
					"required": []interface{}{"envVars"},
					"properties": map[string]interface{}{
						"envVars": map[string]interface{}{
							"type":     "object",
							"required": []interface{}{"environment"},
							"properties": map[string]interface{}{
								"environment": map[string]interface{}{"type": "string"},
							},
						},
					},
				},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("my-app"),
			}
			err := container.FromCatalogItemRefApplicationProviderSpec(apiv1beta1.CatalogItemRefApplicationProviderSpec{
				CatalogItemRef: apiv1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    "defaults-schema-app",
					Version: "1.0.0",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			device := apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("defaults-schema-device"),
				},
				Spec: &apiv1beta1.DeviceSpec{
					Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
				},
			}
			_, devStatus := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, device)
			Expect(devStatus.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(devStatus.Message).To(ContainSubstring("configSchema"))
		})
	})

	Context("ConfigSchema validation for fleets", func() {
		var catalogName string

		BeforeEach(func() {
			catalogName = "fleet-schema-catalog"
			catalog := api.Catalog{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr(catalogName),
				},
				Spec: api.CatalogSpec{
					DisplayName: lo.ToPtr("Fleet Schema Catalog"),
				},
			}
			_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, catalog)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		})

		It("should reject a fleet whose template app violates configSchema", func() {
			item := createValidCatalogItem("fleet-strict-app")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"},
					Channels:   []string{"stable"},
					ConfigSchema: &map[string]interface{}{
						"type":     "object",
						"required": []interface{}{"envVars"},
						"properties": map[string]interface{}{
							"envVars": map[string]interface{}{
								"type":     "object",
								"required": []interface{}{"environment"},
								"properties": map[string]interface{}{
									"environment": map[string]interface{}{"type": "string"},
								},
							},
						},
					},
				},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("fleet-app"),
			}
			err := container.FromCatalogItemRefApplicationProviderSpec(apiv1beta1.CatalogItemRefApplicationProviderSpec{
				CatalogItemRef: apiv1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    "fleet-strict-app",
					Version: "1.0.0",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			fleet := apiv1beta1.Fleet{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("schema-fail-fleet"),
				},
				Spec: apiv1beta1.FleetSpec{
					Template: struct {
						Metadata *apiv1beta1.ObjectMeta `json:"metadata,omitempty"`
						Spec     apiv1beta1.DeviceSpec  `json:"spec"`
					}{
						Spec: apiv1beta1.DeviceSpec{
							Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
						},
					},
				},
			}
			_, fleetStatus := suite.Fleet.ReplaceFleet(suite.Ctx, suite.OrgID, "schema-fail-fleet", fleet, false)
			Expect(fleetStatus.Code).To(BeEquivalentTo(http.StatusBadRequest))
			Expect(fleetStatus.Message).To(ContainSubstring("configSchema"))
		})

		It("should accept a fleet whose template app conforms to configSchema with environment envvar", func() {
			item := createValidCatalogItem("fleet-ok-app")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"},
					Channels:   []string{"stable"},
					ConfigSchema: &map[string]interface{}{
						"type":     "object",
						"required": []interface{}{"envVars"},
						"properties": map[string]interface{}{
							"envVars": map[string]interface{}{
								"type":     "object",
								"required": []interface{}{"environment"},
								"properties": map[string]interface{}{
									"environment": map[string]interface{}{"type": "string"},
								},
							},
						},
					},
				},
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			container := apiv1beta1.ContainerApplication{
				AppType: apiv1beta1.AppTypeContainer,
				Name:    lo.ToPtr("fleet-app"),
				EnvVars: &map[string]string{"environment": "staging"},
			}
			err := container.FromCatalogItemRefApplicationProviderSpec(apiv1beta1.CatalogItemRefApplicationProviderSpec{
				CatalogItemRef: apiv1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    "fleet-ok-app",
					Version: "1.0.0",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			var appSpec apiv1beta1.ApplicationProviderSpec
			err = appSpec.FromContainerApplication(container)
			Expect(err).ToNot(HaveOccurred())

			fleet := apiv1beta1.Fleet{
				Metadata: apiv1beta1.ObjectMeta{
					Name: lo.ToPtr("schema-ok-fleet"),
				},
				Spec: apiv1beta1.FleetSpec{
					Template: struct {
						Metadata *apiv1beta1.ObjectMeta `json:"metadata,omitempty"`
						Spec     apiv1beta1.DeviceSpec  `json:"spec"`
					}{
						Spec: apiv1beta1.DeviceSpec{
							Applications: &[]apiv1beta1.ApplicationProviderSpec{appSpec},
						},
					},
				},
			}
			_, fleetStatus := suite.Fleet.ReplaceFleet(suite.Ctx, suite.OrgID, "schema-ok-fleet", fleet, false)
			Expect(fleetStatus.Code).To(BeEquivalentTo(http.StatusCreated))
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
			Artifacts: []api.CatalogItemArtifact{
				{Type: api.CatalogItemArtifactTypeContainer, Uri: "quay.io/test/image"},
			},
			Versions: []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"},
					Channels:   []string{"stable"},
				},
			},
		},
	}
}

func createValidOSCatalogItem(name string) api.CatalogItem {
	return api.CatalogItem{
		Metadata: api.CatalogItemMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.CatalogItemSpec{
			DisplayName: lo.ToPtr("Test OS Item"),
			Category:    lo.ToPtr(api.CatalogItemCategorySystem),
			Type:        api.CatalogItemTypeOS,
			Artifacts: []api.CatalogItemArtifact{
				{Type: api.CatalogItemArtifactTypeQcow2, Uri: "quay.io/test/os-image"},
			},
			Versions: []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"qcow2": "v1.0.0"},
					Channels:   []string{"stable"},
				},
			},
		},
	}
}

func createValidDataCatalogItem(name string) api.CatalogItem {
	return api.CatalogItem{
		Metadata: api.CatalogItemMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.CatalogItemSpec{
			DisplayName: lo.ToPtr("Test Data Item"),
			Category:    lo.ToPtr(api.CatalogItemCategoryApplication),
			Type:        api.CatalogItemTypeData,
			Artifacts: []api.CatalogItemArtifact{
				{Type: api.CatalogItemArtifactTypeContainer, Uri: "quay.io/test/data-image"},
			},
			Versions: []api.CatalogItemVersion{
				{
					Version:    "1.0.0",
					References: map[api.CatalogItemArtifactType]string{"container": "v1.0.0"},
					Channels:   []string{"stable"},
				},
			},
		},
	}
}
