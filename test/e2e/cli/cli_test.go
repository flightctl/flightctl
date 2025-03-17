package cli_test

import (
	"crypto/rand"
	"fmt"
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

	Context("login", func() {
		It("should have worked, and we can list devices", func() {
			out, err := harness.CLI("get", "devices")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("NAME"))
		})
	})

	Context("apply/recursive", func() {
		It("should work for a complete set of yamls", func() {
			out, err := harness.CLI("apply", "-R", "-f", util.GetTestExamplesYamlPath("/"))
			Expect(err).ToNot(HaveOccurred())
			// expect out to contain 200 OK or 201 Created
			Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))

			// check a for a couple of the yamls we know to exist in the examples directory
			Expect(out).To(ContainSubstring("examples/device-standalone.yaml/f68dfb5f5d2cdbb9339363b7f19f3ce269d75650bdc80004f1e04293a8ef9c4"))
			Expect(out).To(ContainSubstring("examples/resourcesync.yaml/default-sync"))
		})
	})

	Context("apply/fleet", func() {
		It("should error when creating incomplete fleet", func() {
			out, err := harness.CLIWithStdin(incompleteFleetYaml, "apply", "-f", "-")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("fleet: failed to apply"))
		})

		It("should work for a complete fleet", func() {
			// make sure it doesn't exist
			_, _ = harness.CLI("delete", "fleet/e2e-test-fleet")

			out, err := harness.CLIWithStdin(completeFleetYaml, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("201 Created"))

			// Applying a 2nd time it should also work, the fleet is just updated
			out, err = harness.CLIWithStdin(completeFleetYaml, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("200 OK"))
		})
	})

	Context("console", func() {
		It("should let you connect to a device", func() {
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
		It("should have worked, and we can have a certificate", func() {
			out, err := harness.CLI("certificate", "request", "-n", randString(5))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("enrollment-service:"))
		})
	})

	Context("list devices", func() {
		It("Should let you list devices", func() {
			out, err := harness.CLI("get", "devices")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("Fleet/default"))
		})
	})

	Context("list fleets", func() {
		It("Should let you list fleets", func() {
			out, err := harness.CLI("get", "fleets")
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
