package service_test

import (
	"encoding/json"
	"reflect"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Device status PATCH immutability", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	// newUnionSpec builds a spec that exercises the generated oneOf types. Those keep
	// their payload in an unexported json.RawMessage field, so the exact bytes a spec
	// was decoded from survive in memory. Postgres returns jsonb with a space after
	// every separator while Go's encoder compacts and HTML-escapes, which makes a
	// byte-wise comparison of two semantically identical specs fail.
	newUnionSpec := func() *api.DeviceSpec {
		inlineConfig := api.ConfigProviderSpec{}
		Expect(inlineConfig.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
			Name: "inline-config",
			Inline: []api.FileSpec{
				// The angle brackets and ampersand are escaped by Go's encoder but
				// stored verbatim by Postgres.
				{Path: "/etc/motd", Content: "welcome <edge> & hello"},
				{Path: "/etc/hosts.extra", Content: "127.0.0.1 localhost"},
			},
		})).To(Succeed())

		gitConfig := api.ConfigProviderSpec{}
		Expect(gitConfig.FromGitConfigProviderSpec(api.GitConfigProviderSpec{
			Name: "git-config",
			GitRef: struct {
				Path           string `json:"path"`
				Repository     string `json:"repository"`
				TargetRevision string `json:"targetRevision"`
			}{
				Path:           "/config",
				Repository:     "test-repo",
				TargetRevision: "main",
			},
		})).To(Succeed())

		composeApp := api.ComposeApplication{
			Name:    lo.ToPtr("test-app"),
			AppType: api.AppTypeCompose,
			EnvVars: &map[string]string{"ENV1": "value1", "ENV2": "value2"},
		}
		Expect(composeApp.FromImageApplicationProviderSpec(api.ImageApplicationProviderSpec{
			Image: "quay.io/test/app:v1",
		})).To(Succeed())
		var appSpec api.ApplicationProviderSpec
		Expect(appSpec.FromComposeApplication(composeApp)).To(Succeed())

		return &api.DeviceSpec{
			Os:           &api.DeviceOsSpec{Image: "quay.io/test/os:v1.0.0"},
			Config:       &[]api.ConfigProviderSpec{inlineConfig, gitConfig},
			Applications: &[]api.ApplicationProviderSpec{appSpec},
		}
	}

	createDevice := func(name string, spec *api.DeviceSpec) {
		GinkgoHelper()
		deviceStatus := domain.NewDeviceStatus()
		deviceStatus.SystemInfo.Architecture = "amd64"
		_, status := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, api.Device{
			ApiVersion: api.DeviceAPIVersion,
			Kind:       api.DeviceKind,
			Metadata: api.ObjectMeta{
				Name:   lo.ToPtr(name),
				Labels: &map[string]string{"site": "east"},
			},
			Spec:   spec,
			Status: &deviceStatus,
		})
		Expect(status.Code).To(Equal(int32(201)), status.Message)
	}

	// systemInfoPatch is a status-only patch: it must always be accepted.
	systemInfoPatch := func(architecture string) api.PatchRequest {
		GinkgoHelper()
		info, err := util.StructToMap(api.DeviceSystemInfo{Architecture: architecture})
		Expect(err).ToNot(HaveOccurred())
		var value interface{} = info
		return api.PatchRequest{{Op: "replace", Path: "/status/systemInfo", Value: &value}}
	}

	Context("When the stored spec round-trips through Postgres jsonb", func() {
		It("it should accept a status patch even though the raw union bytes differ", func() {
			deviceName := "status-patch-jsonb-spec"
			createDevice(deviceName, newUnionSpec())

			stored, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, deviceName)
			Expect(err).ToNot(HaveOccurred())

			// Reproduce what ApplyJSONPatch does internally: marshal the whole device
			// and decode it again. Go compacts and HTML-escapes the union payloads, so
			// the resulting bytes no longer match the jsonb-formatted bytes Postgres
			// handed back, even though the documents are identical.
			encoded, err := json.Marshal(stored)
			Expect(err).ToNot(HaveOccurred())
			roundTripped := &domain.Device{}
			Expect(json.Unmarshal(encoded, roundTripped)).To(Succeed())

			Expect(reflect.DeepEqual(stored.Spec, roundTripped.Spec)).To(BeFalse(),
				"precondition: a byte-wise comparison must see a difference, otherwise this test proves nothing")
			Expect(domain.DeviceSpecsAreEqual(lo.FromPtr(stored.Spec), lo.FromPtr(roundTripped.Spec))).To(BeTrue(),
				"the semantic comparison must treat the two specs as equal")

			_, status := suite.Device.PatchDeviceStatus(suite.Ctx, suite.OrgID, deviceName, systemInfoPatch("arm64"))
			Expect(status.Code).To(Equal(int32(200)), status.Message)

			persisted, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(persisted.Status.SystemInfo.Architecture).To(Equal("arm64"))
			Expect(persisted.Spec.Os.Image).To(Equal("quay.io/test/os:v1.0.0"))
		})
	})

	Context("When a status patch mutates fields it does not own", func() {
		It("it should reject a patch that changes the spec", func() {
			deviceName := "status-patch-spec-mutation"
			createDevice(deviceName, newUnionSpec())

			var image interface{} = "quay.io/test/os:v2.0.0"
			patch := api.PatchRequest{{Op: "replace", Path: "/spec/os/image", Value: &image}}
			_, status := suite.Device.PatchDeviceStatus(suite.Ctx, suite.OrgID, deviceName, patch)
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("spec is immutable"))

			persisted, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(persisted.Spec.Os.Image).To(Equal("quay.io/test/os:v1.0.0"))
		})

		It("it should reject a patch that changes a union-typed spec field", func() {
			deviceName := "status-patch-union-spec-mutation"
			createDevice(deviceName, newUnionSpec())

			replacement := api.ConfigProviderSpec{}
			Expect(replacement.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
				Name:   "inline-config",
				Inline: []api.FileSpec{{Path: "/etc/motd", Content: "tampered"}},
			})).To(Succeed())
			encoded, err := json.Marshal([]api.ConfigProviderSpec{replacement})
			Expect(err).ToNot(HaveOccurred())
			var decoded interface{}
			Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())

			patch := api.PatchRequest{{Op: "replace", Path: "/spec/config", Value: &decoded}}
			_, status := suite.Device.PatchDeviceStatus(suite.Ctx, suite.OrgID, deviceName, patch)
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("spec is immutable"))
		})

		It("it should reject a patch that changes metadata labels", func() {
			deviceName := "status-patch-metadata-mutation"
			createDevice(deviceName, newUnionSpec())

			var labels interface{} = map[string]string{"site": "west"}
			patch := api.PatchRequest{{Op: "replace", Path: "/metadata/labels", Value: &labels}}
			_, status := suite.Device.PatchDeviceStatus(suite.Ctx, suite.OrgID, deviceName, patch)
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("metadata is immutable"))

			persisted, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(lo.FromPtr(persisted.Metadata.Labels)).To(HaveKeyWithValue("site", "east"))
		})

		It("it should reject a patch that changes metadata owner", func() {
			deviceName := "status-patch-owner-mutation"
			createDevice(deviceName, newUnionSpec())

			var owner interface{} = "Fleet/attacker"
			patch := api.PatchRequest{{Op: "replace", Path: "/metadata/owner", Value: &owner}}
			_, status := suite.Device.PatchDeviceStatus(suite.Ctx, suite.OrgID, deviceName, patch)
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("metadata is immutable"))
		})
	})
})
