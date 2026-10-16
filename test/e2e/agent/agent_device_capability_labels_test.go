package agent_test

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// Labels the server derives from the capability data the agent reports under
// status.systemInfo, using the mappings installed at deployment time from
// packaging/flightctl/label-sync/mappings.yaml.
const (
	capabilityGpuPresentLabel   = "feature.flightctl.io/gpu.present"
	capabilityKvmEnabledLabel   = "feature.flightctl.io/kvm.enabled"
	capabilityOsModeLabel       = "feature.flightctl.io/os.mode"
	capabilityArchitectureLabel = "systeminfo.flightctl.io/architecture"
	capabilityAgentVersionLabel = "systeminfo.flightctl.io/agentVersion"
)

// expectedCapabilityLabels derives the labels the server should produce for the
// capability data this device actually reported. A capability the agent does
// not report must stay absent so clients can tell "unknown" from a reported
// "false", so the expectation is built from the reported systemInfo rather than
// from assumptions about the e2e device image.
func expectedCapabilityLabels(systemInfo *v1beta1.DeviceSystemInfo) (present map[string]string, absent []string) {
	present = map[string]string{}
	if systemInfo.Architecture != "" {
		present[capabilityArchitectureLabel] = systemInfo.Architecture
	}
	if systemInfo.AgentVersion != "" {
		present[capabilityAgentVersionLabel] = systemInfo.AgentVersion
	}

	if systemInfo.Gpus != nil {
		present[capabilityGpuPresentLabel] = strconv.FormatBool(len(*systemInfo.Gpus) > 0)
	} else {
		absent = append(absent, capabilityGpuPresentLabel)
	}

	if systemInfo.Kvm != nil && systemInfo.Kvm.Enabled != nil {
		present[capabilityKvmEnabledLabel] = strconv.FormatBool(*systemInfo.Kvm.Enabled)
	} else {
		absent = append(absent, capabilityKvmEnabledLabel)
	}

	if systemInfo.OsMode != nil && *systemInfo.OsMode != "" {
		present[capabilityOsModeLabel] = string(*systemInfo.OsMode)
	} else {
		absent = append(absent, capabilityOsModeLabel)
	}
	return present, absent
}

var _ = Describe("Device capability feature labels", func() {
	var (
		harness  *e2e.Harness
		deviceID string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		deviceID = harness.StartVMAndEnroll()
	})

	It("When an enrolled device reports systemInfo it should derive matching feature labels", Label("sanity", "agent"), func() {
		By("Waiting for the agent to report systemInfo")
		var systemInfo *v1beta1.DeviceSystemInfo
		Eventually(func() bool {
			systemInfo = harness.GetDeviceSystemInfo(deviceID)
			return systemInfo != nil && systemInfo.Architecture != "" && systemInfo.AgentVersion != ""
		}, TIMEOUT, POLLING).Should(BeTrue(), "the device should report systemInfo after enrollment")

		expectedPresent, expectedAbsent := expectedCapabilityLabels(systemInfo)
		GinkgoWriter.Printf("Expecting derived labels %v and absent keys %v\n", expectedPresent, expectedAbsent)

		By("Waiting for the server to derive the labels from the reported systemInfo")
		harness.WaitForDeviceLabels(deviceID, "device labels are derived from systemInfo", expectedPresent, TIMEOUT)

		By("Verifying unreported capabilities do not produce a feature label")
		device, err := harness.GetDevice(deviceID)
		Expect(err).ToNot(HaveOccurred())
		labels := lo.FromPtr(device.Metadata.Labels)
		for _, key := range expectedAbsent {
			Expect(labels).ToNot(HaveKey(key),
				"capability %q was not reported, so the feature label must stay absent", key)
		}

		By("Verifying the label applied at enrollment is preserved alongside the derived labels")
		Expect(labels).To(HaveKeyWithValue("test-id", harness.GetTestIDFromContext()))
	})

	It("When filtering by a derived label it should return only the matching devices", Label("sanity", "agent"), func() {
		By("Waiting for the architecture label to be derived")
		architecture := harness.WaitForDeviceReportedLabel(deviceID, capabilityArchitectureLabel, TIMEOUT)

		By("Listing devices with a matching label selector")
		matching := fmt.Sprintf("%s=%s,test-id=%s", capabilityArchitectureLabel, architecture, harness.GetTestIDFromContext())
		response, err := harness.Client.ListDevicesWithResponse(harness.Context, &v1beta1.ListDevicesParams{
			LabelSelector: lo.ToPtr(matching),
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(response.HTTPResponse.StatusCode).To(Equal(http.StatusOK), string(response.Body))
		names := make([]string, 0, len(response.JSON200.Items))
		for _, device := range response.JSON200.Items {
			names = append(names, lo.FromPtr(device.Metadata.Name))
		}
		Expect(names).To(ContainElement(deviceID))

		By("Listing devices with a non-matching label selector")
		nonMatching := fmt.Sprintf("%s=not-a-real-architecture,test-id=%s", capabilityArchitectureLabel, harness.GetTestIDFromContext())
		response, err = harness.Client.ListDevicesWithResponse(harness.Context, &v1beta1.ListDevicesParams{
			LabelSelector: lo.ToPtr(nonMatching),
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(response.HTTPResponse.StatusCode).To(Equal(http.StatusOK), string(response.Body))
		Expect(response.JSON200.Items).To(BeEmpty())
	})

	It("When an operator edits a derived label it should be rejected and operator labels stay editable", Label("sanity", "agent"), func() {
		By("Waiting for the architecture label to be derived")
		derivedArchitecture := harness.WaitForDeviceReportedLabel(deviceID, capabilityArchitectureLabel, TIMEOUT)

		device, err := harness.GetDevice(deviceID)
		Expect(err).ToNot(HaveOccurred())

		By("Attempting to overwrite the server-owned label")
		spoofed := *device
		spoofedLabels := map[string]string{}
		for key, value := range lo.FromPtr(device.Metadata.Labels) {
			spoofedLabels[key] = value
		}
		spoofedLabels[capabilityArchitectureLabel] = "spoofed-architecture"
		spoofed.Metadata.Labels = &spoofedLabels

		replaceResponse, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceID, spoofed)
		Expect(err).ToNot(HaveOccurred())
		Expect(replaceResponse.HTTPResponse.StatusCode).To(Equal(http.StatusConflict), string(replaceResponse.Body))

		By("Verifying the derived value is unchanged")
		device, err = harness.GetDevice(deviceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(HaveKeyWithValue(capabilityArchitectureLabel, derivedArchitecture))

		By("Verifying operator labels outside the reserved namespaces remain editable")
		Expect(harness.SetLabelsForDevice(deviceID, map[string]string{"site": "capability-e2e"})).To(Succeed())
		device, err = harness.GetDevice(deviceID)
		Expect(err).ToNot(HaveOccurred())
		labels := lo.FromPtr(device.Metadata.Labels)
		Expect(labels).To(HaveKeyWithValue("site", "capability-e2e"))
		Expect(labels).To(HaveKeyWithValue(capabilityArchitectureLabel, derivedArchitecture))
	})
})
