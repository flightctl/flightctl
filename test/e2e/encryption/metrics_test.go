package encryption_test

import (
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Encryption at rest — Metrics", Label("encryption", "observability"), func() {
	var (
		harness *e2e.Harness
		promURL string
		cleanup func()
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		providers := setup.GetDefaultProviders()
		infra.SkipIfObservabilityNotConfigured(harness.GetTestContext(), providers)

		var err error
		promURL, cleanup, err = prometheusURL()
		Expect(err).ToNot(HaveOccurred(), "must be able to reach Prometheus")
		Expect(promURL).ToNot(BeEmpty(), "Prometheus URL must not be empty")
	})

	AfterEach(func() {
		if cleanup != nil {
			cleanup()
		}
	})

	// S4: Prometheus exposes encryption metrics (§7).
	Context("When the flightctl services run with encryption at rest enabled", func() {
		It("S4: should expose encryption active-key info metric with expected labels (§7)", func() {
			By("[§7] verifying flightctl_encryption_active_key_info gauge is present")
			query := `flightctl_encryption_active_key_info`
			Eventually(harness.PromQueryResultCount(promURL, query),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeNumerically(">", 0),
				"[§7] flightctl_encryption_active_key_info must have at least one sample")

			By("[§7] verifying the active-key gauge has the required label dimensions")
			requiredLabels := []string{"strategy", "key_id", "algorithm"}
			gaugeQuery := query + `{key_id="` + defaultKeyID + `"}`
			Eventually(harness.PromQueryHasLabels(promURL, gaugeQuery, nil, requiredLabels),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeTrue(),
				"[§7] flightctl_encryption_active_key_info must have strategy, key_id, algorithm labels")
		})

		It("S4: should expose encryption operation counter metric with expected labels (§7)", func() {
			By("[§7] verifying flightctl_encryption_operations_total counter is present")
			query := `flightctl_encryption_operations_total`
			Eventually(harness.PromQueryResultCount(promURL, query),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeNumerically(">", 0),
				"[§7] flightctl_encryption_operations_total must have at least one sample")

			By("[§7] verifying the operations counter has the required label dimensions")
			requiredLabels := []string{"operation", "strategy", "key_id", "status"}
			Eventually(harness.PromQueryHasLabels(promURL, query, nil, requiredLabels),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeTrue(),
				"[§7] flightctl_encryption_operations_total must have operation, strategy, key_id, status labels")
		})
	})
})
