package selectors

import (
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	. "github.com/flightctl/flightctl/test/common"
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
	DefaultTimeout  = "60s"
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
	var skipTests = false

	// Setup for the suite
	BeforeAll(func() {
		harness = e2e.NewTestHarness()
		login.LoginToAPIWithToken(harness)
		if harness == nil {
			Skip("Skipping tests: suite failed to create test harness")
		}
		logrus.Infof("Harness Created")
	})

	BeforeEach(func() {
		if skipTests {
			Skip("Skipping all tests because due to failure in resource creation")
		}
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
			_, _ = RunGetDevices(harness, false, "NAME")

			By("create a complete fleet")
			// Creating fleet
			_, _ = ManageResource(harness, "delete", "fleet")
			out, err := ManageResource(harness, "apply", fleetYAMLPath)
			// Verifying fleet is valid
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			if err != nil {
				skipTests = true
			}
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
			_, _ = harness.CLI("apply", "-R", "-f", GetTestExamplesYamlPath(deviceYAMLPath))
			out, err = harness.CLI("apply", "-R", "-f", GetTestExamplesYamlPath(deviceBYAMLPath))
			if err != nil {
				skipTests = true
			}

			logrus.Infof("Device Created: %s", deviceName)
			logrus.Infof("Device Created: %s", deviceBName)

			// to establish fleet before adding device to it
			time.Sleep(30 * time.Second)
			Eventually(func() error {
				_, err := harness.CLI("get", "fleet")
				if err != nil {
					skipTests = true
				}
				return err
			}, DefaultTimeout, RetryInterval).Should(BeNil(), "Timeout waiting for fleet to be ready")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			Expect(out).To(ContainSubstring(fmt.Sprintf("%s/%s", deviceBYAMLPath, deviceBName)))

			By("create a complete repo")
			out, err = ManageResource(harness, "apply", repoYAMLPath)
			if err != nil {
				skipTests = true
			}
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
		})

		It("filters devices", func() {

			By("filters devices by name")
			_, _ = RunGetDevices(harness, false, deviceName, "--field-selector", fmt.Sprintf("metadata.name=%s", deviceName))

			By("filters devices by alias")
			deviceAlias = (*deviceInfo.Metadata.Labels)["alias"]
			_, _ = RunGetDevices(harness, false, deviceName, "--field-selector", fmt.Sprintf("metadata.alias=%s", deviceAlias))

			By("filters devices by nameOrAlias")
			_, _ = RunGetDevices(harness, false, deviceName, "--field-selector", fmt.Sprintf("metadata.nameOrAlias=%s", deviceAlias))

			By("filters devices by owner")
			_, _ = RunGetDevices(harness, false, "", "--field-selector", "metadata.owner=Fleet/default", "-owide")

			By("filters devices by creation timestamp")
			startTimestamp, endTimestamp := GenerateTimestamps()
			_, _ = RunGetDevices(harness, false, deviceName, "--field-selector",
				fmt.Sprintf("metadata.creationTimestamp>=%s,metadata.creationTimestamp<%s", startTimestamp, endTimestamp), "-owide")
		})
	})

	Context("Advanced Functionality Tests", func() {
		It("Advanced Functionality Tests", Label("77947"), func() {
			By("filters devices by multiple field selectors")
			startTimestamp, _ := GenerateTimestamps()
			_, _ = RunGetDevices(harness, false, deviceName, "-l", "region=eu-west-1", "--field-selector",
				fmt.Sprintf("metadata.creationTimestamp>=%s", startTimestamp))

			By("excludes devices by name")
			_, _ = RunGetDevices(harness, false, deviceName, "--field-selector", "metadata.name!=device1-name")
		})
	})

	Context("Label Selectors Tests", func() {
		It("Label Selectors Tests", Label("78751"), func() {
			By("filters devices by region in a set")
			_, _ = RunGetDevices(harness, false, deviceName, "-l", "region in (test, eu-west-1)", "-owide")

			By("filters devices by region not in a set")
			_, _ = RunGetDevices(harness, false, deviceName, "-l", "region notin (test, eu-west-2)", "-owide")

			By("filters devices by label existence")
			_, _ = RunGetDevices(harness, false, deviceName, "-l", "region", "-owide")

			By("filters devices by label non-existence")
			_, _ = RunGetDevices(harness, false, deviceBName, "-l", "!region")

			By("filters devices by exact label match")
			_, _ = RunGetDevices(harness, false, deviceName, "-l", "region=eu-west-1")

			By("filters devices by label mismatch")
			_, _ = RunGetDevices(harness, false, deviceBName, "-l", "region!=eu-west-1")

			By("filters devices by label and field selector")
			_, _ = RunGetDevices(harness, false, deviceName, "-l", "region=eu-west-1", "--field-selector", "status.updated.status in (UpToDate, Unknown)")
		})
	})

	Context("Negative Tests", func() {
		It("Negative Tests", Label("77948"), func() {
			By("returns an error for an invalid field selector")
			_, _ = RunGetDevices(harness, true, unknownSelector, "--field-selector", "invalid.field")

			By("returns an error for an unsupported field selector")
			_, _ = RunGetDevices(harness, true, unknownSelector, "--field-selector", "unsupported.field")

			By("returns an error for an invalid field selector syntax")
			_, _ = RunGetDevices(harness, true, failedToParse, "--field-selector", "metadata.name@=device1-name")

			By("returns an error for an incorrect field type")
			_, _ = RunGetDevices(harness, true, "unsupported for type string", "--field-selector", "metadata.name>10")

			By("returns an error for filtering devices by deprecated contains operator")
			_, _ = RunGetDevices(harness, true, "field is marked as private and cannot be selected", "--field-selector", "metadata.labels contains region=eu-west-1")

			By("returns an error for filtering devices by deprecated owner flag")
			_, _ = RunGetDevices(harness, true, unknownFlag, "--owner")

			By("returns an error for filtering devices by deprecated status-filter flag")
			_, _ = RunGetDevices(harness, true, unknownFlag, "--status-filter=updated.status=UpToDate")

			By("returns an error for bad syntax using =! operator")
			_, _ = RunGetDevices(harness, true, failedToParse, "-l", "'region=!eu-west-1'")

			By("returns an error for bad syntax using ! operator outside the quotes")
			_, _ = RunGetDevices(harness, true, failedToParse, "-l", "!'region=eu-west-1'")

			By("returns an error for bad syntax using ! operator at the start of the quotes")
			_, _ = RunGetDevices(harness, true, failedToParse, "-l", "'!region=eu-west-1'")
		})
	})
})
