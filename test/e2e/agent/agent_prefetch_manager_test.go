package agent_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	defaultArtifactRef = "quay.io/flightctl-tests/busybox-dummy-artifact"

	prefetchInlineAppName   = "my-inline"
	prefetchComposePath     = "docker-compose.yaml"
	prefetchArtifactVolName = "my-inline-data"

	logPrefetchTargetPrefix = "Prefetching OCI target: "
	logPrefetchCleanup      = "Prefetch cleanup:"
	logStatusPushed         = "Completed pushing device status"
)

var _ = Describe("Non-blocking OCI dependency prefetch manager", func() {
	var (
		harness  *e2e.Harness
		deviceID string
		testID   string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		testID = harness.GetTestIDFromContext()
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	It("Status is reported during an OCI image pre-fetch", Label("83871", "sanity", "agent"), func() {
		err := harness.UpdateAgentConfigWith(func(cfg *agentcfg.Config) {
			cfg.LogLevel = "trace"
			cfg.StatusUpdateInterval = agentcfg.MinSyncInterval
		})
		Expect(err).ToNot(HaveOccurred())

		appImageRef := AlpineImage
		_, _ = harness.VM.RunSSH([]string{"sudo", "podman", "rmi", "-f", appImageRef}, nil)

		fleetName := fmt.Sprintf("prefetch-status-%s", testID)
		selectorValue := fmt.Sprintf("prefetch-status-%s", testID)
		compose := composeSleepInfinity(appImageRef)

		err = harness.CreateFleetWithInlineComposeAndVolumes(
			fleetName,
			selectorValue,
			prefetchInlineAppName,
			prefetchComposePath,
			compose,
			nil,
		)
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = harness.DeleteFleet(fleetName) }()

		err = harness.SetLabelsForDevice(deviceID, map[string]string{"fleet": selectorValue})
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return hasStatusPushBetweenPrefetchAndCleanup(agentLogsOrEmpty(harness), appImageRef)
		}, LONGTIMEOUT, LONGPOLLING).Should(BeTrue(), "expected status publishing to continue while prefetch is running")
	})

	It("Agent can pull big layers images by increasing pull-timeout", Label("83857", "sanity", "agent"), func() {
		bigImageRef := NginxImage

		_, _ = harness.VM.RunSSH([]string{"sudo", "podman", "rmi", "-f", bigImageRef}, nil)

		err := harness.UpdateAgentConfigWith(func(cfg *agentcfg.Config) {
			cfg.LogLevel = "debug"
			cfg.PullTimeout = agentcfg.MinSyncInterval
		})
		Expect(err).ToNot(HaveOccurred())

		// Trigger a pull via the application image.
		fleetName := fmt.Sprintf("prefetch-timeout-%s", testID)
		selectorValue := fmt.Sprintf("prefetch-timeout-%s", testID)
		compose := composeSleepInfinity(bigImageRef)

		err = harness.CreateFleetWithInlineComposeAndVolumes(
			fleetName,
			selectorValue,
			prefetchInlineAppName,
			prefetchComposePath,
			compose,
			nil,
		)
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = harness.DeleteFleet(fleetName) }()

		err = harness.SetLabelsForDevice(deviceID, map[string]string{"fleet": selectorValue})
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			logs := agentLogsOrEmpty(harness)
			return strings.Contains(logs, "Retrying prefetch") ||
				strings.Contains(logs, "context deadline exceeded") ||
				strings.Contains(logs, "deadline exceeded")
		}, "2m", LONGPOLLING).Should(BeTrue(), "expected prefetch retries/timeouts with a small pull-timeout")

		err = harness.UpdateAgentConfigWith(func(cfg *agentcfg.Config) {
			cfg.PullTimeout = agentcfg.DefaultPullTimeout
		})
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			_, err := harness.VM.RunSSH([]string{"sudo", "podman", "image", "exists", bigImageRef}, nil)
			return err == nil
		}, LONGTIMEOUT, LONGPOLLING).Should(BeTrue(), "expected image to be present after increasing pull-timeout")
	})

	It("Prefetch manager pulls OCI artifacts (IfNotPresent vs Always)", Label("83847", "sanity", "agent"), func() {
		serviceImageRef := AlpineImage
		artifactRef := defaultArtifactRef

		err := harness.UpdateAgentConfigWith(func(cfg *agentcfg.Config) {
			cfg.LogLevel = "debug"
		})
		Expect(err).ToNot(HaveOccurred())

		fleetName := fmt.Sprintf("prefetch-policy-%s", testID)
		selectorValue := fmt.Sprintf("prefetch-policy-%s", testID)

		compose := composeSleepInfinity(serviceImageRef)

		// IfNotPresent: artifact should be prefetched on first association.
		err = harness.CreateFleetWithInlineComposeAndVolumes(
			fleetName,
			selectorValue,
			prefetchInlineAppName,
			prefetchComposePath,
			compose,
			[]e2e.InlineComposeVolume{
				{Name: prefetchArtifactVolName, Reference: artifactRef, PullPolicy: v1beta1.PullIfNotPresent},
			},
		)
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = harness.DeleteFleet(fleetName) }()

		err = harness.SetLabelsForDevice(deviceID, map[string]string{"fleet": selectorValue})
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return strings.Contains(agentLogsOrEmpty(harness), logPrefetchTargetPrefix+artifactRef)
		}, LONGTIMEOUT, LONGPOLLING).Should(BeTrue(), "expected artifact to be prefetched")

		// Ensure the content is present locally (manual pull is idempotent).
		emptyFleetName := fmt.Sprintf("prefetch-empty-%s", testID)
		emptySelector := fmt.Sprintf("prefetch-empty-%s", testID)
		emptyFleetSelector := v1beta1.LabelSelector{MatchLabels: &map[string]string{"fleet": emptySelector}}
		err = harness.CreateOrUpdateTestFleet(emptyFleetName, emptyFleetSelector, v1beta1.DeviceSpec{})
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = harness.DeleteFleet(emptyFleetName) }()

		err = harness.SetLabelsForDevice(deviceID, map[string]string{"fleet": emptySelector})
		Expect(err).ToNot(HaveOccurred())

		By("Ensuring artifact exists locally on the device")
		_, err = harness.VM.RunSSH([]string{"sudo", "podman", "artifact", "pull", artifactRef}, nil)
		Expect(err).ToNot(HaveOccurred())

		// Fleet with IfNotPresent: prefetch should NOT try to pull again.
		logsBeforeReassoc, err := harness.GetFlightctlAgentLogs()
		Expect(err).ToNot(HaveOccurred())
		beforeArtifactCount := strings.Count(logsBeforeReassoc, logPrefetchTargetPrefix+artifactRef)

		err = harness.SetLabelsForDevice(deviceID, map[string]string{"fleet": selectorValue})
		Expect(err).ToNot(HaveOccurred())

		Consistently(func() bool {
			afterArtifactCount := strings.Count(agentLogsOrEmpty(harness), logPrefetchTargetPrefix+artifactRef)
			return afterArtifactCount == beforeArtifactCount
		}, "45s", LONGPOLLING).Should(BeTrue(), "did not expect prefetch pulls when pullPolicy=IfNotPresent and content already exists")

		// PullAlways is not implemented for prefetch: the agent treats it as IfNotPresent. The PullPolicy API field is retained for backward compatibility.
	})
})

func composeSleepInfinity(imageRef string) string {
	// Keep YAML stable for collector parsing.
	return fmt.Sprintf(`version: "3.8"
services:
  service1:
    image: %s
    command: ["sleep", "infinity"]
`, imageRef)
}

func agentLogsOrEmpty(h *e2e.Harness) string {
	if h == nil {
		return ""
	}
	logs, err := h.GetFlightctlAgentLogs()
	if err != nil {
		return ""
	}
	return logs
}

func hasStatusPushBetweenPrefetchAndCleanup(logs, targetRef string) bool {
	if logs == "" || targetRef == "" {
		return false
	}
	prefIdx := strings.Index(logs, logPrefetchTargetPrefix+targetRef)
	if prefIdx < 0 {
		return false
	}
	cleanupIdx := strings.Index(logs[prefIdx:], logPrefetchCleanup)
	if cleanupIdx < 0 {
		return false
	}
	window := logs[prefIdx : prefIdx+cleanupIdx]
	return strings.Contains(window, logStatusPushed)
}
