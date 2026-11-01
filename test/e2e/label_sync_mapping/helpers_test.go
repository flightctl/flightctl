package label_sync_mapping

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	apiEventuallyTimeout = 30 * time.Second
	apiPollingInterval   = time.Second
)

func uniqueName(harness *e2e.Harness, prefix string) string {
	suffix := strings.TrimPrefix(harness.GetTestIDFromContext(), "test-")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("%s-%s", prefix, suffix)
}

func createLabelSyncMapping(harness *e2e.Harness, name string, key *string, expression string) *api.LabelSyncMapping {
	labels := map[string]string{"test-id": harness.GetTestIDFromContext()}
	mapping := api.LabelSyncMapping{
		ApiVersion: api.LabelSyncMappingAPIVersion,
		Kind:       api.LabelSyncMappingKind(api.LabelSyncMappingKindValue),
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: &labels,
		},
		Spec: api.LabelSyncMappingSpec{
			Expression:   expression,
			Key:          key,
			ResourceType: api.LabelSyncMappingSpecResourceTypeDevice,
		},
	}

	response, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, mapping)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusCreated), string(response.Body))
	Expect(response.JSON201).NotTo(BeNil())

	DeferCleanup(func() {
		response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode()).To(BeElementOf(http.StatusOK, http.StatusNotFound), string(response.Body))
	})

	return response.JSON201
}

func createDeviceWithStatus(harness *e2e.Harness, name string, labels map[string]string, architecture, operatingSystem string, customInfo map[string]string) *api.Device {
	device, err := resources.CreateDevice(harness, name, &labels)
	Expect(err).NotTo(HaveOccurred())
	replaceDeviceStatus(harness, name, architecture, operatingSystem, customInfo)
	return device
}

func replaceDeviceStatus(harness *e2e.Harness, name, architecture, operatingSystem string, customInfo map[string]string) {
	Eventually(func(g Gomega) {
		getResponse, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(getResponse.StatusCode()).To(Equal(http.StatusOK), string(getResponse.Body))
		g.Expect(getResponse.JSON200).NotTo(BeNil())

		status := api.NewDeviceStatus()
		status.SystemInfo.Architecture = architecture
		status.SystemInfo.OperatingSystem = operatingSystem
		if customInfo != nil {
			info := api.CustomDeviceInfo(customInfo)
			status.SystemInfo.CustomInfo = &info
		}
		getResponse.JSON200.Status = &status

		replaceResponse, err := harness.Client.ReplaceDeviceStatusWithResponse(harness.Context, name, *getResponse.JSON200)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(replaceResponse.StatusCode()).To(Equal(http.StatusOK), string(replaceResponse.Body))
	}).WithTimeout(apiEventuallyTimeout).WithPolling(apiPollingInterval).Should(Succeed())
}

func waitForDeviceLabels(harness *e2e.Harness, name string, expected map[string]string, absent ...string) *api.Device {
	var device *api.Device
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Metadata.Labels).NotTo(BeNil())

		labels := *response.JSON200.Metadata.Labels
		for key, value := range expected {
			g.Expect(labels).To(HaveKeyWithValue(key, value))
		}
		for _, key := range absent {
			g.Expect(labels).NotTo(HaveKey(key))
		}
		device = response.JSON200
	}).Should(Succeed())
	return device
}

func waitForMappingReady(harness *e2e.Harness, name string) *api.LabelSyncMapping {
	var mapping *api.LabelSyncMapping
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Status).NotTo(BeNil())
		g.Expect(response.JSON200.Status.Conditions).NotTo(BeNil())

		ready := false
		for _, condition := range *response.JSON200.Status.Conditions {
			if condition.Type == api.ConditionTypeLabelSyncMappingReady && condition.Status == api.ConditionStatusTrue {
				ready = true
				break
			}
		}
		g.Expect(ready).To(BeTrue())
		mapping = response.JSON200
	}).Should(Succeed())
	return mapping
}

func waitForMappingDeleted(harness *e2e.Harness, name string) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusNotFound), string(response.Body))
	}).Should(Succeed())
}

func deleteLabelSyncMapping(harness *e2e.Harness, name string) {
	response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, name)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
}

func replaceLabelSyncMapping(harness *e2e.Harness, name string, key *string, expression string) *api.LabelSyncMapping {
	getResponse, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
	Expect(err).NotTo(HaveOccurred())
	Expect(getResponse.StatusCode()).To(Equal(http.StatusOK), string(getResponse.Body))
	Expect(getResponse.JSON200).NotTo(BeNil())

	getResponse.JSON200.Spec.Key = key
	getResponse.JSON200.Spec.Expression = expression
	replaceResponse, err := harness.Client.ReplaceLabelSyncMappingWithResponse(harness.Context, name, *getResponse.JSON200)
	Expect(err).NotTo(HaveOccurred())
	Expect(replaceResponse.StatusCode()).To(Equal(http.StatusOK), string(replaceResponse.Body))
	Expect(replaceResponse.JSON200).NotTo(BeNil())
	return replaceResponse.JSON200
}

func listDevicesByLabel(harness *e2e.Harness, selector string) []api.Device {
	response, err := harness.Client.ListDevicesWithResponse(harness.Context, &api.ListDevicesParams{LabelSelector: &selector})
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
	Expect(response.JSON200).NotTo(BeNil())
	return response.JSON200.Items
}
