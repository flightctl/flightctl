package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var (
	invalidSyntax = "invalid syntax"
	kind          = "involvedObject.kind"
	fieldSelector = "--field-selector"
	limit         = "--limit"
	jsonFlag      = "-ojson"
)

var _ = Describe("cli events operation", func() {
	var (
		ctx     context.Context
		harness *e2e.Harness
	)

	BeforeEach(func() {
		ctx = util.StartSpecTracerForGinkgo(suiteCtx)
		harness = e2e.NewTestHarness(ctx)
		login.LoginToAPIWithToken(harness)
	})

	AfterEach(func() {
		err := harness.CleanUpAllResources()
		Expect(err).ToNot(HaveOccurred())
		harness.Cleanup(false) // do not print console on error
	})

	Context("Events API Tests", func() {
		It("should list events resource is created/updated/deleted", Label("81779", "sanity"), func() {
			var deviceName, fleetName, repoName string
			var er *v1alpha1.EnrollmentRequest

			resources := []struct {
				resourceType string
				yamlPath     string
			}{
				{util.DeviceResource, util.DeviceYAMLName},
				{util.FleetResource, util.FleetBYAMLName},
				{util.RepoResource, util.RepoYAMLName},
				{util.ErResource, util.ErYAMLName},
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
					Expect(out).To(MatchRegexp(resourceCreated))
					er = harness.GetEnrollmentRequestByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
				}
			}

			By("Verifying Created events")
			createdEvents, err := verifyEventsByReason(harness, resources, deviceName, fleetName, repoName, er, "ResourceCreated")

			Expect(err).ToNot(HaveOccurred())
			Expect(len(createdEvents)).To(BeZero(), fmt.Sprintf("Missing created events for: %v", createdEvents))

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
					Expect(out).To(MatchRegexp(resourceCreated))
					er = harness.GetEnrollmentRequestByYaml(util.GetTestExamplesYamlPath(r.yamlPath))
				}
			}

			By("Verifying updated events")
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
			Expect(out).To(MatchRegexp(formatResourceEvent("Fleet", fleetName, util.EventCreated)))

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
			Expect(out).To(ContainSubstring("accepts 1 arg(s), received 2"))

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
			By("Creating a device from YAML")
			out, err := harness.ManageResource("apply", "device.yaml")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			// Get the device details from the YAML
			device := harness.GetDeviceByYaml(util.GetTestExamplesYamlPath("device.yaml"))
			deviceName := *device.Metadata.Name

			By("Making a change to device spec that breaks the configuration")
			// Create invalid device spec with non-existent repository
			nonExistentRepo := "non-existent"
			invalidDevice := device
			gitConfig := v1alpha1.GitConfigProviderSpec{
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
			gitItem := v1alpha1.ConfigProviderSpec{}
			err = gitItem.FromGitConfigProviderSpec(gitConfig)
			Expect(err).ToNot(HaveOccurred())

			invalidDevice.Spec = &v1alpha1.DeviceSpec{
				Os: &v1alpha1.DeviceOsSpec{
					Image: "quay.io/redhat/rhde:9.2",
				},
				Config: &[]v1alpha1.ConfigProviderSpec{gitItem},
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
			revertedDevice.Spec = &v1alpha1.DeviceSpec{
				Os: &v1alpha1.DeviceOsSpec{
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
			//TODO: Add another invalid app validation after EDM-1869 is fixed
			By("Creating a device without applications")
			appName1 := "image-app"
			out, err := harness.ManageResource("apply", "device.yaml")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			// Get the device details from the YAML
			device := harness.GetDeviceByYaml(util.GetTestExamplesYamlPath("device.yaml"))
			deviceName := *device.Metadata.Name

			By("Updating device with non-existing app image")
			// Create device spec with invalid application image
			deviceWithInvalidApp := device

			// Create the first invalid application
			imageApp1 := v1alpha1.ImageApplicationProviderSpec{
				Image: "quay.io/rh_ee_camadorg/oci-app-ko", // Non-existing image
			}
			imageAppItem1 := v1alpha1.ApplicationProviderSpec{
				Name: &appName1,
			}
			err = imageAppItem1.FromImageApplicationProviderSpec(imageApp1)
			Expect(err).ToNot(HaveOccurred())

			deviceWithInvalidApp.Spec = &v1alpha1.DeviceSpec{
				Applications: &[]v1alpha1.ApplicationProviderSpec{imageAppItem1},
			}

			// Apply the invalid application configuration
			deviceData, err := json.Marshal(&deviceWithInvalidApp)
			Expect(err).ToNot(HaveOccurred())

			out, err = harness.CLIWithStdin(string(deviceData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			out, _ = harness.CLI("get", fmt.Sprintf("device/%s", deviceName), "-oyaml")

			logrus.Infof("Updated Device Resource: %s", out)
			out, _ = harness.CLI("get", "events", "-oyaml")

			logrus.Infof("EVENTS: %s", out)

			//TODO: add type=Warning and event reason after fixing the issue EDM-1869
			By("Checking that application error event is shown")
			Eventually(func() string {
				out, err := harness.RunGetEvents(fieldSelector, fmt.Sprintf("involvedObject.name=%s", deviceName))
				if err != nil {
					return ""
				}
				return out
			}, "5m", "5s").Should(ContainSubstring("pull image: authentication failed"))
			By("Fixing the application by using valid image or removing it")
			// Remove the application to fix the issue
			deviceFixed := device
			deviceFixed.Spec = &v1alpha1.DeviceSpec{
				// Remove applications entirely
			}

			// Apply the fixed configuration
			fixedData, err := json.Marshal(&deviceFixed)
			Expect(err).ToNot(HaveOccurred())

			out, err = harness.CLIWithStdin(string(fixedData), "apply", "-f", "-")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(MatchRegexp(resourceCreated))

			By("Checking that 'No application workloads' success event is shown")
			Eventually(func() string {
				out, err := harness.RunGetEvents(fieldSelector, fmt.Sprintf("involvedObject.name=%s,type=Normal,reason=%s", deviceName, util.DeviceApplicationHealthy))
				if err != nil {
					return ""
				}
				return out
			}, "60s", "2s").Should(ContainSubstring("No application workloads are defined"))
		})
	})
})

// formatResourceEvent formats the event's message and returns it as a string
func formatResourceEvent(resource, name, action string) string {
	return fmt.Sprintf("%s\\s+%s\\s+Normal\\s+%s\\s+was\\s+%s\\s+successfully", resource, name, resource, action)
}

func getEventsPage(harness *e2e.Harness, args ...string) (v1alpha1.EventList, error) {
	out, err := harness.RunGetEvents(args...)
	if err != nil {
		return v1alpha1.EventList{}, err
	}

	var page v1alpha1.EventList
	err = json.Unmarshal([]byte(out), &page)
	if err != nil {
		return v1alpha1.EventList{}, err
	}

	return page, nil
}

func extractTimestamps(events []v1alpha1.Event) ([]time.Time, error) {
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
}, deviceName, fleetName, repoName string, er *v1alpha1.EnrollmentRequest, eventReason string) ([]string /* missingEvents */, error) {
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
