package rollout_test

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func promotedReplaceMapping(harness *e2e.Harness, name, expression string) {
	response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
	Expect(response.JSON200).NotTo(BeNil())
	response.JSON200.Spec.Expression = expression
	replaced, err := harness.Client.ReplaceLabelSyncMappingWithResponse(harness.Context, name, *response.JSON200)
	Expect(err).NotTo(HaveOccurred())
	Expect(replaced.StatusCode()).To(Equal(http.StatusOK), string(replaced.Body))
}

func promotedExpectSelectedBatchDevice(harness *e2e.Harness, expectedID string) {
	Eventually(func(g Gomega) {
		selected, err := harness.GetSelectedDevicesForBatch(fleetName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(selected).To(HaveLen(1))
		g.Expect(selected[0].Metadata.Name).NotTo(BeNil())
		g.Expect(*selected[0].Metadata.Name).To(Equal(expectedID))
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}

func promotedFleetTemplateVersion(harness *e2e.Harness, name string) string {
	fleet, err := harness.GetFleet(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(fleet.Metadata.Annotations).NotTo(BeNil())
	version := (*fleet.Metadata.Annotations)[api.FleetAnnotationTemplateVersion]
	Expect(version).NotTo(BeEmpty())
	return version
}

func promotedFleetEvents(harness *e2e.Harness, name string) []api.Event {
	selector := "involvedObject.name=" + name
	response, err := harness.Client.ListEventsWithResponse(harness.Context, &api.ListEventsParams{FieldSelector: &selector})
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
	Expect(response.JSON200).NotTo(BeNil())
	return response.JSON200.Items
}

func promotedRolloutEventErrors(events []api.Event, version string) []string {
	sort.SliceStable(events, func(i, j int) bool {
		left, right := events[i].Metadata.CreationTimestamp, events[j].Metadata.CreationTimestamp
		if left == nil || right == nil {
			return false
		}
		return left.Before(*right)
	})
	batchNumbers := map[string]int{
		"-1": -1, api.PreliminaryBatchName: -1,
		"0": 0, "batch 1": 0,
		"1": 1, api.FinalImplicitBatchName: 1,
	}
	var batches []string
	var errors []string
	seen := map[int]bool{}
	lastBatch := -1
	for _, event := range events {
		if event.Reason != api.EventReasonFleetRolloutBatchDispatched || event.Details == nil {
			continue
		}
		details, err := event.Details.AsFleetRolloutBatchDispatchedDetails()
		if err != nil || details.TemplateVersion != version {
			continue
		}
		batches = append(batches, details.Batch)
		number, valid := batchNumbers[details.Batch]
		if !valid || number < lastBatch {
			errors = append(errors, fmt.Sprintf("template %s has stale or unknown dispatch batch %q", version, details.Batch))
			continue
		}
		seen[number] = true
		lastBatch = number
	}
	if !seen[0] || !seen[1] {
		errors = append(errors, fmt.Sprintf("template %s dispatched %v; expected first and final batches", version, batches))
	}
	if count := promotedCompletionCount(events, version); count != 1 {
		errors = append(errors, fmt.Sprintf("template %s has %d completion events; expected 1", version, count))
	}
	return errors
}

func promotedCompletionCount(events []api.Event, version string) int {
	count := 0
	for _, event := range events {
		if event.Reason != api.EventReasonFleetRolloutCompleted || event.Details == nil {
			continue
		}
		details, err := event.Details.AsFleetRolloutCompletedDetails()
		if err == nil && details.TemplateVersion == version {
			count++
		}
	}
	return count
}

func promotedInlineConfig(name, content string) api.ConfigProviderSpec {
	var config api.ConfigProviderSpec
	Expect(config.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
		Name:   name,
		Inline: []api.FileSpec{{Path: "/etc/flightctl/selected-region", Content: content}},
	})).To(Succeed())
	return config
}

func promotedFleetSpec(selector map[string]string, config api.ConfigProviderSpec) api.FleetSpec {
	return api.FleetSpec{
		Selector: &api.LabelSelector{MatchLabels: &selector},
		Template: struct {
			Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
			Spec     api.DeviceSpec  "json:\"spec\""
		}{Spec: api.DeviceSpec{Config: &[]api.ConfigProviderSpec{config}}},
	}
}

func promotedTestSuffix(harness *e2e.Harness) string {
	suffix := strings.TrimPrefix(harness.GetTestIDFromContext(), "test-")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return suffix
}

func promotedScalarExpression(harness *e2e.Harness, expression string) string {
	return fmt.Sprintf(`metadata.labels["test-id"] == %q ? (%s) : dyn(null)`, harness.GetTestIDFromContext(), expression)
}

func promotedMapExpression(harness *e2e.Harness, expression string) string {
	return fmt.Sprintf(`metadata.labels["test-id"] == %q ? (%s) : {}`, harness.GetTestIDFromContext(), expression)
}

func promotedCreateMapping(harness *e2e.Harness, name string, key *string, expression string) {
	labels := map[string]string{"test-id": harness.GetTestIDFromContext()}
	mapping := api.LabelSyncMapping{
		ApiVersion: api.LabelSyncMappingAPIVersion,
		Kind:       api.LabelSyncMappingKind(api.LabelSyncMappingKindValue),
		Metadata:   api.ObjectMeta{Name: &name, Labels: &labels},
		Spec: api.LabelSyncMappingSpec{
			Expression:   expression,
			Key:          key,
			ResourceType: api.LabelSyncMappingSpecResourceTypeDevice,
		},
	}
	response, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, mapping)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusCreated), string(response.Body))
	DeferCleanup(func() {
		response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode()).To(BeElementOf(http.StatusOK, http.StatusNotFound), string(response.Body))
	})
}

func promotedCreateDeviceWithStatus(harness *e2e.Harness, name string, labels map[string]string, architecture, region string) {
	_, err := resources.CreateDevice(harness, name, &labels)
	Expect(err).NotTo(HaveOccurred())
	promotedReplaceDeviceStatus(harness, name, architecture, region)
}

func promotedReplaceDeviceStatus(harness *e2e.Harness, name, architecture, region string) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		status := api.NewDeviceStatus()
		if response.JSON200.Status != nil {
			status = *response.JSON200.Status
		}
		status.SystemInfo.Architecture = architecture
		status.SystemInfo.OperatingSystem = "linux"
		info := api.CustomDeviceInfo{"region": region}
		status.SystemInfo.CustomInfo = &info
		response.JSON200.Status = &status
		replaced, err := harness.Client.ReplaceDeviceStatusWithResponse(harness.Context, name, *response.JSON200)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(replaced.StatusCode()).To(Equal(http.StatusOK), string(replaced.Body))
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}

func promotedWaitForLabels(harness *e2e.Harness, name string, expected map[string]string) {
	Eventually(func(g Gomega) {
		device, err := harness.GetDevice(name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(device.Metadata.Labels).NotTo(BeNil())
		for key, value := range expected {
			g.Expect(*device.Metadata.Labels).To(HaveKeyWithValue(key, value))
		}
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}

func promotedWaitForMappingReady(harness *e2e.Harness, name string) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Status).NotTo(BeNil())
		g.Expect(response.JSON200.Status.Conditions).NotTo(BeNil())
		g.Expect(api.IsStatusConditionTrue(*response.JSON200.Status.Conditions, api.ConditionTypeLabelSyncMappingReady)).To(BeTrue())
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}

func promotedWaitForOwner(harness *e2e.Harness, deviceName, fleetName string) {
	Eventually(func(g Gomega) {
		device, err := harness.GetDevice(deviceName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(device.Metadata.Owner).NotTo(BeNil())
		g.Expect(*device.Metadata.Owner).To(Equal("Fleet/" + fleetName))
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}

func promotedExpectSelectedDevice(harness *e2e.Harness, selector, expectedName string) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.ListDevicesWithResponse(harness.Context, &api.ListDevicesParams{LabelSelector: &selector})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Items).To(HaveLen(1))
		g.Expect(response.JSON200.Items[0].Metadata.Name).NotTo(BeNil())
		g.Expect(*response.JSON200.Items[0].Metadata.Name).To(Equal(expectedName))
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}
