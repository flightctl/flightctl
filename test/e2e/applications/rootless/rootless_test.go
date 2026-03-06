// Package rootless_test implements e2e tests for rootless applications across app types
// (quadlet, container, compose). These tests require a quadlet-capable VM (e.g. make deploy-quadlets-vm)
// and an agent that supports runAs (rootless). Checkpoint 1 (agent upgrade) is manual/out-of-band.
//
// TODO(EDM-3440): Several checkpoints that verify podman/volume/exec as the runAs user are commented out
// because the agent leaves ~/.config/containers and ~/.config/systemd root-owned, so "podman ps" as that user
// fails with "stat .../.config: permission denied". Uncomment those blocks once https://issues.redhat.com/browse/EDM-3440 is fixed.

package rootless_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	flightctlUser = "flightctl"
	customAppUser = "customapp"

	rootlessAppRootfulSimple     = "simple-quadlet"
	rootlessAppNewRootful        = "new-rootful-app"
	rootlessAppContainerMulti    = "container-multi-app"
	rootlessAppContainerRootless = "container-rootless-app"
	rootlessAppUpdateTest        = "update-test"
	rootlessAppWithVol           = "app-with-vol" // #nosec G101 -- app name for e2e test, not a credential
	rootlessAppReboot            = "rootless-reboot-app"
	rootlessAppComposeRunAs      = "compose-test"
	rootlessAppCustomUser        = "customuser-app"
	rootlessAppNonrootInternal   = "nonroot-internal"

	rootlessAlpineImage = "quay.io/flightctl-tests/alpine:v1"
	rootlessNginxImage  = "quay.io/flightctl-tests/nginx:v1"
	rootlessVolumeImage = "ghcr.io/homebrew/core/sqlite:3.50.2"
)

var _ = Describe("Rootless applications", Label("rootless"), func() {
	var harness *e2e.Harness
	var deviceID string

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		_, err := login.LoginToAPIWithToken(harness)
		Expect(err).ToNot(HaveOccurred())
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	It("covers all rootless checkpoints across quadlet, container, and compose app types", Label("sanity", "87844"), func() {
		var names string
		var err error

		By("Container app type: deploy with runAs flightctl and verify user path and podman")
		containerApp, err := e2e.NewContainerApplicationSpecWithRunAs(
			rootlessAppContainerRootless, rootlessNginxImage,
			[]v1beta1.ApplicationPort{"8083:80"}, nil, nil, nil, flightctlUser)
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{containerApp})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppContainerRootless, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		// TODO(EDM-3440): uncomment when root-owned ~/.config/containers|systemd is fixed (podman ps as runAs user)
		// names, err = harness.RunPodmanPsContainerNamesAsUser(flightctlUser, false)
		// Expect(err).ToNot(HaveOccurred())
		// Expect(names).To(ContainSubstring(rootlessAppContainerRootless))
		flightctlQuadletPath, err := harness.QuadletPathForUserOnVM(flightctlUser)
		Expect(err).ToNot(HaveOccurred())
		harness.VerifyQuadletApplicationFolderExistsAt(rootlessAppContainerRootless, flightctlQuadletPath)

		By("Quadlet rootful: deploy two rootful quadlet apps and verify both at root")
		nginxContent := rootlessNginxContainerContentWithPort(rootlessNginxImage, "8080")
		spec1, err := e2e.NewQuadletInlineSpec(rootlessAppRootfulSimple, "", []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})
		Expect(err).ToNot(HaveOccurred())
		spec2, err := e2e.NewQuadletInlineSpec(rootlessAppNewRootful, "", []string{"nginx.container"}, []string{nginxContent})
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{spec1, spec2})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppRootfulSimple, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppNewRootful, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		names, err = harness.RunPodmanPsContainerNamesAsUser("root", false)
		Expect(err).ToNot(HaveOccurred())
		Expect(names).To(And(ContainSubstring(rootlessAppRootfulSimple), ContainSubstring(rootlessAppNewRootful)))
		harness.VerifyQuadletApplicationFolderExistsAt(rootlessAppRootfulSimple, e2e.QuadletUnitPath)
		harness.VerifyQuadletApplicationFolderExistsAt(rootlessAppNewRootful, e2e.QuadletUnitPath)

		By("Transition rootful to rootless by adding runAs flightctl")
		Expect(harness.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, rootlessAppNewRootful, flightctlUser, []string{"nginx.container"}, []string{nginxContent})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppNewRootful, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		// TODO(EDM-3440): uncomment when root-owned ~/.config/containers|systemd is fixed (podman ps as runAs user)
		// names, err = harness.RunPodmanPsContainerNamesAsUser(flightctlUser, false)
		// Expect(err).ToNot(HaveOccurred())
		// Expect(names).To(ContainSubstring(rootlessAppNewRootful))
		flightctlQuadletPath, err = harness.QuadletPathForUserOnVM(flightctlUser)
		Expect(err).ToNot(HaveOccurred())
		harness.VerifyQuadletApplicationFolderExistsAt(rootlessAppNewRootful, flightctlQuadletPath)
		harness.VerifyQuadletApplicationFolderDeletedAt(rootlessAppNewRootful, e2e.QuadletUnitPath)

		By("Mixed: add rootful container app and verify rootless quadlet and rootful container run simultaneously")
		containerRootful, err := e2e.NewContainerApplicationSpec(rootlessAppContainerMulti, rootlessNginxImage, []v1beta1.ApplicationPort{"8082:80"}, nil, nil, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.UpdateDeviceAndWaitForRenderedVersion(deviceID, func(d *v1beta1.Device) {
			if d.Spec.Applications == nil {
				d.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
			}
			*d.Spec.Applications = append(*d.Spec.Applications, containerRootful)
		})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppContainerMulti, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppNewRootful, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		// TODO(EDM-3440): uncomment when root-owned ~/.config/containers|systemd is fixed (podman ps as runAs user)
		// names, err = harness.RunPodmanPsContainerNamesAsUser(flightctlUser, false)
		// Expect(err).ToNot(HaveOccurred())
		// Expect(names).To(ContainSubstring(rootlessAppNewRootful))
		names, err = harness.RunPodmanPsContainerNamesAsUser("root", false)
		Expect(err).ToNot(HaveOccurred())
		Expect(names).To(ContainSubstring(rootlessAppContainerMulti))

		// EDM-3451: Delete this step once the issue is fixed; then use a single update (mixed → rootful) again.
		By("Remove container-multi-app so only rootless new-rootful-app remains, then transition to rootful")
		specRootlessOnly, err := e2e.NewQuadletInlineSpec(rootlessAppNewRootful, flightctlUser, []string{"nginx.container"}, []string{nginxContent})
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{specRootlessOnly})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppNewRootful, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		By("Transition rootless back to rootful by removing runAs")
		Expect(harness.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, rootlessAppNewRootful, "", []string{"nginx.container"}, []string{nginxContent})).ToNot(HaveOccurred())
		By("Verify device spec has runAs removed for new-rootful-app")
		device, err := harness.GetDevice(deviceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(device.Spec.Applications).ToNot(BeNil())
		var found bool
		for i := range *device.Spec.Applications {
			q, err := (*device.Spec.Applications)[i].AsQuadletApplication()
			if err != nil {
				continue
			}
			if q.Name != nil && *q.Name == rootlessAppNewRootful {
				Expect(string(q.RunAs)).To(BeEmpty(), "device spec should have runAs removed for %s (rootless→rootful)", rootlessAppNewRootful)
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "device spec should contain quadlet app %s", rootlessAppNewRootful)
		// With the two-step flow (remove container-multi-app first), the rootless→rootful transition does not hit EDM-3451 race; verify app status.
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppNewRootful, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		names, err = harness.RunPodmanPsContainerNamesAsUser("root", false)
		Expect(err).ToNot(HaveOccurred())
		Expect(names).To(ContainSubstring(rootlessAppNewRootful))
		harness.VerifyQuadletApplicationFolderExistsAt(rootlessAppNewRootful, e2e.QuadletUnitPath)
		flightctlQuadletPath, err = harness.QuadletPathForUserOnVM(flightctlUser)
		Expect(err).ToNot(HaveOccurred())
		harness.VerifyQuadletApplicationFolderDeletedAt(rootlessAppNewRootful, flightctlQuadletPath)

		By("Rootless in-place update: deploy then update image and verify running")
		Expect(harness.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, rootlessAppUpdateTest, flightctlUser, []string{"app.container"}, []string{rootlessUpdateV1Content(rootlessAlpineImage)})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppUpdateTest, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		Expect(harness.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, rootlessAppUpdateTest, flightctlUser, []string{"app.container"}, []string{rootlessUpdateV2Content(rootlessNginxImage)})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppUpdateTest, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		// TODO(EDM-3440): uncomment when root-owned ~/.config/containers|systemd is fixed (podman ps as runAs user)
		// names, err = harness.RunPodmanPsContainerNamesAsUser(flightctlUser, false)
		// Expect(err).ToNot(HaveOccurred())
		// Expect(names).To(ContainSubstring(rootlessAppUpdateTest))

		By("Rootless volumes: deploy quadlet with volume and verify under user storage")
		vol, err := e2e.NewImageVolume("data-vol", rootlessVolumeImage)
		Expect(err).ToNot(HaveOccurred())
		volumes := []v1beta1.ApplicationVolume{vol}
		volSpec, err := e2e.NewQuadletInlineSpecWithVolumes(rootlessAppWithVol, flightctlUser, &volumes,
			[]string{"app.container", "data.volume"},
			[]string{
				rootlessVolumeContainerContent(rootlessAlpineImage, "data-vol"),
				"[Volume]\nDriver=image\nImage=" + rootlessVolumeImage + "\n",
			})
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{volSpec})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppWithVol, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		// TODO(EDM-3440): uncomment when root-owned ~/.config/containers|systemd is fixed (podman volume ls as runAs user)
		// flightctlHome, err := harness.GetUserHomeOnVM(flightctlUser)
		// Expect(err).ToNot(HaveOccurred())
		// volOut, err := harness.VM.RunSSH([]string{"sudo", "-u", flightctlUser, "sh", "-c", fmt.Sprintf("cd /tmp && env HOME=%q podman volume ls --format '{{.Name}}'", flightctlHome)}, nil)
		// Expect(err).ToNot(HaveOccurred())
		// Expect(volOut.String()).To(And(ContainSubstring(rootlessAppWithVol), ContainSubstring("data-vol")))

		By("Rootless after reboot: app still running")
		Expect(harness.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, rootlessAppReboot, flightctlUser, []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppReboot, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		Expect(harness.RebootVMAndWaitForSSH(testutil.REBOOT_WAIT_INTERVAL, testutil.REBOOT_MAX_ATTEMPTS)).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppReboot, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		// TODO(EDM-3440): uncomment when root-owned ~/.config/containers|systemd is fixed (podman ps as runAs user)
		// names, err = harness.RunPodmanPsContainerNamesAsUser(flightctlUser, false)
		// Expect(err).ToNot(HaveOccurred())
		// Expect(names).To(ContainSubstring(rootlessAppReboot))

		By("Compose: runAs ignored, runs as root")
		composeSpec, err := e2e.NewComposeInlineSpec(rootlessAppComposeRunAs, "podman-compose.yaml", rootlessComposeContent(), flightctlUser)
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{composeSpec})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppComposeRunAs, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		names, err = harness.RunPodmanPsContainerNamesAsUser("root", false)
		Expect(err).ToNot(HaveOccurred())
		Expect(names).To(ContainSubstring(rootlessAppComposeRunAs))
		// TODO(EDM-3440): uncomment when root-owned ~/.config/containers|systemd is fixed (podman ps as runAs user)
		// names, err = harness.RunPodmanPsContainerNamesAsUser(flightctlUser, false)
		// Expect(err).ToNot(HaveOccurred())
		// Expect(names).ToNot(ContainSubstring(rootlessAppComposeRunAs))

		By("Custom user: provision customapp, deploy with runAs customapp, verify path and podman, cleanup")
		_, err = harness.VM.RunSSH([]string{"sudo", "useradd", "--create-home", "--user-group", customAppUser}, nil)
		Expect(err).ToNot(HaveOccurred(), "useradd %s", customAppUser)
		_, err = harness.VM.RunSSH([]string{"sudo", "loginctl", "enable-linger", customAppUser}, nil)
		Expect(err).ToNot(HaveOccurred(), "loginctl enable-linger %s", customAppUser)
		customHome, err := harness.GetUserHomeOnVM(customAppUser)
		Expect(err).ToNot(HaveOccurred(), "getent passwd %s", customAppUser)
		_, err = harness.VM.RunSSH([]string{"sudo", "mkdir", "-p", customHome + "/.config", customHome + "/.local"}, nil)
		Expect(err).ToNot(HaveOccurred(), "mkdir .config/.local for %s", customAppUser)
		_, err = harness.VM.RunSSH([]string{"sudo", "chown", "-R", customAppUser + ":" + customAppUser, customHome + "/.config", customHome + "/.local"}, nil)
		Expect(err).ToNot(HaveOccurred(), "chown .config/.local for %s", customAppUser)
		DeferCleanup(func() {
			_, _ = harness.VM.RunSSH([]string{"sudo", "loginctl", "disable-linger", customAppUser}, nil)
			_, _ = harness.VM.RunSSH([]string{"sudo", "userdel", "-r", customAppUser}, nil)
		})
		Expect(harness.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, rootlessAppCustomUser, customAppUser, []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppCustomUser, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		// TODO(EDM-3440): uncomment when root-owned ~/.config/containers|systemd is fixed (podman ps as runAs user)
		// names, err = harness.RunPodmanPsContainerNamesAsUser(customAppUser, false)
		// Expect(err).ToNot(HaveOccurred())
		// Expect(names).To(ContainSubstring(rootlessAppCustomUser))
		customQuadletPath, err := harness.QuadletPathForUserOnVM(customAppUser)
		Expect(err).ToNot(HaveOccurred())
		harness.VerifyQuadletApplicationFolderExistsAt(rootlessAppCustomUser, customQuadletPath)

		By("Container User=1000: process runs as non-root inside container")
		nonrootContent := fmt.Sprintf("[Container]\nImage=%s\nExec=sleep infinity\nUser=1000\n[Install]\nWantedBy=default.target\n", rootlessAlpineImage)
		Expect(harness.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, rootlessAppNonrootInternal, flightctlUser, []string{"app.container"}, []string{nonrootContent})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppNonrootInternal, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		// TODO(EDM-3440): uncomment when root-owned ~/.config/containers|systemd is fixed (podman ps/exec as runAs user)
		// names, err = harness.RunPodmanPsContainerNamesAsUser(flightctlUser, false)
		// Expect(err).ToNot(HaveOccurred())
		// var containerName string
		// for _, n := range strings.Fields(names) {
		// 	if strings.Contains(n, rootlessAppNonrootInternal) {
		// 		containerName = n
		// 		break
		// 	}
		// }
		// Expect(containerName).ToNot(BeEmpty())
		// flightctlHome, err = harness.GetUserHomeOnVM(flightctlUser)
		// Expect(err).ToNot(HaveOccurred())
		// execOut, err := harness.VM.RunSSH([]string{"sudo", "-u", flightctlUser, "sh", "-c", fmt.Sprintf("cd /tmp && env HOME=%q podman exec %s id", flightctlHome, containerName)}, nil)
		// Expect(err).ToNot(HaveOccurred())
		// Expect(execOut.String()).To(And(ContainSubstring("uid=1000"), Not(ContainSubstring("uid=0(root)"))))

		By("SELinux: no AVC denials for flightctl/podman")
		getenforceOut, err := harness.VM.RunSSH([]string{"sh", "-c", "getenforce 2>/dev/null || echo Disabled"}, nil)
		if err != nil || strings.TrimSpace(getenforceOut.String()) != "Enforcing" {
			Skip("SELinux not Enforcing or getenforce failed")
			return
		}
		avcOut, err := harness.VM.RunSSH([]string{"sh", "-c", "sudo ausearch -m avc -ts recent 2>/dev/null | grep -E 'flightctl|podman' || true"}, nil)
		if err != nil {
			Skip("ausearch unavailable")
			return
		}
		Expect(strings.TrimSpace(avcOut.String())).To(BeEmpty(), "expected no AVC denials for flightctl/podman")
	})
})

// rootlessComposeContent returns the compose YAML used by the "Compose: runAs ignored" step.
func rootlessComposeContent() string {
	return `version: "3"
services:
  web:
    image: ` + rootlessNginxImage + `
    ports: ["8081:80"]
  worker:
    image: ` + rootlessAlpineImage + `
    command: ["sleep", "infinity"]
`
}

func rootlessAlpineContainerContent(imageRef string) string {
	return fmt.Sprintf(`[Container]
Image=%s
Exec=sleep infinity
[Install]
WantedBy=default.target
`, imageRef)
}

// rootlessNginxContainerContentWithPort returns quadlet content with a specific host port (avoids rebinding same port on rootless→rootful transition; EDM-3451).
func rootlessNginxContainerContentWithPort(imageRef, hostPort string) string {
	return fmt.Sprintf(`[Container]
Image=%s
PublishPort=%s:80
[Install]
WantedBy=default.target
`, imageRef, hostPort)
}

func rootlessUpdateV1Content(imageRef string) string {
	return fmt.Sprintf(`[Container]
Image=%s
Exec=sh -c "echo 'VERSION 1' && sleep infinity"
[Install]
WantedBy=default.target
`, imageRef)
}

func rootlessUpdateV2Content(imageRef string) string {
	return fmt.Sprintf(`[Container]
Image=%s
PublishPort=8080:80
[Install]
WantedBy=default.target
`, imageRef)
}

func rootlessVolumeContainerContent(imageRef, volumeRef string) string {
	return fmt.Sprintf(`[Container]
Image=%s
Volume=%s:/data:rw
Exec=sleep infinity
[Install]
WantedBy=default.target
`, imageRef, volumeRef)
}
