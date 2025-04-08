package cli_test

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gbytes"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

const TIMEOUT = "1m"
const POLLING = "250ms"

var _ = BeforeSuite(func() {
	// This will be executed before all tests run.
	var h *e2e.Harness

	fmt.Println("Before all tests!")
	h = e2e.NewTestHarness()
	err := h.CleanUpAllResources()
	Expect(err).ToNot(HaveOccurred())
})

func TestCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CLI E2E Suite")
}

var _ = Describe("cli operation", func() {
	var (
		harness *e2e.Harness
	)

	BeforeEach(func() {
		harness = e2e.NewTestHarness()
		login.LoginToAPIWithToken(harness)
	})

	AfterEach(func() {
		harness.Cleanup(false) // do not print console on error
	})

	Context("apply/fleet", func() {
		It("Resources creation validations work well", Label("77667"), func() {
			By("should error when creating incomplete fleet")
			out, err := harness.CLIWithStdin(incompleteFleetYaml, "apply", "-f", "-")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("fleet: failed to apply"))

			By("should work for a complete fleet")
			// make sure it doesn't exist
			_, _ = harness.CLI("delete", "fleet/e2e-test-fleet")

			out, err = harness.CLIWithStdin(completeFleetYaml, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("201 Created"))

			// Applying a 2nd time it should also work, the fleet is just updated
			out, err = harness.CLIWithStdin(completeFleetYaml, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("200 OK"))
		})
		It("should let you connect to a device", Label("80483"), func() {
			By("Connecting to a device")
			deviceID := harness.StartVMAndEnroll()
			logrus.Infof("Attempting console connect command to device %s", deviceID)
			stdin, stdoutReader, err := harness.RunInteractiveCLI("console", "device/"+deviceID)
			Expect(err).ToNot(HaveOccurred())

			stdout := BufferReader(stdoutReader)

			send := func(cmd string) {
				_, err := stdin.Write([]byte(cmd + "\n"))
				Expect(err).ToNot(HaveOccurred())
			}

			// we don't have a virtual pty, so we need to make sure the console
			// will print a \n so stdout is flushed to us
			Eventually(stdout, TIMEOUT, POLLING).Should(Say(".*Connecting to .*\n"))
			Eventually(stdout, TIMEOUT, POLLING).Should(Say(".*to exit console.*\n"))

			logrus.Infof("Waiting for root prompt on device %s console", deviceID)
			send("")
			Eventually(stdout, TIMEOUT, POLLING).Should(Say(".*root@.*#"))

			logrus.Infof("Waiting fo ls output  on device %s console", deviceID)
			send("ls")

			Eventually(stdout, TIMEOUT, POLLING).Should(Say(".*bin"))

			logrus.Infof("Sending exit to the remote bash on device %s console", deviceID)
			send("exit")

			stdin.Close()

			// Make sure that there is no panic output from the console client
			Consistently(stdout, "2s").ShouldNot(Say(".*panic:"))
			stdout.Close()

		})

	})

	Context("certificate generation per user", func() {
		It("should have worked, and we can have a certificate", Label("75865"), func() {
			By("The certificated is generated for the user")
			out, err := harness.CLI("certificate", "request", "-n", randString(5))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("enrollment-service:"))
		})
	})

	Context("Plural names for resources and autocompletion in the cli work well", func() {
		It("Should let you list resources by plural names", Label("80453"), func() {
			deviceID := harness.StartVMAndEnroll()
			By("Should let you list devices")
			out, err := harness.CLI("get", "devices")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceID))

			By("Should let you list fleets")
			out, err = harness.CLI("get", "fleets")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("e2e-test-fleet"))
		})
	})

	Context("Resources lifecycle for", func() {
		It("Device, Fleet, ResourceSync, Repository, EnrollmentRequest, CertificateSigningRequest", Label("75506"), func() {
			By("Verify there are no resources created")
			err := harness.CleanUpAllResources()
			Expect(err).ToNot(HaveOccurred())

			By("Testing Device resource lifecycle")
			out, err := harness.CLI("apply", "-f", util.GetTestExamplesYamlPath("device.yaml"))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))

			device := harness.GetDeviceByYaml(util.GetTestExamplesYamlPath("device.yaml"))
			Expect(*device.Metadata.Name).ToNot(BeEmpty(), "device name should not be empty")

			(*device.Metadata.Labels)[newTestKey] = newTestValue
			deviceData, err := yaml.Marshal(&device)
			Expect(err).ToNot(HaveOccurred())
			_, err = harness.CLIWithStdin(string(deviceData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())

			By("Verifying Device update")
			devName := *device.Metadata.Name
			dev, err := harness.Client.GetDeviceWithResponse(harness.Context, devName)
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.JSON200).ToNot(BeNil(), "failed to read updated device")
			responseLabelValue := (*dev.JSON200.Metadata.Labels)[newTestKey]
			Expect(responseLabelValue).To(ContainSubstring(newTestValue))

			By("Cleaning up Device")
			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.Device, devName))
			Expect(err).ToNot(HaveOccurred())

			// Verify deletion
			dev, err = harness.Client.GetDeviceWithResponse(harness.Context, devName)
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.JSON404).ToNot(BeNil(), "device should not exist after deletion")

			By("Testing Fleet resource lifecycle")
			out, err = harness.CLI("apply", "-f", util.GetTestExamplesYamlPath("fleet-b.yaml"))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))
			fleet := harness.GetFleetByYaml(util.GetTestExamplesYamlPath("fleet-b.yaml"))
			Expect(fleet.Spec.Template).ToNot(BeNil(), "fleet template should not be nil")
			Expect(fleet.Spec.Selector).ToNot(BeNil(), "fleet selector should not be nil")

			By("Updating Fleet labels")
			(*fleet.Spec.Template.Metadata.Labels)[newTestKey] = newTestValue
			fleetData, err := yaml.Marshal(&fleet)
			Expect(err).ToNot(HaveOccurred())
			_, err = harness.CLIWithStdin(string(fleetData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())

			By("Verifying Fleet update")
			fleetName := *fleet.Metadata.Name
			fleetUpdated, err := harness.Client.GetFleetWithResponse(harness.Context, fleetName, nil)
			Expect(fleetUpdated.JSON200).ToNot(BeNil(), "failed to read updated fleet")

			Expect(err).ToNot(HaveOccurred())
			responseLabelValue = (*fleetUpdated.JSON200.Spec.Template.Metadata.Labels)[newTestKey]
			Expect(responseLabelValue).To(ContainSubstring(newTestValue))

			By("Cleaning up Fleet")
			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.Fleet, fleetName))
			Expect(err).ToNot(HaveOccurred())

			By("Repository: Resources lifecycle")
			Eventually(func() error {
				out, err = harness.CLI("apply", "-f", util.GetTestExamplesYamlPath("repository-flightctl.yaml"))
				return err
			}, "30s", "1s").Should(BeNil(), "failed to apply Repository")
			Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))

			repo := harness.GetRepositoryByYaml(util.GetTestExamplesYamlPath("repository-flightctl.yaml"))

			//Update repo name
			updatedName := "flightctl-new"
			*repo.Metadata.Name = updatedName
			repoData, err := yaml.Marshal(&repo)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(repoData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(updatedName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.Repository, updatedName))
			Expect(err).ToNot(HaveOccurred())

			By("ResourceSync: Resources lifecycle")
			out, err = harness.CLI("apply", "-f", util.GetTestExamplesYamlPath("resourcesync.yaml"))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))
			rSync := harness.GetResourceSyncByYaml(util.GetTestExamplesYamlPath("resourcesync.yaml"))

			//Update rSync name
			rSyncNewName := "flightctl-new"
			*rSync.Metadata.Name = rSyncNewName
			rSyncData, err := yaml.Marshal(&rSync)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(rSyncData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(rSyncNewName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.ResourceSync, rSyncNewName))
			Expect(err).ToNot(HaveOccurred())

			By("EnrollmentRequest: Resources lifecycle")
			out, err = harness.CLI("apply", "-f", util.GetTestExamplesYamlPath("enrollmentrequest.yaml"))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))
			er := harness.GetEnrollmentRequestByYaml(util.GetTestExamplesYamlPath("enrollmentrequest.yaml"))

			//Update er name
			erNewName := randString(64)
			*er.Metadata.Name = erNewName
			erData, err := yaml.Marshal(&er)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(erData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(erNewName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.EnrollmentRequest, erNewName))
			Expect(err).ToNot(HaveOccurred())

			By("CertificateSigningRequest: Resources lifecycle")
			Eventually(func() error {
				out, err = harness.CLI("apply", "-f", util.GetTestExamplesYamlPath("csr.yaml"))
				return err
			}, "30s", "1s").Should(BeNil(), "failed to apply CSR")
			Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))
			csr := harness.GetCertificateSigningRequestByYaml(util.GetTestExamplesYamlPath("csr.yaml"))

			//Update csr name
			csrNewName := randString(64)
			*csr.Metadata.Name = csrNewName
			csrData, err := yaml.Marshal(&csr)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(csrData), "apply", "-f", "-")

			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(csrNewName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.CertificateSigningRequest, csrNewName))
			Expect(err).ToNot(HaveOccurred())
		})
	})
})

var _ = Describe("cli login", func() {
	var (
		harness *e2e.Harness
	)

	Context("login validation", func() {
		BeforeEach(func() {
			harness = e2e.NewTestHarness()
		})

		It("Validations work when logging into flightctl CLI", Label("78748"), func() {
			By("Prepare invalid API endpoint")
			invalidEndpoint := "https://not-existing.lab.redhat.com"
			loginArgs := []string{"login", invalidEndpoint}

			By("Try login using a wrong API endpoint without --insecure-skip-tls-verify flag")
			logrus.Infof("Executing CLI with args: %v", loginArgs)
			out, err := harness.CLI(loginArgs...)
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("failed to get auth info"))

			By("Retry login using invalid API endpoint with  --insecure-skip-tls-verify flag")
			loginArgs = append(loginArgs, "--insecure-skip-tls-verify")
			logrus.Infof("Executing CLI with args: %v", loginArgs)
			out, err = harness.CLI(loginArgs...)
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("failed to get auth info"))

			By("Retry login using an invalid token")
			loginArgs = []string{"login", os.Getenv("API_ENDPOINT"), "--insecure-skip-tls-verify"}
			invalidToken := "fake-token"
			loginArgsToken := append(loginArgs, "--token", invalidToken)

			logrus.Infof("Executing CLI with args: %v", loginArgsToken)
			out, _ = harness.CLI(loginArgsToken...)
			if !strings.Contains(out, "Auth is disabled") {
				Expect(out).To(Or(
					ContainSubstring("the token provided is invalid or expired"),
					ContainSubstring("failed to validate token"),
					ContainSubstring("invalid JWT")))

				By("Retry login with the invalid password")
				invalidPassword := "passW0RD"
				loginArgsPassword := append(loginArgs, "-k", "-u", "demouser", "-p", invalidPassword)

				logrus.Infof("Executing CLI with args: %v", loginArgsPassword)
				out, _ = harness.CLI(loginArgsPassword...)
				// We don't check for error here as we're only interested in the output message
				Expect(out).To(Or(
					ContainSubstring("Invalid user credentials"),
					ContainSubstring("unexpected http code: 401")))
			}
		})
	})
})

func randString(n int) string {
	const alphanum = "abcdefghijklmnopqrstuvwxyz"
	var bytes = make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		Expect(err).ToNot(HaveOccurred())
		return ""
	}
	for i, b := range bytes {
		bytes[i] = alphanum[b%byte(len(alphanum))]
	}
	return string(bytes)
}

const completeFleetYaml = `
apiVersion: v1alpha1
kind: Fleet
metadata:
    name: e2e-test-fleet
spec:
    selector:
        matchLabels:
            fleet: default
    template:
        spec:
            os:
                image: quay.io/redhat/rhde:9.2
`

const incompleteFleetYaml = `
apiVersion: v1alpha1
kind: Fleet
metadata:
    name: e2e-test-fleet
spec:
    selector:
        matchLabels:
            fleet: default
`

var (
	newTestKey   = "testKey"
	newTestValue = "newValue"
)
