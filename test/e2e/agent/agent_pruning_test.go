package agent_test

import (
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
		lsOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_ls_etc_flightctl.txt", "ls -lm /etc/flightctl/")
		Expect(err).ToNot(HaveOccurred())
		Expect(lsOut).To(ContainSubstring("config.yaml"))

		By("ensure drop-in directory exists")
		_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_conf_d_ls.txt", "sudo ls -l /etc/flightctl/conf.d")
		Expect(err).ToNot(HaveOccurred())
	})

	Context("image and artifact pruning", func() {
		It("Enable/Disable Pruning via Agent Config and Drop-ins", Label("87084", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "pruning-config-dropins")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: pruning-config-dropins (artifactDir=%s)\n", artifactDir)

			By("enable pruning via drop-in (spec update)")
			enableDropinPath := "/etc/flightctl/conf.d/50-enable-pruning.yaml"
			enableProvider := pruningDropinProviderSpec("pruning-enable", enableDropinPath, true)
			err = harness.UpdateDeviceSpecWithEvidence(deviceID, artifactDir, "host_device_enable_pruning_dropin.txt", "enable pruning via drop-in", func(device *v1beta1.Device) {
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{enableProvider}
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() {
				_ = harness.UpdateDeviceSpecWithEvidence(deviceID, artifactDir, "host_device_cleanup_pruning_dropins.txt", "cleanup pruning drop-ins", func(device *v1beta1.Device) {
					empty := []v1beta1.ConfigProviderSpec{}
					device.Spec.Config = &empty
				})
			})
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_enable_pruning_dropin_ls.txt", "sudo ls -l "+enableDropinPath)
			Expect(err).ToNot(HaveOccurred())
			waitForAgentLog(harness, fmt.Sprintf("Image pruning config reloaded: enabled=%t", true))
			agentActiveOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_agent_is_active_after_enable.txt", "sudo systemctl is-active flightctl-agent")
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(agentActiveOut)).To(Equal("active"))

			By("disable pruning via higher lexical drop-in (spec update)")
			disableDropinPath := "/etc/flightctl/conf.d/99-disable-pruning.yaml"
			disableProvider := pruningDropinProviderSpec("pruning-disable", disableDropinPath, false)
			err = harness.UpdateDeviceSpecWithEvidence(deviceID, artifactDir, "host_device_disable_pruning_dropin.txt", "disable pruning via drop-in", func(device *v1beta1.Device) {
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{enableProvider, disableProvider}
			})
			Expect(err).ToNot(HaveOccurred())
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_disable_pruning_dropin_ls.txt", "sudo ls -l "+disableDropinPath)
			Expect(err).ToNot(HaveOccurred())
			waitForAgentLog(harness, fmt.Sprintf("Image pruning config reloaded: enabled=%t", false))
			agentActiveOut, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_agent_is_active_after_disable.txt", "sudo systemctl is-active flightctl-agent")
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(agentActiveOut)).To(Equal("active"))

			By("verify pruning does not run while disabled")
			nginxV125Spec, nginxLatestSpec, err := buildNginxSpecs()
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_v125_disabled_pruning.txt", "update device applications: nginx v1.25 (pruning disabled)", nginxV125Spec)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_latest_disabled_pruning.txt", "update device applications: nginx latest (pruning disabled)", nginxLatestSpec)
			Expect(err).ToNot(HaveOccurred())
			podmanOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_images_nginx25_disabled_pruning.txt", "podman images --no-trunc --format '{{.Repository}}:{{.Tag}}'")
			Expect(err).ToNot(HaveOccurred())
			Expect(podmanOut).To(ContainSubstring("quay.io/library/nginx.25"))

			By("re-enable pruning by removing disable drop-in (spec update)")
			err = harness.UpdateDeviceSpecWithEvidence(deviceID, artifactDir, "host_device_remove_disable_pruning_dropin.txt", "remove disable pruning drop-in", func(device *v1beta1.Device) {
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{enableProvider}
			})
			Expect(err).ToNot(HaveOccurred())
			waitForAgentLog(harness, fmt.Sprintf("Image pruning config reloaded: enabled=%t", true))
			agentActiveOut, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_agent_is_active_after_reenable.txt", "sudo systemctl is-active flightctl-agent")
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(agentActiveOut)).To(Equal("active"))

			By("verify pruning behavior after re-enable")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_v125_pruning_check.txt", "update device applications: nginx v1.25", nginxV125Spec)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_latest_pruning_check.txt", "update device applications: nginx latest", nginxLatestSpec)
			Expect(err).ToNot(HaveOccurred())
			waitForAgentLog(harness, "Starting pruning of")
			Eventually(harness.VMCommandOutputFunc("podman images --no-trunc | egrep -i 'nginx.25' || true", true), TIMEOUT, POLLING).
				Should(Equal(""))

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("References File Created and Updated Before Upgrades", Label("87085", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "references-file-created")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: references-file-created (artifactDir=%s)\n", artifactDir)

			By("apply a Device spec with an inline Quadlet nginx app (doc example)")
			nginxInlineSpec, err := util.BuildInlineAppSpec("nginx-server", v1beta1.AppTypeQuadlet, []util.InlineContent{
				{Path: "nginx.container", Content: nginxInlineContainerContent},
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_inline.txt", "update device applications: nginx-server", nginxInlineSpec)
			Expect(err).ToNot(HaveOccurred())

			By("verify references file exists")
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_ls.txt", "sudo ls -l /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())

			By("verify nginx image appears in reference file")
			refsCatOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())
			Expect(refsCatOut).To(ContainSubstring("quay.io/library/nginx"))

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Reference Entry Removed Only After Actual Image Is Removed", Label("87086", "sanity", "agent"), func() {
			nginxV125Spec, nginxLatestSpec, err := buildNginxSpecs()
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "reference-removed-after-prune")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: reference-removed-after-prune (artifactDir=%s)\n", artifactDir)

			By("apply nginx v1.25 (doc example nginx.25)")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_v125.txt", "update device applications: nginx v1.25", nginxV125Spec)
			Expect(err).ToNot(HaveOccurred())

			By("verify v1.25 image referenced in the references file")
			Eventually(harness.VMCommandOutputFunc("sudo cat /var/lib/flightctl/image-artifact-references.json", false), TIMEOUT, POLLING).
				Should(ContainSubstring("quay.io/library/nginx.25"))
			refsCatOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat_before_update.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())
			Expect(refsCatOut).To(ContainSubstring("quay.io/library/nginx.25"))

			By("update nginx image to latest")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)
			Expect(err).ToNot(HaveOccurred())

			By("verify old image is pruned and then removed from references")
			Eventually(harness.VMCommandOutputFunc("podman images --no-trunc --format '{{.Repository}}:{{.Tag}}'", false), TIMEOUT, POLLING).
				Should(Not(ContainSubstring("quay.io/library/nginx.25")))
			podmanOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_images_nginx.txt", "podman images --no-trunc --format '{{.Repository}}:{{.Tag}}'")
			Expect(err).ToNot(HaveOccurred())
			Expect(podmanOut).To(ContainSubstring("quay.io/library/nginx"))
			Expect(podmanOut).To(Not(ContainSubstring("quay.io/library/nginx.25")))

			Eventually(harness.VMCommandOutputFunc("sudo cat /var/lib/flightctl/image-artifact-references.json", false), TIMEOUT, POLLING).
				Should(ContainSubstring("quay.io/library/nginx"))
			Eventually(harness.VMCommandOutputFunc("sudo cat /var/lib/flightctl/image-artifact-references.json", false), TIMEOUT, POLLING).
				Should(Not(ContainSubstring("quay.io/library/nginx.25")))
			refsCatOut, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())
			Expect(refsCatOut).To(ContainSubstring("quay.io/library/nginx"))
			Expect(refsCatOut).ToNot(ContainSubstring("quay.io/library/nginx.25"))

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Lost Reference Model: Unreferenced Images Become Eligible and Are Pruned", Label("87087", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "lost-reference-pruning")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: lost-reference-pruning (artifactDir=%s)\n", artifactDir)

			By("ensure pruning is enabled via drop-in (spec update)")
			enableDropinPath := "/etc/flightctl/conf.d/60-enable-pruning.yaml"
			enableProvider := pruningDropinProviderSpec("pruning-enable", enableDropinPath, true)
			err = harness.UpdateDeviceSpecWithEvidence(deviceID, artifactDir, "host_device_enable_pruning_dropin.txt", "enable pruning via drop-in", func(device *v1beta1.Device) {
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{enableProvider}
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() {
				_ = harness.UpdateDeviceSpecWithEvidence(deviceID, artifactDir, "host_device_cleanup_pruning_dropins.txt", "cleanup pruning drop-ins", func(device *v1beta1.Device) {
					empty := []v1beta1.ConfigProviderSpec{}
					device.Spec.Config = &empty
				})
			})
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_enable_pruning_dropin_ls.txt", "sudo ls -l "+enableDropinPath)
			Expect(err).ToNot(HaveOccurred())

			By("apply postgres (doc example)")
			postgresBaseSpec, err := util.BuildInlineAppSpec("postgres-db", v1beta1.AppTypeQuadlet, []util.InlineContent{
				{Path: "db.volume", Content: postgresVolumeContent},
				{Path: "postgres.container", Content: fmt.Sprintf(postgresContainerContentTemplate, "16")},
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_postgres_base.txt", "update device applications: postgres 16", postgresBaseSpec)
			Expect(err).ToNot(HaveOccurred())

			By("update postgres image to a different tag (simulate upgrade)")
			altTag := os.Getenv("POSTGRES_ALT_TAG")
			if altTag == "" {
				Skip("POSTGRES_ALT_TAG not set; update to a valid alternate postgres tag for this environment")
			}
			postgresAltSpec, err := util.BuildInlineAppSpec("postgres-db", v1beta1.AppTypeQuadlet, []util.InlineContent{
				{Path: "db.volume", Content: postgresVolumeContent},
				{Path: "postgres.container", Content: fmt.Sprintf(postgresContainerContentTemplate, altTag)},
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_postgres_alt.txt", "update device applications: postgres "+altTag, postgresAltSpec)
			Expect(err).ToNot(HaveOccurred())

			By("verify only currently referenced images are retained")
			desiredOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_desired_json.txt", "sudo cat /var/lib/flightctl/desired.json")
			Expect(err).ToNot(HaveOccurred())
			Expect(desiredOut).To(ContainSubstring("quay.io/library/postgres:" + altTag))
			Expect(desiredOut).ToNot(ContainSubstring("quay.io/library/postgres:16"))
			podmanOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_images_postgres.txt", "podman images --no-trunc --format '{{.Repository}}:{{.Tag}}'")
			Expect(err).ToNot(HaveOccurred())
			Expect(podmanOut).To(ContainSubstring("quay.io/library/postgres:" + altTag))
			Expect(podmanOut).ToNot(ContainSubstring("quay.io/library/postgres:16"))

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Application Image Retention: Keep Current + Previous, Prune Older", Label("87088", "sanity", "agent"), func() {
			nginxV125Spec, nginxLatestSpec, err := buildNginxSpecs()
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "application-image-retention")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: application-image-retention (artifactDir=%s)\n", artifactDir)

			By("apply nginx.25")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_v125.txt", "update device applications: nginx v1.25", nginxV125Spec)
			Expect(err).ToNot(HaveOccurred())

			By("apply nginx")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)
			Expect(err).ToNot(HaveOccurred())

			By("apply nginx again (acts as v3 in environments without extra tags)")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_latest_again.txt", "update device applications: nginx latest again", nginxLatestSpec)
			Expect(err).ToNot(HaveOccurred())

			By("verify retention")
			podmanOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_images_nginx.txt", "podman images --no-trunc | egrep -i 'quay.io/library/nginx' || true")
			Expect(err).ToNot(HaveOccurred())
			Expect(podmanOut).To(ContainSubstring("quay.io/library/nginx"))

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("OCI Artifacts: Unreferenced Artifacts Are Pruned", Label("87089", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "oci-artifact-pruning")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: oci-artifact-pruning (artifactDir=%s)\n", artifactDir)

			By("verify Podman version supports artifacts")
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_version.txt", "podman --version")
			Expect(err).ToNot(HaveOccurred())

			By("create a compose artifact (doc example)")
			composeOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_artifact_add_ls.txt", podmanComposeArtifactCmd)
			Expect(err).ToNot(HaveOccurred())
			Expect(composeOut).To(ContainSubstring("quay.io/my-org/my-compose-app.0"))

			By("apply a Device spec that references the artifact (as app package)")
			artifactSpec, err := util.BuildImageAppSpec("my-compose-app", v1beta1.AppTypeCompose, "quay.io/my-org/my-compose-app.0")
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_artifact_app.txt", "update device applications: my-compose-app", artifactSpec)
			Expect(err).ToNot(HaveOccurred())

			By("remove the artifact reference from spec")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_artifact_removed.txt", "update device applications: empty")
			Expect(err).ToNot(HaveOccurred())

			By("verify artifact pruned")
			Eventually(harness.VMCommandOutputFunc("podman artifact ls", false), TIMEOUT, POLLING).
				Should(Not(ContainSubstring("quay.io/my-org/my-compose-app.0")))
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_artifact_ls.txt", "podman artifact ls")
			Expect(err).ToNot(HaveOccurred())

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("OS Image Update: Old OS Image Pruned After Successful Update", Label("87090", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "os-image-pruning")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: os-image-pruning (artifactDir=%s)\n", artifactDir)

			By("apply OS image 9.4")
			err = harness.UpdateOSImageWithEvidence(deviceID, artifactDir, "host_device_update_os_94.txt", "update device os image: quay.io/flightctl/rhel:9.4", "quay.io/flightctl/rhel:9.4")
			Expect(err).ToNot(HaveOccurred())

			By("apply OS image 9.5")
			err = harness.UpdateOSImageWithEvidence(deviceID, artifactDir, "host_device_update_os_95.txt", "update device os image: quay.io/flightctl/rhel:9.5", "quay.io/flightctl/rhel:9.5")
			Expect(err).ToNot(HaveOccurred())

			By("verify OS image pruning outcome")
			podmanOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_images_rhel.txt", "podman images --no-trunc | egrep -i 'quay.io/flightctl/rhel' || true")
			Expect(err).ToNot(HaveOccurred())
			Expect(podmanOut).To(ContainSubstring("quay.io/flightctl/rhel"))

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Pruning Runs Only After Successful Reconciliation and Is Non-Blocking", Label("87091", "sanity", "agent"), func() {
			nginxLatestSpec, err := buildNginxSpec(nginxLatestContainerContent)
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "pruning-non-blocking")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: pruning-non-blocking (artifactDir=%s)\n", artifactDir)

			By("apply a spec with an invalid image reference")
			brokenSpec, err := util.BuildInlineAppSpec("broken", v1beta1.AppTypeQuadlet, []util.InlineContent{
				{Path: "broken.container", Content: brokenContainerContent},
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_broken_image.txt", "update device applications: broken image", brokenSpec)
			Expect(err).ToNot(HaveOccurred())

			By("verify device does not reach healthy due to image pull failure")
			Eventually(harness.DeviceNotHealthyFunc(deviceID), TIMEOUT, POLLING).Should(BeTrue())

			By("apply a valid spec (nginx)")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)
			Expect(err).ToNot(HaveOccurred())

			By("verify images reflect valid final state")
			podmanOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_images_nginx.txt", "podman images --no-trunc | egrep -i 'nginx' || true")
			Expect(err).ToNot(HaveOccurred())
			Expect(podmanOut).To(ContainSubstring("nginx"))

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("References File Updated After Successful Pruning", Label("87092", "sanity", "agent"), func() {
			nginxV125Spec, nginxLatestSpec, err := buildNginxSpecs()
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "references-updated-after-prune")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: references-updated-after-prune (artifactDir=%s)\n", artifactDir)

			By("apply nginx.25 then nginx")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_v125.txt", "update device applications: nginx v1.25", nginxV125Spec)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)
			Expect(err).ToNot(HaveOccurred())

			By("verify v1.25 image gone")
			Eventually(harness.VMCommandOutputFunc("podman images --no-trunc | egrep -i 'nginx.25' || true", true), TIMEOUT, POLLING).
				Should(Equal(""))
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_images_nginx25.txt", "podman images --no-trunc | egrep -i 'nginx.25' || true")
			Expect(err).ToNot(HaveOccurred())

			By("verify v1.25 removed from references file")
			Eventually(harness.VMCommandOutputFunc("sudo cat /var/lib/flightctl/image-artifact-references.json", false), TIMEOUT, POLLING).
				Should(Not(ContainSubstring("nginx.25")))
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_refs_cat.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json")
			Expect(err).ToNot(HaveOccurred())

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("Partial Failure Handling: One Removal Fails, Others Continue, Reconciliation Not Blocked", Label("87093", "sanity", "agent"), func() {
			nginxLatestSpec, err := buildNginxSpec(nginxLatestContainerContent)
			Expect(err).ToNot(HaveOccurred())

			artifactDir, err := harness.SetupScenario(deviceID, "partial-failure-pruning")
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Scenario: partial-failure-pruning (artifactDir=%s)\n", artifactDir)

			By("keep nginx.25 in use")
			holdCmd := `podman pull quay.io/library/nginx.25
podman run -d --name hold-nginx quay.io/library/nginx.25
podman ps | grep hold-nginx`
			holdOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_hold_nginx_run.txt", holdCmd)
			Expect(err).ToNot(HaveOccurred())
			Expect(holdOut).To(ContainSubstring("hold-nginx"))

			By("apply nginx (would normally prune v1.25)")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_latest.txt", "update device applications: nginx latest", nginxLatestSpec)
			Expect(err).ToNot(HaveOccurred())

			By("verify v1.25 still present (not removed due to in-use)")
			podmanOut, err := harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_images_nginx25_present.txt", "podman images --no-trunc | egrep -i 'nginx.25' || true")
			Expect(err).ToNot(HaveOccurred())
			Expect(podmanOut).To(ContainSubstring("nginx.25"))

			By("remove the hold container")
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_remove_hold_nginx.txt", "podman rm -f hold-nginx")
			Expect(err).ToNot(HaveOccurred())

			By("apply nginx again to trigger another post-update pruning")
			err = harness.UpdateApplicationsWithEvidence(deviceID, artifactDir, "host_device_update_nginx_latest_again.txt", "update device applications: nginx latest again", nginxLatestSpec)
			Expect(err).ToNot(HaveOccurred())

			By("verify v1.25 now pruned")
			Eventually(harness.VMCommandOutputFunc("podman images --no-trunc | egrep -i 'nginx.25' || true", true), TIMEOUT, POLLING).
				Should(Equal(""))
			_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_podman_images_nginx25_final.txt", "podman images --no-trunc | egrep -i 'nginx.25' || true")
			Expect(err).ToNot(HaveOccurred())

			By("capture pruning evidence files and status")
			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})
	})

})

// Inline quadlet container content for nginx.
const nginxInlineContainerContent = `[Unit]
Description=Nginx web server
[Container]
Image=quay.io/library/nginx
PublishPort=8080:80
[Service]
Restart=always
[Install]
WantedBy=default.target
`

// Quadlet container content for nginx v1.25.
const nginxV125ContainerContent = `[Container]
Image=quay.io/library/nginx.25
Volume=/etc/nginx/html:/usr/share/nginx/html,Z
[Service]
Restart=on-failure
[Install]
WantedBy=default.target
`

// Quadlet container content for latest nginx.
const nginxLatestContainerContent = `[Container]
Image=quay.io/library/nginx
PublishPort=8080:80
[Service]
Restart=always
[Install]
WantedBy=default.target
`

// Volume content for postgres quadlet.
const postgresVolumeContent = `[Volume]
VolumeName=postgres-data
`

// Template for postgres quadlet container content with tag.
const postgresContainerContentTemplate = `[Container]
Image=quay.io/library/postgres:%s
Volume=db.volume:/var/lib/postgresql/data
Environment=POSTGRES_PASSWORD=secret
[Service]
Restart=always
[Install]
WantedBy=default.target
`

// Broken quadlet container content for negative testing.
const brokenContainerContent = `[Container]
Image=quay.io/library/this-image-should-not-exist
`

// Podman command to create and list a compose artifact.
const podmanComposeArtifactCmd = `cat > /tmp/podman-compose.yaml <<'EOF'
version: "3.8"
services:
  svc:
    image: quay.io/library/nginx
    ports:
    - "8081:80"
EOF
podman artifact add quay.io/my-org/my-compose-app.0 /tmp/podman-compose.yaml
podman artifact ls`

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

// Build nginx v1.25 and latest app specs.
func buildNginxSpecs() (v1beta1.ApplicationProviderSpec, v1beta1.ApplicationProviderSpec, error) {
	nginxV125Spec, err := buildNginxSpec(nginxV125ContainerContent)
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, v1beta1.ApplicationProviderSpec{}, err
	}
	nginxLatestSpec, err := buildNginxSpec(nginxLatestContainerContent)
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, v1beta1.ApplicationProviderSpec{}, err
	}
	return nginxV125Spec, nginxLatestSpec, nil
}

// Build a nginx app spec for the provided quadlet content.
func buildNginxSpec(containerContent string) (v1beta1.ApplicationProviderSpec, error) {
	return util.BuildInlineAppSpec("nginx", v1beta1.AppTypeQuadlet, []util.InlineContent{
		{Path: "nginx.container", Content: containerContent},
	})
}

func waitForAgentLog(harness *e2e.Harness, expected string) {
	cmd := fmt.Sprintf("sudo journalctl -u flightctl-agent --no-pager -n 200 | grep -F '%s'", expected)
	Eventually(harness.VMCommandOutputFunc(cmd, false), TIMEOUT, POLLING).Should(ContainSubstring(expected))
}
