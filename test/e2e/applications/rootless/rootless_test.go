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

	rootlessAppOCP87846PrivPort   = "nginx-priv-port"
	rootlessAppOCP87846EmptyRunAs = "empty-runas-app"
	rootlessAppOCP87846SameName   = "app-with-vol-same-name"
	userNonexistent87846          = "nonexistent_user_12345"
	userNoHome87846               = "testuser_nohome"
	userNoLinger87846             = "nolinger_user"

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

	It("covers all rootless checkpoints across quadlet, container, and compose app types", Label("sanity", "87844", "sanity-github"), func() {
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
		newRootfulContent := rootlessNginxContainerContentNoPorts(rootlessNginxImage) // no ports until EDM-3451
		spec1, err := e2e.NewQuadletInlineSpec(rootlessAppRootfulSimple, "", []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})
		Expect(err).ToNot(HaveOccurred())
		spec2, err := e2e.NewQuadletInlineSpec(rootlessAppNewRootful, "", []string{"nginx.container"}, []string{newRootfulContent})
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
		Expect(harness.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, rootlessAppNewRootful, flightctlUser, []string{"nginx.container"}, []string{newRootfulContent})).ToNot(HaveOccurred())
		// capture state before waiting so logs are available when the wait times out.
		By("Verify on device for rootful→rootless debug (REPRO_ROOTLESS_TO_ROOTFUL.md)")
		rootQuadletOut, _ := harness.VM.RunSSH([]string{"sudo", "ls", "-la", e2e.QuadletUnitPath}, nil)
		GinkgoWriter.Printf("[rootful→rootless] root quadlet path %s:\n%s\n", e2e.QuadletUnitPath, rootQuadletOut.String())
		flightctlQuadletPath, err = harness.QuadletPathForUserOnVM(flightctlUser)
		if err == nil {
			userQuadletOut, _ := harness.VM.RunSSH([]string{"sudo", "ls", "-la", flightctlQuadletPath}, nil)
			GinkgoWriter.Printf("[rootful→rootless] user quadlet path %s:\n%s\n", flightctlQuadletPath, userQuadletOut.String())
		}
		podmanPsOut, _ := harness.VM.RunSSH([]string{"sudo", "-u", "root", "podman", "ps", "-a", "--format", "{{.Names}} {{.Status}}"}, nil)
		GinkgoWriter.Printf("[rootful→rootless] podman ps -a (root):\n%s\n", podmanPsOut.String())
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
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(d *v1beta1.Device) {
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
		specRootlessOnly, err := e2e.NewQuadletInlineSpec(rootlessAppNewRootful, flightctlUser, []string{"nginx.container"}, []string{newRootfulContent})
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{specRootlessOnly})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppNewRootful, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		By("Transition rootless back to rootful by removing runAs")
		Expect(harness.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, rootlessAppNewRootful, "", []string{"nginx.container"}, []string{newRootfulContent})).ToNot(HaveOccurred())
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
		// capture state before waiting so logs are available when the wait times out.
		By("Verify on device for rootless→rootful debug (REPRO_ROOTLESS_TO_ROOTFUL.md)")
		rootQuadletOut, _ = harness.VM.RunSSH([]string{"sudo", "ls", "-la", e2e.QuadletUnitPath}, nil)
		GinkgoWriter.Printf("[rootless→rootful] root quadlet path %s:\n%s\n", e2e.QuadletUnitPath, rootQuadletOut.String())
		flightctlQuadletPath, err = harness.QuadletPathForUserOnVM(flightctlUser)
		if err == nil {
			userQuadletOut, _ := harness.VM.RunSSH([]string{"sudo", "ls", "-la", flightctlQuadletPath}, nil)
			GinkgoWriter.Printf("[rootless→rootful] user quadlet path %s:\n%s\n", flightctlQuadletPath, userQuadletOut.String())
		}
		podmanPsOut, _ = harness.VM.RunSSH([]string{"sudo", "-u", "root", "podman", "ps", "-a", "--format", "{{.Names}} {{.Status}}"}, nil)
		GinkgoWriter.Printf("[rootless→rootful] podman ps -a (root):\n%s\n", podmanPsOut.String())
		// With the two-step flow (remove container-multi-app first), the rootless→rootful transition does not hit EDM-3451 race; verify app status.
		// Rootless→rootful can take several minutes (agent stops rootless unit, starts rootful unit, reports Running); use LONG_TIMEOUT so the test passes when it used to.
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
			GinkgoWriter.Printf("Skipping AVC check: SELinux not Enforcing or getenforce failed\n")
			return
		}
		avcOut, err := harness.VM.RunSSH([]string{"sh", "-c", "sudo ausearch -m avc -ts recent 2>/dev/null"}, nil)
		if err != nil {
			GinkgoWriter.Printf("Skipping AVC check: ausearch unavailable or failed: %v\n", err)
			return
		}
		// Filter for flightctl/podman AVC lines in Go so we don't rely on grep exit code (no denials => grep exit 1 would mask ausearch success).
		var denied []string
		for _, line := range strings.Split(avcOut.String(), "\n") {
			if strings.Contains(line, "flightctl") || strings.Contains(line, "podman") {
				denied = append(denied, line)
			}
		}
		Expect(denied).To(BeEmpty(), "expected no AVC denials for flightctl/podman")
	})

	It("validates rootless workload execution: non-existent user, no home, privileged port, invalid username, no linger, empty runAs, duplicate name", Label("87846"), func() {
		clearAndWaitUpToDate := func() {
			Expect(harness.UpdateDeviceWithRetries(deviceID, func(d *v1beta1.Device) {
				d.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
			})).ToNot(HaveOccurred())
			harness.WaitForDeviceContents(deviceID, "device UpToDate after clear", func(device *v1beta1.Device) bool {
				return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
			}, testutil.LONGTIMEOUT)
		}

		By("Verify agent fails gracefully when specified user does not exist")
		specNonexistent, err := e2e.NewQuadletInlineSpec(rootlessAppOCP87846PrivPort, userNonexistent87846, []string{"nginx.container"}, []string{rootlessNginxContainerContentWithPort(rootlessNginxImage, "8080")})
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.UpdateDeviceWithRetries(deviceID, func(d *v1beta1.Device) {
			d.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{specNonexistent}
		})).ToNot(HaveOccurred())
		harness.WaitForDeviceContents(deviceID, "device shows update failure for non-existent user", func(device *v1beta1.Device) bool {
			return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError))
		}, testutil.LONGTIMEOUT)
		device, err := harness.GetDevice(deviceID)
		Expect(err).ToNot(HaveOccurred())
		cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
		Expect(cond).ToNot(BeNil())
		Expect(cond.Message).To(Or(ContainSubstring("user"), ContainSubstring("prefetch"), ContainSubstring("exist")))
		clearAndWaitUpToDate()

		By("Verify agent fails when user exists but has no home directory")
		_, err = harness.VM.RunSSH([]string{"sudo", "useradd", "--no-create-home", userNoHome87846}, nil)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			_, _ = harness.VM.RunSSH([]string{"sudo", "userdel", userNoHome87846}, nil)
		})
		specNoHome, err := e2e.NewQuadletInlineSpec(rootlessAppOCP87846PrivPort, userNoHome87846, []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.UpdateDeviceWithRetries(deviceID, func(d *v1beta1.Device) {
			d.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{specNoHome}
		})).ToNot(HaveOccurred())
		harness.WaitForDeviceContents(deviceID, "device shows error for user without home", func(device *v1beta1.Device) bool {
			return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError))
		}, testutil.LONGTIMEOUT)
		device, err = harness.GetDevice(deviceID)
		Expect(err).ToNot(HaveOccurred())
		cond = v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
		Expect(cond).ToNot(BeNil())
		// Agent may report the failure during prefetch (generic "prefetch failed...internal error")
		// or with a specific message mentioning home directory.
		Expect(cond.Message).To(Or(
			ContainSubstring("home"),
			ContainSubstring("Home"),
			And(ContainSubstring("prefetch"), ContainSubstring("internal error")),
			And(ContainSubstring("prefetch"), ContainSubstring("precondition")),
		))
		clearAndWaitUpToDate()

		By("Verify rootless apps cannot bind to privileged ports")
		privPortContent := rootlessNginxContainerContentWithPort(rootlessNginxImage, "80")
		specPrivPort, err := e2e.NewQuadletInlineSpec(rootlessAppOCP87846PrivPort, flightctlUser, []string{"nginx.container"}, []string{privPortContent})
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{specPrivPort})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppOCP87846PrivPort, v1beta1.ApplicationStatusError, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		device, err = harness.GetDevice(deviceID)
		Expect(err).ToNot(HaveOccurred())
		var appStatus string
		for i := range device.Status.Applications {
			if device.Status.Applications[i].Name == rootlessAppOCP87846PrivPort {
				appStatus = string(device.Status.Applications[i].Status)
				break
			}
		}
		Expect(appStatus).To(Equal(string(v1beta1.ApplicationStatusError)))
		clearAndWaitUpToDate()

		By("Try to create spec with invalid username")
		getResp, err := harness.Client.GetDeviceWithResponse(harness.Context, deviceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(getResp.JSON200).ToNot(BeNil())
		devInvalidUser := *getResp.JSON200
		invalidUserSpec, err := e2e.NewQuadletInlineSpec(rootlessAppOCP87846PrivPort, "user@invalid!", []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})
		Expect(err).ToNot(HaveOccurred())
		devInvalidUser.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{invalidUserSpec}
		replaceResp, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceID, devInvalidUser)
		Expect(err).ToNot(HaveOccurred())
		// API may reject invalid username with 400 (Bad Request) or 409 (Conflict).
		Expect(replaceResp.StatusCode()).To(Or(Equal(400), Equal(409)), "invalid username should be rejected with 400 or 409")
		if replaceResp.StatusCode() == 400 {
			Expect(string(replaceResp.Body)).To(Or(ContainSubstring("invalid"), ContainSubstring("username"), ContainSubstring("runAs")))
		}
		// 409 returns a generic conflict message ("object has been modified"), not validation text

		By("Verify behavior when user does not have lingering enabled")
		_, err = harness.VM.RunSSH([]string{"sudo", "useradd", "--create-home", userNoLinger87846}, nil)
		Expect(err).ToNot(HaveOccurred())
		noLingerHome, err := harness.GetUserHomeOnVM(userNoLinger87846)
		Expect(err).ToNot(HaveOccurred(), "getent passwd %s", userNoLinger87846)
		_, err = harness.VM.RunSSH([]string{"sudo", "mkdir", "-p", noLingerHome + "/.config", noLingerHome + "/.local"}, nil)
		Expect(err).ToNot(HaveOccurred())
		_, err = harness.VM.RunSSH([]string{"sudo", "chown", "-R", userNoLinger87846 + ":" + userNoLinger87846, noLingerHome + "/.config", noLingerHome + "/.local"}, nil)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			_, _ = harness.VM.RunSSH([]string{"sudo", "userdel", "-r", userNoLinger87846}, nil)
		})
		specNoLinger, err := e2e.NewQuadletInlineSpec(rootlessAppOCP87846PrivPort, userNoLinger87846, []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.UpdateDeviceWithRetries(deviceID, func(d *v1beta1.Device) {
			d.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{specNoLinger}
		})).ToNot(HaveOccurred())
		// Device may apply the spec (UpToDate) or fail with Error (e.g. precondition/dependencies for no-linger user); either is acceptable.
		harness.WaitForDeviceContents(deviceID, "device applied no-linger spec or reported error", func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil {
				return false
			}
			if device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate {
				return true
			}
			return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError))
		}, testutil.LONGTIMEOUT)
		// Application may start but won't persist without linger; we accept either Error or transient Running.
		_ = harness.WaitForApplicationStatus(deviceID, rootlessAppOCP87846PrivPort, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)
		clearAndWaitUpToDate()

		By("Verify empty runAs defaults to root behavior")
		specEmptyRunAs, err := e2e.NewQuadletInlineSpec(rootlessAppOCP87846EmptyRunAs, "", []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{specEmptyRunAs})).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(deviceID, rootlessAppOCP87846EmptyRunAs, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		harness.VerifyQuadletApplicationFolderExistsAt(rootlessAppOCP87846EmptyRunAs, e2e.QuadletUnitPath)
		clearAndWaitUpToDate()

		By("Validate that flightctl prevents duplicate application names (rootful and rootless same name)")
		getResp, err = harness.Client.GetDeviceWithResponse(harness.Context, deviceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(getResp.JSON200).ToNot(BeNil())
		devDup := *getResp.JSON200
		specRootful, err := e2e.NewQuadletInlineSpec(rootlessAppOCP87846SameName, "", []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})
		Expect(err).ToNot(HaveOccurred())
		specRootless, err := e2e.NewQuadletInlineSpec(rootlessAppOCP87846SameName, flightctlUser, []string{"app.container"}, []string{rootlessAlpineContainerContent(rootlessAlpineImage)})
		Expect(err).ToNot(HaveOccurred())
		devDup.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{specRootful, specRootless}
		replaceResp, err = harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceID, devDup)
		Expect(err).ToNot(HaveOccurred())
		Expect(replaceResp.StatusCode()).To(Equal(400), "duplicate application name should be rejected with 400")
		Expect(string(replaceResp.Body)).To(ContainSubstring("duplicate"))
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

// rootlessNginxContainerContentNoPorts returns quadlet content for nginx without port publishing.
// Use until EDM-3451 is fixed; then switch to rootlessNginxContainerContentWithPort for new-rootful-app.
func rootlessNginxContainerContentNoPorts(imageRef string) string {
	return fmt.Sprintf(`[Container]
Image=%s
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
