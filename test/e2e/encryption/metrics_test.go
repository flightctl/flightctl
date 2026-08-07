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
			// Use sum(errors_total) value (not series count) so that a new error that
			// increments an existing label-set series is detected. PromQueryResultCount
			// returns the number of matching time series — it stays constant even when
			// an existing series is incremented, making it unable to detect such errors.
			errQuery := `sum(flightctl_encryption_errors_total)`

			// Capture the baseline before creating the AuthProvider so any error
			// produced by the creation itself is counted as a delta, not part of
			// the baseline. Prometheus counters are process-lifetime values; other
			// tests (e.g. N2) may have already incremented the counter.
			baselineErrorValue := harness.PromQueryCountValue(promURL, errQuery)()

			// Capture the baseline operations count before creating the AuthProvider so we can
			// wait for the S6 operation to be recorded in Prometheus before checking errors.
			// This prevents a race where the error check runs before the S6 operation reaches Prometheus.
			opsQuery := `sum(flightctl_encryption_operations_total)`
			baselineOpsValue := harness.PromQueryCountValue(promURL, opsQuery)()

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
			// Wait for the S6 create operation to be recorded (ops counter increased), then confirm
			// errors didn't grow. Using the counter value (not series count) ensures we detect the
			// S6 operation itself rather than passing on a pre-existing series from an earlier test.
			Eventually(harness.PromQueryCountValue(promURL, opsQuery),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeNumerically(">", baselineOpsValue),
				"must see the S6 encrypt operation recorded before checking error counter")

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
