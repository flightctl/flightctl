package selectors

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	deviceYAMLPath  = "examples/device.yaml"
	fleetYAMLPath   = "examples/fleet.yaml"
	repoYAMLPath    = "examples/repository-flightctl.yaml"
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
		harness *e2e.Harness
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

	// Helper function to dynamically extract device name
	extractDeviceName := func() string {
		device := harness.GetDeviceByYaml(util.GetExamplesYamlPath("device.yaml"))
		Expect(*device.Metadata.Name).ToNot(BeEmpty(), "device name should not be empty")
		return strings.TrimSpace(*device.Metadata.Name)
	}

	Context("Basic Functionality Tests", Label("77917"), func() {
		It("We can list devices and create resources", func() {
			By("Listing devices", func() {
				out, err := harness.CLI("get", "devices")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring("NAME"))
			})
			By("create a complete fleet", func() {
				_, _ = harness.CLI("delete", "fleet")
				out, err := harness.CLI("apply", "-f", filepath.Join(util.GetTopLevelDir(), fleetYAMLPath))
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(MatchRegexp(resourceCreated))
			})
			By("create a device", func() {
				_, _ = harness.CLI("delete", "device")
				deviceName := extractDeviceName()
				out, err := harness.CLI("apply", "-R", "-f", filepath.Join(util.GetTopLevelDir(), deviceYAMLPath))
				time.Sleep(30 * time.Second) // to establish fleet before adding device to it
				Eventually(func() error {
					_, err := harness.CLI("get", "fleet")
					return err
				}, "30s", "1s").Should(BeNil(), "Timeout waiting for fleet to be ready")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(MatchRegexp(resourceCreated))
				Expect(out).To(ContainSubstring(fmt.Sprintf("%s/%s", deviceYAMLPath, deviceName)))
			})
			By("create a complete repo", func() {
				out, err := harness.CLI("apply", "-f", filepath.Join(util.GetTopLevelDir(), repoYAMLPath))
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(MatchRegexp(resourceCreated))
			})
		})

		It("filters devices", func() {
			By("filters devices by name", func() {
				deviceName := extractDeviceName()
				out, err := harness.CLI("get", "devices", "--field-selector", fmt.Sprintf("metadata.name=%s", deviceName))
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(deviceName))
			})

			By("filters devices by owner", func() {
				deviceName := extractDeviceName()
				out, err := harness.CLI("get", "devices", "--field-selector", "metadata.owner=Fleet/default", "-owide")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(deviceName))
			})

			By("filters devices by creation timestamp", func() {
				deviceName := extractDeviceName()
				startTimestamp, endTimestamp := generateTimestamps()
				out, err := harness.CLI("get", "devices", "--field-selector",
					fmt.Sprintf("metadata.creationTimestamp>=%s,metadata.creationTimestamp<%s", startTimestamp, endTimestamp), "-owide")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(deviceName))
			})
		})
	})

	Context("Advanced Functionality Tests", Label("77947"), func() {
		It("Advanced Functionality Tests", func() {
			By("filters devices by multiple field selectors", func() {
				deviceName := extractDeviceName()
				startTimestamp, _ := generateTimestamps()
				out, err := harness.CLI("get", "devices", "-l", "region=eu-west-1", "--field-selector",
					fmt.Sprintf("metadata.creationTimestamp>=%s", startTimestamp))
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(deviceName))
			})

			By("excludes devices by name", func() {
				out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name!=device1-name")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).ToNot(ContainSubstring("device1-name"))
			})
		})
	})

	Context("Label Selectors Tests", Label("78751"), func() {
		It("Label Selectors Tests", func() {
			By("filters devices by region in a set", func() {
				deviceName := extractDeviceName()
				out, err := harness.CLI("get", "devices", "-l", "region in (test, eu-west-1)", "-owide")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(deviceName))
			})

			By("filters devices by region not in a set", func() {
				deviceName := extractDeviceName()
				out, err := harness.CLI("get", "devices", "-l", "region notin (test, eu-west-2)", "-owide")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(deviceName))
			})

			By("filters devices by label existence", func() {
				deviceName := extractDeviceName()
				out, err := harness.CLI("get", "devices", "-l", "region", "-owide")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(deviceName))
			})

			By("filters devices by label non-existence", func() {
				deviceName := extractDeviceName()
				out, err := harness.CLI("get", "devices", "-l", "!region", "-owide")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).ToNot(ContainSubstring(deviceName))
			})

			By("filters devices by exact label match", func() {
				deviceName := extractDeviceName()
				out, err := harness.CLI("get", "devices", "-l", "region=eu-west-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(deviceName))
			})

			By("filters devices by label mismatch", func() {
				deviceName := extractDeviceName()
				out, err := harness.CLI("get", "devices", "-l", "region!=eu-west-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).ToNot(ContainSubstring(deviceName))
			})

			By("filters devices by label and field selector", func() {
				deviceName := extractDeviceName()
				out, err := harness.CLI("get", "devices", "-l", "region=eu-west-1", "--field-selector", "status.updated.status in (UpToDate, Unknown)")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(deviceName))
			})
		})
	})

	Context("Negative Tests", Label("77948"), func() {
		It("Negative Tests", func() {
			By("returns an error for an invalid field selector", func() {
				out, err := harness.CLI("get", "devices", "--field-selector", "invalid.field")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(unknownSelector))
			})
			By("returns an error for an unsupported field selector", func() {
				out, err := harness.CLI("get", "devices", "--field-selector", "unsupported.field")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(unknownSelector))
			})
			By("returns an error for an invalid operator", func() {
				out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name@=device1-name")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(unknownSelector))
			})
			By("returns an error for an incorrect field type", func() {
				out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name>10")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring("unsupported for type string"))
				Expect(out).To(ContainSubstring("metadata.name"))
			})
			By("returns an error for filtering devices by deprecated contains operator", func() {
				out, err := harness.CLI("get", "devices", "--field-selector", "metadata.labels contains region=eu-west-1")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring("field is marked as private and cannot be selected"))
			})
			By("returns an error for filtering devices by deprecated owner flag", func() {
				out, err := harness.CLI("get", "devices", "--owner")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(unknownFlag))
			})
			By("returns an error for filtering devices by deprecated status-filter flag", func() {
				out, err := harness.CLI("get", "devices", "--status-filter=updated.status=UpToDate")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(unknownFlag))
			})
			By("returns an error for bad syntax using =! operator", func() {
				out, err := harness.CLI("get", "devices", "-l", "'region=!eu-west-1'")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(failedToParse))
			})
			By("returns an error for bad syntax using ! operator outside the quotes", func() {
				out, err := harness.CLI("get", "devices", "-l", "!'region=eu-west-1'")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(failedToParse))
			})
			By("returns an error for bad syntax using ! operator at the start of the quotes", func() {
				out, err := harness.CLI("get", "devices", "-l", "'!region=eu-west-1'")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(failedToParse))
			})
		})
	})
})
