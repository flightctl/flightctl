package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/harness"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent instrumentation", func() {
	var (
		ctx context.Context
		h   *harness.TestHarness
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)

		var err error
		h, err = harness.NewTestHarnessWithOptions(ctx,
			GinkgoT().TempDir(),
			func(err error) {
				// this inline function handles any errors that are returned from go routines
				fmt.Fprintf(os.Stderr, "Error in test harness go routine: %v\n", err)
				GinkgoWriter.Printf("Error in go routine: %v\n", err)
				GinkgoRecover()
			},
			harness.WithAgentMetrics(),
			harness.WithAgentPprof())
		// check for test harness creation errors
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if h != nil {
			h.Cleanup()
		}
	})

	It("exposes /metrics and /debug/pprof endpoints on loopback", func() {
		client := &http.Client{Timeout: 2 * time.Second}

		get := func(url string) (int, string, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return 0, "", err
			}
			resp, err := client.Do(req)
			if err != nil {
				return 0, "", err
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				return resp.StatusCode, "", err
			}
			return resp.StatusCode, string(b), nil
		}

		// Metrics: expect 200 and some known tokens (promhttp* or our histograms)
		Eventually(func() bool {
			code, body, err := get("http://127.0.0.1:15690/metrics")
			if err != nil || code != http.StatusOK {
				return false
			}
			return strings.Contains(body, "promhttp_metric_handler_requests_total") ||
				strings.Contains(body, "update_device_status_duration_seconds")
		}, TIMEOUT, POLLING).Should(BeTrue(), "metrics endpoint should be available")

		// pprof: expect 200 and index contains known entries
		Eventually(func() bool {
			code, body, err := get("http://127.0.0.1:15689/debug/pprof/")
			if err != nil || code != http.StatusOK {
				return false
			}
			return strings.Contains(body, "heap") || strings.Contains(body, "profile")
		}, TIMEOUT, POLLING).Should(BeTrue(), "pprof endpoint should be available")
	})

})
