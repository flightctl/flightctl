package service_test

import (
	"encoding/json"

	catalogservice "github.com/flightctl/flightctl/internal/service/updatercatalog"
	catalogstore "github.com/flightctl/flightctl/internal/store/updatercatalog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Updater catalog extraction budgets", func() {
	var (
		suite    *ServiceTestSuite
		provider *catalogservice.Service
	)
	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		st, err := catalogstore.NewStore(suite.DB)
		Expect(err).NotTo(HaveOccurred())
		provider, err = catalogservice.NewService(st)
		Expect(err).NotTo(HaveOccurred())
	})
	AfterEach(func() { suite.Teardown() })

	It("When a release declares a valid extraction object it should be activated and delivered in the release descriptor", func() {
		config := approvedCatalog()
		config.Releases[0].Extraction = &catalogservice.ExtractionLimits{MaxExtractedManifestBytes: 65536, MaxExtractedFiles: 64, MaxExtractedFileBytes: 1 << 26, MaxExtractedTotalBytes: 1 << 27, ExtractionScratchBytes: 1<<27 + 65536, MaxExtractionSeconds: 300}
		config.Releases[0].BudgetKeySet = append(config.Releases[0].BudgetKeySet, "maxExtractedManifestBytes", "maxExtractedFiles", "maxExtractedFileBytes", "maxExtractedTotalBytes", "extractionScratchBytes", "maxExtractionSeconds")
		_, err := provider.Activate(suite.Ctx, config)
		Expect(err).NotTo(HaveOccurred())
		descriptor, err := provider.LookupRelease(suite.Ctx, "nvidia-orin", "0.1.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(descriptor.Approved).To(BeTrue())
		Expect(descriptor.BudgetKeySet).To(ContainElement("maxExtractionSeconds"))
		var document map[string]any
		Expect(json.Unmarshal(descriptor.DescriptorJSON, &document)).To(Succeed())
		Expect(document).To(HaveKey("extraction"))
		Expect(document["extraction"]).To(HaveKeyWithValue("maxExtractedFiles", BeNumerically("==", 64)))
	})

	It("When a release declares an invalid extraction object it should reject the catalog and keep the accepted revision", func() {
		accepted := approvedCatalog()
		_, err := provider.Activate(suite.Ctx, accepted)
		Expect(err).NotTo(HaveOccurred())
		invalid := approvedCatalog()
		invalid.Revision = 2
		invalid.Releases[0].Extraction = &catalogservice.ExtractionLimits{MaxExtractedManifestBytes: 65536, MaxExtractedFiles: 64, MaxExtractedFileBytes: 1 << 26, MaxExtractedTotalBytes: 1 << 27, ExtractionScratchBytes: 1 << 27, MaxExtractionSeconds: 300}
		invalid.Releases[0].BudgetKeySet = append(invalid.Releases[0].BudgetKeySet, "maxExtractedManifestBytes", "maxExtractedFiles", "maxExtractedFileBytes", "maxExtractedTotalBytes", "extractionScratchBytes", "maxExtractionSeconds")
		_, err = provider.Activate(suite.Ctx, invalid)
		Expect(err).To(HaveOccurred())
		descriptor, err := provider.LookupRelease(suite.Ctx, "nvidia-orin", "0.1.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(descriptor.Approved).To(BeTrue())
		Expect(string(descriptor.DescriptorJSON)).NotTo(ContainSubstring("extraction"))
	})
})
