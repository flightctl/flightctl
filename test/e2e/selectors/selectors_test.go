package selectors

import (
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	. "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const (
	deviceAYAMLPath = "device.yaml"
	deviceBYAMLPath = "device-b.yaml"
	fleetYAMLPath   = "fleet.yaml"
	unknownSelector = "unknown or unsupported selector"
	resourceCreated = `(200 OK|201 Created)`
	unknownFlag     = "unknown flag"
	failedToParse   = "failed to parse"
	DefaultTimeout  = "120s"
	RetryInterval   = "1s"
)

func TestFieldSelectors(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Field Selectors E2E Suite")
}

var _ = Describe("Field Selectors in Flight Control", Ordered, func() {
	var (
		harness       *e2e.Harness
		deviceInfo    v1alpha1.Device
		deviceBInfo   v1alpha1.Device
		deviceAName   string
		deviceBName   string
		deviceAlias   string
		DeviceARegion string
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
			out, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("NAME"))

			By("create a complete fleet")
			// Creating fleet
			_, _ = harness.ManageResource("delete", "fleet")
			out, err = harness.ManageResource("apply", fleetYAMLPath)
			// Verifying fleet is valid
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			By("Get device info from the yaml")
			deviceInfo = harness.GetDeviceByYaml(GetTestExamplesYamlPath(deviceAYAMLPath))
			deviceAName = *deviceInfo.Metadata.Name
			DeviceARegion = (*deviceInfo.Metadata.Labels)["region"]
			deviceBInfo = harness.GetDeviceByYaml(GetTestExamplesYamlPath(deviceBYAMLPath))
			deviceBName = *deviceBInfo.Metadata.Name
			Expect(deviceAName).ToNot(BeEmpty())
			Expect(deviceBName).ToNot(BeEmpty())

			// deleting previous devices
			_, _ = harness.CLI("delete", "device")
			// Creating new devices
			out, err = harness.ManageResource("apply", deviceAYAMLPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			out, err = harness.ManageResource("apply", deviceBYAMLPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			logrus.Infof("Device Created: %s", deviceAName)
			logrus.Infof("Device Created: %s", deviceBName)

			// to establish fleet before adding device to it
			Eventually(func() error {
				out, err = harness.CLI("get", "device")
				return err
			}, DefaultTimeout, RetryInterval).Should(BeNil(), "Timeout waiting for fleet to be ready")
			Expect(out).To(ContainSubstring("defaultDevice"))

			By("filters devices by name")
			out, err = harness.RunGetDevices("--field-selector", fmt.Sprintf("metadata.name=%s", deviceAName))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))

			By("filters devices by alias")
			deviceAlias = (*deviceInfo.Metadata.Labels)["alias"]
			out, err = harness.RunGetDevices("--field-selector", fmt.Sprintf("metadata.alias=%s", deviceAlias))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))

			By("filters devices by nameOrAlias")
			out, err = harness.RunGetDevices("--field-selector", fmt.Sprintf("metadata.nameOrAlias=%s", deviceAlias))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))

			By("filters devices by owner")
			_, err = harness.RunGetDevices("--field-selector", "metadata.owner=Fleet/default", "-owide")
			Expect(err).ToNot(HaveOccurred())

			By("filters devices by creation timestamp")
			startTimestamp, endTimestamp := GetCurrentYearBounds()
			out, err = harness.RunGetDevices("--field-selector",
				fmt.Sprintf("metadata.creationTimestamp>=%s,metadata.creationTimestamp<%s", startTimestamp, endTimestamp), "-owide")
			Expect(out).To(ContainSubstring(deviceAName))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))
		})
	})

	Context("Advanced Functionality Tests", func() {
		It("Advanced Functionality Tests", Label("77947"), func() {
			By("filters devices by multiple field selectors")
			startTimestamp, _ := GetCurrentYearBounds()
			out, err := harness.RunGetDevices(
				"-l", fmt.Sprintf("region=%s", DeviceARegion),
				"--field-selector", fmt.Sprintf("metadata.creationTimestamp>=%s", startTimestamp),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))

			By("excludes devices by name")
			out, err = harness.RunGetDevices("--field-selector", "metadata.name!=device1-name")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring("device1-name"))
		})
	})

	Context("Label Selectors Tests", func() {
		It("Label Selectors Tests", Label("78751"), func() {
			By("filters devices by region in a set")
			out, err := harness.RunGetDevices(
				"-l", fmt.Sprintf("region in (test, %s)", DeviceARegion),
				"-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))

			By("filters devices by region not in a set")
			out, err = harness.RunGetDevices("-l", "region notin (test, eu-west-2)", "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))

			By("filters devices by label existence")
			out, err = harness.RunGetDevices("-l", "region", "-owide")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))

			By("filters devices by label non-existence")
			out, err = harness.RunGetDevices("-l", "!region")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring(deviceAName))

			By("filters devices by exact label match")
			out, err = harness.RunGetDevices("-l", fmt.Sprintf("region=%s", DeviceARegion))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))

			By("filters devices by label mismatch")
			out, err = harness.RunGetDevices("-l", fmt.Sprintf("region!=%s", DeviceARegion))

			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring(deviceAName))

			By("filters devices by label and field selector")
			out, err = harness.RunGetDevices(
				"-l", fmt.Sprintf("region=%s", DeviceARegion),
				"--field-selector", "status.updated.status in (UpToDate, Unknown)",
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceAName))
		})
	})

	Context("Negative Tests", func() {
		It("Negative Tests", Label("77948"), func() {
			By("returns an error for an invalid field selector")
			out, err := harness.RunGetDevices("--field-selector", "invalid.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))

			By("returns an error for an unsupported field selector")
			out, err = harness.RunGetDevices("--field-selector", "unsupported.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))

			By("returns an error for an invalid field selector syntax")
			out, err = harness.RunGetDevices("--field-selector", "metadata.name@=device1-name")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

			By("returns an error for an incorrect field type")
			out, err = harness.RunGetDevices("--field-selector", "metadata.name>10")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unsupported for type string"))
			Expect(out).To(ContainSubstring("metadata.name"))

			By("returns an error for filtering devices by deprecated contains operator")
			out, err = harness.RunGetDevices("--field-selector", fmt.Sprintf("metadata.labels contains region=%s", DeviceARegion))
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("field is marked as private and cannot be selected"))

			By("returns an error for filtering devices by deprecated owner flag")
			out, err = harness.RunGetDevices("--owner")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownFlag))

			By("returns an error for filtering devices by deprecated status-filter flag")
			out, err = harness.RunGetDevices("--status-filter=updated.status=UpToDate")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownFlag))

			By("returns an error for bad syntax using =! operator")
			out, err = harness.RunGetDevices("-l", fmt.Sprintf("'region!=%s'", DeviceARegion))
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

			By("returns an error for bad syntax using ! operator outside the quotes")
			out, err = harness.RunGetDevices("-l", fmt.Sprintf("!'region=%s'", DeviceARegion))
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))

			By("returns an error for bad syntax using ! operator at the start of the quotes")
			out, err = harness.RunGetDevices("-l", fmt.Sprintf("'!region=%s'", DeviceARegion))
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(failedToParse))
		})
	})
})
