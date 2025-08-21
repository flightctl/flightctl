package cli_test

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var (
	unspecifiedResource = "Error: name must be specified when deleting"
	resourceCreated     = `(200 OK|201 Created)`
)

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

			By("Should work for a complete fleet")
			uniqueFleetYAML, err := util.CreateUniqueYAMLFile("fleet.yaml", harness.GetTestIDFromContext())
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueFleetYAML)

			out, err = harness.ManageResource("apply", uniqueFleetYAML)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("201 Created"))

			// Applying a 2nd time it should also work, the fleet is just updated
			out, err = harness.ManageResource("apply", uniqueFleetYAML)
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
			_, err = harness.CLIWithStdin(completeFleetYaml, "apply", "-f", "-")
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

			out, err := harness.ManageResource("apply", uniqueDeviceYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			device := harness.GetDeviceByYaml(uniqueDeviceYAML)
			Expect(*device.Metadata.Name).ToNot(BeEmpty(), "device name should not be empty")

			devName := *device.Metadata.Name
			Eventually(func() error {
				err := harness.UpdateDevice(devName, func(device *v1alpha1.Device) {
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

			out, err = harness.ManageResource("apply", uniqueFleetYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			fleet := harness.GetFleetByYaml(uniqueFleetYAML)
			Expect(fleet.Spec.Template).ToNot(BeNil(), "fleet template should not be nil")
			Expect(fleet.Spec.Selector).ToNot(BeNil(), "fleet selector should not be nil")
			fleetName := *fleet.Metadata.Name

			By("Updating Fleet labels")
			Eventually(func() error {
				err := harness.UpdateFleet(fleetName, func(fleet *v1alpha1.Fleet) {
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
				out, err = harness.ManageResource("apply", uniqueRepoYAML)
				return err
			}).Should(BeNil(), "failed to apply Repository")
			Expect(out).To(MatchRegexp(resourceCreated))

			repo := harness.GetRepositoryByYaml(uniqueRepoYAML)

			//Update repo name
			updatedName := "flightctl-new-" + harness.GetTestIDFromContext()
			*repo.Metadata.Name = updatedName
			repoData, err := yaml.Marshal(&repo)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(repoData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(updatedName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.Repository, updatedName))
			Expect(err).ToNot(HaveOccurred())

			By("ResourceSync: Resources lifecycle")
			uniqueResourceSyncYAML, err := util.CreateUniqueYAMLFile("resourcesync.yaml", harness.GetTestIDFromContext())
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueResourceSyncYAML)

			out, err = harness.ManageResource("apply", uniqueResourceSyncYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			rSync := harness.GetResourceSyncByYaml(uniqueResourceSyncYAML)

			//Update rSync name
			rSyncNewName := "flightctl-new-" + harness.GetTestIDFromContext()
			*rSync.Metadata.Name = rSyncNewName
			rSyncData, err := yaml.Marshal(&rSync)
			Expect(err).ToNot(HaveOccurred())
			out, err = harness.CLIWithStdin(string(rSyncData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(rSyncNewName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.ResourceSync, rSyncNewName))
			Expect(err).ToNot(HaveOccurred())

			By("EnrollmentRequest: Resources lifecycle")
			erYAMLPath, err := CreateTestERAndWriteToTempFile()
			Expect(err).ToNot(HaveOccurred())
			defer os.Remove(erYAMLPath)

			out, err = harness.ManageResource("apply", erYAMLPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			er := harness.GetEnrollmentRequestByYaml(erYAMLPath)

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
			uniqueCsrYAML, err := util.CreateUniqueYAMLFile("csr.yaml", harness.GetTestIDFromContext())
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueCsrYAML)

			Eventually(func() error {
				out, err = harness.CLI("apply", "-f", uniqueCsrYAML)
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
			out, err = harness.CLIWithStdin(string(csrData), "apply", "-f", "-")

			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(csrNewName))

			_, err = harness.CLI("delete", fmt.Sprintf("%s/%s", util.CertificateSigningRequest, csrNewName))
			Expect(err).ToNot(HaveOccurred())
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

			out, err := harness.ManageResource("apply", uniqueDeviceYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))
			device1 := harness.GetDeviceByYaml(uniqueDeviceYAML)
			device1Name := *device1.Metadata.Name

			out, err = harness.ManageResource("apply", uniqueDeviceBYAML)
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
				out, err := harness.ManageResource("apply", file)
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
		It("should show matching client and server versions", Label("79621", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

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

				By("Retry login with the invalid password")
				invalidPassword := "passW0RD"
				loginArgsPassword := append(loginArgs, "-k", "-u", "demouser", "-p", invalidPassword)

				GinkgoWriter.Printf("Executing CLI with args: %v\n", loginArgsPassword)
				out, _ = harness.CLI(loginArgsPassword...)
				// We don't check for error here as we're only interested in the output message
				Expect(out).To(Or(
					ContainSubstring("Invalid user credentials"),
					ContainSubstring("unexpected http code: 401")))
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
			initialToken := cfg.AuthInfo.Token

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
			secondToken := cfg.AuthInfo.Token
			Expect(secondToken).ToNot(Equal(initialToken), "Token should have been refreshed")

			By("Remove connectivity to the auth service")
			providerUrl := cfg.AuthInfo.AuthProvider.Config[client.AuthUrlKey]
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
			Expect(cfg.AuthInfo.Token).To(Equal(secondToken), "Token should not have been refreshed")

			By("Bring the auth service back up")
			err = restoreAuth()
			Expect(err).ToNot(HaveOccurred())

			By("Another token should be been generated")
			// we don't care about the result, only that an action that could regenerate the token should have been taken
			_, _ = harness.RunGetDevices()
			cfg, err = harness.ReadClientConfig(configPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.AuthInfo.Token).ToNot(Equal(secondToken), "Token should have been refreshed")
		})
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

// completeFleetYaml defines a YAML template for creating a Fleet resource with specified metadata and spec configuration.
const completeFleetYaml = `
apiVersion: v1alpha1
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
apiVersion: v1alpha1
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
