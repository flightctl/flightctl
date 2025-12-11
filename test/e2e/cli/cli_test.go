package cli_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/yaml"
)

var (
	unspecifiedResource  = "Error: name must be specified when deleting"
	resourceCreated      = `(200 OK|201 Created)`
	invalidResource      = "invalid resource kind"
	strictfipsruntimeTag = "X:strictfipsruntime"
	applyOperation       = "apply"
)

type fleetTestManager struct {
	harness          *e2e.Harness
	testID           string
	fleetA           v1beta1.Fleet
	fleetB           v1beta1.Fleet
	device           v1beta1.Device
	uniqueFleetAYAML string
	uniqueFleetBYAML string
	uniqueDeviceYAML string
	fleetAName       string
	fleetBName       string
	deviceName       string
}

// _ is a blank identifier used to ignore values or expressions, often applied to satisfy interface or assignment requirements.
var _ = Describe("cli operation", func() {
	BeforeEach(func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()
		login.LoginToAPIWithToken(harness)
	})

	Context("apply/fleet", func() {
		It("Resources creation validations work well", Label("77667", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("should error when creating incomplete fleet")
			out, err := harness.CLIWithStdin(incompleteFleetYaml, applyOperation, "-f", "-")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("fleet: failed to apply"))

			By("should work for a complete fleet")
			// make sure it doesn't exist
			_, _ = harness.CLI("delete", "fleet/e2e-test-fleet")

			By("Should error when creating a device with decimal in percentages")

			out, err = harness.CLI(applyOperation, "-f", util.GetTestExamplesYamlPath("badfleetrequest.yaml"))
			Expect(err).To(HaveOccurred())
			Expect(out).To(MatchRegexp(`doesn't match percentage pattern`))

			By("Should work for a complete fleet")
			uniqueFleetYAML, err := util.CreateUniqueYAMLFile("fleet.yaml", harness.GetTestIDFromContext())
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueFleetYAML)

			out, err = harness.ManageResource(applyOperation, uniqueFleetYAML)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("201 Created"))

			// Applying a 2nd time it should also work, the fleet is just updated
			out, err = harness.ManageResource(applyOperation, uniqueFleetYAML)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("200 OK"))
		})
	})

	Context("certificate generation per user", func() {
		It("should have worked, and we can have a certificate", Label("75865", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

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
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
			By("Should let you list devices")
			out, err := harness.CLI("get", "devices")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceID))

			By("Should let you list fleets")
			_, err = harness.CLIWithStdin(completeFleetYaml, applyOperation, "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLI("get", "fleets")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("e2e-test-fleet"))
		})
	})

	Context("Enrollment Request reapplication validation", func() {
		It("should prevent reapplying enrollment request with same name after device creation", Label("83301", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Applying enrollment request initially")
			erYAMLPath, err := CreateTestERAndWriteToTempFile()
			Expect(err).ToNot(HaveOccurred())
			defer os.Remove(erYAMLPath)
			out, err := harness.ApplyResource(erYAMLPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			// Get the enrollment request to extract its name
			er := harness.GetEnrollmentRequestByYaml(erYAMLPath)
			erName := *er.Metadata.Name

			By("Approving the enrollment request")
			_, err = harness.ManageResource("approve", fmt.Sprintf("er/%s", erName))
			Expect(err).ToNot(HaveOccurred())

			By("Verifying device was created")
			device, err := harness.GetDevice(erName)
			Expect(err).ToNot(HaveOccurred())
			Expect(device).ToNot(BeNil())

			By("Attempting to delete the enrollment request for live device")
			out, err = harness.ManageResource("delete", fmt.Sprintf("er/%s", erName))
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("device exists"))

			By("Attempting to reapply the same enrollment request")
			out, err = harness.ApplyResource(erYAMLPath)
			Expect(err).To(HaveOccurred())
			badRequestMessage := fmt.Sprintf("%d %s", http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
			Expect(out).To(ContainSubstring(badRequestMessage))
			Expect(out).To(ContainSubstring("a resource with this name already exists"))
		})
	})

	Context("Resources lifecycle for", func() {
		It("Device, Fleet, ResourceSync, Repository, EnrollmentRequest, CertificateSigningRequest", Label("75506", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Testing Device resource lifecycle")
			uniqueDeviceYAML, err := util.CreateUniqueYAMLFile("device.yaml", harness.GetTestIDFromContext())
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueDeviceYAML)

			out, err := harness.ManageResource(applyOperation, uniqueDeviceYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			device := harness.GetDeviceByYaml(uniqueDeviceYAML)
			Expect(*device.Metadata.Name).ToNot(BeEmpty(), "device name should not be empty")

			devName := *device.Metadata.Name
			Eventually(func() error {
				err := harness.UpdateDevice(devName, func(device *v1beta1.Device) {
					(*device.Metadata.Labels)[newTestKey] = newTestValue
				})
				return err
			}).Should(BeNil(), "failed to update device")

			By("Verifying Device update")
			dev, err := harness.Client.GetDeviceWithResponse(harness.Context, devName)
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.JSON200).ToNot(BeNil(), "failed to read updated device")
			responseLabelValue := (*dev.JSON200.Metadata.Labels)[newTestKey]
			Expect(responseLabelValue).To(ContainSubstring(newTestValue))

			By("Verify Shell Expansion works")
			out, err = harness.GetResourcesByName(util.Device, devName)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(devName))

			By("Cleaning up Device")
			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.Device, devName))
			Expect(err).ToNot(HaveOccurred())

			// Verify deletion
			dev, err = harness.Client.GetDeviceWithResponse(harness.Context, devName)
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.JSON404).ToNot(BeNil(), "device should not exist after deletion")

			By("Testing Fleet resource lifecycle")
			uniqueFleetYAML, err := util.CreateUniqueYAMLFile("fleet.yaml", harness.GetTestIDFromContext())
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueFleetYAML)

			out, err = harness.ManageResource(applyOperation, uniqueFleetYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			fleet := harness.GetFleetByYaml(uniqueFleetYAML)
			Expect(fleet.Spec.Template).ToNot(BeNil(), "fleet template should not be nil")
			Expect(fleet.Spec.Selector).ToNot(BeNil(), "fleet selector should not be nil")
			fleetName := *fleet.Metadata.Name

			By("Updating Fleet labels")
			Eventually(func() error {
				err := harness.UpdateFleet(fleetName, func(fleet *v1beta1.Fleet) {
					(*fleet.Spec.Template.Metadata.Labels)[newTestKey] = newTestValue
				})
				return err
			}).Should(BeNil(), "failed to update fleet")

			By("Verifying Fleet update")
			fleetUpdated, err := harness.Client.GetFleetWithResponse(harness.Context, fleetName, nil)
			Expect(fleetUpdated.JSON200).ToNot(BeNil(), "failed to read updated fleet")

			Expect(err).ToNot(HaveOccurred())
			responseLabelValue = (*fleetUpdated.JSON200.Spec.Template.Metadata.Labels)[newTestKey]
			Expect(responseLabelValue).To(ContainSubstring(newTestValue))

			By("Cleaning up Fleet")
			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.Fleet, fleetName))
			Expect(err).ToNot(HaveOccurred())

			By("Repository: Resources lifecycle")
			uniqueRepoYAML, err := util.CreateUniqueYAMLFile("repository-flightctl.yaml", harness.GetTestIDFromContext())
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueRepoYAML)

			Eventually(func() error {
				out, err = harness.ManageResource(applyOperation, uniqueRepoYAML)
				return err
			}).Should(BeNil(), "failed to apply Repository")
			Expect(out).To(MatchRegexp(resourceCreated))

			repo := harness.GetRepositoryByYaml(uniqueRepoYAML)

			//Update repo name
			updatedName := "flightctl-new-" + harness.GetTestIDFromContext()
			*repo.Metadata.Name = updatedName
			repoData, err := yaml.Marshal(&repo)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(repoData), applyOperation, "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(updatedName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.Repository, updatedName))
			Expect(err).ToNot(HaveOccurred())

			By("ResourceSync: Resources lifecycle")
			uniqueResourceSyncYAML, err := util.CreateUniqueYAMLFile("resourcesync.yaml", harness.GetTestIDFromContext())
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueResourceSyncYAML)

			out, err = harness.ManageResource(applyOperation, uniqueResourceSyncYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			rSync := harness.GetResourceSyncByYaml(uniqueResourceSyncYAML)

			//Update rSync name
			rSyncNewName := "flightctl-new-" + harness.GetTestIDFromContext()
			*rSync.Metadata.Name = rSyncNewName
			rSyncData, err := yaml.Marshal(&rSync)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(rSyncData), applyOperation, "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(rSyncNewName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.ResourceSync, rSyncNewName))
			Expect(err).ToNot(HaveOccurred())

			By("EnrollmentRequest: Resources lifecycle")
			erYAMLPath, err := CreateTestERAndWriteToTempFile()
			Expect(err).ToNot(HaveOccurred())
			defer os.Remove(erYAMLPath)

			out, err = harness.ManageResource(applyOperation, erYAMLPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			er := harness.GetEnrollmentRequestByYaml(erYAMLPath)

			//Update er name
			erNewName, err := util.RandString(64)
			Expect(err).ToNot(HaveOccurred())
			*er.Metadata.Name = erNewName
			erData, err := yaml.Marshal(&er)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(erData), applyOperation, "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(erNewName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.EnrollmentRequest, erNewName))
			Expect(err).ToNot(HaveOccurred())

			By("CertificateSigningRequest: Resources lifecycle")
			uniqueCsrYAML, err := util.CreateUniqueYAMLFile("csr.yaml", harness.GetTestIDFromContext())
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueCsrYAML)

			Eventually(func() error {
				out, err = harness.CLI(applyOperation, "-f", uniqueCsrYAML)
				return err
			}).Should(BeNil(), "failed to apply CSR")
			Expect(out).To(MatchRegexp(resourceCreated))
			csr := harness.GetCertificateSigningRequestByYaml(uniqueCsrYAML)

			//Update csr name
			csrNewName, err := util.RandString(64)
			Expect(err).ToNot(HaveOccurred())
			*csr.Metadata.Name = csrNewName
			csrData, err := yaml.Marshal(&csr)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(csrData), applyOperation, "-f", "-")

			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(csrNewName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.CertificateSigningRequest, csrNewName))
			Expect(err).ToNot(HaveOccurred())
		})

		It("should verify that `flightctl get kind NAME' works", Label("85509", "sanity"), func() {
			harness := e2e.GetWorkerHarness()
			testID := harness.GetTestIDFromContext()

			By("Creating a test device")
			uniqueDeviceYAML, err := util.CreateUniqueYAMLFile("device.yaml", testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueDeviceYAML)

			out, err := harness.ManageResource("apply", uniqueDeviceYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			device := harness.GetDeviceByYaml(uniqueDeviceYAML)
			deviceName := *device.Metadata.Name

			By("Confirming device appears in device list")
			out, err = harness.CLI("get", "devices")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(deviceName))

			By("Comparing with-slash and no-slash forms for table and JSON output")
			withSlash, err := harness.CLI("get", "device/"+deviceName)
			Expect(err).NotTo(HaveOccurred(), "flightctl get device/%s failed", deviceName)

			noSlash, err := harness.CLI("get", "device", deviceName)
			Expect(err).NotTo(HaveOccurred(), "flightctl get device %s failed", deviceName)

			Expect(collapse(withSlash)).To(Equal(collapse(noSlash)),
				"no-slash table output must equal with-slash")

			withSlashJSON, err := harness.CLI("get", "device/"+deviceName, "-o", "json")
			Expect(err).NotTo(HaveOccurred(), "flightctl get device/%s -o json failed", deviceName)

			noSlashJSON, err := harness.CLI("get", "device", deviceName, "-o", "json")
			Expect(err).NotTo(HaveOccurred(), "flightctl get device %s -o json failed", deviceName)

			Expect(noSlashJSON).To(MatchJSON(withSlashJSON),
				"no-slash JSON must deep-equal with-slash")
		})

	})

	Context("CLI Multi-Device Delete", func() {
		It("should delete multiple devices", Label("75506", "sanity"), func() {
			By("Creating multiple test devices")
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			// Generate unique test ID for this test
			testID := harness.GetTestIDFromContext()

			// Create unique YAML files for this test
			uniqueDeviceYAML, err := util.CreateUniqueYAMLFile("device.yaml", testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueDeviceYAML)

			uniqueDeviceBYAML, err := util.CreateUniqueYAMLFile("device-b.yaml", testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueDeviceBYAML)

			out, err := harness.ManageResource(applyOperation, uniqueDeviceYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			device1 := harness.GetDeviceByYaml(uniqueDeviceYAML)
			device1Name := *device1.Metadata.Name

			out, err = harness.ManageResource(applyOperation, uniqueDeviceBYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			device2 := harness.GetDeviceByYaml(uniqueDeviceBYAML)
			device2Name := *device2.Metadata.Name

			devices, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred())
			Expect(devices).To(ContainSubstring(device1Name))
			Expect(devices).To(ContainSubstring(device2Name))

			By("Deleting multiple devices at once")
			out, err = harness.CLI("delete", util.Device, device1Name, device2Name)

			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("completed"))

			By("Verifying both devices were deleted")
			dev1, err := harness.Client.GetDeviceWithResponse(harness.Context, device1Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(dev1.JSON404).ToNot(BeNil(), "first device should not exist after deletion")

			dev2, err := harness.Client.GetDeviceWithResponse(harness.Context, device2Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(dev2.JSON404).ToNot(BeNil(), "second device should not exist after deletion")
		})

		It("Validation works when trying to delete resources without names", Label("82540", "sanity"), func() {
			By("Creating multiple test resources")
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			// Generate unique test ID for this test
			testID := harness.GetTestIDFromContext()

			// Create unique YAML files for this test
			uniqueDeviceYAML, err := util.CreateUniqueYAMLFile("device.yaml", testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueDeviceYAML)

			uniqueFleetYAML, err := util.CreateUniqueYAMLFile("fleet.yaml", testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueFleetYAML)

			uniqueRepoYAML, err := util.CreateUniqueYAMLFile("repository-flightctl.yaml", testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueRepoYAML)

			erYAMLPath, err := CreateTestERAndWriteToTempFile()
			Expect(err).ToNot(HaveOccurred())
			defer os.Remove(erYAMLPath)

			uniqueResourceSyncYAML, err := util.CreateUniqueYAMLFile("resourcesync.yaml", testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueResourceSyncYAML)

			applyResources := []string{
				uniqueDeviceYAML,
				uniqueFleetYAML,
				uniqueRepoYAML,
				erYAMLPath,
				uniqueResourceSyncYAML,
			}

			for _, file := range applyResources {
				out, err := harness.ManageResource(applyOperation, file)
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(MatchRegexp(resourceCreated))
			}

			tests := util.Cases[DeleteWithoutNameTestParams](
				DeleteEntryCase("fails deleting unspecified device", util.Device),
				DeleteEntryCase("fails deleting unspecified device", "devices"),
				DeleteEntryCase("fails deleting unspecified fleet", util.Fleet),
				DeleteEntryCase("fails deleting unspecified fleet", "fleets"),
				DeleteEntryCase("fails deleting unspecified repository", util.Repository),
				DeleteEntryCase("fails deleting unspecified repository", "repositories"),
				DeleteEntryCase("fails deleting unspecified enrollment request", util.EnrollmentRequest),
				DeleteEntryCase("fails deleting unspecified enrollment request)", "enrollmentrequests"),
				DeleteEntryCase("fails deleting unspecified resource sync", util.ResourceSync),
				DeleteEntryCase("fails deleting unspecified resource sync", "resourcesyncs"),
			)

			util.RunTable[DeleteWithoutNameTestParams](tests, func(params DeleteWithoutNameTestParams) {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				out, err := harness.ManageResource("delete", params.ResourceArg)
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(unspecifiedResource))
			})
		})
	})

	Context("Flightctl Version Checks", func() {
		It("should show matching client and server versions", Label("79621", "sanity", "rpm-sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Getting all versions from CLI")
			clientVersion, serverVersion, agentVersion, err := harness.GetVersionsFromCLI()
			Expect(err).ToNot(HaveOccurred())
			Expect(clientVersion).ToNot(BeEmpty(), "client version should be found")
			Expect(serverVersion).ToNot(BeEmpty(), "server version should be found")
			Expect(agentVersion).ToNot(BeEmpty(), "agent version should be found")

			GinkgoWriter.Printf("Client version: %s\n", clientVersion)
			GinkgoWriter.Printf("Server version: %s\n", serverVersion)
			GinkgoWriter.Printf("Agent version: %s\n", agentVersion)

			By("Comparing versions")
			// Expect(clientVersion).To(Equal(serverVersion), "client and server versions should match")
			Expect(agentVersion).To(Equal(serverVersion), "agent and server versions should match")
		})

		It("should show FIPS runtime compliance", Label("rpm-sanity", "84648"), func() {
			// Skip test if neither BREW_BUILD_URL nor RPM_COPR is set since it applies only to RPM builds
			brewBuildURL := os.Getenv("BREW_BUILD_URL")
			rpmCopr := os.Getenv("RPM_COPR")
			if brewBuildURL == "" && rpmCopr == "" {
				Skip("Skipping FIPS test - neither BREW_BUILD_URL nor RPM_COPR is set")
			}

			harness := e2e.GetWorkerHarness()

			By("Checking that OpenSSL symbols are loaded when running with FIPS environment variables")
			// Run flightctl version with FIPS environment and capture stderr for symbol loading info
			fipsEnv := map[string]string{
				"GOLANG_FIPS":             "1",
				"OPENSSL_FORCE_FIPS_MODE": "1",
				"LD_DEBUG":                "symbols",
			}
			flightctlPath := harness.GetFlightctlPath()
			openSSLOutput, err := harness.CLIWithEnvAndShell(fipsEnv, fmt.Sprintf("%s version 2>&1 | grep OPENSSL", flightctlPath))
			Expect(err).ToNot(HaveOccurred(), "Failed to run flightctl with FIPS environment")
			Expect(openSSLOutput).ToNot(BeEmpty(), "No OpenSSL symbols found in output")
			Expect(openSSLOutput).To(ContainSubstring("OPENSSL"), "OpenSSL symbols should be loaded")
			GinkgoWriter.Printf("OpenSSL symbol loading output: %s\n", openSSLOutput)

			By("Checking that version YAML output contains strictfipsruntime")
			yamlOut, err := harness.CLI("version", "-o", "yaml")
			Expect(err).ToNot(HaveOccurred())
			Expect(yamlOut).To(ContainSubstring("X:strictfipsruntime"), "Version YAML should contain strictfipsruntime tag")
			GinkgoWriter.Printf("Version YAML output contains strictfipsruntime: %t\n", strings.Contains(yamlOut, strictfipsruntimeTag))
		})
	})

	Context("Verify fleet check shows devices", func() {
		It("Show number of devices associated with each fleet", Label("84266", "sanity"), func() {
			harness := e2e.GetWorkerHarness()
			testID := harness.GetTestIDFromContext()
			fleetDevicesCount := map[string]int64{}
			fleetManager := fleetTestManager{harness: harness, testID: testID}

			By("Creating a fleet")
			out, err := createTestFleet(&fleetManager, fleetDevicesCount, "fleet.yaml", "fleetA")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			defer util.CleanupTempYAMLFile(fleetManager.uniqueFleetAYAML)

			// Checking if number of devices is shown
			By("Checking if number of devices is shown")
			notMatched, err := checkDevicesInFleetStatus(harness, fleetDevicesCount)
			Expect(err).ToNot(HaveOccurred())
			Expect(notMatched).To(BeEmpty())

			// Creating a device in the same fleet
			By("Creating a device in the same fleet")
			out, err = createDeviceInFleet(&fleetManager, fleetDevicesCount, "device.yaml", "fleetA")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			defer util.CleanupTempYAMLFile(fleetManager.uniqueDeviceYAML)

			// Checking if the number of devices is shown and changed
			By("Checking if number of devices is shown and changed")
			notMatched, err = checkDevicesInFleetStatus(harness, fleetDevicesCount)
			Expect(err).ToNot(HaveOccurred())
			Expect(notMatched).To(BeEmpty())

			By("Creating another fleet")
			out, err = createTestFleet(&fleetManager, fleetDevicesCount, "fleet-b.yaml", "fleetB")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			defer util.CleanupTempYAMLFile(fleetManager.uniqueFleetBYAML)

			// Checking if the number of devices is shown for both fleetsS
			By("Checking if number of devices is shown for both fleets")
			notMatched, err = checkDevicesInFleetStatus(harness, fleetDevicesCount)
			Expect(err).ToNot(HaveOccurred())
			Expect(notMatched).To(BeEmpty())

			// Deleting a device from the first fleet
			By("Deleting a device from the first fleet")
			out, err = harness.CLI("delete", util.Device, fleetManager.deviceName)
			fleetDevicesCount[fleetManager.fleetAName]--
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("completed"))

			// Check if the number is changed after deletion
			By("Checking if number of devices is shown and changed")
			notMatched, err = checkDevicesInFleetStatus(harness, fleetDevicesCount)
			Expect(err).ToNot(HaveOccurred())
			Expect(notMatched).To(BeEmpty())
		})
	})

	Context("Verify fleet check shows aggregated device status", func() {
		It("Show aggregated device status for each fleet", Label("84270", "sanity"), func() {
			harness := e2e.GetWorkerHarness()
			testID := harness.GetTestIDFromContext()

			By("Creating a fleet")
			uniqueFleetYAML, err := util.CreateUniqueYAMLFile("fleet.yaml", testID)
			Expect(err).NotTo(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueFleetYAML)

			// Checking the fleet was created
			out, err := harness.ManageResource(applyOperation, uniqueFleetYAML)
			fleet := harness.GetFleetByYaml(uniqueFleetYAML)
			fleetName := *fleet.Metadata.Name
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			By("Verify the device summary matches expected count")
			count, err := validateDevicesSummary(harness, fleetName, 0)
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(0))

			By("Creating a device in the fleet")
			uniqueDeviceYAML, err := util.CreateUniqueYAMLFile("device.yaml", testID)
			Expect(err).NotTo(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueDeviceYAML)

			// Checking the device was created
			out, err = harness.ManageResource(applyOperation, uniqueDeviceYAML)
			device := harness.GetDeviceByYaml(uniqueDeviceYAML)
			deviceName := *device.Metadata.Name
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			By("Verify the device summary matches expected count")
			count, err = validateDevicesSummary(harness, fleetName, 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(1))

			By("Deleting a device from the fleet")
			out, err = harness.CLI("delete", util.Device, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("completed"))

			By("Verify the device summary matches expected count")
			count, err = validateDevicesSummary(harness, fleetName, 0)
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(0))

		})
	})

})

var _ = Describe("cli login", func() {

	Context("login validation", func() {

		It("Validations work when logging into flightctl CLI", Label("78748", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Prepare invalid API endpoint")
			invalidEndpoint := "https://not-existing.lab.redhat.com"
			loginArgs := []string{"login", invalidEndpoint}

			By("Try login using a wrong API endpoint without --insecure-skip-tls-verify flag")
			GinkgoWriter.Printf("Executing CLI with args: %v\n", loginArgs)
			out, err := harness.CLI(loginArgs...)
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("failed to get auth info"))

			By("Retry login using invalid API endpoint with  --insecure-skip-tls-verify flag")
			loginArgs = append(loginArgs, "--insecure-skip-tls-verify")
			GinkgoWriter.Printf("Executing CLI with args: %v\n", loginArgs)
			out, err = harness.CLI(loginArgs...)
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("failed to get auth info"))

			By("Retry login using an empty config-dir flag")
			loginArgs = append(loginArgs, "--config-dir")
			GinkgoWriter.Printf("Executing CLI with args: %v\n", loginArgs)
			out, err = harness.CLI(loginArgs...)
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("Error: flag needs an argument: --config-dir"))

			By("Retry login using an invalid token")
			loginArgs = []string{"login", os.Getenv("API_ENDPOINT"), "--insecure-skip-tls-verify"}
			invalidToken := "fake-token"
			loginArgsToken := append(loginArgs, "--token", invalidToken)

			GinkgoWriter.Printf("Executing CLI with args: %v\n", loginArgsToken)
			out, _ = harness.CLI(loginArgsToken...)
			if !strings.Contains(out, "Auth is disabled") {
				Expect(out).To(Or(
					ContainSubstring("the token provided is invalid or expired"),
					ContainSubstring("failed to validate token"),
					ContainSubstring("invalid JWT")))
			}
		})

		It("Should refresh token when expiration is reached", Label("81481"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			const configPath = "" // default to nothing to let the default path be used
			By("Login to the service")
			// We need to ensure that the login mechanism was user/pass otherwise the refresh flow isn't
			// active.
			if login.LoginToAPIWithToken(harness) != login.AuthUsernamePassword {
				Skip("This test requires authentication with username/password to be enabled")
			}
			By("Ensure actions can be taken")
			_, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "Failed to get device info")

			By("Read the current access token")
			cfg, err := harness.ReadClientConfig(configPath)
			Expect(err).ToNot(HaveOccurred(), "Failed to read client config")
			initialToken := cfg.AuthInfo.AccessToken

			By("Expire the current access token and run an action")
			err = harness.MarkClientAccessTokenExpired(configPath)
			Expect(err).ToNot(HaveOccurred(), "Failed to read client config")
			// again all we care is that this doesn't error out
			_, err = harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "Failed to get device info after expiring the token")

			By("Ensure a new token was generated")
			// Note: The old token is still valid at this point as it hasn't actually expired
			cfg, err = harness.ReadClientConfig(configPath)
			Expect(err).ToNot(HaveOccurred(), "Failed to read client config after expiring token")
			secondToken := cfg.AuthInfo.AccessToken
			Expect(secondToken).ToNot(Equal(initialToken), "Token should have been refreshed")

			By("Remove connectivity to the auth service")
			providerUrl := e2e.ExtractAuthURL(&cfg.AuthInfo.AuthProvider.AuthProvider)
			Expect(providerUrl).ToNot(BeEmpty(), "Auth provider URL should not be empty")
			authIp, authPort, err := util.ParseURIForIPAndPort(providerUrl)
			Expect(err).ToNot(HaveOccurred())
			Expect(authIp).ToNot(BeEmpty(), "The IP address of the auth provider should not be empty")
			Expect(authPort).ToNot(BeZero(), "The port of the auth provider should not be empty")

			restoreAuth, err := harness.SimulateNetworkFailureForCLI(authIp, authPort)
			Expect(err).ToNot(HaveOccurred())
			// ensure we restore traffic in the event an assertion below fails
			defer func() { _ = restoreAuth() }()

			By("Expire the access token again and run an action")
			err = harness.MarkClientAccessTokenExpired(configPath)
			Expect(err).NotTo(HaveOccurred())
			// we don't care about the result, only that an action that could regenerate the token should have been taken
			_, _ = harness.RunGetDevices()

			By("Token should not have been refreshed")
			cfg, err = harness.ReadClientConfig(configPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.AuthInfo.AccessToken).To(Equal(secondToken), "Token should not have been refreshed")

			By("Bring the auth service back up")
			err = restoreAuth()
			Expect(err).ToNot(HaveOccurred())

			By("Another token should be been generated")
			// we don't care about the result, only that an action that could regenerate the token should have been taken
			_, _ = harness.RunGetDevices()
			cfg, err = harness.ReadClientConfig(configPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.AuthInfo.AccessToken).ToNot(Equal(secondToken), "Token should have been refreshed")
		})

		It("CertificateSigningRequest deny flow validation", Label("85396", "sanity"),
			func() {

				harness := e2e.GetWorkerHarness()
				login.LoginToAPIWithToken(harness)

				By("CertificateSigningRequest: Resources lifecycle")
				// Prepare a unique CSR YAML and ensure cleanup
				uniqueCsrYAML, err := util.CreateUniqueYAMLFile("csr.yaml", harness.GetTestIDFromContext())
				Expect(err).ToNot(HaveOccurred())
				defer util.CleanupTempYAMLFile(uniqueCsrYAML)

				// Apply CSR
				var out string
				Eventually(func() error {
					var applyErr error
					out, applyErr = harness.CLI("apply", "-f", uniqueCsrYAML)
					return applyErr
				}).Should(BeNil(), "failed to apply CSR")
				Expect(out).To(MatchRegexp(resourceCreated))

				// Extract CSR name from YAML
				csr := harness.GetCertificateSigningRequestByYaml(uniqueCsrYAML)
				Expect(csr.Metadata.Name).ToNot(BeNil(), "csr metadata.name should be set")
				csrName := *csr.Metadata.Name
				Expect(csrName).ToNot(BeEmpty())

				By("verifying `flightctl deny -h` prints usage")
				out, err = harness.ManageResource(util.DenyAction, "-h")
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("Deny a certificate signing request."))
				Expect(out).To(ContainSubstring("Usage:"))
				Expect(out).To(ContainSubstring("flightctl deny csr/NAME"))

				By(fmt.Sprintf("denying csr/%s", csrName))
				out, err = harness.ManageResource(util.DenyAction, fmt.Sprintf("csr/%s", csrName))
				Expect(err).NotTo(HaveOccurred(), "first deny should succeed")

				By("verifying `flightctl get csr` shows CONDITION=Denied for the CSR")
				out, err = harness.CLI("get", "csr")
				Expect(err).NotTo(HaveOccurred())
				AssertTableValue(out, csrName, "CONDITION", "Denied")

				By("denying the same CSR again should fail")
				out, err = harness.ManageResource(util.DenyAction, fmt.Sprintf("csr/%s", csrName))
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring("409"))

				By("denying a non-existent CSR should fail with 404")
				out, err = harness.ManageResource(util.DenyAction, "csr/fake-does-not-exist")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring("404"))

				By("deleting the CSR before testing timeout flag")
				out, err = harness.CLI("delete", fmt.Sprintf("csr/%s", csrName))
				Expect(err).NotTo(HaveOccurred())

				By("accepting --request-timeout flag on successful deny and confirming execution within timeout")
				out, err = harness.CLI("apply", "-f", uniqueCsrYAML)
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(MatchRegexp(resourceCreated))

				start := time.Now()
				out, err = harness.ManageResource(util.DenyAction, fmt.Sprintf("csr/%s", csrName), "--request-timeout", "10")
				elapsed := time.Since(start)

				Expect(err).NotTo(HaveOccurred())
				Expect(elapsed.Seconds()).To(BeNumerically("<=", 10.5),
					fmt.Sprintf("deny took too long: %.1fs", elapsed.Seconds()))

				// Verify that the CSR was actually denied after the command
				out, err = harness.CLI("get", "csr")
				Expect(err).NotTo(HaveOccurred())
				AssertTableValue(out, csrName, "CONDITION", "Denied")

				By("calling deny with zero args should error")
				out, err = harness.ManageResource(util.DenyAction, "")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring("Error: accepts between 1 and 2 arg(s), received 0"))

				By("calling deny with empty resource should fail")
				out, err = harness.ManageResource(util.DenyAction, "csr")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring("Error: exactly one resource name must be specified"))

				By("calling deny with invalid resource kind should error")
				out, err = harness.ManageResource(util.DenyAction, "1234")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(invalidResource))

			})

	})

	It("Creates a device, edits via headless editor (yaml & json), and validates negatives", Label("83301"), func() {
		harness := e2e.GetWorkerHarness()
		login.LoginToAPIWithToken(harness)

		By("creating a unique Device from template")
		uniqueDeviceYAML, err := util.CreateUniqueYAMLFile("device.yaml", harness.GetTestIDFromContext())
		Expect(err).ToNot(HaveOccurred())
		defer util.CleanupTempYAMLFile(uniqueDeviceYAML)

		out, err := harness.ManageResource("apply", uniqueDeviceYAML)
		Expect(err).ToNot(HaveOccurred(), out)
		Expect(out).To(MatchRegexp(resourceCreated), out)

		device := harness.GetDeviceByYaml(uniqueDeviceYAML)
		Expect(device.Metadata).ToNot(BeNil())
		Expect(device.Metadata.Name).ToNot(BeNil())
		Expect(*device.Metadata.Name).ToNot(BeEmpty(), "device name should not be empty")

		devName := *device.Metadata.Name
		devicePath := "device/" + devName

		newTestKey := "e2e-prelabel"
		newTestValue := "ok"

		By("patching the device once via API to ensure it is reachable")
		Eventually(func() error {
			return harness.UpdateDevice(devName, func(d *v1beta1.Device) {
				if d.Metadata.Labels == nil {
					d.Metadata.Labels = &map[string]string{}
				}
				(*d.Metadata.Labels)[newTestKey] = newTestValue
			})
		}).Should(BeNil(), "failed to update device preliminarily")

		// -----------------------------
		// Positive: headless edit (YAML)
		// -----------------------------
		By("editing the Device via headless editor in YAML mode")
		markerYAML := "autotest-edit-yaml-" + time.Now().Format("150405")

		editorYAML, err := harness.HeadlessEditorWrapper(markerYAML)
		Expect(err).ToNot(HaveOccurred())

		// Use retry helper to tolerate 409 conflicts
		_, err = harness.EditWithRetry("yaml", editorYAML, devicePath)
		Expect(err).ToNot(HaveOccurred())

		yamlOut, err := harness.GetYAML(devicePath)
		Expect(err).ToNot(HaveOccurred())
		Expect(yamlOut).To(ContainSubstring("autotest-edit: " + markerYAML))

		// -----------------------------
		// Positive: headless edit (JSON)
		// -----------------------------
		By("editing the Device via headless editor in JSON mode")
		markerJSON := "autotest-edit-json-" + time.Now().Format("150405")

		editorJSON, err := harness.HeadlessEditorWrapper(markerJSON)
		Expect(err).ToNot(HaveOccurred())

		_, err = harness.EditWithRetry("json", editorJSON, devicePath)
		Expect(err).ToNot(HaveOccurred())

		yamlOut, err = harness.GetYAML(devicePath)
		Expect(err).ToNot(HaveOccurred())
		Expect(yamlOut).To(ContainSubstring("autotest-edit: " + markerJSON))

		// -----------------------------
		// Negative tests
		// -----------------------------
		By("failing when no arguments are provided")
		out, err = harness.CLI("edit")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("Error: accepts between 1 and 2 arg(s), received 0"))

		By("failing on invalid resource kind (numeric)")
		out, err = harness.CLI("edit", "1234")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("Error: invalid resource kind: 1234"))

		By("failing on invalid resource kind (empty string)")
		out, err = harness.CLI("edit", "")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("Error: invalid resource kind:"))

		By("failing when too many arguments are provided")
		out, err = harness.CLI("edit", "1", "2", "3", "4")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("Error: accepts between 1 and 2 arg(s), received 4"))
	})

	It("generates completion and can be sourced for each supported shell (harness.CLI only for flightctl calls)", Label("85470"), func() {
		harness := e2e.GetWorkerHarness()
		login.LoginToAPIWithToken(harness)

		type shellCase struct {
			name      string
			markers   []string
			syntaxCmd func(ctx context.Context, p string) *exec.Cmd
			sourceCmd func(ctx context.Context, p, tempHome string) *exec.Cmd
			tmpName   string
		}

		tmpRoot := GinkgoT().TempDir()
		mkHome := func(tag string) (home, tmp string) {
			home = filepath.Join(tmpRoot, "home_"+tag)
			tmp = filepath.Join(home, "tmp")
			Expect(os.MkdirAll(tmp, 0o755)).To(Succeed())
			return
		}
		write0600 := func(dir, name, data string) string {
			p := filepath.Join(dir, name)
			// G306: use 0600
			Expect(os.WriteFile(p, []byte(data), 0o600)).To(Succeed())
			return p
		}

		shells := []shellCase{
			{
				name:    "bash",
				markers: []string{"# bash completion for flightctl", "__flightctl_"},
				tmpName: "flightctl.bash",
				syntaxCmd: func(ctx context.Context, p string) *exec.Cmd {
					if !util.BinaryExistsOnPath("bash") {
						return nil
					}
					// No tainted args: constant argv, script path via env
					cmd := exec.CommandContext(ctx, "bash", "-n", p)
					return cmd
				},
				sourceCmd: func(ctx context.Context, p, home string) *exec.Cmd {
					if !util.BinaryExistsOnPath("bash") {
						return nil
					}
					// Use env var to pass path; constant argv avoids G204
					cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-lc", `source "$SCRIPT"`)
					cmd.Env = append(os.Environ(), "HOME="+home, "SCRIPT="+p)
					return cmd
				},
			},
			{
				name:    "zsh",
				markers: []string{"#compdef flightctl", "_flightctl"},
				tmpName: "_flightctl",
				syntaxCmd: func(ctx context.Context, p string) *exec.Cmd {
					if !util.BinaryExistsOnPath("zsh") {
						return nil
					}
					return exec.CommandContext(ctx, "zsh", "-n", p)
				},
				sourceCmd: func(ctx context.Context, p, home string) *exec.Cmd {
					if !util.BinaryExistsOnPath("zsh") {
						return nil
					}
					cmd := exec.CommandContext(ctx, "zsh", "-f", "-lc", `source "$SCRIPT"`)
					cmd.Env = append(os.Environ(), "HOME="+home, "SCRIPT="+p)
					return cmd
				},
			},
			{
				name:    "fish",
				markers: []string{"complete -c flightctl"},
				tmpName: "flightctl.fish",
				syntaxCmd: func(ctx context.Context, p string) *exec.Cmd {
					if !util.BinaryExistsOnPath("fish") {
						return nil
					}
					return exec.CommandContext(ctx, "fish", "-n", p)
				},
				sourceCmd: func(ctx context.Context, p, home string) *exec.Cmd {
					if !util.BinaryExistsOnPath("fish") {
						return nil
					}
					cmd := exec.CommandContext(ctx, "fish", "--no-config", "-lc", `source "$SCRIPT"`)
					cmd.Env = append(os.Environ(),
						"HOME="+home,
						"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
						"SCRIPT="+p,
					)
					return cmd
				},
			},
			{
				name:    "powershell",
				markers: []string{"Register-ArgumentCompleter", "flightctl"},
				tmpName: "completion.ps1",
				syntaxCmd: func(ctx context.Context, p string) *exec.Cmd {
					if !util.BinaryExistsOnPath("pwsh") {
						return nil
					}
					// Dot-source via env var
					return exec.CommandContext(ctx, "pwsh", "-NoLogo", "-NoProfile", "-Command", `. $env:SCRIPT`)
				},
				sourceCmd: func(ctx context.Context, p, home string) *exec.Cmd {
					if !util.BinaryExistsOnPath("pwsh") {
						return nil
					}
					cmd := exec.CommandContext(ctx, "pwsh", "-NoLogo", "-NoProfile", "-Command", `. $env:SCRIPT`)
					cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "SCRIPT="+p)
					return cmd
				},
			},
		}

		for _, sc := range shells {
			home, tmp := mkHome(sc.name)

			// Check if generation is supported
			if sc.name == "powershell" && runtime.GOOS != "windows" {
				By("powershell completion may not be supported on non-Windows — skipping")
				continue
			}

			By("generating " + sc.name + " completion via harness.CLI")
			out, err := harness.CLI("completion", sc.name) // the only place we invoke flightctl
			// Note: if your binary doesn’t support powershell on Linux, you can gate that here
			Expect(err).NotTo(HaveOccurred(), "generation failed for %s", sc.name)
			Expect(strings.TrimSpace(out)).NotTo(BeEmpty(), "empty completion for %s", sc.name)
			for _, m := range sc.markers {
				Expect(out).To(ContainSubstring(m), "missing marker %q for %s", m, sc.name)
			}

			By("saving script to a temp file with 0600 perms")
			path := write0600(tmp, sc.tmpName, out)

			By("light syntax check (if shell present): " + sc.name)
			{
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if cmd := sc.syntaxCmd(ctx, path); cmd != nil {
					cmd.Env = append(os.Environ(), "SCRIPT="+path) // for pwsh syntax step
					cmd.Stdout, cmd.Stderr = GinkgoWriter, GinkgoWriter
					Expect(cmd.Run()).To(Succeed())
				} else {
					By(sc.name + " not available — skipping syntax check")
				}
			}

			By("sourcing in a non-interactive subshell (no profiles/rc): " + sc.name)
			{
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if cmd := sc.sourceCmd(ctx, path, home); cmd != nil {
					cmd.Stdout, cmd.Stderr = GinkgoWriter, GinkgoWriter
					Expect(cmd.Run()).To(Succeed())
				} else {
					By(sc.name + " not available — skipping source step")
				}
			}
		}
		By("Creating resources for autocompletion validation")

		// Create resources  so names exist for completion
		var (
			devName   string
			fleetName string
			repoName  string
			erName    string
			csrName   string
		)

		//Creating a device
		devYAML, err := util.CreateUniqueYAMLFile("device.yaml", harness.GetTestIDFromContext())
		Expect(err).ToNot(HaveOccurred())
		defer util.CleanupTempYAMLFile(devYAML)
		out, err := harness.ManageResource(applyOperation, devYAML)
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(MatchRegexp(resourceCreated))
		device := harness.GetDeviceByYaml(devYAML)
		devName = *device.Metadata.Name

		// Creating a fleet
		fleetYAML, err := util.CreateUniqueYAMLFile("fleet.yaml", harness.GetTestIDFromContext())
		Expect(err).ToNot(HaveOccurred())
		defer util.CleanupTempYAMLFile(fleetYAML)
		out, err = harness.ManageResource(applyOperation, fleetYAML)
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(MatchRegexp(resourceCreated))
		fleet := harness.GetFleetByYaml(fleetYAML)
		fleetName = *fleet.Metadata.Name

		//Creating a repo
		repoYAML, err := util.CreateUniqueYAMLFile("repository-flightctl.yaml", harness.GetTestIDFromContext())
		Expect(err).ToNot(HaveOccurred())
		defer util.CleanupTempYAMLFile(repoYAML)
		out, err = harness.ManageResource(applyOperation, repoYAML)
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(MatchRegexp(resourceCreated))
		repo := harness.GetRepositoryByYaml(repoYAML)
		repoName = *repo.Metadata.Name

		//Creating ER
		erYAMLPath, err := CreateTestERAndWriteToTempFile()
		Expect(err).ToNot(HaveOccurred())
		defer os.Remove(erYAMLPath)
		out, err = harness.ManageResource(applyOperation, erYAMLPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(MatchRegexp(resourceCreated))
		er := harness.GetEnrollmentRequestByYaml(erYAMLPath)
		erName = *er.Metadata.Name

		//Creating CSR
		csrYAML, err := util.CreateUniqueYAMLFile("csr.yaml", harness.GetTestIDFromContext())
		Expect(err).ToNot(HaveOccurred())
		defer util.CleanupTempYAMLFile(csrYAML)
		Eventually(func() error {
			_, err := harness.CLI(applyOperation, "-f", csrYAML)
			return err
		}).Should(BeNil(), "failed to apply CSR")
		csr := harness.GetCertificateSigningRequestByYaml(csrYAML)
		csrName = *csr.Metadata.Name
		Expect(csrName).NotTo(BeEmpty())

		By("Autocompletion: Scenario A (get <resource>/<prefix> AND get <resource> <prefix>)")
		// Slash form: get resource/prefix
		ExpectCompletion(harness, []string{"get", fmt.Sprintf("%s/%s", util.Device, devName[:1])}, devName)
		ExpectCompletion(harness, []string{"get", fmt.Sprintf("%s/%s", util.Fleet, fleetName[:1])}, fleetName)
		ExpectCompletion(harness, []string{"get", fmt.Sprintf("%s/%s", util.CertificateSigningRequest, csrName[:1])}, csrName)
		ExpectCompletion(harness, []string{"get", fmt.Sprintf("%s/%s", util.Repository, repoName[:1])}, repoName)
		ExpectCompletion(harness, []string{"get", fmt.Sprintf("%s/%s", util.EnrollmentRequest, erName[:1])}, erName)
		// Space form: get resource prefix
		ExpectCompletion(harness, []string{"get", util.Device, devName[:1]}, devName)
		ExpectCompletion(harness, []string{"get", util.Fleet, fleetName[:1]}, fleetName)
		ExpectCompletion(harness, []string{"get", util.CertificateSigningRequest, csrName[:1]}, csrName)
		ExpectCompletion(harness, []string{"get", util.Repository, repoName[:1]}, repoName)
		ExpectCompletion(harness, []string{"get", util.EnrollmentRequest, erName[:1]}, erName)

		By("Autocompletion: Scenario B (edit <resource>/<prefix> AND edit <resource> <prefix>)")
		// Edit currently supports device, fleet, certificateSigningRequest, and repository — no enrollmentrequests.
		// Slash
		ExpectCompletion(harness, []string{"edit", fmt.Sprintf("%s/%s", util.Device, devName[:1])}, devName)
		ExpectCompletion(harness, []string{"edit", fmt.Sprintf("%s/%s", util.Fleet, fleetName[:1])}, fleetName)
		ExpectCompletion(harness, []string{"edit", fmt.Sprintf("%s/%s", util.CertificateSigningRequest, csrName[:1])}, csrName)
		ExpectCompletion(harness, []string{"edit", fmt.Sprintf("%s/%s", util.Repository, repoName[:1])}, repoName)
		// Space
		ExpectCompletion(harness, []string{"edit", util.Device, devName[:1]}, devName)
		ExpectCompletion(harness, []string{"edit", util.Fleet, fleetName[:1]}, fleetName)
		ExpectCompletion(harness, []string{"edit", util.CertificateSigningRequest, csrName[:1]}, csrName)
		ExpectCompletion(harness, []string{"edit", util.Repository, repoName[:1]}, repoName)

		By("Autocompletion: Scenario C (approve csr/er <prefix> in both ways)")
		// Slash
		ExpectCompletion(harness, []string{"approve", fmt.Sprintf("%s/%s", util.CertificateSigningRequest, csrName[:1])}, csrName)
		ExpectCompletion(harness, []string{"approve", fmt.Sprintf("%s/%s", util.EnrollmentRequest, erName[:1])}, erName)
		// Space
		ExpectCompletion(harness, []string{"approve", util.CertificateSigningRequest, csrName[:1]}, csrName)
		ExpectCompletion(harness, []string{"approve", util.EnrollmentRequest, erName[:1]}, erName)

		By("Autocompletion: Scenario D (deny csr <prefix> in both ways)")
		// Deny only supports certificateSigningRequest, not enrollmentrequest.
		// Slash
		ExpectCompletion(harness, []string{"deny", fmt.Sprintf("%s/%s", util.CertificateSigningRequest, csrName[:1])}, csrName)
		// Space
		ExpectCompletion(harness, []string{"deny", util.CertificateSigningRequest, csrName[:1]}, csrName)

		By("Autocompletion: Scenario E (decommission device <prefix> in both ways)")
		// Slash
		ExpectCompletion(harness, []string{"decommission", fmt.Sprintf("%s/%s", util.Device, devName[:1])}, devName)
		// Space
		ExpectCompletion(harness, []string{"decommission", util.Device, devName[:1]}, devName)
	})

})

// formatResourceEvent formats the event's message and returns it as a string
func formatResourceEvent(resource, name, action string) string {
	return fmt.Sprintf("%s\\s+%s\\s+Normal\\s+%s\\s+was\\s+%s\\s+successfully", resource, name, resource, action)
}

// DeleteWithoutNameTestParams defines the parameters for delete-without-name tests.
type DeleteWithoutNameTestParams struct {
	ResourceArg string
}

// For delete-without-name test cases
func DeleteEntryCase(desc string, resourceArg string) util.TestCase[DeleteWithoutNameTestParams] {
	return util.TestCase[DeleteWithoutNameTestParams]{
		Description: desc,
		Params: DeleteWithoutNameTestParams{
			ResourceArg: resourceArg,
		},
	}
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

// Comparing devices count output in yaml with fleet data in map
func compareDeviceCountCliOutput(output string, expectedDevicesCount map[string]int64) []string {

	var notMatched []string

	type fleetData struct {
		Name   string `yaml:"name"`
		Status struct {
			DevicesSummary struct {
				Total int64 `yaml:"total"`
			} `yaml:"devicesSummary"`
		} `yaml:"status"`
	}

	var parsed struct {
		Fleets []fleetData `yaml:"items"`
	}

	err := yaml.Unmarshal([]byte(output), &parsed)
	if err != nil {
		return []string{"Failed to parse YAML: " + err.Error()}
	}

	// Compare each item against expected map
	for _, fleet := range parsed.Fleets {
		expectedCount, ok := expectedDevicesCount[fleet.Name]
		if !ok || fleet.Status.DevicesSummary.Total != expectedCount {
			notMatched = append(notMatched, fleet.Name)
		}
	}

	return notMatched
}

// collapse collapses all whitespace in a string into single spaces.
func collapse(s string) string {
	fields := strings.Fields(strings.TrimSpace(s))
	return strings.Join(fields, " ")
}

// Creating a test fleet
func createTestFleet(fleetTestManager *fleetTestManager, fleetDevicesCount map[string]int64, originalYamlPath string, fleetIdentifier string) (string, error) {
	uniqueFleetYAML, err := util.CreateUniqueYAMLFile(originalYamlPath, fleetTestManager.testID)
	if err != nil {
		return "", err
	}

	// Checking the fleet was created and updating in map
	out, err := fleetTestManager.harness.ManageResource(applyOperation, uniqueFleetYAML)

	if strings.Contains(fleetIdentifier, "fleetA") {
		fleetTestManager.fleetA = fleetTestManager.harness.GetFleetByYaml(uniqueFleetYAML)
		fleetTestManager.uniqueFleetAYAML = uniqueFleetYAML
		fleetTestManager.fleetAName = *fleetTestManager.fleetA.Metadata.Name
		fleetDevicesCount[fleetTestManager.fleetAName] = 0
	} else if strings.Contains(fleetIdentifier, "fleetB") {
		fleetTestManager.fleetB = fleetTestManager.harness.GetFleetByYaml(uniqueFleetYAML)
		fleetTestManager.uniqueFleetBYAML = uniqueFleetYAML
		fleetTestManager.fleetBName = *fleetTestManager.fleetB.Metadata.Name
		fleetDevicesCount[fleetTestManager.fleetBName] = 0
	}

	GinkgoWriter.Printf("Created fleet: %s and set 0 in devices count\n", fleetIdentifier)
	return out, err
}

// Creating a device in a fleet
func createDeviceInFleet(fleetTestManager *fleetTestManager, fleetDevicesCount map[string]int64, originalYamlPath string, fleetIdentifier string) (string, error) {

	uniqueDeviceYAML, err := util.CreateUniqueYAMLFile(originalYamlPath, fleetTestManager.testID)
	if err != nil {
		return "", err
	}

	// Checking the device was created and updating in map
	out, err := fleetTestManager.harness.ManageResource(applyOperation, uniqueDeviceYAML)
	fleetTestManager.device = fleetTestManager.harness.GetDeviceByYaml(uniqueDeviceYAML)
	fleetTestManager.deviceName = *fleetTestManager.device.Metadata.Name
	fleetTestManager.uniqueDeviceYAML = uniqueDeviceYAML

	if strings.Contains(fleetIdentifier, "fleetA") {
		fleetDevicesCount[fleetTestManager.fleetAName]++
	} else if strings.Contains(fleetIdentifier, "fleetB") {
		fleetDevicesCount[fleetTestManager.fleetBName]++
	}

	GinkgoWriter.Printf("Created a device in fleet: %s and updated devices count\n", fleetIdentifier)
	return out, err
}

// Checking the status of devices in fleet and devices count
func checkDevicesInFleetStatus(harness *e2e.Harness, fleetDevicesCount map[string]int64) ([]string, error) {

	out, err := harness.CLI("get", "fleet", "-s", "-o", "yaml")

	notMatched := compareDeviceCountCliOutput(out, fleetDevicesCount)

	// Printing unmatched fleets names
	for _, fleet := range notMatched {
		GinkgoWriter.Printf("Unmatched fleet: %s\n", fleet)
	}

	return notMatched, err
}

// Checking the devicesSummary of a fleet
func validateDevicesSummary(harness *e2e.Harness, fleetName string, expectedDevicesCount int) (int, error) {

	total := 0

	out, err := harness.CLI("get", "fleet/"+fleetName, "-s", "-o", "json")
	if err != nil {
		return total, err
	}

	// Parsing output to get the fleet data in json format
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return total, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Extracting devicesSummary from output
	status, _ := parsed["status"].(map[string]any)
	devicesSummary, ok := status["devicesSummary"].(map[string]any)
	if !ok {
		return total, fmt.Errorf("missing devicesSummary")
	}

	// Extracting total device count
	totalVal, ok := devicesSummary["total"].(int64)
	if !ok {
		return total, fmt.Errorf("missing or invalid 'total' in devicesSummary")
	}
	total = int(totalVal)

	for _, statusKey := range []string{"applicationStatus", "summaryStatus", "updateStatus"} {
		statusMap, _ := devicesSummary[statusKey].(map[string]any)

		// Map should be empty if there are no devices
		if expectedDevicesCount == 0 {
			if len(statusMap) > 0 {
				return total, fmt.Errorf("%s should be empty but has %d entries", statusKey, len(statusMap))
			}
		} else {

			// All value of the keys must be equal to expectedDevicesCount
			for _, value := range statusMap {
				count := value.(int64)
				if int(count) != expectedDevicesCount {
					return total, fmt.Errorf("%s has wrong value: %d, expected %d", statusKey, count, expectedDevicesCount)
				}
			}
		}
	}

	return total, err
}

// AssertTableValue checks that a specific row (identified by resourceName)
// has the expected value under the specified column name in the table output.
func AssertTableValue(out, resourceName, column, expected string) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	Expect(len(lines)).To(BeNumerically(">=", 2),
		fmt.Sprintf("expected at least one data row in output:\n%s", out))

	header := strings.Fields(lines[0])
	colIdx := slices.Index(header, column)
	Expect(colIdx).To(BeNumerically(">=", 0),
		fmt.Sprintf("column %q not found in headers: %v", column, header))

	for _, line := range lines[1:] {
		cols := strings.Fields(line)
		if len(cols) <= colIdx {
			continue
		}
		if cols[0] == resourceName {
			Expect(cols[colIdx]).To(Equal(expected),
				fmt.Sprintf("expected %s[%s] = %s, got %s",
					resourceName, column, expected, cols[colIdx]))
			return
		}
	}

	Fail(fmt.Sprintf("expected resource %q in output but not found:\n%s", resourceName, out))
}

// CLIRunner is the minimal surface we need from the harness.
type CLIRunner interface {
	CLI(args ...string) (string, error)
}

// ExpectCompletion runs `flightctl __complete <args...>` and asserts that a suggestion contains `expected`.
func ExpectCompletion(h CLIRunner, args []string, expected string) {
	out, err := h.CLI(append([]string{"__complete"}, args...)...)
	Expect(err).ToNot(HaveOccurred(), "completion failed for: flightctl %s", strings.Join(args, " "))
	// Cobra prints candidates one-per-line, sometimes with "\t" description and a directive line at the end.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	Expect(strings.Join(lines, "\n")).To(ContainSubstring(expected),
		"expected completion %q for args: flightctl %s\nFull output:\n%s",
		expected, strings.Join(args, " "), out)
}

// completeFleetYaml defines a YAML template for creating a Fleet resource with specified metadata and spec configuration.
const completeFleetYaml = `
apiVersion: v1beta1
kind: Fleet
metadata:
    name: e2e-test-fleet
spec:
    selector:
        matchLabels:
            fleet: label-for-standalone-fleet-test
    template:
        spec:
            os:
                image: quay.io/redhat/rhde:9.2
`

// incompleteFleetYaml defines a YAML configuration string for a Fleet resource with minimal and incomplete fields.
const incompleteFleetYaml = `
apiVersion: v1beta1
kind: Fleet
metadata:
    name: e2e-test-fleet
spec:
    selector:
        matchLabels:
            fleet: label-for-standalone-fleet-test
`

var (
	newTestKey = "testKey"

	// newTestValue holds the string value "newValue" used as a test variable in the application.
	newTestValue = "newValue"
)
