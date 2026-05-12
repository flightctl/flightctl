package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/test/harness"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent label-from-systeminfo", func() {
	var (
		ctx context.Context
		h   *harness.TestHarness
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)

		var err error
		h, err = harness.NewTestHarness(ctx,
			GinkgoT().TempDir(),
			func(err error) {
				fmt.Fprintf(os.Stderr, "Error in test harness go routine: %v\n", err)
				GinkgoWriter.Printf("Error in go routine: %v\n", err)
				GinkgoRecover()
			},
			harness.WithoutAutoStartAgent())
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if h != nil {
			h.Cleanup()
		}
	})

	It("should populate labels from built-in systemInfo fields at enrollment", func() {
		h.AgentConfig().LabelFromSystemInfo = map[string]string{
			"arch": "architecture",
			"os":   "operatingSystem",
		}
		h.StartAgent()

		dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())

		// Verify labels were populated from systemInfo
		Expect(dev.Metadata.Labels).ToNot(BeNil())
		labels := *dev.Metadata.Labels
		Expect(labels).To(HaveKey("arch"))
		Expect(labels).To(HaveKey("os"))
		Expect(labels).To(HaveKey("alias")) // default alias=hostname
	})

	It("should populate labels from customInfo fields at enrollment", func() {
		// Create custom-info script directory
		customInfoDir := filepath.Join(h.TestDirPath, "usr", "lib", "flightctl", "custom-info.d")
		Expect(os.MkdirAll(customInfoDir, 0o755)).To(Succeed())

		// Create a custom-info script
		scriptPath := filepath.Join(customInfoDir, "testCustomField")
		scriptContent := "#!/bin/bash\necho 'test-value-123'"
		Expect(os.WriteFile(scriptPath, []byte(scriptContent), 0o600)).To(Succeed())
		Expect(os.Chmod(scriptPath, 0o700)).To(Succeed())

		h.AgentConfig().SystemInfoCustom = []string{"testCustomField"}
		h.AgentConfig().LabelFromSystemInfo = map[string]string{
			"custom-label": "customInfo.testCustomField",
		}
		h.StartAgent()

		dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())

		// Verify custom label was populated
		Expect(dev.Metadata.Labels).ToNot(BeNil())
		labels := *dev.Metadata.Labels
		Expect(labels).To(HaveKeyWithValue("custom-label", "test-value-123"))
		Expect(labels).To(HaveKey("alias")) // default alias=hostname
	})

	It("should add default alias=hostname when no alias is configured", func() {
		h.AgentConfig().LabelFromSystemInfo = map[string]string{
			"arch": "architecture",
		}
		h.StartAgent()

		dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())

		// Verify default alias was added
		Expect(dev.Metadata.Labels).ToNot(BeNil())
		labels := *dev.Metadata.Labels
		Expect(labels).To(HaveKey("alias"))
		Expect(labels["alias"]).ToNot(BeEmpty())
	})

	It("should not add default alias when custom alias is configured", func() {
		// Create custom-info script for productName
		customInfoDir := filepath.Join(h.TestDirPath, "usr", "lib", "flightctl", "custom-info.d")
		Expect(os.MkdirAll(customInfoDir, 0o755)).To(Succeed())

		scriptPath := filepath.Join(customInfoDir, "productName")
		scriptContent := "#!/bin/bash\necho 'custom-product-name'"
		Expect(os.WriteFile(scriptPath, []byte(scriptContent), 0o600)).To(Succeed())
		Expect(os.Chmod(scriptPath, 0o700)).To(Succeed())

		h.AgentConfig().SystemInfoCustom = []string{"productName"}
		h.AgentConfig().LabelFromSystemInfo = map[string]string{
			"alias": "customInfo.productName",
		}
		h.StartAgent()

		dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())

		// Verify alias is from productName, not hostname
		Expect(dev.Metadata.Labels).ToNot(BeNil())
		labels := *dev.Metadata.Labels
		Expect(labels).To(HaveKeyWithValue("alias", "custom-product-name"))
	})

	It("should give default-labels precedence over label-from-systeminfo", func() {
		h.AgentConfig().DefaultLabels = map[string]string{
			"env": "production",
		}
		h.AgentConfig().LabelFromSystemInfo = map[string]string{
			"env": "architecture", // This should be overridden by default-labels
		}
		h.StartAgent()

		dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())

		// Verify default-labels took precedence
		Expect(dev.Metadata.Labels).ToNot(BeNil())
		labels := *dev.Metadata.Labels
		Expect(labels).To(HaveKeyWithValue("env", "production"))
	})

	It("should handle missing systemInfo fields gracefully", func() {
		h.AgentConfig().LabelFromSystemInfo = map[string]string{
			"arch":           "architecture",
			"missing-field":  "nonExistentField",
			"missing-custom": "customInfo.doesNotExist",
		}
		h.StartAgent()

		dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())

		// Verify device enrolled successfully with available labels only
		Expect(dev.Metadata.Labels).ToNot(BeNil())
		labels := *dev.Metadata.Labels
		Expect(labels).To(HaveKey("arch"))              // This should exist
		Expect(labels).ToNot(HaveKey("missing-field"))  // This should not
		Expect(labels).ToNot(HaveKey("missing-custom")) // This should not
		Expect(labels).To(HaveKey("alias"))             // Default alias should still be added
	})

	It("should populate multiple labels from mixed sources", func() {
		// Create custom-info scripts
		customInfoDir := filepath.Join(h.TestDirPath, "usr", "lib", "flightctl", "custom-info.d")
		Expect(os.MkdirAll(customInfoDir, 0o755)).To(Succeed())

		siteScript := filepath.Join(customInfoDir, "siteId")
		Expect(os.WriteFile(siteScript, []byte("#!/bin/bash\necho 'site-123'"), 0o600)).To(Succeed())
		Expect(os.Chmod(siteScript, 0o700)).To(Succeed())

		rackScript := filepath.Join(customInfoDir, "rackNumber")
		Expect(os.WriteFile(rackScript, []byte("#!/bin/bash\necho 'rack-5'"), 0o600)).To(Succeed())
		Expect(os.Chmod(rackScript, 0o700)).To(Succeed())

		h.AgentConfig().SystemInfoCustom = []string{"siteId", "rackNumber"}
		h.AgentConfig().LabelFromSystemInfo = map[string]string{
			"arch": "architecture",
			"site": "customInfo.siteId",
			"rack": "customInfo.rackNumber",
		}
		h.AgentConfig().DefaultLabels = map[string]string{
			"env": "production",
		}
		h.StartAgent()

		dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())

		// Verify all labels were populated correctly
		Expect(dev.Metadata.Labels).ToNot(BeNil())
		labels := *dev.Metadata.Labels
		Expect(labels).To(HaveKey("arch"))
		Expect(labels).To(HaveKeyWithValue("site", "site-123"))
		Expect(labels).To(HaveKeyWithValue("rack", "rack-5"))
		Expect(labels).To(HaveKeyWithValue("env", "production"))
		Expect(labels).To(HaveKey("alias")) // default alias
	})
})
