package field_selectors

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

const (
	deviceYAMLPath  = "examples/device.yaml"
	fleetYAMLPath   = "examples/fleet.yaml"
	repoYAMLPath    = "examples/repository-flightctl.yaml"
	unknownSelector = "unknown or unsupported selector"
	resourceCreated = `(200 OK|201 Created)`
)

// Define the struct for parsing device.yaml
type Device struct {
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

func TestFieldSelectors(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Field Selectors E2E Suite")
}

var _ = Describe("Field Selectors in Flight Control", func() {
	var (
		harness *e2e.Harness
	)

	BeforeEach(func() {
		harness = e2e.NewTestHarness()
		login.LoginToAPIWithToken(harness)
	})

	AfterEach(func() {
		harness.Cleanup(false)
	})

	// Helper function to dynamically extract device name
	extractDeviceName := func() string {
		deviceName, err := extractNameFromYAML(filepath.Join(util.GetTopLevelDir(), deviceYAMLPath))
		Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to extract name from %s", deviceYAMLPath))
		return strings.TrimSpace(deviceName)
	}

	Context("login", func() {
		It("should have worked, and we can list devices", func() {
			out, err := harness.CLI("get", "devices")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("NAME"))
		})
	})

	Context("apply/fleet", func() {
		It("create a complete fleet", func() {
			_, _ = harness.CLI("delete", "fleet")
			out, err := harness.CLI("apply", "-f", filepath.Join(util.GetTopLevelDir(), fleetYAMLPath))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

		})
	})

	Context("apply/recursive", func() {
		It("create a device", func() {
			_, _ = harness.CLI("delete", "device")
			deviceName := extractDeviceName()
			out, err := harness.CLI("apply", "-R", "-f", filepath.Join(util.GetTopLevelDir(), deviceYAMLPath))
			time.Sleep(30 * time.Second) //to Establish fleet before adding device to it
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			Expect(out).To(ContainSubstring(fmt.Sprintf("%s/%s", deviceYAMLPath, deviceName)))
		})
	})

	Context("apply/repo", func() {
		It("create a complete repo", func() {
			out, err := harness.CLI("apply", "-f", filepath.Join(util.GetTopLevelDir(), repoYAMLPath))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
		})

	})

	Context("Basic Functionality Tests", Label("77917"), func() {

		It("filters devices by name", func() {
			deviceName := extractDeviceName()
			out, err := harness.CLI("get", "devices", "--field-selector", fmt.Sprintf("metadata.name=%s", deviceName))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))
		})

		It("filters devices by owner", func() {
			deviceName := extractDeviceName()
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.owner=Fleet/default")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))
		})

		It("filters devices by creation timestamp", func() {
			deviceName := extractDeviceName()
			startTimestamp, endTimestamp := generateTimestamps()
			out, err := harness.CLI("get", "devices", "--field-selector",
				fmt.Sprintf("metadata.creationTimestamp>=%s,metadata.creationTimestamp<%s", startTimestamp, endTimestamp))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))
		})

	})

	Context("Advanced Functionality Tests", Label("77947"), func() {
		It("filters devices by multiple field selectors", func() {
			deviceName := extractDeviceName()
			startTimestamp, _ := generateTimestamps()
			out, err := harness.CLI("get", "devices", "-l", "region=eu-west-1", "--field-selector",
				fmt.Sprintf("metadata.creationTimestamp>=%s", startTimestamp))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))
		})

		It("excludes devices by name", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name!=device1-name")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring("device1-name"))
		})
	})

	Context("Negative Tests", Label("77948"), func() {
		It("returns an error for an invalid field selector", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "invalid.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))
		})
		It("returns an error for an unsupported field selector", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "unsupported.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))
		})

		It("returns an error for an invalid operator", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name@=device1-name")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))
		})

		It("returns an error for an incorrect field type", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name>10")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unsupported for type string"))
		})

		It("returns an error for an invalid timestamp format", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "'metadata.creationTimestamp>=2024-01-01'")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("value must be a number or a valid time in RFC3339 format"))
		})

		It("returns an error for no field selector (an empty string field selector is valid)", func() {
			out, err := harness.CLI("get", "devices", "--field-selector")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("flag needs an argument"))
		})

		It("returns an error for multiple errors in the field selector", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "invalid.field,metadata.name@=device1-name")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(unknownSelector))
		})

		It("Deleting resources", func() {
			_, _ = harness.CLI("delete", "repo")
			_, _ = harness.CLI("delete", "fleet")
			_, _ = harness.CLI("delete", "device")
		})

	})
})

// Utility function to extract the metadata.name from a YAML file
func extractNameFromYAML(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var device Device
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&device); err != nil {
		return "", err
	}

	return device.Metadata.Name, nil
}

// Utility function to generate dynamic timestamps
func generateTimestamps() (string, string) {
	now := time.Now()
	startOfYear := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	endOfYear := time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)

	return startOfYear.Format(time.RFC3339), endOfYear.Format(time.RFC3339)
}
