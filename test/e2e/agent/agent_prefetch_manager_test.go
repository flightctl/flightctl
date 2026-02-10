package agent_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"sigs.k8s.io/yaml"
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
		err := updateAgentConfigWith(harness, func(cfg *agentcfg.Config) {
			cfg.LogLevel = "trace"
			cfg.StatusUpdateInterval = agentcfg.MinSyncInterval
		})
		Expect(err).ToNot(HaveOccurred())

		appImageRef := AlpineImage
		_, _ = harness.VM.RunSSH([]string{"sudo", "podman", "rmi", "-f", appImageRef}, nil)

		fleetName := fmt.Sprintf("prefetch-status-%s", testID)
		selectorValue := fmt.Sprintf("prefetch-status-%s", testID)
		compose := composeSleepInfinity(appImageRef)

		err = createFleetWithInlineComposeAndVolumes(
			harness,
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

		err := updateAgentConfigWith(harness, func(cfg *agentcfg.Config) {
			cfg.LogLevel = "debug"
			cfg.PullTimeout = agentcfg.MinSyncInterval
		})
		Expect(err).ToNot(HaveOccurred())

		// Trigger a pull via the application image.
		fleetName := fmt.Sprintf("prefetch-timeout-%s", testID)
		selectorValue := fmt.Sprintf("prefetch-timeout-%s", testID)
		compose := composeSleepInfinity(bigImageRef)

		err = createFleetWithInlineComposeAndVolumes(
			harness,
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

		err = updateAgentConfigWith(harness, func(cfg *agentcfg.Config) {
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

		err := updateAgentConfigWith(harness, func(cfg *agentcfg.Config) {
			cfg.LogLevel = "debug"
		})
		Expect(err).ToNot(HaveOccurred())

		fleetName := fmt.Sprintf("prefetch-policy-%s", testID)
		selectorValue := fmt.Sprintf("prefetch-policy-%s", testID)

		compose := composeSleepInfinity(serviceImageRef)

		// IfNotPresent: artifact should be prefetched on first association.
		err = createFleetWithInlineComposeAndVolumes(
			harness,
			fleetName,
			selectorValue,
			prefetchInlineAppName,
			prefetchComposePath,
			compose,
			[]prefetchVolume{
				{name: prefetchArtifactVolName, reference: artifactRef, pullPolicy: v1beta1.PullIfNotPresent},
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

		// Update fleet to PullAlways: prefetch SHOULD pull again (refresh).
		logsBeforeAlways, err := harness.GetFlightctlAgentLogs()
		Expect(err).ToNot(HaveOccurred())
		beforeArtifactAlways := strings.Count(logsBeforeAlways, logPrefetchTargetPrefix+artifactRef)

		harness.UpdateFleetWithRetries(fleetName, func(f *v1beta1.Fleet) {
			Expect(f.Spec.Template.Spec.Applications).ToNot(BeNil())
			Expect(len(*f.Spec.Template.Spec.Applications)).To(BeNumerically(">=", 1))

			app := (*f.Spec.Template.Spec.Applications)[0]
			compose, err := app.AsComposeApplication()
			Expect(err).ToNot(HaveOccurred())
			Expect(compose.Volumes).ToNot(BeNil())
			Expect(len(*compose.Volumes)).To(BeNumerically(">=", 1))

			*compose.Volumes = []v1beta1.ApplicationVolume{
				{
					Name: prefetchArtifactVolName,
				},
			}

			Expect((*compose.Volumes)[0].FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
				Image: v1beta1.ImageVolumeSource{
					Reference:  artifactRef,
					PullPolicy: lo.ToPtr(v1beta1.PullAlways),
				},
			})).To(Succeed())

			Expect(app.FromComposeApplication(compose)).To(Succeed())
			(*f.Spec.Template.Spec.Applications)[0] = app
		})

		Eventually(func() bool {
			afterArtifact := strings.Count(agentLogsOrEmpty(harness), logPrefetchTargetPrefix+artifactRef)
			return afterArtifact > beforeArtifactAlways
		}, LONGTIMEOUT, LONGPOLLING).Should(BeTrue(), "expected prefetch pulls when pullPolicy=Always")
	})
})


type prefetchVolume struct {
	name       string
	reference  string
	pullPolicy v1beta1.ImagePullPolicy
}

func updateAgentConfigWith(h *e2e.Harness, mutate func(*agentcfg.Config)) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	stdout, err := h.VM.RunSSH([]string{"sudo", "cat", agentConfigPath}, nil)
	if err != nil {
		return fmt.Errorf("reading agent config: %w", err)
	}

	cfg := &agentcfg.Config{}
	if err := yaml.Unmarshal(stdout.Bytes(), cfg); err != nil {
		return fmt.Errorf("parsing agent config: %w", err)
	}

	if mutate != nil {
		mutate(cfg)
	}

	if err := h.SetAgentConfig(cfg); err != nil {
		return err
	}

	return h.RestartFlightCtlAgent()
}

func createFleetWithInlineComposeAndVolumes(
	h *e2e.Harness,
	fleetName string,
	selectorValue string,
	appName string,
	composePath string,
	composeContent string,
	volumes []prefetchVolume,
) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if fleetName == "" || selectorValue == "" || appName == "" || composePath == "" || composeContent == "" {
		return fmt.Errorf("invalid fleet inputs: fleetName=%q selectorValue=%q appName=%q composePath=%q composeContentEmpty=%t",
			fleetName, selectorValue, appName, composePath, composeContent == "")
	}

	selector := v1beta1.LabelSelector{
		MatchLabels: &map[string]string{"fleet": selectorValue},
	}

	var vols []v1beta1.ApplicationVolume
	for _, v := range volumes {
		vol := v1beta1.ApplicationVolume{Name: v.name}
		if err := vol.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
			Image: v1beta1.ImageVolumeSource{
				Reference:  v.reference,
				PullPolicy: lo.ToPtr(v.pullPolicy),
			},
		}); err != nil {
			return err
		}
		vols = append(vols, vol)
	}

	inline := v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Path:    composePath,
				Content: lo.ToPtr(composeContent),
			},
		},
	}

	compose := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    lo.ToPtr(appName),
	}
	if len(vols) > 0 {
		compose.Volumes = &vols
	}
	if err := compose.FromInlineApplicationProviderSpec(inline); err != nil {
		return err
	}

	app := v1beta1.ApplicationProviderSpec{}
	if err := app.FromComposeApplication(compose); err != nil {
		return err
	}

	deviceSpec := v1beta1.DeviceSpec{
		Applications: &[]v1beta1.ApplicationProviderSpec{app},
	}

	return h.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
}

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
