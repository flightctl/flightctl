package microshift

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("Microshift cluster ACM enrollment tests", func() {

	Describe("Test Setup", Ordered, func() {

		BeforeAll(func() {
			isAcmInstalled, err := isAcmInstalled()
			if err != nil {
				logrus.Warnf("An error happened %v", err)
			}
			if !isAcmInstalled {
				Skip("Skipping test suite because ACM is not installed.")
			}
		})

		var (
			deviceId string
		)

		BeforeEach(func() {
			deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
		})

		Context("microshift", func() {
			It(`Verifies that a microshift cluster can enroll to acm`, Label("75967"), func() {
				By("Getting the host")
				out, err := exec.Command("bash", "-c", "oc get route -n multicluster-engine agent-registration -o=jsonpath=\"{.spec.host}\"").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				agentRegistrationHost := string(out)
				logrus.Infof("This is the agent registration host: %s", agentRegistrationHost)

				By("Getting the ca_cert")
				out, err = exec.Command("bash", "-c", "oc get configmap -n kube-system kube-root-ca.crt -o=jsonpath=\"{.data['ca\\.crt']}\" | base64 -w0").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				caCrt := string(out)
				logrus.Infof("This is the caCrt: %s", caCrt)

				By("Applying resources")
				_, err = exec.Command("bash", "-c", "oc apply -f $(pwd)/data/content.yaml").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				logrus.Infof("Enrollment resources were applied")

				By("Get the token")
				out, err = exec.Command("bash", "-c", "oc get secret -n multicluster-engine managed-cluster-import-agent-registration-sa-token -o=jsonpath='{.data.token}' | base64 -d").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				token := string(out)
				logrus.Infof("This is the token: %s", token)

				By("Create the acm-registration repository")
				httpRepoUrl := fmt.Sprintf("https://%s", agentRegistrationHost)
				httpRepoConfig := v1alpha1.HttpConfig{
					CaCrt: &caCrt,
					Token: &token,
				}
				httpRepoSpec := v1alpha1.HttpRepoSpec{
					HttpConfig: httpRepoConfig,

					Type: v1alpha1.Http,

					Url: httpRepoUrl,

					ValidationSuffix: &validationSuffix,
				}

				err = httpRepositoryspec.FromHttpRepoSpec(httpRepoSpec)
				Expect(err).ToNot(HaveOccurred())

				err = harness.CreateRepository(httpRepositoryspec, httpRepoMetadata)
				Expect(err).ToNot(HaveOccurred())
				logrus.Infof("Created git repository %s", *httpRepoMetadata.Name)

				By("Get the Pull-Secret")
				out, err = exec.Command("bash", "-c", "oc get secret/pull-secret -n openshift-config -o json | jq '.data.\".dockerconfigjson\"' | base64 -di").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				pullSecret := string(out)
				logrus.Infof("This is the pull-secret %s", pullSecret)

				By("Upgrade to the microshift image, and add the pull-secret to the device")
				nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())
				deviceImage := fmt.Sprintf("%s/flightctl-device:v7", harness.RegistryEndpoint())
				var osImageSpec = v1alpha1.DeviceOsSpec{
					Image: deviceImage,
				}

				var inlineConfigSpec = v1alpha1.FileSpec{
					Path:    inlinePath,
					Content: pullSecret,
				}

				var pullSecretInlineConfig = v1alpha1.InlineConfigProviderSpec{
					Inline: []v1alpha1.FileSpec{inlineConfigSpec},
					Name:   "pull-secret",
				}

				inlineConfigProviderSpec := v1alpha1.ConfigProviderSpec{}
				err = inlineConfigProviderSpec.FromInlineConfigProviderSpec(pullSecretInlineConfig)
				Expect(err).ToNot(HaveOccurred())

				deviceSpecConfig := []v1alpha1.ConfigProviderSpec{inlineConfigProviderSpec}

				var deviceSpec v1alpha1.DeviceSpec

				deviceSpec.Os = &osImageSpec
				deviceSpec.Config = &deviceSpecConfig

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
					device.Spec = &deviceSpec
					logrus.Infof("Updating %s with a new image and pull-secret configuration", deviceId)
				})
				Expect(err).ToNot(HaveOccurred())

				By("Wait for the device to get the fleet configuration")
				err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())

				By("Make sure the pull-secret was injected")
				psPath := "/etc/crio/openshift-pull-secret"
				readyMsg := "The file was found"
				stdout, err := harness.WaitForFileInDevice(psPath, util.TIMEOUT, util.POLLING)
				Expect(err).ToNot(HaveOccurred())
				Expect(stdout.String()).To(ContainSubstring(readyMsg))

				By("Wait for the kubeconfig to be in place")
				kubeconfigPath := "/var/lib/microshift/resources/kubeadmin/kubeconfig"
				stdout, err = harness.WaitForFileInDevice(kubeconfigPath, util.TIMEOUT, util.POLLING)
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
				logrus.Infof("The fleet %s is created", testFleetName)

				By("Patch the mch of the hub cluster with annotation: mch-imageRepository=brew.registry.redhat.io")
				acmNamespace, err := getAcmNamespace()
				Expect(err).ToNot(HaveOccurred())

				args := fmt.Sprintf("oc annotate mch multiclusterhub -n %s mch-imageRepository='quay.io:443/acm-d'", acmNamespace)

				_, err = exec.Command("bash", "-c", args).CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				logrus.Info("The multiclusterhub is patched")

				By("Enable the auto-approver")
				_, err = exec.Command("bash", "-c", "oc patch clustermanager cluster-manager --type=merge -p '{\"spec\":{\"registrationConfiguration\":{\"featureGates\":[{\"feature\": \"ManagedClusterAutoApproval\", \"mode\": \"Enable\"}], \"autoApproveUsers\":[\"system:serviceaccount:multicluster-engine:agent-registration-bootstrap\"]}}}'").CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				logrus.Info("The auto-approver is enabled")

				By("Check that the device status is Online")
				_, err = harness.CheckDeviceStatus(deviceId, v1alpha1.DeviceSummaryStatusOnline)
				Expect(err).ToNot(HaveOccurred())

				By("Add the fleet selector and the team label to the device")
				nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).ToNot(HaveOccurred())

				err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
					harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
						fleetSelectorKey: fleetSelectorValue,
					})
					logrus.Infof("Updating %s with label %s=%s", deviceId,
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

var httpConfigvalid = v1alpha1.HttpConfigProviderSpec{
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
var httpConfigvalidSecond = v1alpha1.HttpConfigProviderSpec{
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

var testFleetSelector = v1alpha1.LabelSelector{
	MatchLabels: &map[string]string{fleetSelectorKey: fleetSelectorValue},
}

var httpRepositoryspec v1alpha1.RepositorySpec

var httpRepoMetadata = v1alpha1.ObjectMeta{
	Name: &httpRepoName,
}

func createAcmRegistrationFleetDeviceSpec(harness *e2e.Harness, pullSecretinlineConfigProviderSpec v1alpha1.ConfigProviderSpec) (v1alpha1.DeviceSpec, error) {
	hookFile := fmt.Sprintf("%s/test/e2e/microshift_acm_enrollment/data/acm-hook.yaml", util.GetTopLevelDir())
	inlineContentSecondByte, err := os.ReadFile(hookFile)
	if err != nil {
		return v1alpha1.DeviceSpec{}, fmt.Errorf("failed to read hook file: %w", err)
	}
	inlineContentSecond := string(inlineContentSecondByte)

	inlineConfigSecondSpec := v1alpha1.FileSpec{
		Path:    inlinePathSecond,
		Mode:    modePointer,
		Content: inlineContentSecond,
	}

	inlineConfigValidSecond := v1alpha1.InlineConfigProviderSpec{
		Inline: []v1alpha1.FileSpec{inlineConfigSecondSpec},
		Name:   inlineConfigNameSecond,
	}

	httpConfigProviderSpec := v1alpha1.ConfigProviderSpec{}
	err = httpConfigProviderSpec.FromHttpConfigProviderSpec(httpConfigvalid)
	if err != nil {
		return v1alpha1.DeviceSpec{}, fmt.Errorf("failed to read httpConfigProviderSpec: %w", err)
	}

	httpConfigProviderSpecSecond := v1alpha1.ConfigProviderSpec{}
	err = httpConfigProviderSpecSecond.FromHttpConfigProviderSpec(httpConfigvalidSecond)
	if err != nil {
		return v1alpha1.DeviceSpec{}, fmt.Errorf("failed to read second httpConfigProviderSpec: %w", err)
	}

	inlineConfigProviderSpecSecond := v1alpha1.ConfigProviderSpec{}
	err = inlineConfigProviderSpecSecond.FromInlineConfigProviderSpec(inlineConfigValidSecond)
	if err != nil {
		return v1alpha1.DeviceSpec{}, fmt.Errorf("failed to read inlineConfigProviderSpec: %w", err)
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
	logrus.Infof("This is the Acm namespace: %s", outputClean)

	return outputClean, nil
}

func isAcmInstalled() (bool, error) {
	cmd := exec.Command("oc", "get", "multiclusterhub", "-A")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, err
	}
	outputString := string(output)
	if outputString == "error: the server doesn't have a resource type \"multiclusterhub\"" {
		return false, fmt.Errorf("ACM is not installed: %s", outputString)
	}
	if strings.Contains(outputString, "Running") || strings.Contains(outputString, "Paused") {
		logrus.Infof("The cluster has ACM installed")
		return true, nil
	}
	return false, fmt.Errorf("multiclusterhub is not in Running status")
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
