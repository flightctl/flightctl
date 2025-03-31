package selectors

import (
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	. "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const (
	deviceYAMLPath  = "device.yaml"
	deviceBYAMLPath = "device-b.yaml"
	fleetYAMLPath   = "fleet.yaml"
	repoYAMLPath    = "repository-flightctl.yaml"
	unknownSelector = "unknown or unsupported selector"
	resourceCreated = `(200 OK|201 Created)`
	unknownFlag     = "unknown flag"
	failedToParse   = "failed to parse"
	DefaultTimeout  = "90s"
	RetryInterval   = "1s"
)

func TestFieldSelectors(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Field Selectors E2E Suite")
}

var _ = Describe("Field Selectors in Flight Control", Ordered, func() {
	var (
		harness     *e2e.Harness
		deviceInfo  v1alpha1.Device
		deviceBInfo v1alpha1.Device
		deviceName  string
		deviceBName string
		deviceAlias string
	)

	// Setup for the suite
	BeforeAll(func() {
		harness = e2e.NewTestHarness()
		login.LoginToAPIWithToken(harness)
		logrus.Infof("Harness Created")
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
			out, err := RunGetDevices(harness)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("NAME"))

			By("create a complete fleet")
			// Creating fleet
			_, _ = ManageResource(harness, "delete", "fleet")
			out, err = ManageResource(harness, "apply", fleetYAMLPath)
			// Verifying fleet is valid
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			By("Get device info from the yaml")
			deviceInfo = harness.GetDeviceByYaml(GetTestExamplesYamlPath(deviceYAMLPath))
			deviceName = *deviceInfo.Metadata.Name
			deviceBInfo = harness.GetDeviceByYaml(GetTestExamplesYamlPath(deviceBYAMLPath))
			deviceBName = *deviceBInfo.Metadata.Name
			Expect(deviceName).ToNot(BeEmpty())
			Expect(deviceBName).ToNot(BeEmpty())

			// deleting previous devices
			_, _ = harness.CLI("delete", "device")
			// Creating new devices
			out, err = ManageResource(harness, "apply", deviceYAMLPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			out, err = ManageResource(harness, "apply", deviceBYAMLPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			logrus.Infof("Device Created: %s", deviceName)
			logrus.Infof("Device Created: %s", deviceBName)

			// to establish fleet before adding device to it
			time.Sleep(30 * time.Second)
			Eventually(func() error {
				out, err = harness.CLI("get", "device")
				return err
			}, DefaultTimeout, RetryInterval).Should(BeNil(), "Timeout waiting for fleet to be ready")
			Expect(out).To(ContainSubstring("defaultDevice"))

			By("create a complete repo")
			out, err = ManageResource(harness, "apply", repoYAMLPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
		})

		It("filters devices", func() {

			By("filters devices by name")
			out, err := RunGetDevices(harness, "--field-selector", fmt.Sprintf("metadata.name=%s", deviceName))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by alias")
			deviceAlias = (*deviceInfo.Metadata.Labels)["alias"]
			out, err = RunGetDevices(harness, "--field-selector", fmt.Sprintf("metadata.alias=%s", deviceAlias))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by nameOrAlias")
			out, err = RunGetDevices(harness, "--field-selector", fmt.Sprintf("metadata.nameOrAlias=%s", deviceAlias))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by owner")
			_, err = RunGetDevices(harness, "--field-selector", "metadata.owner=Fleet/default", "-owide")
			Expect(err).ToNot(HaveOccurred())

			By("filters devices by creation timestamp")
			startTimestamp, endTimestamp := GetCurrentYearBounds()
			out, err = RunGetDevices(harness, "--field-selector",
				fmt.Sprintf("metadata.creationTimestamp>=%s,metadata.creationTimestamp<%s", startTimestamp, endTimestamp), "-owide")
			Expect(out).To(ContainSubstring(deviceName))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))
		})
	})

	Context("Advanced Functionality Tests", func() {
		It("Advanced Functionality Tests", Label("77947"), func() {
			By("filters devices by multiple field selectors")
			startTimestamp, _ := GetCurrentYearBounds()
			out, err := RunGetDevices(harness, "-l", "region=eu-west-1", "--field-selector",
				fmt.Sprintf("metadata.creationTimestamp>=%s", startTimestamp))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("excludes devices by name")
			out, err = RunGetDevices(harness, "--field-selector", "metadata.name!=device1-name")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring("device1-name"))
		})
	})

	Context("Label Selectors Tests", func() {
		It("Label Selectors Tests", Label("78751"), func() {
			By("filters devices by region in a set")
			out, err := RunGetDevices(harness, "-l", "region in (test, eu-west-1)", "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by region not in a set")
			out, err = RunGetDevices(harness, "-l", "region notin (test, eu-west-2)", "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by label existence")
			out, err = RunGetDevices(harness, "-l", "region", "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by label non-existence")
			out, err = RunGetDevices(harness, "-l", "!region")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring(deviceName))

			By("filters devices by exact label match")
			out, err = RunGetDevices(harness, "-l", "region=eu-west-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("filters devices by label mismatch")
			out, err = RunGetDevices(harness, "-l", "region!=eu-west-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring(deviceName))

			By("filters devices by label and field selector")
			out, err = RunGetDevices(harness, "-l", "region=eu-west-1", "--field-selector", "status.updated.status in (UpToDate, Unknown)")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))
		})
	})

	Context("Negative Tests", func() {
		It("Negative Tests", Label("77948"), func() {
			By("returns an error for an invalid field selector")
			out, err := RunGetDevices(harness, "--field-selector", "invalid.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))

			By("returns an error for an unsupported field selector")
			out, err = RunGetDevices(harness, "--field-selector", "unsupported.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))

			By("returns an error for an invalid field selector syntax")
			out, err = RunGetDevices(harness, "--field-selector", "metadata.name@=device1-name")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

			By("returns an error for an incorrect field type")
			out, err = RunGetDevices(harness, "--field-selector", "metadata.name>10")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unsupported for type string"))
			Expect(out).To(ContainSubstring("metadata.name"))

			By("returns an error for filtering devices by deprecated contains operator")
			out, err = RunGetDevices(harness, "--field-selector", "metadata.labels contains region=eu-west-1")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("field is marked as private and cannot be selected"))

			By("returns an error for filtering devices by deprecated owner flag")
			out, err = RunGetDevices(harness, "--owner")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownFlag))

			By("returns an error for filtering devices by deprecated status-filter flag")
			out, err = RunGetDevices(harness, "--status-filter=updated.status=UpToDate")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownFlag))

			By("returns an error for bad syntax using =! operator")
			out, err = RunGetDevices(harness, "-l", "'region=!eu-west-1'")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

			By("returns an error for bad syntax using ! operator outside the quotes")
			out, err = RunGetDevices(harness, "-l", "!'region=eu-west-1'")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

			By("returns an error for bad syntax using ! operator at the start of the quotes")
			out, err = RunGetDevices(harness, "-l", "'!region=eu-west-1'")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))
		})
	})
})

// RunGetDevices executes "get devices" CLI command with optional arguments.
func RunGetDevices(harness *e2e.Harness, args ...string) (string, error) {
	allArgs := append([]string{"get", "devices"}, args...)
	return harness.CLI(allArgs...)
}

// ManageResource performs an operation ("apply" or "delete") on a specified resource.
func ManageResource(harness *e2e.Harness, operation, resource string, args ...string) (string, error) {
	switch operation {
	case "apply":
		return harness.CLI("apply", "-f", GetTestExamplesYamlPath(resource))
	case "delete":
		return harness.CLI("delete", resource)
	default:
		return "", fmt.Errorf("unsupported operation: %s", operation)
	}
}
