package selectors

import (
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	testutil "github.com/flightctl/flightctl/test/util"
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

// FieldSelectorTestParams defines the parameters for field selector tests, including arguments, expected match status, and expected output.
type FieldSelectorTestParams struct {
	Args        []string
	ShouldMatch bool
	Expected    string
}

// EntryCase creates a test case for field selector tests with the given description, arguments, expected match status, and expected output.
func EntryCase(desc string, args []string, shouldMatch bool, expected string) testutil.TestCase[FieldSelectorTestParams] {
	return testutil.TestCase[FieldSelectorTestParams]{
		Description: desc,
		Params: FieldSelectorTestParams{
			Args:        args,
			ShouldMatch: shouldMatch,
			Expected:    expected,
		},
	}
}

var _ = Describe("Field Selectors in Flight Control", func() {
	var (
		deviceInfo    v1alpha1.Device
		deviceBInfo   v1alpha1.Device
		deviceAName   string
		deviceBName   string
		DeviceARegion string
		fleetName     string
		tempFiles     []string // Track temporary files for cleanup
	)

	BeforeEach(func() {
		_ = testutil.StartSpecTracerForGinkgo(suiteCtx)

		// Generate unique test ID for this test
		testID := harness.GetTestIDFromContext()

		// Create unique YAML files for this test
		uniqueDeviceAYAML, err := testutil.CreateUniqueYAMLFile(deviceAYAMLPath, testID)
		Expect(err).ToNot(HaveOccurred())
		tempFiles = append(tempFiles, uniqueDeviceAYAML)

		uniqueDeviceBYAML, err := testutil.CreateUniqueYAMLFile(deviceBYAMLPath, testID)
		Expect(err).ToNot(HaveOccurred())
		tempFiles = append(tempFiles, uniqueDeviceBYAML)

		uniqueFleetYAML, err := testutil.CreateUniqueYAMLFile(fleetYAMLPath, testID)
		Expect(err).ToNot(HaveOccurred())
		tempFiles = append(tempFiles, uniqueFleetYAML)

		By("Listing devices")
		out, err := harness.RunGetDevices()
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring("NAME"))

		By("create a complete fleet")
		//_, _ = harness.ManageResource("delete", "fleet")
		out, err = harness.ManageResource("apply", uniqueFleetYAML)
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(MatchRegexp(resourceCreated))

		// Get fleet name from the unique YAML
		fleetInfo := harness.GetFleetByYaml(uniqueFleetYAML)
		fleetName = *fleetInfo.Metadata.Name

		By("Get device info from the yaml")
		deviceInfo = harness.GetDeviceByYaml(uniqueDeviceAYAML)
		deviceAName = *deviceInfo.Metadata.Name
		DeviceARegion = (*deviceInfo.Metadata.Labels)["region"]
		deviceBInfo = harness.GetDeviceByYaml(uniqueDeviceBYAML)
		deviceBName = *deviceBInfo.Metadata.Name
		Expect(deviceAName).ToNot(BeEmpty())
		Expect(deviceBName).ToNot(BeEmpty())

		_, _ = harness.ManageResource("delete", "device")
		out, err = harness.ManageResource("apply", uniqueDeviceAYAML)
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(MatchRegexp(resourceCreated))
		out, err = harness.ManageResource("apply", uniqueDeviceBYAML)
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(MatchRegexp(resourceCreated))

		logrus.Infof("Device Created: %s", deviceAName)
		logrus.Infof("Device Created: %s", deviceBName)

		Eventually(func() error {
			out, err = harness.CLI("get", "device")
			return err
		}, DefaultTimeout, RetryInterval).Should(BeNil(), "Timeout waiting for fleet to be ready")
		Expect(out).To(ContainSubstring("defaultDevice"))
	})

	AfterEach(func() {
		// Clean up temporary YAML files
		testutil.CleanupTempYAMLFiles(tempFiles)
		tempFiles = nil
	})

	Context("Basic Functionality Tests", Serial, func() {
		It("We can list devices and create resources", Label("77917", "sanity"), func() {
			// Test that devices were created successfully
			Expect(deviceAName).ToNot(BeEmpty())
			Expect(deviceBName).ToNot(BeEmpty())
			Expect(DeviceARegion).ToNot(BeEmpty())
		})

		It("Field selector filters", Label("77947", "sanity"), func() {
			start, end := testutil.GetCurrentYearBounds()
			tests := testutil.Cases(
				EntryCase("filters devices by name", []string{"--field-selector", fmt.Sprintf("metadata.name=%s", deviceAName)}, true, deviceAName),
				EntryCase("filters devices by alias", []string{"--field-selector", fmt.Sprintf("metadata.alias=%s", (*deviceInfo.Metadata.Labels)["alias"])}, true, deviceAName),
				EntryCase("filters devices by nameOrAlias", []string{"--field-selector", fmt.Sprintf("metadata.nameOrAlias=%s", (*deviceInfo.Metadata.Labels)["alias"])}, true, deviceAName),
				EntryCase("filters devices by owner", []string{"--field-selector", fmt.Sprintf("metadata.owner=Fleet/%s", fleetName), "-owide"}, true, ""),
				EntryCase("filters devices by creation timestamp", []string{"--field-selector", fmt.Sprintf("metadata.creationTimestamp>=%s,metadata.creationTimestamp<%s", start, end), "-owide"}, true, deviceAName),
			)
			testutil.RunTable(tests, func(params FieldSelectorTestParams) {
				out, err := harness.CLI(append([]string{"get", "device"}, params.Args...)...)
				if params.ShouldMatch {
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(ContainSubstring(params.Expected))
				} else {
					Expect(out).ToNot(ContainSubstring(params.Expected))
				}
			})
		})

		It("Label selector filters", Label("78751", "sanity"), func() {
			tests := testutil.Cases(
				EntryCase("filters by region in set", []string{"-l", fmt.Sprintf("region in (test, %s)", DeviceARegion), "-owide"}, true, deviceAName),
				EntryCase("filters by region not in set", []string{"-l", "region notin (test, eu-west-2)", "-owide"}, true, deviceAName),
				EntryCase("filters by label existence", []string{"-l", "region", "-owide"}, true, deviceAName),
				EntryCase("filters by label non-existence", []string{"-l", "!region"}, false, deviceAName),
				EntryCase("filters by exact label match", []string{"-l", fmt.Sprintf("region=%s", DeviceARegion)}, true, deviceAName),
				EntryCase("filters by label mismatch", []string{"-l", fmt.Sprintf("region!=%s", DeviceARegion)}, false, deviceAName),
				EntryCase("filters by label and field selector", []string{"-l", fmt.Sprintf("region=%s", DeviceARegion), "--field-selector", "status.updated.status in (UpToDate, Unknown)"}, true, deviceAName),
			)
			testutil.RunTable(tests, func(params FieldSelectorTestParams) {
				out, err := harness.CLI(append([]string{"get", "device"}, params.Args...)...)
				if params.ShouldMatch {
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(ContainSubstring(params.Expected))
				} else {
					Expect(out).ToNot(ContainSubstring(params.Expected))
				}
			})
		})

		It("Negative field selector and label cases", Label("77948", "sanity"), func() {
			tests := testutil.Cases(
				EntryCase("invalid field selector", []string{"--field-selector", "invalid.field"}, false, unknownSelector),
				EntryCase("unsupported field selector", []string{"--field-selector", "unsupported.field"}, false, unknownSelector),
				EntryCase("invalid selector syntax", []string{"--field-selector", "metadata.name@=device1-name"}, false, failedToParse),
				EntryCase("incorrect field type", []string{"--field-selector", "metadata.name>10"}, false, "unsupported for type string"),
				EntryCase("deprecated contains operator", []string{"--field-selector", fmt.Sprintf("metadata.labels contains region=%s", DeviceARegion)}, false, "field is marked as private and cannot be selected"),
				EntryCase("deprecated owner flag", []string{"--owner"}, false, unknownFlag),
				EntryCase("deprecated status-filter flag", []string{"--status-filter=updated.status=UpToDate"}, false, unknownFlag),
				EntryCase("bad syntax =!", []string{"-l", fmt.Sprintf("'region!=%s'", DeviceARegion)}, false, failedToParse),
				EntryCase("bad syntax ! outside quotes", []string{"-l", fmt.Sprintf("!'region=%s'", DeviceARegion)}, false, failedToParse),
				EntryCase("bad syntax ! at start of quotes", []string{"-l", fmt.Sprintf("'!region=%s'", DeviceARegion)}, false, failedToParse),
			)
			testutil.RunTable(tests, func(params FieldSelectorTestParams) {
				out, err := harness.CLI(append([]string{"get", "device"}, params.Args...)...)
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(params.Expected))
			})
		})
	})
})
