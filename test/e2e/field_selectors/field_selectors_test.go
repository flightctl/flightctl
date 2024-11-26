package field_selectors_test

import (
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"testing"
)

const TIMEOUT = "1m"
const POLLING = "250ms"

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
	})

	AfterEach(func() {
		harness.Cleanup(false)
	})

	Context("Basic Functionality Tests", func() {
		It("filters devices by name", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name=device1-name")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("device1-name"))
		})

		It("filters devices by owner", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.owner=Fleet/pos-fleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("Fleet/pos-fleet"))
		})

		It("filters devices by creation timestamp", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.creationTimestamp>=2024-01-01T00:00:00Z,metadata.creationTimestamp<2025-01-01T00:00:00Z")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("2024"))
		})
	})

	Context("Advanced Functionality Tests", func() {
		It("filters devices by label with contains operator", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.labels contains region=us")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("region=us"))
		})

		It("filters devices by multiple field selectors", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.owner=Fleet/pos-fleet,metadata.labels contains region=us,metadata.creationTimestamp>=2024-01-01T00:00:00Z,metadata.creationTimestamp<2025-01-01T00:00:00Z")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("Fleet/pos-fleet"))
			Expect(out).To(ContainSubstring("region=us"))
			Expect(out).To(ContainSubstring("2024"))
		})

		It("filters devices by nested field", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "status.updated.status=Unknown")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("Unknown"))
		})

		It("excludes devices by name", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name!=device1-name")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring("device1-name"))
		})
	})

	Context("Negative Tests", func() {
		It("returns an error for an invalid field selector", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "invalid.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})

		It("returns an error for an unsupported field selector", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "unsupported.field")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})

		It("returns an error for an invalid operator", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name@=device1-name")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})

		It("returns an error for an incorrect field type", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name>10")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})

		It("returns an error for an invalid timestamp format", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.creationTimestamp>=2024-01-01")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})

		It("returns an error for an empty field selector", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})

		It("returns an error for multiple errors in the field selector", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "invalid.field,metadata.name@=device1-name")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})

		It("returns an error for an invalid value for an enum field", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "status.updated.status=InvalidStatus")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})

		It("returns an error for too many field selectors", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name=device1-name,status.updated.status=Unknown,metadata.owner=Fleet/pos-fleet,metadata.labels contains region=us,metadata.creationTimestamp>=2024-01-01T00:00:00Z,metadata.creationTimestamp<2025-01-01T00:00:00Z,status.applicationsSummary.status=Running,status.lastSeen>=2024-01-01T00:00:00Z")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})

		It("returns an error for special characters in the field selector", func() {
			out, err := harness.CLI("get", "devices", "--field-selector", "metadata.name=device@name")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unknown or unsupported selector"))
		})
	})
})
