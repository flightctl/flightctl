package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var (
	invalidSyntax = "invalid syntax"
	kind          = "involvedObject.kind"
	fieldSelector = "--field-selector"
	limit         = "--limit"
	jsonFlag      = "-ojson"
)

var _ = Describe("cli events operation", func() {
	BeforeEach(func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()
		login.LoginToAPIWithToken(harness)
	})

	Context("Events API Tests", func() {
		It("should list events resource is created/updated/deleted", Label("81779", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			var deviceName, fleetName, repoName string
			var er *v1beta1.EnrollmentRequest

			// Generate unique test ID for this test
			testID := harness.GetTestIDFromContext()

			// Create unique YAML files for this test
			uniqueDeviceYAML, err := util.CreateUniqueYAMLFile(util.DeviceYAMLName, testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueDeviceYAML)

			uniqueFleetYAML, err := util.CreateUniqueYAMLFile(util.FleetYAMLName, testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueFleetYAML)

			uniqueRepoYAML, err := util.CreateUniqueYAMLFile(util.RepoYAMLName, testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueRepoYAML)

			// Create unique enrollment request
			erYAMLPath, err := CreateTestERAndWriteToTempFile()
			Expect(err).ToNot(HaveOccurred())
			defer os.Remove(erYAMLPath)

			resources := []struct {
				resourceType string
				yamlPath     string
			}{
				{util.DeviceResource, uniqueDeviceYAML},
				{util.FleetResource, uniqueFleetYAML},
				{util.RepoResource, uniqueRepoYAML},
				{util.ErResource, erYAMLPath},
			}

			By("Applying resources: device, fleet, repo, enrollment request")
			for _, r := range resources {
				_, err := harness.ManageResource(util.ApplyAction, r.yamlPath)
				Expect(err).ToNot(HaveOccurred())

				switch r.resourceType {
				case util.DeviceResource:
					device := harness.GetDeviceByYaml(r.yamlPath)
					deviceName = *device.Metadata.Name
				case util.FleetResource:
					fleet := harness.GetFleetByYaml(r.yamlPath)
					fleetName = *fleet.Metadata.Name
				case util.RepoResource:
					repo := harness.GetRepositoryByYaml(r.yamlPath)
					repoName = *repo.Metadata.Name
				case util.ErResource:
					out, err := harness.CLI(util.ApplyAction, util.ForceFlag, r.yamlPath)
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(MatchRegexp(`(200 OK|201 Created)`))
					er = harness.GetEnrollmentRequestByYaml(r.yamlPath)
				}
			}

			By("Verifying Created events")
			createdEvents, err := verifyEventsByReason(harness, resources, deviceName, fleetName, repoName, er, util.ResourceCreated)

			Expect(err).ToNot(HaveOccurred())
			Expect(len(createdEvents)).To(BeZero(), fmt.Sprintf("Missing created events for: %v", createdEvents))

			By("Reapplying resources (updates)")
			for _, r := range resources {
				// Read the YAML file and add a test-update label
				yamlData, err := os.ReadFile(r.yamlPath)
				Expect(err).ToNot(HaveOccurred())

				// Parse YAML and add test-update label
				var resource map[string]interface{}
				err = yaml.Unmarshal(yamlData, &resource)
				Expect(err).ToNot(HaveOccurred())

				// Ensure metadata exists
				if resource["metadata"] == nil {
					resource["metadata"] = make(map[string]interface{})
				}
				metadata := resource["metadata"].(map[string]interface{})

				// Ensure labels exist
				if metadata["labels"] == nil {
					metadata["labels"] = make(map[string]interface{})
				}
				labels := metadata["labels"].(map[string]interface{})

				// Add test-update label
				labels["test-update"] = "true"

				// Marshal back to YAML
				modifiedYaml, err := yaml.Marshal(&resource)
				Expect(err).ToNot(HaveOccurred())
				yamlStr := string(modifiedYaml)

				_, err = harness.CLIWithStdin(yamlStr, "apply", "-f", "-")
				Expect(err).ToNot(HaveOccurred())

				// Update resource names for verification
				switch r.resourceType {
				case util.DeviceResource:
					device := harness.GetDeviceByYaml(r.yamlPath)
					deviceName = *device.Metadata.Name
				case util.FleetResource:
					fleet := harness.GetFleetByYaml(r.yamlPath)
					fleetName = *fleet.Metadata.Name
				case util.RepoResource:
					repo := harness.GetRepositoryByYaml(r.yamlPath)
					repoName = *repo.Metadata.Name
				case util.ErResource:
					er = harness.GetEnrollmentRequestByYaml(r.yamlPath)
				}
			}

			By("Verifying updated events")
			// Check for all resources since we made changes to all of them
			updatedEvents, err := verifyEventsByReason(harness, resources, deviceName, fleetName, repoName, er, "ResourceUpdated")
			Expect(err).ToNot(HaveOccurred())
			Expect(len(updatedEvents)).To(BeZero(), fmt.Sprintf("Missing updated events for: %v", updatedEvents))

			By("Querying events with fieldSelector kind=Device")
			out, err := harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=%s", kind, util.DeviceResource))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(formatResourceEvent(util.DeviceResource, deviceName, util.EventCreated)))

			By("Querying events with fieldSelector kind=Fleet")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=%s", kind, util.FleetResource))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(formatResourceEvent(util.FleetResource, fleetName, util.EventCreated)))

			By("Querying events with fieldSelector kind=Repository")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=%s", kind, util.RepoResource))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(formatResourceEvent(util.RepoResource, repoName, util.EventCreated)))

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
			Expect(out).To(MatchRegexp(formatResourceEvent(util.DeviceResource, deviceName, util.EventCreated)))
			Expect(out).To(ContainSubstring("Normal"))

			By("Querying with an invalid fieldSelector key")
			out, err = harness.RunGetEvents(fieldSelector, "invalidField=xyz")
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("unable to resolve selector name"))

			By("Querying with an unknown kind in fieldSelector")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=AlienDevice", kind))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(ContainSubstring("Normal"))

			By("Querying events with fieldSelector kind=System")
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("%s=%s", kind, util.SystemResource))
			Expect(err).ToNot(HaveOccurred())
			// System events are only generated during restore operations, so we expect no results in normal CLI tests
			// But the filter should work without errors and return empty results
			Expect(out).ToNot(ContainSubstring("Error"))
			Expect(out).ToNot(ContainSubstring("unable to resolve selector name"))

			By("Deleting the resource")
			_, err = harness.ManageResource("delete", util.Device, deviceName)
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
			Expect(out).To(ContainSubstring("you cannot get individual events"))

			By("fetching the next page of events using the continue flag", func() {
				page, err := getEventsPage(harness, limit, "1", jsonFlag)
				Expect(err).ToNot(HaveOccurred())
				Expect(page.Items).To(HaveLen(1))
				Expect(page.Metadata.Continue).ToNot(BeNil(), "expected non-nil continue token")

				nextPage, err := getEventsPage(harness, "--continue", *page.Metadata.Continue, jsonFlag)
				Expect(err).ToNot(HaveOccurred())
				Expect(nextPage.Items).ToNot(BeEmpty())
			})

			By("verifying that events are sorted by creationTimestamp descending", func() {
				page, err := getEventsPage(harness, jsonFlag)
				Expect(err).ToNot(HaveOccurred())
				timestamps, err := extractTimestamps(page.Items)
				Expect(err).ToNot(HaveOccurred())

				for i := 1; i < len(timestamps); i++ {
					Expect(timestamps[i-1].After(timestamps[i]) || timestamps[i-1].Equal(timestamps[i])).To(BeTrue(),
						"Events should be sorted descending by creationTimestamp")
				}
			})

		})

		It("should show events for device configuration validation", Label("sanity", "83585"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			// Generate unique test ID for this test
			testID := harness.GetTestIDFromContext()

			// Create unique device YAML file for this test
			uniqueDeviceYAML, err := util.CreateUniqueYAMLFile(util.DeviceYAMLName, testID)
			Expect(err).ToNot(HaveOccurred())
			defer util.CleanupTempYAMLFile(uniqueDeviceYAML)

			By("Creating a device from YAML")
			out, err := harness.ManageResource("apply", uniqueDeviceYAML)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			// Get the device details from the YAML
			device := harness.GetDeviceByYaml(uniqueDeviceYAML)
			deviceName := *device.Metadata.Name

			By("Making a change to device spec that breaks the configuration")
			// Create invalid device spec with non-existent repository
			nonExistentRepo := "non-existent"
			invalidDevice := device
			gitConfig := v1beta1.GitConfigProviderSpec{
				Name: "base",
				GitRef: struct {
					Path           string `json:"path"`
					Repository     string `json:"repository"`
					TargetRevision string `json:"targetRevision"`
				}{
					Repository:     nonExistentRepo,
					TargetRevision: "main",
					Path:           "/some/path",
				},
			}
			gitItem := v1beta1.ConfigProviderSpec{}
			err = gitItem.FromGitConfigProviderSpec(gitConfig)
			Expect(err).ToNot(HaveOccurred())

			invalidDevice.Spec = &v1beta1.DeviceSpec{
				Os: &v1beta1.DeviceOsSpec{
					Image: "quay.io/redhat/rhde:9.2",
				},
				Config: &[]v1beta1.ConfigProviderSpec{gitItem},
			}

			// Apply the invalid configuration
			deviceData, err := json.Marshal(&invalidDevice)
			Expect(err).ToNot(HaveOccurred())

			out, err = harness.CLIWithStdin(string(deviceData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			By("Checking that error event is shown")
			Eventually(func() string {
				out, err := harness.RunGetEvents(fieldSelector, fmt.Sprintf("involvedObject.name=%s,type=Warning,reason=%s", deviceName, util.DeviceSpecInvalid))
				if err != nil {
					return ""
				}
				return out
			}, "30s", "2s").Should(ContainSubstring("Device specification is invalid"))

			// Verify the specific error message
			out, err = harness.RunGetEvents(fieldSelector, fmt.Sprintf("involvedObject.name=%s,type=Warning,reason=%s", deviceName, util.DeviceSpecInvalid))
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(fmt.Sprintf("Repository of name \"%s\" not found", nonExistentRepo)))

			By("Reverting the changes to fix the device configuration")
			// Restore the original device configuration (remove the invalid config)
			revertedDevice := device
			revertedDevice.Spec = &v1beta1.DeviceSpec{
				Os: &v1beta1.DeviceOsSpec{
					Image: "quay.io/redhat/rhde:9.2",
				},
				// Remove the invalid config section entirely
			}

			// Apply the reverted configuration
			revertedData, err := json.Marshal(&revertedDevice)
			Expect(err).ToNot(HaveOccurred())

			out, err = harness.CLIWithStdin(string(revertedData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			By("Checking that success event is shown")
			Eventually(func() string {
				out, err := harness.RunGetEvents(fieldSelector, fmt.Sprintf("involvedObject.name=%s,type=Normal,reason=%s", deviceName, util.DeviceSpecValid))
				if err != nil {
					return ""
				}
				return out
			}, "30s", "2s").Should(ContainSubstring("Device specification is valid"))
		})

		It("should show events for application workload validation", Label("83588", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Creating a device without applications")
			deviceName := harness.StartVMAndEnroll()
			By("Check the device status")
			_, err := harness.CheckDeviceStatus(deviceName, v1beta1.DeviceSummaryStatusOnline)
			Expect(err).ToNot(HaveOccurred())

			By("Updating device with non-existing app image")

			GinkgoWriter.Printf("Starting update to add invalid application for %s\n", deviceName)

			// Apply the invalid application configuration

			err = harness.UpdateDeviceWithRetries(deviceName, func(device *v1beta1.Device) {
				imageName := "quay.io/rh_ee_camadorg/oci-app-ko:latest"
				// Create the application spec with the invalid image
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: imageName,
				}
				composeApp := v1beta1.ComposeApplication{
					AppType: v1beta1.AppTypeCompose,
				}
				err := composeApp.FromImageApplicationProviderSpec(imageSpec)
				Expect(err).ToNot(HaveOccurred())

				var appSpec v1beta1.ApplicationProviderSpec
				err = appSpec.FromComposeApplication(composeApp)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
				GinkgoWriter.Printf("Updating %s with application\n", deviceName)
			})
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Finished update to add invalid application for %s\n", deviceName)

			By("Checking that the application error event is shown")
			Eventually(func() string {
				out, err := harness.RunGetEvents(fieldSelector, fmt.Sprintf("involvedObject.name=%s,type=Warning,reason=%s", deviceName, util.DeviceUpdateFailed))
				if err != nil {
					return ""
				}
				return out
			}, "2m", "5s").Should(Not(BeEmpty()), "Expected application error event to be shown")

			By("Fixing the application by using valid image or removing it")
			GinkgoWriter.Printf("Starting update to remove applications for %s\n", deviceName)
			// Remove the application to fix the issue
			err = harness.UpdateDeviceWithRetries(deviceName, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
				GinkgoWriter.Printf("Updating %s removing applications\n", deviceName)
			})

			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Finished update to remove applications for %s\n", deviceName)

			By("Checking that 'No application workloads' success event is shown")
			Eventually(func() string {
				out, err := harness.RunGetEvents(fieldSelector, fmt.Sprintf("involvedObject.name=%s,type=Normal,reason=%s", deviceName, util.DeviceApplicationHealthy))
				if err != nil {
					return ""
				}
				return out
			}, "60s", "2s").Should(ContainSubstring("not reported any application workloads"))
		})
	})
})

func getEventsPage(harness *e2e.Harness, args ...string) (v1beta1.EventList, error) {
	out, err := harness.RunGetEvents(args...)
	if err != nil {
		return v1beta1.EventList{}, err
	}

	var page v1beta1.EventList
	err = json.Unmarshal([]byte(out), &page)
	if err != nil {
		return v1beta1.EventList{}, err
	}

	return page, nil
}

func extractTimestamps(events []v1beta1.Event) ([]time.Time, error) {
	var timestamps []time.Time

	for _, event := range events {
		if event.Metadata.CreationTimestamp == nil {
			return nil, fmt.Errorf("event missing CreationTimestamp")
		}
		timestamps = append(timestamps, *event.Metadata.CreationTimestamp)
	}

	return timestamps, nil
}

func verifyEventsByReason(harness *e2e.Harness, resources []struct {
	resourceType string
	yamlPath     string
}, deviceName, fleetName, repoName string, er *v1beta1.EnrollmentRequest, eventReason string) ([]string /* missingEvents */, error) {
	out, err := harness.RunGetEvents(fmt.Sprintf("--field-selector=reason=%s", eventReason))
	if err != nil {
		return nil, err
	}

	lines := strings.Split(out, "\n")
	missingEvents := []string{}

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

		matched := false
		for _, line := range lines {
			if strings.Contains(line, r.resourceType) && strings.Contains(line, name) {
				matched = true
				break
			}
		}

		if !matched {
			missingEvents = append(missingEvents, fmt.Sprintf("%s %s", r.resourceType, name))
			fmt.Fprintf(GinkgoWriter,
				"\n[DEBUG] No event with reason='%s' found for %s %s\nEvent output:\n%s\n\n",
				eventReason, r.resourceType, name, out)
		}
	}

	return missingEvents, nil
}
