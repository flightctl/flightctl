package microshift

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Microshift cluster ACM enrollment tests", func() {

	Describe("Test Setup", Ordered, func() {

		BeforeAll(func() {
			_, isAcmRunning, err := util.IsAcmInstalled()
			if err != nil {
				GinkgoWriter.Printf("An error happened %v\n", err)
			}
			if !isAcmRunning {
				Skip("Skipping test suite because ACM is not installed.")
			}
		})

		var (
			deviceId string
		)

		BeforeEach(func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()
			deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
		})

		Context("microshift", func() {
			It(`Verifies that a microshift cluster can enroll to acm`, Label("75967"), func() {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				By("Getting the host")
				out, err := exec.Command("bash", "-c", "oc get route -n multicluster-engine agent-registration -o=jsonpath=\"{.spec.host}\"").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				agentRegistrationHost := string(out)
				GinkgoWriter.Printf("This is the agent registration host: %s\n", agentRegistrationHost)

				By("Getting the ca_cert")
				out, err = exec.Command("bash", "-c", "oc get configmap -n kube-system kube-root-ca.crt -o=jsonpath=\"{.data['ca\\.crt']}\" | base64 -w0").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				caCrt := string(out)
				GinkgoWriter.Printf("This is the caCrt: %s\n", caCrt)

				By("Applying resources")
				_, err = exec.Command("bash", "-c", "oc apply -f $(pwd)/data/content.yaml").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				GinkgoWriter.Printf("Enrollment resources were applied\n")

				By("Get the token")
				out, err = exec.Command("bash", "-c", "oc get secret -n multicluster-engine managed-cluster-import-agent-registration-sa-token -o=jsonpath='{.data.token}' | base64 -d").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				token := string(out)
				GinkgoWriter.Printf("This is the token: %s\n", token)

				By("Create the acm-registration repository")
				httpRepoUrl := fmt.Sprintf("https://%s", agentRegistrationHost)
				httpRepoConfig := v1beta1.HttpConfig{
					CaCrt: &caCrt,
					Token: &token,
				}
				httpRepoSpec := v1beta1.HttpRepoSpec{
					HttpConfig: httpRepoConfig,

					Type: v1beta1.Http,

					Url: httpRepoUrl,

					ValidationSuffix: &validationSuffix,
				}

				err = httpRepositoryspec.FromHttpRepoSpec(httpRepoSpec)
				Expect(err).ToNot(HaveOccurred())

				err = harness.CreateRepository(httpRepositoryspec, httpRepoMetadata)
				Expect(err).ToNot(HaveOccurred())
				GinkgoWriter.Printf("Created git repository %s\n", *httpRepoMetadata.Name)

				By("Get the Pull-Secret")
				out, err = exec.Command("bash", "-c", "oc get secret/pull-secret -n openshift-config -o json | jq '.data.\".dockerconfigjson\"' | base64 -di").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				pullSecret := string(out)
				GinkgoWriter.Printf("This is the pull-secret %s\n", pullSecret)

				By("Upgrade to the microshift image, and add the pull-secret to the device")
				nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())
				deviceImage := util.NewDeviceImageReference(util.DeviceTags.V7).String()
				var osImageSpec = v1beta1.DeviceOsSpec{
					Image: deviceImage,
				}

				var inlineConfigSpec = v1beta1.FileSpec{
					Path:    inlinePath,
					Content: pullSecret,
				}

				var pullSecretInlineConfig = v1beta1.InlineConfigProviderSpec{
					Inline: []v1beta1.FileSpec{inlineConfigSpec},
					Name:   "pull-secret",
				}

				inlineConfigProviderSpec := v1beta1.ConfigProviderSpec{}
				err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(pullSecretInlineConfig)
				Expect(err).ToNot(HaveOccurred())

				deviceSpecConfig := []v1beta1.ConfigProviderSpec{inlineConfigProviderSpec}

				var deviceSpec v1beta1.DeviceSpec

				deviceSpec.Os = &osImageSpec
				deviceSpec.Config = &deviceSpecConfig

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					device.Spec = &deviceSpec
					GinkgoWriter.Printf("Updating %s with a new image and pull-secret configuration\n", deviceId)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to get the fleet configuration")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Make sure the pull-secret was injected")
				psPath := "/etc/crio/openshift-pull-secret"
				readyMsg := "The file was found"
				stdout, err := harness.WaitForFileInDevice(psPath, util.TIMEOUT_5M, util.SHORT_POLLING)
				Expect(err).ToNot(HaveOccurred())
				Expect(stdout.String()).To(ContainSubstring(readyMsg))

				By("Wait for the kubeconfig to be in place")
				kubeconfigPath := "/var/lib/microshift/resources/kubeadmin/kubeconfig"
				stdout, err = harness.WaitForFileInDevice(kubeconfigPath, util.TIMEOUT_5M, util.SHORT_POLLING)
				Expect(err).ToNot(HaveOccurred())
				Expect(stdout.String()).To(ContainSubstring(readyMsg))

				By("Wait for microshift cluster pods to become ready")
				err = waitForMicroshiftReady(harness, kubeconfigPath)
				Expect(err).ToNot(HaveOccurred())

				By("Create the acm-registration fleet")
				testFleetDeviceSpec, err := createAcmRegistrationFleetDeviceSpec(harness, inlineConfigProviderSpec)
				Expect(err).ToNot(HaveOccurred())

				err = harness.CreateOrUpdateTestFleet(testFleetName, testFleetSelector, testFleetDeviceSpec)
				Expect(err).ToNot(HaveOccurred())
				GinkgoWriter.Printf("The fleet %s is created\n", testFleetName)

				By("Patch the mch of the hub cluster with annotation: mch-imageRepository=brew.registry.redhat.io")
				acmNamespace, err := getAcmNamespace()
				Expect(err).ToNot(HaveOccurred())

				args := fmt.Sprintf("oc annotate mch multiclusterhub -n %s mch-imageRepository='quay.io:443/acm-d'", acmNamespace)

				_, err = exec.Command("bash", "-c", args).CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				GinkgoWriter.Printf("The multiclusterhub is patched\n")

				By("Enable the auto-approver")
				_, err = exec.Command("bash", "-c", "oc patch clustermanager cluster-manager --type=merge -p '{\"spec\":{\"registrationConfiguration\":{\"featureGates\":[{\"feature\": \"ManagedClusterAutoApproval\", \"mode\": \"Enable\"}], \"autoApproveUsers\":[\"system:serviceaccount:multicluster-engine:agent-registration-bootstrap\"]}}}'").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				GinkgoWriter.Printf("The auto-approver is enabled\n")

				By("Check that the device status is Online")
				_, err = harness.CheckDeviceStatus(deviceId, v1beta1.DeviceSummaryStatusOnline)
				Expect(err).ToNot(HaveOccurred())

				By("Add the fleet selector and the team label to the device")
				nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						fleetSelectorKey: fleetSelectorValue,
					})
					GinkgoWriter.Printf("Updating %s with label %s=%s\n", deviceId,
						fleetSelectorKey, fleetSelectorValue)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to get the fleet configuration")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the klusterlet and registration pods to be ready")
				cmd := []string{
					"sudo", "oc", "wait",
					"--for=condition=Ready", "pods",
					"--all", "-A", "--timeout=300s",
					fmt.Sprintf("--kubeconfig=%s", kubeconfigPath),
				}
				_, err = harness.VM.RunSSH(cmd, nil)
				Expect(err).ToNot(HaveOccurred())

				By("Check that the cluster is registered in acm")
				err = harness.WaitForClusterRegistered(deviceId, util.DURATION_TIMEOUT)
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})
})

var (
	testFleetName          = "fleet-acm"
	fleetSelectorKey       = util.Fleet
	fleetSelectorValue     = "acm"
	httpRepoName           = "acm-registration"
	validationSuffix       = "/agent-registration/crds/v1"
	inlineConfigNameSecond = "apply-acm-manifests"
	inlinePath             = "/etc/crio/openshift-pull-secret"
	inlinePathSecond       = "/etc/flightctl/hooks.d/afterupdating/50-acm-registration.yaml"
	httpSuffix             = "/agent-registration/crds/v1"
	httpSuffixSecond       = "/agent-registration/manifests/{{.metadata.name}}"
	httpConfigPath         = "/var/local/acm-import/crd.yaml"
	httpConfigPathSecond   = "/var/local/acm-import/import.yaml"
	httpConfigName         = "acm-crd"
	httpConfigNameSecond   = "acm-import"
)

var httpConfigvalid = v1beta1.HttpConfigProviderSpec{
	HttpRef: struct {
		FilePath   string  `json:"filePath"`
		Repository string  `json:"repository"`
		Suffix     *string `json:"suffix,omitempty"`
	}{
		FilePath:   httpConfigPath,
		Repository: httpRepoName,
		Suffix:     &httpSuffix,
	},
	Name: httpConfigName,
}
var httpConfigvalidSecond = v1beta1.HttpConfigProviderSpec{
	HttpRef: struct {
		FilePath   string  `json:"filePath"`
		Repository string  `json:"repository"`
		Suffix     *string `json:"suffix,omitempty"`
	}{
		FilePath:   httpConfigPathSecond,
		Repository: httpRepoName,
		Suffix:     &httpSuffixSecond,
	},
	Name: httpConfigNameSecond,
}

var mode = 0644
var modePointer = &mode

var testFleetSelector = v1beta1.LabelSelector{
	MatchLabels: &map[string]string{fleetSelectorKey: fleetSelectorValue},
}

var httpRepositoryspec v1beta1.RepositorySpec

var httpRepoMetadata = v1beta1.ObjectMeta{
	Name: &httpRepoName,
}

func createAcmRegistrationFleetDeviceSpec(harness *e2e.Harness, pullSecretinlineConfigProviderSpec v1beta1.ConfigProviderSpec) (v1beta1.DeviceSpec, error) {
	hookFile := fmt.Sprintf("%s/test/e2e/microshift_acm_enrollment/data/acm-hook.yaml", util.GetTopLevelDir())
	inlineContentSecondByte, err := os.ReadFile(hookFile)
	if err != nil {
		return v1beta1.DeviceSpec{}, fmt.Errorf("failed to read hook file: %w", err)
	}
	inlineContentSecond := string(inlineContentSecondByte)

	inlineConfigSecondSpec := v1beta1.FileSpec{
		Path:    inlinePathSecond,
		Mode:    modePointer,
		Content: inlineContentSecond,
	}

	inlineConfigValidSecond := v1beta1.InlineConfigProviderSpec{
		Inline: []v1beta1.FileSpec{inlineConfigSecondSpec},
		Name:   inlineConfigNameSecond,
	}

	httpConfigProviderSpec := v1beta1.ConfigProviderSpec{}
	err = httpConfigProviderSpec.FromHttpConfigProviderSpec(httpConfigvalid)
	if err != nil {
		return v1beta1.DeviceSpec{}, fmt.Errorf("failed to read httpConfigProviderSpec: %w", err)
	}

	httpConfigProviderSpecSecond := v1beta1.ConfigProviderSpec{}
	err = httpConfigProviderSpecSecond.FromHttpConfigProviderSpec(httpConfigvalidSecond)
	if err != nil {
		return v1beta1.DeviceSpec{}, fmt.Errorf("failed to read second httpConfigProviderSpec: %w", err)
	}

	inlineConfigProviderSpecSecond := v1beta1.ConfigProviderSpec{}
	err = inlineConfigProviderSpecSecond.FromInlineConfigProviderSpec(inlineConfigValidSecond)
	if err != nil {
		return v1beta1.DeviceSpec{}, fmt.Errorf("failed to read inlineConfigProviderSpec: %w", err)
	}

	return harness.CreateFleetDeviceSpec("v7", pullSecretinlineConfigProviderSpec, inlineConfigProviderSpecSecond, httpConfigProviderSpec, httpConfigProviderSpecSecond)
}

func getAcmNamespace() (string, error) {
	cmd := exec.Command("bash", "-c", "oc get mch -A -ojson | jq '.items[0].metadata.namespace'")
	output, err := cmd.CombinedOutput()

	if err != nil {
		return "", fmt.Errorf("failed to get acm namespac %w", err)
	}
	outputClean := string(output)
	outputClean = strings.ReplaceAll(outputClean, "\\\"", "")
	outputClean = strings.ReplaceAll(outputClean, "\n", "")
	GinkgoWriter.Printf("This is the Acm namespace: %s\n", outputClean)

	return outputClean, nil
}

func waitForMicroshiftReady(harness *e2e.Harness, kubeconfigPath string) error {
	timeout := util.DURATION_TIMEOUT
	interval := 10 * time.Second
	start := time.Now()
	var err error

	for time.Since(start) < timeout {
		cmd := []string{
			"sudo", "oc", "wait",
			"--for=condition=Ready", "pods",
			"--all", "-A", "--timeout=300s",
			fmt.Sprintf("--kubeconfig=%s", kubeconfigPath),
		}

		_, err = harness.VM.RunSSH(cmd, nil)
		if err == nil {
			break // success
		}
		time.Sleep(interval)
	}

	return err
}
