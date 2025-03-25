package selectors

import (
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	deviceYAMLPath  = "device.yaml"
	fleetYAMLPath   = "fleet.yaml"
	repoYAMLPath    = "repository-flightctl.yaml"
	unknownSelector = "unknown or unsupported selector"
	resourceCreated = `(200 OK|201 Created)`
	unknownFlag     = "unknown flag"
	failedToParse   = "failed to parse"
)

func TestFieldSelectors(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Field Selectors E2E Suite")
}

// Utility function to generate dynamic timestamps
func generateTimestamps() (string, string) {
	now := time.Now()
	startOfYear := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	endOfYear := time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)

	return startOfYear.Format(time.RFC3339), endOfYear.Format(time.RFC3339)
}

var _ = Describe("Field Selectors in Flight Control", Ordered, func() {
	var (
		harness     *e2e.Harness
		deviceInfo  v1alpha1.Device
		deviceName  string
		deviceAlias string
	)

	// Setup for the suite
	BeforeAll(func() {
		harness = e2e.NewTestHarness()
		login.LoginToAPIWithToken(harness)
	})

	// Cleanup after each test
	AfterEach(func() {
		harness.Cleanup(false)
	})

	// Cleanup after the suite
	AfterAll(func() {
		err := harness.CleanUpAllResources()
		Expect(err).ToNot(HaveOccurred())
	})

	Context("Basic Functionality Tests", func() {
		It("We can list devices and create resources", Label("77917"), func() {
			By("Listing devices")
			out, err := harness.CLI("get", "devices")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("NAME"))

			By("create a complete fleet")
			_, _ = harness.CLI("delete", "fleet")
			out, err = harness.CLI("apply", "-f", util.GetTestExamplesYamlPath(fleetYAMLPath))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			By("Get device info from the yaml")
			deviceInfo = harness.GetDeviceByYaml(util.GetTestExamplesYamlPath(deviceYAMLPath))
			deviceName = *deviceInfo.Metadata.Name
			Expect(deviceName).ToNot(BeEmpty())

			_, _ = harness.CLI("delete", "device")
			out, err = harness.CLI("apply", "-R", "-f", util.GetTestExamplesYamlPath(deviceYAMLPath))
			time.Sleep(30 * time.Second) // to establish fleet before adding device to it
			Eventually(func() error {
				_, err := harness.CLI("get", "fleet")
				return err
			}, "30s", "1s").Should(BeNil(), "Timeout waiting for fleet to be ready")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			Expect(out).To(ContainSubstring(fmt.Sprintf("%s/%s", deviceYAMLPath, deviceName)))

			By("create a complete repo")
			out, err = harness.CLI("apply", "-f", util.GetTestExamplesYamlPath(repoYAMLPath))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
		})

		It("filters devices", func() {

			By("filters devices by name")
			out, err := harness.CLI("get", "devices", "--field-selector", fmt.Sprintf("metadata.name=%s", deviceName))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by alias")
			deviceAlias = (*deviceInfo.Metadata.Labels)["alias"]
			out, err = harness.CLI("get", "devices", "--field-selector", fmt.Sprintf("metadata.alias=%s", deviceAlias))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by nameOrAlias")
			out, err = harness.CLI("get", "devices", "--field-selector", fmt.Sprintf("metadata.nameOrAlias=%s", deviceAlias))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by owner")
			_, err = harness.CLI("get", "devices", "--field-selector", "metadata.owner=Fleet/default", "-owide")
			Expect(err).ToNot(HaveOccurred())

			By("filters devices by creation timestamp")
			startTimestamp, endTimestamp := generateTimestamps()
			out, err = harness.CLI("get", "devices", "--field-selector",
				fmt.Sprintf("metadata.creationTimestamp>=%s,metadata.creationTimestamp<%s", startTimestamp, endTimestamp), "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))
		})
	})

	Context("Advanced Functionality Tests", func() {
		It("Advanced Functionality Tests", Label("77947"), func() {
			By("filters devices by multiple field selectors")
			startTimestamp, _ := generateTimestamps()
			out, err := harness.CLI("get", "devices", "-l", "region=eu-west-1", "--field-selector",
				fmt.Sprintf("metadata.creationTimestamp>=%s", startTimestamp))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("excludes devices by name")
			out, err = harness.CLI("get", "devices", "--field-selector", "metadata.name!=device1-name")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring("device1-name"))
		})
	})

	Context("Label Selectors Tests", func() {
		It("Label Selectors Tests", Label("78751"), func() {
			By("filters devices by region in a set")
			out, err := harness.CLI("get", "devices", "-l", "region in (test, eu-west-1)", "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by region not in a set")
			out, err = harness.CLI("get", "devices", "-l", "region notin (test, eu-west-2)", "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by label existence")
			out, err = harness.CLI("get", "devices", "-l", "region", "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by label non-existence")
			out, err = harness.CLI("get", "devices", "-l", "!region", "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring(deviceName))

			By("filters devices by exact label match")
			out, err = harness.CLI("get", "devices", "-l", "region=eu-west-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by label mismatch")
			out, err = harness.CLI("get", "devices", "-l", "region!=eu-west-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring(deviceName))

			By("filters devices by label and field selector")
			out, err = harness.CLI("get", "devices", "-l", "region=eu-west-1", "--field-selector", "status.updated.status in (UpToDate, Unknown)")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))
		})
	})

	Context("Negative Tests", func() {
		It("Negative Tests", Label("77948"), func() {
			By("returns an error for an invalid field selector")
			out, err := harness.CLI("get", "devices", "--field-selector", "invalid.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))

			By("returns an error for an unsupported field selector")
			out, err = harness.CLI("get", "devices", "--field-selector", "unsupported.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))

			By("returns an error for an invalid field selector syntax")
			out, err = harness.CLI("get", "devices", "--field-selector", "metadata.name@=device1-name")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

			By("returns an error for an incorrect field type")
			out, err = harness.CLI("get", "devices", "--field-selector", "metadata.name>10")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unsupported for type string"))
			Expect(out).To(ContainSubstring("metadata.name"))

			By("returns an error for filtering devices by deprecated contains operator")
			out, err = harness.CLI("get", "devices", "--field-selector", "metadata.labels contains region=eu-west-1")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("field is marked as private and cannot be selected"))

			By("returns an error for filtering devices by deprecated owner flag")
			out, err = harness.CLI("get", "devices", "--owner")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownFlag))

			By("returns an error for filtering devices by deprecated status-filter flag")
			out, err = harness.CLI("get", "devices", "--status-filter=updated.status=UpToDate")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownFlag))

			By("returns an error for bad syntax using =! operator")
			out, err = harness.CLI("get", "devices", "-l", "'region=!eu-west-1'")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

			By("returns an error for bad syntax using ! operator outside the quotes")
			out, err = harness.CLI("get", "devices", "-l", "!'region=eu-west-1'")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

			By("returns an error for bad syntax using ! operator at the start of the quotes")
			out, err = harness.CLI("get", "devices", "-l", "'!region=eu-west-1'")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

		})
	})
})
