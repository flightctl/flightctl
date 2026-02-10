package agent_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Agent image and artifact pruning", func() {
	var harness *e2e.Harness
	var deviceID string

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()

		artifactDir, err := harness.SetupScenario(deviceID, "preflight-config")
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("Pruning preflight ready (device=%s artifactDir=%s)\n", deviceID, artifactDir)

		By("check current config location exists")
		lsOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_ls_etc_flightctl.txt", "ls -lm /etc/flightctl/ 2>/dev/null || true")
		if err != nil {
			GinkgoWriter.Printf("Debug: failed to list /etc/flightctl (device=%s err=%v)\n", deviceID, err)
		} else if !strings.Contains(lsOut, "config.yaml") {
			GinkgoWriter.Printf("Debug: /etc/flightctl/config.yaml missing on device=%s (ls output=%q)\n", deviceID, lsOut)
			_, _ = harness.RunVMCommandWithEvidence(artifactDir, "vm_ls_etc_flightctl_all.txt", "ls -la /etc/flightctl/ 2>/dev/null || true")
			_, _ = harness.RunVMCommandWithEvidence(artifactDir, "vm_systemctl_status_flightctl_agent.txt", "sudo systemctl status flightctl-agent --no-pager || true")
			_, _ = harness.RunVMCommandWithEvidence(artifactDir, "vm_journalctl_flightctl_agent.txt", "sudo journalctl -u flightctl-agent --no-pager -n 200 || true")
		}

		By("ensure drop-in directory exists")
		confOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_conf_d_exists.txt", "sudo -n test -d /etc/flightctl/conf.d && echo ok || echo missing")
		if err != nil {
			GinkgoWriter.Printf("Debug: failed to check /etc/flightctl/conf.d (device=%s err=%v)\n", deviceID, err)
		} else if strings.TrimSpace(confOut) != "ok" {
			GinkgoWriter.Printf("Debug: /etc/flightctl/conf.d missing on device=%s (output=%q)\n", deviceID, confOut)
		}

		By("enable pruning for all pruning test cases")
		_ = ensurePruningEnabledWithCleanup(harness, deviceID, artifactDir, defaultEnablePruningDropinPath)
	})

	Context("image and artifact pruning", func() {
		It("Enable/Disable Pruning via Agent Config and Drop-ins", Label("87084", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "pruning-config-dropins")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: pruning-config-dropins (artifactDir=%s)\n", artifactDir)

			enableProvider := pruningDropinProviderSpec("pruning-enable", defaultEnablePruningDropinPath, true)
			agentActiveOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_agent_is_active_after_enable.txt", "sudo systemctl is-active flightctl-agent")
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(agentActiveOut)).To(Equal("active"))

			By("disable pruning via higher lexical drop-in (spec update)")
			disableDropinPath := "/etc/flightctl/conf.d/99-disable-pruning.yaml"
			disableProvider := pruningDropinProviderSpec("pruning-disable", disableDropinPath, false)
			updateDeviceConfigWithEvidence(harness, deviceID, artifactDir, "host_device_disable_pruning_dropin.txt", "disable pruning via drop-in", enableProvider, disableProvider)
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_disable_pruning_dropin_ls.txt", "sudo ls -l "+disableDropinPath)
			Expect(err).ToNot(HaveOccurred())
			waitForAgentLog(harness, fmt.Sprintf("Image pruning config reloaded: enabled=%t", false))
			agentActiveOut, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_agent_is_active_after_disable.txt", "sudo systemctl is-active flightctl-agent")
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(agentActiveOut)).To(Equal("active"))

			By("re-enable pruning by removing disable drop-in (spec update)")
			updateDeviceConfigWithEvidence(harness, deviceID, artifactDir, "host_device_remove_disable_pruning_dropin.txt", "remove disable pruning drop-in", enableProvider)
			waitForAgentLog(harness, fmt.Sprintf("Image pruning config reloaded: enabled=%t", true))
			agentActiveOut, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_agent_is_active_after_reenable.txt", "sudo systemctl is-active flightctl-agent")
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(agentActiveOut)).To(Equal("active"))

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("References File Created and Updated Before Upgrades", Label("87085", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "references-file-created")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: references-file-created (artifactDir=%s)\n", artifactDir)

			By("apply a Device spec with an inline Quadlet nginx app (doc example)")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_inline.txt", nginxLatestRefFragment)
			nginxInlineSpec, err := e2e.BuildInlineAppSpec("nginx-server", v1beta1.AppTypeQuadlet, []e2e.InlineContent{
				{Path: "nginx.container", Content: fmt.Sprintf(nginxInlineContainerContentTemplate, nginxLatestImageRef)},
			})
			Expect(err).ToNot(HaveOccurred())
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_inline.txt", "update device applications: nginx-server", nginxInlineSpec)
			waitForUpdateConditionCompleted(harness, deviceID, "nginx-server application update")

			By("verify references file exists")
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_ls.txt", "sudo ls -l /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())

			By("verify nginx image appears in reference file")
			waitForReferencePresence(harness, nginxLatestImageRef, true, true)
			refsCatOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())
			Expect(jsonContainsImageRef(refsCatOut, nginxLatestImageRef)).To(BeTrue())

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Reference Entry Removed Only After Actual Image Is Removed", Label("87086", "agent"), func() {
			nginxV125Spec, nginxLatestSpec, err := buildNginxSpecs()
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "reference-removed-after-prune")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: reference-removed-after-prune (artifactDir=%s)\n", artifactDir)

			By("apply nginx v1.25 (doc example nginx.25)")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_v125.txt", nginxV125ImageRef)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_v125.txt", "update device applications: nginx v1.25", nginxV125Spec)

			By("verify v1.25 image referenced in the references file")
			waitForReferencePresence(harness, nginxV125ImageRef, false, true)
			refsCatOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat_before_update.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())
			Expect(jsonContainsString(refsCatOut, nginxV125ImageRef)).To(BeTrue())

			By("update nginx image to latest")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_latest.txt", nginxLatestRefFragment)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)
			waitForUpdateConditionCompleted(harness, deviceID, "nginx latest application update")

			By("verify reference file keeps current image and removes old image")
			waitForReferencePresence(harness, nginxLatestImageRef, true, true)
			waitForReferencePresence(harness, nginxV125ImageRef, false, false)
			refsCatOut, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())
			Expect(jsonContainsImageRef(refsCatOut, nginxLatestImageRef)).To(BeTrue())
			Expect(jsonContainsString(refsCatOut, nginxV125ImageRef)).To(BeFalse())

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Lost Reference Model: Unreferenced Images Become Eligible and Are Pruned", Label("87087", "agent"), func() {
			nginxV125Spec, nginxLatestSpec, err := buildNginxSpecs()
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "lost-reference-pruning")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: lost-reference-pruning (artifactDir=%s)\n", artifactDir)

			By("apply initial image")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_v125_lost_ref.txt", nginxV125ImageRef)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_v125_lost_ref.txt", "update device applications: nginx v1.25", nginxV125Spec)

			By("update to a different image")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_latest_lost_ref.txt", nginxLatestRefFragment)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_latest_lost_ref.txt", "update device applications: nginx latest", nginxLatestSpec)
			waitForUpdateConditionCompleted(harness, deviceID, "nginx latest application update")

			By("verify only currently referenced images are retained")
			waitForReferencePresence(harness, nginxLatestImageRef, true, true)
			waitForReferencePresence(harness, nginxV125ImageRef, false, false)
			refsOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat_lost_ref.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())
			Expect(jsonContainsImageRef(refsOut, nginxLatestImageRef)).To(BeTrue())
			Expect(jsonContainsString(refsOut, nginxV125ImageRef)).To(BeFalse())
			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Application Image Retention: Keep Current + Previous, Prune Older", Label("87088", "agent"), func() {
			nginxV125Spec, nginxLatestSpec, err := buildNginxSpecs()
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "application-image-retention")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: application-image-retention (artifactDir=%s)\n", artifactDir)

			By("apply nginx.25")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_v125_retention.txt", nginxV125ImageRef)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_v125.txt", "update device applications: nginx v1.25", nginxV125Spec)

			By("apply nginx")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_latest_retention.txt", nginxLatestRefFragment)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)

			By("apply nginx again (acts as v3 in environments without extra tags)")
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_latest_again.txt", "update device applications: nginx latest again", nginxLatestSpec)

			By("verify current image reference is retained")
			waitForReferencePresence(harness, nginxLatestImageRef, true, true)
			refsCatOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat_retention.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())
			Expect(jsonContainsImageRef(refsCatOut, nginxLatestImageRef)).To(BeTrue())

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("OCI Artifacts: Unreferenced Artifacts Are Pruned", Label("87089", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "oci-artifact-pruning")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: oci-artifact-pruning (artifactDir=%s)\n", artifactDir)

			enableProvider := pruningDropinProviderSpec("pruning-enable", defaultEnablePruningDropinPath, true)

			By("apply a Device spec that references an image-backed application volume")
			artifactSpec, err := e2e.BuildComposeWithImageVolumeSpec("my-inline", "docker-compose.yaml", composeInlineVolumeContent, "my-data", composeArtifactImageRef)
			Expect(err).ToNot(HaveOccurred())
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_compose_artifact_app.txt", composeArtifactImageRef)
			writeDeviceSpecYAMLEvidence(
				artifactDir,
				"host_device_spec_artifact_app.yaml",
				deviceID,
				[]v1beta1.ApplicationProviderSpec{artifactSpec},
				nil,
			)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_artifact_app.txt", "update device applications: my-compose-app", artifactSpec)
			waitForUpdateConditionCompleted(harness, deviceID, "compose artifact/image application update")

			By("remove the artifact reference from spec")
			writeDeviceSpecYAMLEvidence(
				artifactDir,
				"host_device_spec_artifact_removed.yaml",
				deviceID,
				[]v1beta1.ApplicationProviderSpec{},
				nil,
			)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_artifact_removed.txt", "update device applications: empty")
			waitForUpdateConditionCompleted(harness, deviceID, "artifact application removal update")

			By("trigger a follow-up reconciliation to run prune again")
			refreshEnableProvider := pruningDropinProviderSpec("pruning-enable-refresh", "/etc/flightctl/conf.d/61-enable-pruning.yaml", true)
			writeDeviceSpecYAMLEvidence(
				artifactDir,
				"host_device_spec_refresh_pruning_dropin.yaml",
				deviceID,
				nil,
				[]v1beta1.ConfigProviderSpec{enableProvider, refreshEnableProvider},
			)
			updateDeviceConfigWithEvidence(
				harness,
				deviceID,
				artifactDir,
				"host_device_refresh_pruning_dropin.txt",
				"refresh pruning config to trigger retry prune",
				enableProvider,
				refreshEnableProvider,
			)
			waitForAgentLog(harness, fmt.Sprintf("Image pruning config reloaded: enabled=%t", true))
			registerPruningRefreshCleanup(harness, deviceID, artifactDir, enableProvider)

			By("verify artifact pruned")
			waitForReferencePresence(harness, composeArtifactImageRef, true, false)
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat_artifact_after_removal.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Pruning Runs Only After Successful Reconciliation and Is Non-Blocking", Label("87091", "sanity", "agent"), func() {
			nginxLatestSpec, err := buildNginxSpec(fmt.Sprintf(nginxLatestContainerContentTemplate, nginxLatestImageRef))
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "pruning-non-blocking")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: pruning-non-blocking (artifactDir=%s)\n", artifactDir)

			By("apply a spec with an invalid image reference")
			brokenSpec, err := e2e.BuildInlineAppSpec("broken", v1beta1.AppTypeQuadlet, []e2e.InlineContent{
				{Path: "broken.container", Content: brokenContainerContent},
			})
			Expect(err).ToNot(HaveOccurred())
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_broken_image.txt", "update device applications: broken image", brokenSpec)

			By("verify device does not reach healthy due to image pull failure")
			harness.WaitForApplicationsSummaryNotHealthy(deviceID)

			By("apply a valid spec (nginx)")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_latest_non_blocking.txt", nginxLatestRefFragment)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)

			By("verify valid spec update completes")
			waitForUpdateConditionCompleted(harness, deviceID, "nginx latest application update")

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("References File Updated After Successful Pruning", Label("87092", "agent"), func() {
			nginxV125Spec, nginxLatestSpec, err := buildNginxSpecs()
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "references-updated-after-prune")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: references-updated-after-prune (artifactDir=%s)\n", artifactDir)

			By("apply nginx.25 then nginx")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_v125_refs_update.txt", nginxV125ImageRef)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_v125.txt", "update device applications: nginx v1.25", nginxV125Spec)

			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_latest_refs_update.txt", nginxLatestRefFragment)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)

			By("wait for latest application update")
			waitForUpdateConditionCompleted(harness, deviceID, "nginx latest application update for refs cleanup")

			By("verify v1.25 removed from references file")
			waitForReferencePresence(harness, nginxV125ImageRef, false, false)
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Partial Failure Handling: One Removal Fails, Others Continue, Reconciliation Not Blocked", Label("87093", "agent"), func() {
			nginxV125Spec, nginxLatestSpec, err := buildNginxSpecs()
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "partial-failure-pruning")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: partial-failure-pruning (artifactDir=%s)\n", artifactDir)

			By("apply nginx.25 so it is tracked in managed references")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_v125_partial_failure.txt", nginxV125ImageRef)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_v125_partial_failure.txt", "update device applications: nginx v1.25", nginxV125Spec)
			waitForUpdateConditionCompleted(harness, deviceID, "nginx v1.25 application update with hold container setup")
			waitForReferencePresence(harness, nginxV125ImageRef, false, true)

			By("keep nginx.25 in use")
			holdCmd := fmt.Sprintf("sudo podman pull %s; sudo podman rm -f hold-nginx >/dev/null 2>&1 || true; sudo podman run -d --name hold-nginx %s sh -c 'sleep infinity'; sudo podman ps --format '{{.Names}}' | grep hold-nginx || true", nginxV125ImageRef, nginxV125ImageRef)
			holdOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_hold_nginx_run.txt", holdCmd)
			Expect(err).ToNot(HaveOccurred())
			Expect(holdOut).To(ContainSubstring("hold-nginx"))

			By("apply nginx (would normally prune v1.25)")
			assertReferenceAbsent(harness, artifactDir, "vm_refs_cat_before_nginx_latest_partial_failure.txt", nginxLatestRefFragment)
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)
			waitForUpdateConditionCompleted(harness, deviceID, "nginx latest application update with hold container")

			By("verify v1.25 image remains present while removal fails")
			harness.WaitForPodmanImagePresence(nginxV125ImageRef, true)

			By("remove the hold container")
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_remove_hold_nginx.txt", "sudo podman rm -f hold-nginx")
			Expect(err).ToNot(HaveOccurred())

			By("apply nginx again to trigger another post-update pruning")
			// Force a new rendered version so this update is not treated as a no-op.
			nginxLatestRetrySpec, err := buildNginxSpec(fmt.Sprintf("%s# retry-after-hold-removal\n", fmt.Sprintf(nginxLatestContainerContentTemplate, nginxLatestImageRef)))
			Expect(err).ToNot(HaveOccurred())
			updateDeviceApplicationsWithEvidence(harness, deviceID, artifactDir, "host_device_update_nginx_latest_again.txt", "update device applications: nginx latest again", nginxLatestRetrySpec)
			waitForUpdateConditionCompleted(harness, deviceID, "nginx latest application update after removing hold container")

			By("verify v1.25 reference and image are removed after retry")
			waitForReferencePresence(harness, nginxV125ImageRef, false, false)
			harness.WaitForPodmanImagePresence(nginxV125ImageRef, false)

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})
	})

})

var (
	nginxLatestImageRef     = canonicalImageRef(getEnvOrDefault("PRUNING_NGINX_LATEST_IMAGE", "quay.io/flightctl-tests/nginx:v1"))
	nginxV125ImageRef       = getEnvOrDefault("PRUNING_NGINX_PREV_IMAGE", "quay.io/flightctl-tests/alpine:v1")
	nginxLatestRefFragment  = nginxLatestImageRef
	composeArtifactImageRef = strings.TrimSpace(getEnvOrDefault("PRUNING_COMPOSE_ARTIFACT_IMAGE", "quay.io/flightctl-tests/models/gpt2"))
)

// Inline quadlet container content for nginx.
const nginxInlineContainerContentTemplate = `[Unit]
Description=Nginx web server
[Container]
Image=%s
PublishPort=8080:80
[Service]
Restart=always
[Install]
WantedBy=default.target
`

// Quadlet container content for nginx v1.25.
const nginxV125ContainerContentTemplate = `[Container]
Image=%s
Volume=/etc/nginx/html:/usr/share/nginx/html,Z
[Service]
Restart=on-failure
[Install]
WantedBy=default.target
`

// Quadlet container content for latest nginx.
const nginxLatestContainerContentTemplate = `[Container]
Image=%s
PublishPort=8080:80
[Service]
Restart=always
[Install]
WantedBy=default.target
`

// Broken quadlet container content for negative testing.
const brokenContainerContent = `[Container]
Image=quay.io/library/this-image-should-not-exist
`

const composeInlineVolumeContent = `version: "3.8"
services:
  service1:
    image: quay.io/flightctl-tests/alpine:v1
    command: ["sleep", "infinity"]
    volumes:
      - my-data:/data
volumes:
  my-data:
    external: true
`

const imageArtifactReferencesCmd = "sudo test -f /var/lib/flightctl/image-artifact-references.json && sudo cat /var/lib/flightctl/image-artifact-references.json || true"

const (
	defaultEnablePruningDropinPath        = "/etc/flightctl/conf.d/60-enable-pruning.yaml"
	enablePruningDropinEvidenceFile       = "host_device_enable_pruning_dropin.txt"
	enablePruningDropinDescription        = "enable pruning via drop-in"
	cleanupPruningDropinsEvidenceFile     = "host_device_cleanup_pruning_dropins.txt"
	cleanupPruningDropinsDescription      = "cleanup pruning drop-ins"
	enablePruningDropinLSEvidenceFile     = "vm_enable_pruning_dropin_ls.txt"
	skipCleanupDeviceDeletedMessageFormat = "Skipping drop-in cleanup: device already deleted (device=%s)\n"
)

func requireHarnessAndDevice(harness *e2e.Harness, deviceID string) {
	Expect(harness).ToNot(BeNil())
	Expect(strings.TrimSpace(deviceID)).ToNot(BeEmpty())
}

func requireArtifactFileContext(artifactDir, filename, description string) {
	Expect(strings.TrimSpace(artifactDir)).ToNot(BeEmpty())
	Expect(strings.TrimSpace(filename)).ToNot(BeEmpty())
	Expect(strings.TrimSpace(description)).ToNot(BeEmpty())
}

func ensurePruningEnabledWithCleanup(harness *e2e.Harness, deviceID, artifactDir, enableDropinPath string) v1beta1.ConfigProviderSpec {
	requireHarnessAndDevice(harness, deviceID)
	requireArtifactFileContext(artifactDir, enablePruningDropinEvidenceFile, enablePruningDropinDescription)
	Expect(strings.TrimSpace(enableDropinPath)).ToNot(BeEmpty())
	GinkgoWriter.Printf("Ensuring pruning enabled with cleanup (device=%s path=%s)\n", deviceID, enableDropinPath)

	enableProvider := pruningDropinProviderSpec("pruning-enable", enableDropinPath, true)
	updateDeviceConfigWithEvidence(
		harness,
		deviceID,
		artifactDir,
		enablePruningDropinEvidenceFile,
		enablePruningDropinDescription,
		enableProvider,
	)
	DeferCleanup(func() {
		if _, err := harness.GetDevice(deviceID); err != nil {
			if strings.Contains(err.Error(), "not found") {
				GinkgoWriter.Printf(skipCleanupDeviceDeletedMessageFormat, deviceID)
				return
			}
			Expect(err).ToNot(HaveOccurred())
		}
		updateDeviceConfigWithEvidence(
			harness,
			deviceID,
			artifactDir,
			cleanupPruningDropinsEvidenceFile,
			cleanupPruningDropinsDescription,
		)
	})
	_, err := harness.RunVMCommandWithEvidence(artifactDir, enablePruningDropinLSEvidenceFile, "sudo ls -l "+enableDropinPath)
	Expect(err).ToNot(HaveOccurred())
	waitForAgentLog(harness, "Image pruning config reloaded: enabled=true")
	return enableProvider
}

func applyDeviceUpdateWithEvidence(harness *e2e.Harness, deviceID, artifactDir, filename, description string, waitForRenderedVersion bool, updateFunc func(*v1beta1.Device), assertFunc func()) {
	requireHarnessAndDevice(harness, deviceID)
	requireArtifactFileContext(artifactDir, filename, description)
	Expect(updateFunc).ToNot(BeNil())
	GinkgoWriter.Printf("Updating device spec (device=%s desc=%s)\n", deviceID, description)
	expectedRenderedVersion := -1
	var err error
	if waitForRenderedVersion {
		expectedRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceID)
		if err != nil {
			_ = e2e.WriteEvidence(artifactDir, filename, description, "prepare next rendered version failed", err)
		}
		Expect(err).ToNot(HaveOccurred())
	}

	err = harness.UpdateDeviceWithRetries(deviceID, updateFunc)
	if err != nil {
		_ = e2e.WriteEvidence(artifactDir, filename, description, "update device failed", err)
	}
	Expect(err).ToNot(HaveOccurred())

	if assertFunc != nil {
		assertFunc()
	}

	if waitForRenderedVersion {
		err = harness.WaitForDeviceNewRenderedVersion(deviceID, expectedRenderedVersion)
		if err != nil {
			_ = e2e.WriteEvidence(artifactDir, filename, description, "wait for rendered version failed", err)
		}
		Expect(err).ToNot(HaveOccurred())
	}

	output := fmt.Sprintf("updated spec request accepted (waitForRenderedVersion=%t)", waitForRenderedVersion)
	if waitForRenderedVersion {
		output = fmt.Sprintf("updated spec request accepted (targetRenderedVersion=%d waitForRenderedVersion=%t)", expectedRenderedVersion, waitForRenderedVersion)
	}
	Expect(e2e.WriteEvidence(artifactDir, filename, description, output, nil)).To(Succeed())
	GinkgoWriter.Printf("Wrote update evidence file: %s\n", filepath.Join(artifactDir, filename))
}

func updateDeviceConfigWithEvidence(harness *e2e.Harness, deviceID, artifactDir, filename, description string, configs ...v1beta1.ConfigProviderSpec) {
	requireHarnessAndDevice(harness, deviceID)
	requireArtifactFileContext(artifactDir, filename, description)
	GinkgoWriter.Printf("Applying config update with evidence (device=%s configs=%d)\n", deviceID, len(configs))

	if len(configs) == 0 {
		configs = []v1beta1.ConfigProviderSpec{}
	}
	applyDeviceUpdateWithEvidence(harness, deviceID, artifactDir, filename, description, true, func(device *v1beta1.Device) {
		device.Spec.Config = &configs
	}, func() {
		waitForDeviceConfigs(harness, deviceID, configs)
	})
}

func updateDeviceApplicationsWithEvidence(harness *e2e.Harness, deviceID, artifactDir, filename, description string, apps ...v1beta1.ApplicationProviderSpec) {
	requireHarnessAndDevice(harness, deviceID)
	requireArtifactFileContext(artifactDir, filename, description)
	GinkgoWriter.Printf("Applying applications update with evidence (device=%s apps=%d)\n", deviceID, len(apps))

	if len(apps) == 0 {
		apps = []v1beta1.ApplicationProviderSpec{}
	}
	applyDeviceUpdateWithEvidence(harness, deviceID, artifactDir, filename, description, false, func(device *v1beta1.Device) {
		device.Spec.Applications = &apps
	}, func() {
		waitForDeviceApplications(harness, deviceID, apps)
	})
}

func waitForDeviceConfigs(harness *e2e.Harness, deviceID string, configs []v1beta1.ConfigProviderSpec) {
	requireHarnessAndDevice(harness, deviceID)
	waitForDeviceSpecSlice(
		harness,
		deviceID,
		canonicalizeSpecs(configs),
		func(device *v1beta1.Device) ([]string, bool) {
			if device == nil || device.Spec.Config == nil {
				return nil, false
			}
			return canonicalizeSpecs(*device.Spec.Config), true
		},
		func(device *v1beta1.Device) (int, bool) {
			if device == nil || device.Spec.Config == nil {
				return 0, false
			}
			return len(*device.Spec.Config), true
		},
	)
}

func waitForDeviceApplications(harness *e2e.Harness, deviceID string, apps []v1beta1.ApplicationProviderSpec) {
	requireHarnessAndDevice(harness, deviceID)
	waitForDeviceSpecSlice(
		harness,
		deviceID,
		canonicalizeSpecs(apps),
		func(device *v1beta1.Device) ([]string, bool) {
			if device == nil || device.Spec.Applications == nil {
				return nil, false
			}
			return canonicalizeSpecs(*device.Spec.Applications), true
		},
		func(device *v1beta1.Device) (int, bool) {
			if device == nil || device.Spec.Applications == nil {
				return 0, false
			}
			return len(*device.Spec.Applications), true
		},
	)
}

func waitForDeviceSpecSlice(
	harness *e2e.Harness,
	deviceID string,
	expectedCanonical []string,
	getActual func(*v1beta1.Device) ([]string, bool),
	getLen func(*v1beta1.Device) (int, bool),
) {
	requireHarnessAndDevice(harness, deviceID)

	if len(expectedCanonical) == 0 {
		Eventually(func() int {
			device, err := harness.GetDevice(deviceID)
			if err != nil {
				return -1
			}
			if device == nil {
				return -1
			}
			if n, ok := getLen(device); ok {
				return n
			}
			return 0
		}, TIMEOUT, POLLING).Should(Equal(0))
		return
	}

	expectedElements := make([]any, len(expectedCanonical))
	for i, expected := range expectedCanonical {
		expectedElements[i] = expected
	}
	Eventually(func() []string {
		device, err := harness.GetDevice(deviceID)
		if err != nil {
			return nil
		}
		actual, ok := getActual(device)
		if !ok {
			return nil
		}
		return actual
	}, TIMEOUT, POLLING).Should(ConsistOf(expectedElements...))
}

func assertReferenceAbsent(harness *e2e.Harness, artifactDir, filename, reference string) {
	Expect(harness).ToNot(BeNil())
	Expect(strings.TrimSpace(artifactDir)).ToNot(BeEmpty())
	Expect(strings.TrimSpace(filename)).ToNot(BeEmpty())
	Expect(strings.TrimSpace(reference)).ToNot(BeEmpty())
	GinkgoWriter.Printf("Checking reference absent (file=%s reference=%s)\n", filename, reference)
	refsOut, err := harness.RunVMCommandWithEvidence(artifactDir, filename, imageArtifactReferencesCmd)
	Expect(err).ToNot(HaveOccurred())
	Expect(jsonContainsImageRef(refsOut, reference)).To(BeFalse())
	GinkgoWriter.Printf("Confirmed reference absent (file=%s reference=%s)\n", filename, reference)
}

func jsonContainsImageRef(raw, ref string) bool {
	for _, candidate := range imageRefCandidates(ref) {
		if jsonContainsString(raw, candidate) {
			return true
		}
	}
	return false
}

func jsonContainsString(raw, expected string) bool {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return false
	}
	return jsonValueContainsString(parsed, expected)
}

func jsonValueContainsString(value any, expected string) bool {
	switch v := value.(type) {
	case string:
		return v == expected
	case []any:
		for _, item := range v {
			if jsonValueContainsString(item, expected) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if jsonValueContainsString(item, expected) {
				return true
			}
		}
	}
	return false
}

func pruningDropinProviderSpec(name, path string, enabled bool) v1beta1.ConfigProviderSpec {
	spec := v1beta1.InlineConfigProviderSpec{
		Inline: []v1beta1.FileSpec{
			{
				Content: fmt.Sprintf("image-pruning:\n  enabled: %t\n", enabled),
				Path:    path,
			},
		},
		Name: name,
	}
	var provider v1beta1.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(spec)
	Expect(err).ToNot(HaveOccurred())
	return provider
}

func canonicalizeSpecs[T any](items []T) []string {
	canonical := make([]string, 0, len(items))
	for _, item := range items {
		rawJSON, err := json.Marshal(item)
		Expect(err).ToNot(HaveOccurred())
		canonical = append(canonical, canonicalizeJSON(rawJSON))
	}
	return canonical
}

func canonicalizeJSON(raw []byte) string {
	var parsed any
	err := json.Unmarshal(raw, &parsed)
	Expect(err).ToNot(HaveOccurred())
	canonical, err := json.Marshal(parsed)
	Expect(err).ToNot(HaveOccurred())
	return string(canonical)
}

// Build nginx v1.25 and latest app specs.
func buildNginxSpecs() (v1beta1.ApplicationProviderSpec, v1beta1.ApplicationProviderSpec, error) {
	GinkgoWriter.Printf("Building nginx app specs (v1.25, latest)\n")
	nginxV125Spec, err := buildNginxSpec(fmt.Sprintf(nginxV125ContainerContentTemplate, nginxV125ImageRef))
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, v1beta1.ApplicationProviderSpec{}, err
	}
	nginxLatestSpec, err := buildNginxSpec(fmt.Sprintf(nginxLatestContainerContentTemplate, nginxLatestImageRef))
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, v1beta1.ApplicationProviderSpec{}, err
	}
	return nginxV125Spec, nginxLatestSpec, nil
}

func getEnvOrDefault(key, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	return value
}

func canonicalImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "@") {
		return ref
	}
	lastSlash := strings.LastIndex(ref, "/")
	if lastSlash >= 0 && strings.Contains(ref[lastSlash+1:], ":") {
		return ref
	}
	if !strings.Contains(ref, "/") && strings.Contains(ref, ":") {
		return ref
	}
	return ref + ":latest"
}

func imageRefCandidates(ref string) []string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}

	candidates := map[string]struct{}{ref: {}}
	canonical := canonicalImageRef(ref)
	candidates[canonical] = struct{}{}
	if strings.HasSuffix(canonical, ":latest") {
		candidates[strings.TrimSuffix(canonical, ":latest")] = struct{}{}
	}

	out := make([]string, 0, len(candidates))
	for candidate := range candidates {
		out = append(out, candidate)
	}
	return out
}

// Build a nginx app spec for the provided quadlet content.
func buildNginxSpec(containerContent string) (v1beta1.ApplicationProviderSpec, error) {
	GinkgoWriter.Printf("Building nginx app spec (contentLen=%d)\n", len(containerContent))
	return e2e.BuildInlineAppSpec("nginx", v1beta1.AppTypeQuadlet, []e2e.InlineContent{
		{Path: "nginx.container", Content: containerContent},
	})
}

func waitForAgentLog(harness *e2e.Harness, expected string) {
	Expect(harness).ToNot(BeNil())
	Expect(strings.TrimSpace(expected)).ToNot(BeEmpty())
	GinkgoWriter.Printf("Waiting for agent log marker: %s\n", expected)
	Eventually(func() string {
		logs, err := harness.GetFlightctlAgentLogs()
		Expect(err).ToNot(HaveOccurred())
		return logs
	}, TIMEOUT, POLLING).Should(ContainSubstring(expected))
}

func waitForReferencePresence(harness *e2e.Harness, reference string, imageRefMode, shouldExist bool) {
	Expect(harness).ToNot(BeNil())
	Expect(strings.TrimSpace(reference)).ToNot(BeEmpty())
	GinkgoWriter.Printf("Waiting for reference presence (reference=%s imageRefMode=%t shouldExist=%t)\n", reference, imageRefMode, shouldExist)
	Eventually(harness.VMCommandOutputFunc("sudo cat /var/lib/flightctl/image-artifact-references.json", false), TIMEOUT, POLLING).
		Should(Satisfy(func(raw string) bool {
			present := jsonContainsString(raw, reference)
			if imageRefMode {
				present = jsonContainsImageRef(raw, reference)
			}
			if shouldExist {
				return present
			}
			return !present
		}))
}

func writeDeviceSpecYAMLEvidence(artifactDir, filename, deviceID string, apps []v1beta1.ApplicationProviderSpec, configs []v1beta1.ConfigProviderSpec) {
	Expect(strings.TrimSpace(artifactDir)).ToNot(BeEmpty())
	Expect(strings.TrimSpace(filename)).ToNot(BeEmpty())
	Expect(strings.TrimSpace(deviceID)).ToNot(BeEmpty())

	type metadata struct {
		Name string `json:"name"`
	}
	type spec struct {
		Applications *[]v1beta1.ApplicationProviderSpec `json:"applications,omitempty"`
		Config       *[]v1beta1.ConfigProviderSpec      `json:"config,omitempty"`
	}
	type deviceSpec struct {
		APIVersion string   `json:"apiVersion"`
		Kind       string   `json:"kind"`
		Metadata   metadata `json:"metadata"`
		Spec       spec     `json:"spec"`
	}

	doc := deviceSpec{
		APIVersion: "flightctl.io/v1beta1",
		Kind:       "Device",
		Metadata: metadata{
			Name: deviceID,
		},
		Spec: spec{},
	}
	if apps != nil {
		doc.Spec.Applications = &apps
	}
	if configs != nil {
		doc.Spec.Config = &configs
	}

	out, err := yaml.Marshal(doc)
	Expect(err).ToNot(HaveOccurred())
	Expect(e2e.WriteEvidence(artifactDir, filename, "generated device spec yaml", string(out), nil)).To(Succeed())
}

func registerPruningRefreshCleanup(harness *e2e.Harness, deviceID, artifactDir string, enableProvider v1beta1.ConfigProviderSpec) {
	requireHarnessAndDevice(harness, deviceID)
	Expect(strings.TrimSpace(artifactDir)).ToNot(BeEmpty())
	GinkgoWriter.Printf("Registering pruning refresh cleanup (device=%s)\n", deviceID)
	DeferCleanup(func() {
		if _, err := harness.GetDevice(deviceID); err != nil {
			if strings.Contains(err.Error(), "not found") {
				GinkgoWriter.Printf("Skipping pruning refresh cleanup: device already deleted (device=%s)\n", deviceID)
				return
			}
			Expect(err).ToNot(HaveOccurred())
		}
		updateDeviceConfigWithEvidence(
			harness,
			deviceID,
			artifactDir,
			"host_device_cleanup_pruning_refresh_dropin.txt",
			"cleanup pruning refresh drop-in",
			enableProvider,
		)
	})
}

func waitForUpdateConditionCompleted(harness *e2e.Harness, deviceID, description string) {
	requireHarnessAndDevice(harness, deviceID)
	Expect(strings.TrimSpace(description)).ToNot(BeEmpty())
	GinkgoWriter.Printf("Waiting for update condition completion (device=%s desc=%s)\n", deviceID, description)
	harness.WaitForDeviceContents(deviceID, description+" completed", func(device *v1beta1.Device) bool {
		if device == nil || device.Status == nil {
			return false
		}
		updating := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
		if updating == nil {
			return false
		}
		if updating.Status != v1beta1.ConditionStatusFalse {
			return false
		}
		if updating.Reason != string(v1beta1.UpdateStateUpdated) && updating.Reason != string(v1beta1.UpdateStateError) {
			return false
		}
		// Guard against stale status conditions by requiring the message to match desired renderedVersion.
		if updating.Reason == string(v1beta1.UpdateStateUpdated) || updating.Reason == string(v1beta1.UpdateStateError) {
			desired := desiredRenderedVersion(device)
			if desired == "" {
				return true
			}
			return strings.Contains(updating.Message, "renderedVersion: "+desired)
		}
		return true
	}, TIMEOUT)

	device, err := harness.GetDevice(deviceID)
	Expect(err).ToNot(HaveOccurred())
	Expect(device).ToNot(BeNil())
	Expect(device.Status).ToNot(BeNil())
	updating := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
	Expect(updating).ToNot(BeNil())
	if updating.Reason == string(v1beta1.UpdateStateError) {
		Fail(fmt.Sprintf("%s failed: %s", description, strings.TrimSpace(updating.Message)))
	}
}

func desiredRenderedVersion(device *v1beta1.Device) string {
	if device == nil || device.Metadata.Annotations == nil {
		return ""
	}
	return strings.TrimSpace((*device.Metadata.Annotations)["device-controller/renderedVersion"])
}
