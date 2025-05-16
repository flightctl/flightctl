package cli_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gbytes"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

var (
	invalidSyntax = "invalid syntax"
	kind          = "involvedObject.kind"
	fieldSelector = "--field-selector"
	fleetYAMLPath = "fleet.yaml"
	limit         = "--limit"
	repoYAMLPath  = "repository-flightctl.yaml"
	erYAMLPath    = "enrollmentrequest.yaml"
)

// _ is used as a blank identifier to ignore the return value of BeforeSuite, typically for initialization purposes.
var _ = BeforeSuite(func() {
	// This will be executed before all tests run.
	var h *e2e.Harness

	fmt.Println("Before all tests!")
	h = e2e.NewTestHarness()
	err := h.CleanUpAllResources()
	Expect(err).ToNot(HaveOccurred())
})

// TestCLI initializes and runs the suite of end-to-end tests for the Command Line Interface (CLI).
func TestCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CLI E2E Suite")
}

// _ is a blank identifier used to ignore values or expressions, often applied to satisfy interface or assignment requirements.
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

			By("Should error when creating a device with decimal in percentages")
			out, err = harness.CLI("apply", "-f", util.GetTestExamplesYamlPath("badfleetrequest.yaml"))
			Expect(err).To(HaveOccurred())
			Expect(out).To(MatchRegexp(`doesn't match percentage pattern`))

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
			stdin, stdoutReader, err := harness.RunInteractiveCLI("console", "--tty", "device/"+deviceID)
			Expect(err).ToNot(HaveOccurred())

			stdout := BufferReader(stdoutReader)

			send := func(cmd string) {
				_, err := stdin.Write([]byte(cmd + "\n"))
				Expect(err).ToNot(HaveOccurred())
			}

			logrus.Infof("Waiting for root prompt on device %s console", deviceID)
			send("")
			Eventually(stdout, TIMEOUT, POLLING).Should(Say(".*root@.*#"))

			logrus.Infof("Waiting for ls output  on device %s console", deviceID)
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
			By("The certificate is generated for the user")

			// Capture both string and error
			randomString, err := util.RandString(5)
			Expect(err).ToNot(HaveOccurred()) // Check for error

			// Use the string in the CLI command
			out, err := harness.CLI("certificate", "request", "-n", randomString)
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
			erNewName, err := util.RandString(64)
			Expect(err).ToNot(HaveOccurred())
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
			csrNewName, err := util.RandString(64)
			Expect(err).ToNot(HaveOccurred())
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

	Context("Flightctl Version Checks", func() {
		It("should show matching client and server versions", Label("79621"), func() {
			By("Getting the version output")
			out, err := harness.CLI("version")
			clientVersionPrefix := "Client Version:"
			serverVersionPrefix := "Server Version:"
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(clientVersionPrefix))
			Expect(out).To(ContainSubstring(serverVersionPrefix))

			By("Parsing client and server versions")
			clientVersion := GetVersionByPrefix(out, clientVersionPrefix)
			serverVersion := GetVersionByPrefix(out, serverVersionPrefix)

			Expect(clientVersion).ToNot(BeEmpty(), "client version should be found")
			Expect(serverVersion).ToNot(BeEmpty(), "server version should be found")
			Expect(clientVersion).To(Equal(serverVersion), "client and server versions should match")
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

	Context("Events API Tests", func() {
		It("should list events resource is created/updated/deleted", Label("80452"), func() {
			var deviceName, fleetName, repoName string
			var er *v1alpha1.EnrollmentRequest

			resources := []struct {
				resourceType string
				yamlPath     string
			}{
				{util.DeviceResource, util.DeviceYAMLPath},
				{util.FleetResource, fleetYAMLPath},
				{util.RepoResource, repoYAMLPath},
				{util.ErResource, erYAMLPath},
			}

			By("Applying resources: device, fleet, repo, enrollment request")
			for _, r := range resources {
				_, err := harness.ManageResource(util.ApplyAction, r.yamlPath)
				Expect(err).ToNot(HaveOccurred())

				switch r.resourceType {
				case util.DeviceResource:
					device := harness.GetDeviceByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
					deviceName = *device.Metadata.Name
				case util.FleetResource:
					fleet := harness.GetFleetByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
					fleetName = *fleet.Metadata.Name
				case util.RepoResource:
					repo := harness.GetRepositoryByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
					repoName = *repo.Metadata.Name
				case util.ErResource:
					out, err := harness.CLI(util.ApplyAction, util.ForceFlag, util.GetTestExamplesYamlPath(r.yamlPath))
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))
					er = harness.GetEnrollmentRequestByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
				}
			}

			By("Verifying Created events")
			out, err := harness.RunGetEvents()
			Expect(err).ToNot(HaveOccurred())
			for _, r := range resources {
				var name string
				switch r.resourceType {
				case util.DeviceResource:
					name = deviceName
				case util.FleetResource:
					name = fleetName
				case util.RepoResource:
					name = repoName
				case util.ErResource:
					name = *er.Metadata.Name
				}
				Expect(out).To(ContainSubstring(formatResourceEvent(r.resourceType, name, util.EventCreated)))
			}

			By("Reapplying resources (updates)")
			for _, r := range resources {
				_, err := harness.ManageResource(util.ApplyAction, r.yamlPath)
				Expect(err).ToNot(HaveOccurred())

				switch r.resourceType {
				case util.DeviceResource:
					device := harness.GetDeviceByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
					deviceName = *device.Metadata.Name
				case util.FleetResource:
					fleet := harness.GetFleetByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
					fleetName = *fleet.Metadata.Name
				case util.RepoResource:
					repo := harness.GetRepositoryByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
					repoName = *repo.Metadata.Name
				case util.ErResource:
					out, err := harness.CLI(util.ApplyAction, util.ForceFlag, util.GetTestExamplesYamlPath(r.yamlPath))
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))
					er = harness.GetEnrollmentRequestByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
				}
			}

			By("Verifying Updated events")
			out, err = harness.RunGetEvents()
			Expect(err).ToNot(HaveOccurred())
			for _, r := range resources {
				var name string
				switch r.resourceType {
				case util.DeviceResource:
					name = deviceName
				case util.FleetResource:
					name = fleetName
				case util.RepoResource:
					name = repoName
				case util.ErResource:
					name = *er.Metadata.Name
				}
				Expect(out).To(ContainSubstring(formatResourceEvent(r.resourceType, name, util.EventUpdated)))
			}

			By("Querying events with fieldSelector kind=Device")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=%s", kind, util.DeviceResource))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(formatResourceEvent(util.DeviceResource, deviceName, util.EventCreated)))

			By("Querying events with fieldSelector kind=Fleet")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=%s", kind, util.FleetResource))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(formatResourceEvent("Fleet", fleetName, util.EventCreated)))

			By("Querying events with fieldSelector kind=Repository")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=%s", kind, util.RepoResource))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(formatResourceEvent(util.RepoResource, repoName, util.EventCreated)))

			By("Querying events with fieldSelector type=Normal")
			out, err = harness.RunGetEvents(fieldSelector, "type=Normal")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("Normal"))

			By("Querying events with a specific device name")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("involvedObject.name=%s", deviceName))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("Querying events with a combined filter: kind=Device, type=Normal")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=%s,type=Normal", kind, util.DeviceResource))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(formatResourceEvent(util.DeviceResource, deviceName, util.EventCreated)))
			Expect(out).To(ContainSubstring("Normal"))

			By("Querying with an invalid fieldSelector key")
			out, err = harness.RunGetEvents(fieldSelector, "invalidField=xyz")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unable to resolve selector name"))

			By("Querying with an unknown kind in fieldSelector")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=AlienDevice", kind))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring("Normal"))

			By("Deleting the resource")
			_, err = harness.ManageResource("delete", fmt.Sprintf("device/%s", deviceName))
			Expect(err).ToNot(HaveOccurred())

			By("Verifying deleted events are listed")
			out, err = harness.RunGetEvents(limit, "1")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(util.EventDeleted))

			By("Querying events with limit=1")
			out, err = harness.RunGetEvents(limit, "1")
			Expect(err).ToNot(HaveOccurred())
			lines := strings.Split(strings.TrimSpace(out), "\n")
			Expect(len(lines)).To(Equal(2)) // 1 header + 1 event

			By("Running with no argument")
			out, err = harness.RunGetEvents(limit)
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("flag needs an argument"))

			By("Running with empty string as argument")
			out, err = harness.RunGetEvents(limit, "")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(invalidSyntax))

			By("Running with negative number")
			out, err = harness.RunGetEvents(limit, "-1")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("must be greater than 0"))

			By("Running with non-integer string")
			out, err = harness.RunGetEvents(limit, "xyz")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(invalidSyntax))

			By("Running with too many args")
			out, err = harness.RunGetEvents(limit, "1", "2")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("accepts 1 arg(s), received 2"))
		})
	})
})

// formatResourceEvent formats the event's message and returns it as a string
func formatResourceEvent(resource, name, action string) string {
	return fmt.Sprintf("%s %s %s successfully", resource, name, action)
}

// GetVersionByPrefix searches the output for a line starting with the given prefix
// and returns the trimmed value following the prefix. Returns an empty string if not found.
func GetVersionByPrefix(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// TIMEOUT represents the default duration string for timeout, set to 1 minute.
const TIMEOUT = "1m"
const POLLING = "250ms"

// completeFleetYaml defines a YAML template for creating a Fleet resource with specified metadata and spec configuration.
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

// incompleteFleetYaml defines a YAML configuration string for a Fleet resource with minimal and incomplete fields.
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
	newTestKey = "testKey"

	// newTestValue holds the string value "newValue" used as a test variable in the application.
	newTestValue = "newValue"
)
