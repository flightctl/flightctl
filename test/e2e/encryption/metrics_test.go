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

	Context("When the flightctl services run with encryption at rest enabled", func() {
		It("S4: should expose encryption active-key info metric with expected labels", Label("90096"), func() {
			By("verifying flightctl_encryption_active_key_info gauge is present")
			query := `flightctl_encryption_active_key_info`
			Eventually(harness.PromQueryResultCount(promURL, query),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeNumerically(">", 0),
				"flightctl_encryption_active_key_info must have at least one sample")

			By("verifying the active-key gauge has the required label dimensions")
			requiredLabels := []string{"strategy", "key_id", "algorithm"}
			gaugeQuery := query + `{key_id="` + defaultKeyID + `"}`
			Eventually(harness.PromQueryHasLabels(promURL, gaugeQuery, nil, requiredLabels),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeTrue(),
				"flightctl_encryption_active_key_info must have strategy, key_id, algorithm labels")
		})

		It("S5: should expose encryption operation counter metric with expected labels", Label("90098"), func() {
			By("creating an AuthProvider to trigger an encrypt operation")
			apName := "enc-metrics-ap-" + harness.GetTestIDFromContext()
			clientID := "enc-metrics-" + harness.GetTestIDFromContext()
			manifest := buildOIDCAuthProviderYAML(apName, auxSvcs.Keycloak.IssuerURL(), clientID, "metrics-test-secret")
			_, err := applyManifest(harness, manifest)
			Expect(err).ToNot(HaveOccurred(), "create AuthProvider to trigger encrypt metric")
			DeferCleanup(func() {
				if err := deleteAuthProvider(harness, apName); err != nil {
					GinkgoWriter.Printf("Warning: failed to delete AuthProvider %s: %v\n", apName, err)
				}
			})

			By("verifying flightctl_encryption_operations_total counter is present")
			query := `flightctl_encryption_operations_total`
			Eventually(harness.PromQueryResultCount(promURL, query),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeNumerically(">", 0),
				"flightctl_encryption_operations_total must have at least one sample")

			By("verifying the operations counter has the required label dimensions")
			requiredLabels := []string{"operation", "strategy", "key_id", "status"}
			Eventually(harness.PromQueryHasLabels(promURL, query, nil, requiredLabels),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeTrue(),
				"flightctl_encryption_operations_total must have operation, strategy, key_id, status labels")
		})

		It("S6: should report no encryption errors during normal operation", Label("90128"), func() {
			// Capture error baseline before the AuthProvider create so we measure only
			// the delta produced by S6. Use increase()[2m] rather than sum() so that a
			// service restart (which resets in-memory counters) cannot make the value
			// decrease and break the baseline comparison.
			errQuery := `sum(increase(flightctl_encryption_errors_total[2m]))`
			baselineErrorValue := harness.PromQueryCountValue(promURL, errQuery)()

			By("creating an AuthProvider to produce a successful encrypt operation")
			apName := "enc-metrics-noerr-ap-" + harness.GetTestIDFromContext()
			clientID := "enc-metrics-noerr-" + harness.GetTestIDFromContext()
			manifest := buildOIDCAuthProviderYAML(apName, auxSvcs.Keycloak.IssuerURL(), clientID, "no-error-secret")
			_, err := applyManifest(harness, manifest)
			Expect(err).ToNot(HaveOccurred(), "create AuthProvider to drive a clean encrypt operation")
			DeferCleanup(func() {
				if err := deleteAuthProvider(harness, apName); err != nil {
					GinkgoWriter.Printf("Warning: failed to delete AuthProvider %s: %v\n", apName, err)
				}
			})

			By("verifying flightctl_encryption_errors_total did not increase during normal operations")
			// Wait until Prometheus has scraped at least one successful encrypt operation in the
			// last 2 minutes. increase()[2m] is immune to counter resets from service restarts —
			// unlike sum() which can decrease when a restart resets the in-memory counter while
			// a stale pre-restart scrape remains in the TSDB. applyManifest success proves the
			// operation happened server-side; this wait ensures Prometheus caught up before
			// we assert on the error counter.
			opsQuery := `sum(increase(flightctl_encryption_operations_total{status="success"}[2m]))`
			Eventually(harness.PromQueryCountValue(promURL, opsQuery),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeNumerically(">", 0),
				"must see at least one successful encrypt operation in the last 2m before checking errors")

			Expect(harness.PromQueryCountValue(promURL, errQuery)()).To(Equal(baselineErrorValue),
				"flightctl_encryption_errors_total must not increase after a successful encrypt operation")
		})

		It("S7: should record canary validation successes on startup", Label("90129"), func() {
			By("verifying flightctl_encryption_canary_validations_total{status=success} counter is present")
			// The canary manager runs at service startup and records a validation for each
			// configured strategy/key combination. At least one success must be present.
			query := `flightctl_encryption_canary_validations_total{status="success"}`
			Eventually(harness.PromQueryResultCount(promURL, query),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeNumerically(">", 0),
				"flightctl_encryption_canary_validations_total{status=success} must be non-zero after service startup")

			By("verifying the canary counter has the required label dimensions")
			requiredLabels := []string{"strategy", "key_id", "status"}
			Eventually(harness.PromQueryHasLabels(promURL, query, nil, requiredLabels),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeTrue(),
				"flightctl_encryption_canary_validations_total must carry strategy, key_id, status labels")
		})
	})
})
