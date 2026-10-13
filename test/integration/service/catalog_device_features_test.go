package service_test

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// Device feature requirements are declared per catalog item version and are
// compared client-side against the server-derived feature.flightctl.io/*
// device labels. The server stores and returns them but never enforces them.
var _ = Describe("CatalogItem device feature requirements", func() {
	var (
		suite       *ServiceTestSuite
		catalogName string
	)

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()

		catalogName = "capability-catalog"
		_, status := suite.Catalog.CreateCatalog(suite.Ctx, suite.OrgID, api.Catalog{
			Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr(catalogName)},
			Spec:     api.CatalogSpec{DisplayName: lo.ToPtr("Capability Catalog")},
		})
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)
	})

	AfterEach(func() {
		suite.Teardown()
	})

	getItem := func(name string) *api.CatalogItem {
		GinkgoHelper()
		fetched, status := suite.Catalog.GetCatalogItem(suite.Ctx, suite.OrgID, catalogName, name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		return fetched
	}

	Context("Declaring requirements", func() {
		It("When a version declares known device features it should persist and return them", func() {
			item := createValidCatalogItem("gpu-workload")
			item.Spec.Versions[0].DeviceFeatures = &api.DeviceFeatures{
				GpuPresent: lo.ToPtr(api.DeviceFeatureBooleanTrue),
				KvmEnabled: lo.ToPtr(api.DeviceFeatureBooleanFalse),
				OsMode:     lo.ToPtr(apiv1beta1.OsModeImage),
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

			fetched := getItem("gpu-workload")
			Expect(fetched.Spec.Versions).To(HaveLen(1))
			features := fetched.Spec.Versions[0].DeviceFeatures
			Expect(features).ToNot(BeNil())
			Expect(lo.FromPtr(features.GpuPresent)).To(Equal(api.DeviceFeatureBooleanTrue))
			Expect(lo.FromPtr(features.KvmEnabled)).To(Equal(api.DeviceFeatureBooleanFalse))
			Expect(lo.FromPtr(features.OsMode)).To(Equal(apiv1beta1.OsModeImage))
		})

		It("When a version declares no device features it should be stored without requirements", func() {
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, createValidCatalogItem("unconstrained-workload"))
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

			Expect(getItem("unconstrained-workload").Spec.Versions[0].DeviceFeatures).To(BeNil())
		})

		It("When successive versions declare different requirements it should keep them independent", func() {
			item := createValidCatalogItem("multi-version-workload")
			item.Spec.Versions = []api.CatalogItemVersion{
				{
					Version:        "1.0.0",
					References:     map[api.CatalogItemArtifactType]string{"container": "v1.0.0"},
					Channels:       []string{"stable"},
					DeviceFeatures: &api.DeviceFeatures{GpuPresent: lo.ToPtr(api.DeviceFeatureBooleanFalse)},
				},
				{
					Version:        "2.0.0",
					References:     map[api.CatalogItemArtifactType]string{"container": "v2.0.0"},
					Channels:       []string{"stable"},
					DeviceFeatures: &api.DeviceFeatures{GpuPresent: lo.ToPtr(api.DeviceFeatureBooleanTrue)},
				},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

			requirementsByVersion := map[string]api.DeviceFeatureBoolean{}
			for _, version := range getItem("multi-version-workload").Spec.Versions {
				Expect(version.DeviceFeatures).ToNot(BeNil())
				requirementsByVersion[version.Version] = lo.FromPtr(version.DeviceFeatures.GpuPresent)
			}
			Expect(requirementsByVersion).To(HaveKeyWithValue("1.0.0", api.DeviceFeatureBooleanFalse))
			Expect(requirementsByVersion).To(HaveKeyWithValue("2.0.0", api.DeviceFeatureBooleanTrue))
		})

		It("When a device feature map carries an unknown key it should be accepted for forward compatibility", func() {
			item := createValidCatalogItem("forward-compatible-workload")
			item.Spec.Versions[0].DeviceFeatures = &api.DeviceFeatures{
				GpuPresent:           lo.ToPtr(api.DeviceFeatureBooleanTrue),
				AdditionalProperties: map[string]interface{}{"cpu.architecture": "amd64"},
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

			features := getItem("forward-compatible-workload").Spec.Versions[0].DeviceFeatures
			Expect(features).ToNot(BeNil())
			Expect(lo.FromPtr(features.GpuPresent)).To(Equal(api.DeviceFeatureBooleanTrue))
			Expect(features.AdditionalProperties).To(HaveKeyWithValue("cpu.architecture", "amd64"))
		})
	})

	Context("Rejecting invalid requirements", func() {
		It("When a known device feature carries an out-of-enum value it should be rejected and not persisted", func() {
			item := createValidCatalogItem("invalid-requirement-workload")
			item.Spec.Versions[0].DeviceFeatures = &api.DeviceFeatures{
				GpuPresent: lo.ToPtr(api.DeviceFeatureBoolean("yes")),
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest), status.Message)

			_, status = suite.Catalog.GetCatalogItem(suite.Ctx, suite.OrgID, catalogName, "invalid-requirement-workload")
			Expect(status.Code).To(BeEquivalentTo(http.StatusNotFound), status.Message)
		})

		It("When an out-of-enum OS mode is declared it should be rejected", func() {
			item := createValidCatalogItem("invalid-os-mode-workload")
			item.Spec.Versions[0].DeviceFeatures = &api.DeviceFeatures{
				OsMode: lo.ToPtr(apiv1beta1.OsModeType("container")),
			}

			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest), status.Message)
		})

		It("When a replacement declares an invalid requirement it should leave the stored requirement intact", func() {
			item := createValidCatalogItem("replaced-workload")
			item.Spec.Versions[0].DeviceFeatures = &api.DeviceFeatures{
				GpuPresent: lo.ToPtr(api.DeviceFeatureBooleanTrue),
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

			invalid := createValidCatalogItem("replaced-workload")
			invalid.Spec.Versions[0].DeviceFeatures = &api.DeviceFeatures{
				KvmEnabled: lo.ToPtr(api.DeviceFeatureBoolean("maybe")),
			}
			_, status = suite.Catalog.ReplaceCatalogItem(suite.Ctx, suite.OrgID, catalogName, "replaced-workload", invalid, true)
			Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest), status.Message)

			features := getItem("replaced-workload").Spec.Versions[0].DeviceFeatures
			Expect(features).ToNot(BeNil())
			Expect(lo.FromPtr(features.GpuPresent)).To(Equal(api.DeviceFeatureBooleanTrue))
			Expect(features.KvmEnabled).To(BeNil())
		})
	})

	Context("Reading requirements back", func() {
		It("When requirements are added to an existing item it should return the updated requirements", func() {
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, createValidCatalogItem("evolving-workload"))
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)
			Expect(getItem("evolving-workload").Spec.Versions[0].DeviceFeatures).To(BeNil())

			updated := createValidCatalogItem("evolving-workload")
			updated.Spec.Versions[0].DeviceFeatures = &api.DeviceFeatures{
				KvmEnabled: lo.ToPtr(api.DeviceFeatureBooleanTrue),
			}
			_, status = suite.Catalog.ReplaceCatalogItem(suite.Ctx, suite.OrgID, catalogName, "evolving-workload", updated, true)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)

			features := getItem("evolving-workload").Spec.Versions[0].DeviceFeatures
			Expect(features).ToNot(BeNil())
			Expect(lo.FromPtr(features.KvmEnabled)).To(Equal(api.DeviceFeatureBooleanTrue))
		})

		It("When items are listed it should include each item's declared requirements", func() {
			constrained := createValidCatalogItem("listed-constrained-workload")
			constrained.Spec.Versions[0].DeviceFeatures = &api.DeviceFeatures{
				GpuPresent: lo.ToPtr(api.DeviceFeatureBooleanTrue),
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, constrained)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

			_, status = suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, createValidCatalogItem("listed-unconstrained-workload"))
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

			list, status := suite.Catalog.ListCatalogItems(suite.Ctx, suite.OrgID, catalogName, api.ListCatalogItemsParams{})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)

			featuresByItem := map[string]*api.DeviceFeatures{}
			for _, item := range list.Items {
				Expect(item.Spec.Versions).ToNot(BeEmpty())
				featuresByItem[lo.FromPtr(item.Metadata.Name)] = item.Spec.Versions[0].DeviceFeatures
			}
			Expect(featuresByItem).To(HaveKey("listed-constrained-workload"))
			Expect(featuresByItem["listed-constrained-workload"]).ToNot(BeNil())
			Expect(lo.FromPtr(featuresByItem["listed-constrained-workload"].GpuPresent)).To(Equal(api.DeviceFeatureBooleanTrue))
			Expect(featuresByItem).To(HaveKeyWithValue("listed-unconstrained-workload", BeNil()))
		})
	})

	// Comparing requirements against device capabilities is advisory and
	// client-side. The API must never reject a deployment because the target
	// device does not meet a version's declared requirements.
	Context("Requirements never block a deployment", func() {
		// seedDevice enrolls an image-mode device that reports no GPU and
		// carries the derived feature label saying so.
		seedDevice := func(name string) {
			GinkgoHelper()
			labels := map[string]string{"feature.flightctl.io/gpu.present": "false"}
			_, status := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr(name), Labels: &labels},
				Spec:     &apiv1beta1.DeviceSpec{},
			})
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

			deviceStatus := apiv1beta1.NewDeviceStatus()
			deviceStatus.SystemInfo.OsMode = lo.ToPtr(apiv1beta1.OsModeImage)
			deviceStatus.SystemInfo.Gpus = &[]apiv1beta1.DeviceGpu{}
			_, status = suite.Device.ReplaceDeviceStatus(suite.Ctx, suite.OrgID, name, apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr(name)},
				Status:   &deviceStatus,
			}, false)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		}

		seedRequiringOSItem := func(itemName string) {
			GinkgoHelper()
			item := createValidOSCatalogItem(itemName)
			item.Spec.Versions[0].DeviceFeatures = &api.DeviceFeatures{
				GpuPresent: lo.ToPtr(api.DeviceFeatureBooleanTrue),
				KvmEnabled: lo.ToPtr(api.DeviceFeatureBooleanTrue),
			}
			_, status := suite.Catalog.CreateCatalogItem(suite.Ctx, suite.OrgID, catalogName, item)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)
		}

		It("When a device does not meet a version's requirements it should still accept the deployment", func() {
			seedRequiringOSItem("gpu-requiring-os")
			seedDevice("non-matching-device")

			deployed, status := suite.Device.ReplaceDevice(suite.Ctx, suite.OrgID, "non-matching-device", apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr("non-matching-device")},
				Spec: &apiv1beta1.DeviceSpec{Os: &apiv1beta1.DeviceOsSpec{
					CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{Catalog: catalogName, Item: "gpu-requiring-os", Version: "1.0.0"},
				}},
			}, nil, true, true)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
			Expect(deployed.Spec.Os).ToNot(BeNil())
			Expect(deployed.Spec.Os.CatalogItemRef).ToNot(BeNil())
			Expect(deployed.Spec.Os.CatalogItemRef.Item).To(Equal("gpu-requiring-os"))
		})

		It("When a device has not reported a required capability it should still accept the deployment", func() {
			seedRequiringOSItem("unknown-capability-os")

			_, status := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr("unreporting-device")},
				Spec:     &apiv1beta1.DeviceSpec{},
			})
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

			deployed, status := suite.Device.ReplaceDevice(suite.Ctx, suite.OrgID, "unreporting-device", apiv1beta1.Device{
				Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr("unreporting-device")},
				Spec: &apiv1beta1.DeviceSpec{Os: &apiv1beta1.DeviceOsSpec{
					CatalogItemRef: &apiv1beta1.CatalogItemRefSpec{Catalog: catalogName, Item: "unknown-capability-os", Version: "1.0.0"},
				}},
			}, nil, true, true)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
			Expect(deployed.Spec.Os).ToNot(BeNil())
			Expect(deployed.Spec.Os.CatalogItemRef).ToNot(BeNil())
			Expect(deployed.Spec.Os.CatalogItemRef.Item).To(Equal("unknown-capability-os"))
		})
	})
})
