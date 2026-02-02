// Package quadlets_test implements e2e tests for Quadlets application type support
// (Edge Management - application Test Plan, section 4.2).
//
// These tests require a RHEL device with FlightCtl
// deployed via quadlets (e.g. make deploy-quadlets-vm). Standard e2e VMs are
// not quadlet-capable. Polarion IDs for section 4.2 are TBD by QE; OCP-86280
// is used for the inline complex app + reboot scenario.
package quadlets_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// Time constants for reboot wait (aligned with harness VM SSH wait behavior).
	rebootWaitInterval = 15 * time.Second
	rebootMaxAttempts  = 5

	// Quadlet app names used in tests.
	quadletAppName              = "my-app"
	quadletAppNameSecond        = "my-app-2"
	quadletAppNameComplex       = "my-app-4"
	quadletAppNameOCI           = "app-multi-file-artifact-with-image-ref"
	quadletAppNameComplexInline = "complex-quadlet-inline-app"
	quadletAppNameValidation    = "validation-test"
	quadletAppNameCrash         = "crash-app"

	quadletLabelProject   = "io.flightctl.quadlet.project"
	quadletSystemdPath    = "/etc/containers/systemd"
	quadletDropInFilename = "99-flightctl.conf"

	expectedStatusBadRequest = 400
)

var _ = Describe("Quadlets application type support", Label("quadlets"), func() {
	var harness *e2e.Harness
	var deviceID string

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		login.LoginToAPIWithToken(harness)
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	Context("Quadlet application lifecycle", func() {
		// Test plan 4.2: flightctl quadlets application lifecycle
		// Verifies deploy, update (with Volume/Network/Pod), and remove on an edge manager device.
		It("verifies that a quadlets application can be deployed, updated and removed in an edge manager device", Label("86076", "sanity"), func() {
			By("Adding inline quadlet app (Container only) to device")
			registryImage := getQuadletTestImage(harness)
			containerContent := getLifecycleInitialContainerContent(registryImage)
			Expect(harness.UpdateDeviceWithQuadletInline(deviceID, quadletAppName, []string{"test.container"}, []string{containerContent})).ToNot(HaveOccurred())

			By("Checking application status shows Running")
			Expect(harness.WaitForApplicationStatus(deviceID, quadletAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())

			By("Checking quadlet files and labels on device")
			names, err := harness.RunPodmanPsContainerNames(false)
			Expect(err).ToNot(HaveOccurred())
			Expect(names).To(ContainSubstring(quadletAppName))
			// quadletDropInFilename is created by the FlightCtl agent's quadlet installer (createQuadletDropIn in
			// internal/agent/device/applications/provider/quadlet.go) under {appName}-.{type}.d/ when the app is applied.
			confPath := fmt.Sprintf("%s/%s", quadletSystemdPath, quadletAppName)
			_, err = harness.VM.RunSSH([]string{"sh", "-c", fmt.Sprintf("find %s -name %s -print | grep -q .", confPath, quadletDropInFilename)}, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Updating app with Volume, Network, Pod")
			volumeContent, networkContent, podContent, containerContentV2 := getQuadletVolumeNetworkPodUpdateContents(registryImage)
			Expect(harness.UpdateDeviceWithQuadletInline(deviceID, quadletAppName,
				[]string{"test.container", "test.volume", "test.network", "test.pod"},
				[]string{containerContentV2, volumeContent, networkContent, podContent})).ToNot(HaveOccurred())

			By("Checking status shows ready 2/2 and Running")
			Expect(harness.WaitForApplicationStatus(deviceID, quadletAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())

			By("Checking both containers are running")
			names, err = harness.RunPodmanPsContainerNames(false)
			Expect(err).ToNot(HaveOccurred())
			allNames := strings.Fields(strings.TrimSpace(names))
			quadletContainers := make([]string, 0, len(allNames))
			for _, n := range allNames {
				if strings.Contains(n, quadletAppName) {
					quadletContainers = append(quadletContainers, n)
				}
			}
			Expect(quadletContainers).To(HaveLen(2), "expected 2 running containers for app %q, got: %v", quadletAppName, quadletContainers)

			By("Removing application from device config")
			emptyApps := []v1beta1.ApplicationProviderSpec{}
			Expect(harness.SetDeviceApplications(deviceID, &emptyApps)).ToNot(HaveOccurred())

			By("Verifying resources are removed")
			Eventually(func() bool {
				names, err := harness.RunPodmanPsContainerNames(true)
				if err != nil {
					return false
				}
				return !strings.Contains(names, quadletAppName)
			}, testutil.TIMEOUT, testutil.POLLING).Should(BeTrue(), "quadlet container %q should disappear from podman ps -a", quadletAppName)
		})

		// Test plan 4.2: A complex application can be deployed to flightctl
		It("verifies that a complex application including networks, volumes, pods, images, containers and envs can be deployed to an edge manager device", Label("86279", "sanity"), func() {
			By("Adding complex quadlet app with envVars, Container, Volume, Network, Pod")
			imageRef := getQuadletTestImage(harness)
			envVars, paths, contents := getComplexQuadletAppWithEnvsContents(imageRef)
			Expect(harness.UpdateDeviceWithQuadletInlineAndEnvs(deviceID, quadletAppNameComplex, envVars, paths, contents)).ToNot(HaveOccurred())

			By("Checking application folder, labels and .env on device")
			appPath := quadletSystemdPath + "/" + quadletAppNameComplex
			out, err := harness.VM.RunSSH([]string{"cat", appPath + "/.env"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(out.String()).To(ContainSubstring("FOO=FOOO"))
			Expect(out.String()).To(ContainSubstring("SIMPLE=VALUE"))

			By("Checking containers are running")
			names, err := harness.RunPodmanPsContainerNames(false)
			Expect(err).ToNot(HaveOccurred())
			Expect(names).To(ContainSubstring(quadletAppNameComplex))

			By("Removing configuration")
			emptyApps := []v1beta1.ApplicationProviderSpec{}
			Expect(harness.SetDeviceApplications(deviceID, &emptyApps)).ToNot(HaveOccurred())
		})
	})

	Context("Image provider and OCI artifacts", func() {
		// Test plan 4.2: Image provider can extract and deploy Quadlet files from OCI artifacts
		It("verifies that we can create single or multiple files artifacts (also compressed) packaged in an image and install them in an EM device", Label("86280", "sanity"), func() {
			By("Adding quadlet app with image ref (multi-file artifact)")
			appSpec, err := harness.QuadletImageAppSpec(quadletAppNameOCI,
				"quay.io/flightctl-tests/quadlet-test/quadlet-app-artifact:with-image-ref",
				map[string]string{"LOG_MESSAGE": "Multi-file artifact (with .image ref)"})
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{appSpec})).ToNot(HaveOccurred())

			By("Checking container status moves to Running and application folder is created")
			Expect(harness.WaitForApplicationStatus(deviceID, quadletAppNameOCI, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())

			By("Removing spec")
			emptyApps := []v1beta1.ApplicationProviderSpec{}
			Expect(harness.SetDeviceApplications(deviceID, &emptyApps)).ToNot(HaveOccurred())

			By("Verifying applications were removed")
			Eventually(func() bool {
				names, err := harness.RunPodmanPsContainerNames(true)
				if err != nil {
					return false
				}
				return !strings.Contains(names, quadletAppNameOCI)
			}, testutil.TIMEOUT, testutil.POLLING).Should(BeTrue(), "quadlet container %q should disappear from podman ps -a", quadletAppNameOCI)
		})
	})

	Context("Inline quadlets with references and reboot", func() {
		// Test plan 4.2: Inline quadlets complex application with references ... survives a reboot (OCP-86280).
		It("inline quadlets complex application with references can be deployed to an EM device and survives a reboot", Label("86281", "sanity"), func() {
			By("Adding inline quadlet app with refs (network, pod, container, volumes, worker-image.image)")
			imageRef := getQuadletTestImage(harness)
			envVars := map[string]string{"LOG_MESSAGE": "Hello from FlightControl (Inline Ref)"}
			paths := []string{"app.network", "app.pod", "app.container", "data.volume", "sqlite.volume", "worker-image.image", "worker.container"}
			contents := getComplexQuadletInlineContents(imageRef)
			Expect(harness.UpdateDeviceWithQuadletInlineAndEnvs(deviceID, quadletAppNameComplexInline, envVars, paths, contents)).ToNot(HaveOccurred())

			By("Checking application status and containers")
			Expect(harness.WaitForApplicationStatus(deviceID, quadletAppNameComplexInline, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())

			By("Rebooting VM")
			Expect(harness.RebootVMAndWaitForSSH(rebootWaitInterval, rebootMaxAttempts)).ToNot(HaveOccurred())

			By("Waiting for device to report application Running after reboot")
			Expect(harness.WaitForApplicationStatus(deviceID, quadletAppNameComplexInline, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())

			By("Removing config - no leftovers")
			emptyApps := []v1beta1.ApplicationProviderSpec{}
			Expect(harness.SetDeviceApplications(deviceID, &emptyApps)).ToNot(HaveOccurred())
		})
	})

	Context("Validations in quadlets applications", func() {
		// Test plan 4.2: Validations in quadlets applications
		It("verifies that there are validations and readable error messages in quadlets application files", Label("86352", "sanity"), func() {
			By("Adding quadlet app with two volume files with the same VolumeName - update is rejected with 400")
			apps, err := invalidQuadletSpecDuplicateVolumeName()
			Expect(err).ToNot(HaveOccurred())
			getResp, err := harness.Client.GetDeviceWithResponse(harness.Context, deviceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(getResp.JSON200).ToNot(BeNil())
			device := *getResp.JSON200
			device.Spec.Applications = &apps

			replaceResp, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceID, device)
			Expect(err).ToNot(HaveOccurred())
			Expect(replaceResp.StatusCode()).To(Equal(expectedStatusBadRequest), "duplicate quadlet volume names should be rejected with 400")
			Expect(string(replaceResp.Body)).ToNot(BeEmpty(), "error response should include a message")
		})
	})

	Context("Quadlets app with crashed containers", func() {
		// Test plan 4.2: A quadlets app with crashed containers reports Degraded status
		It("verifies that a crashing quadlets app is reported as Degraded", Label("86353", "sanity"), func() {
			By("Adding quadlet app with exit 1 container command")
			imageRef := getQuadletTestImage(harness)
			containerContent := getCrashAppContainerContent(imageRef)
			Expect(harness.UpdateDeviceWithQuadletInline(deviceID, quadletAppNameCrash, []string{"test.container"}, []string{containerContent})).ToNot(HaveOccurred())

			By("Checking application general status is Degraded (or Error)")
			Expect(harness.WaitForApplicationSummaryDegradedOrError(deviceID, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
		})
	})
})

// Helpers (local to this package, no assertions inside)

func getLifecycleInitialContainerContent(registryImage string) string {
	return fmt.Sprintf(`[Container]
Image=%s
Exec=sleep infinity
[Install]
WantedBy=default.target
`, registryImage)
}

func getQuadletVolumeNetworkPodUpdateContents(registryImage string) (volumeContent, networkContent, podContent, containerContent string) {
	volumeContent = `[Volume]
Driver=image
Image=ghcr.io/homebrew/core/sqlite:3.50.2
`
	networkContent = `[Network]
`
	podContent = `[Pod]
`
	containerContent = fmt.Sprintf(`[Container]
Image=%s
Volume=test.volume:/mnt/volume
Volume=name:/mnt/named-volume
Exec=sh -c "echo 'Volume mounted.' && sleep infinity"
[Install]
WantedBy=default.target
`, registryImage)
	return volumeContent, networkContent, podContent, containerContent
}

func getComplexQuadletInlineContents(imageRef string) []string {
	return []string{
		"[Network]\nDriver=bridge\n",
		"[Pod]\nNetwork=app.network\n",
		fmt.Sprintf(`[Container]
Image=%s
Pod=app.pod
Volume=model-data:/mnt/model:ro
Exec=sh -c "echo 'Primary container started.' && sleep infinity"
[Install]
WantedBy=default.target
`, imageRef),
		"[Volume]\nDriver=local\n",
		"[Volume]\nDriver=image\nImage=ghcr.io/homebrew/core/sqlite:3.50.2\n",
		fmt.Sprintf("[Image]\nImage=%s\n", imageRef),
		`[Container]
Image=worker-image.image
Pod=app.pod
Volume=data.volume:/mnt/data
Exec=sh -c "echo 'Worker started.' && sleep infinity"
[Install]
WantedBy=default.target
`,
	}
}

func getComplexQuadletAppWithEnvsContents(imageRef string) (envVars map[string]string, paths, contents []string) {
	envVars = map[string]string{"FOO": "FOOO", "SIMPLE": "VALUE", "SOME_KEY": "default"}
	paths = []string{"test.container", "test.volume", "test-name.volume", "test.network", "test.pod"}
	contents = []string{
		fmt.Sprintf(`[Container]
Image=%s
Network=test.network
Pod=test.pod
Volume=test.volume:/mnt/volume
Volume=test-name.volume:/mnt/data
Exec=sh -c "echo FOO: $FOO && sleep infinity"
[Install]
WantedBy=default.target
`, imageRef),
		`[Volume]
Driver=image
Image=ghcr.io/homebrew/core/sqlite:3.50.2
`,
		`[Volume]
VolumeName=testdata
Driver=local
`,
		`[Network]
Driver=bridge
DNS=1.1.1.1
`,
		`[Pod]
Network=test.network
`,
	}
	return envVars, paths, contents
}

func invalidQuadletSpecDuplicateVolumeName() ([]v1beta1.ApplicationProviderSpec, error) {
	dupVolumeContent := `[Volume]
VolumeName=testdata
Driver=local
`
	inline := []v1beta1.ApplicationContent{
		{Path: "test2.volume", Content: &dupVolumeContent},
		{Path: "test-name.volume", Content: &dupVolumeContent},
	}
	appName := quadletAppNameValidation
	quadletApp := v1beta1.QuadletApplication{
		Name:    &appName,
		AppType: v1beta1.AppTypeQuadlet,
	}
	if err := quadletApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{Inline: inline}); err != nil {
		return nil, err
	}
	var appSpec v1beta1.ApplicationProviderSpec
	if err := appSpec.FromQuadletApplication(quadletApp); err != nil {
		return nil, err
	}
	return []v1beta1.ApplicationProviderSpec{appSpec}, nil
}

func getCrashAppContainerContent(imageRef string) string {
	return fmt.Sprintf(`[Container]
Image=%s
Exec=sh -c "echo FOO && exit 1"
[Install]
WantedBy=default.target
`, imageRef)
}

// getQuadletTestImage returns a plain container image for quadlet tests (not a compose/sleep-app bundle).
// Uses a minimal runtime image (not an OS image); quadlet tests use inline quadlet content and
// only need a simple container image for Image=.
func getQuadletTestImage(harness *e2e.Harness) string {
	return "quay.io/flightctl-tests/alpine:v1"
}
