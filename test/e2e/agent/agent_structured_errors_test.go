package agent_test

import (
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	MaxElementLength = 64

	TruncationTestApp = "truncation-test-app"
	LongImage         = "quay.io/nonexistent-org/this-is-an-extremely-long-image-name-designed-to-exceed-the-64-character-element-limit-for-truncation-testing:v1.0.0"

	ShortAppName     = "test-error-app"
	NonExistentImage = "quay.io/nonexistent-org/fake-image:v1.0.0"

	InvalidConfigUser = "nonexistent-user-xyz"
	InvalidConfigPath = "/tmp/flightctl-test-config"
	InvalidConfigName = "invalid-config"

	FailingHookPath    = "/etc/flightctl/hooks.d/beforeupdating/fail-hook.yaml"
	FailingHookName    = "failing-hook-config"
	FailingHookContent = "- run: /bin/false\n"
	DummyTriggerName   = "dummy-trigger"
	DummyTriggerPath   = "/tmp/flightctl-dummy-trigger"

	ReasonError = "Error"

	WaitReasonUpToDate         = "UpToDate status"
	WaitReasonNoErrorCondition = "no error condition"
	StatusMsgInvalidArgument   = "invalid configuration or input"
	StatusMsgAuthFailed        = "authentication failed"
	StatusMsgNotFound          = "required resource not found"
	StatusMsgPermissionDenied  = "permission denied"
	StatusMsgUnavailable       = "service unavailable (network issue)"
	StatusMsgInternal          = "internal error occurred"
)

var _ = Describe("Agent structured error messages", Ordered, func() {
	var (
		harness  *e2e.Harness
		deviceId string
	)

	// Use a stable test-id so the device is not deleted by any It's AfterEach cleanup.
	const structuredErrorsTestID = "agent-structured-errors"
	BeforeAll(func() {
		harness = e2e.GetWorkerHarness()
	})

	BeforeEach(func() {
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus(map[string]string{"test-id": structuredErrorsTestID})
		GinkgoWriter.Printf("Device enrolled: %s\n", deviceId)
	})

	Context("structured errors", func() {
		It("Verifies structured error format, element, component, status code, and non-retryable state", Label("87807", "sanity", "agent"), func() {
			By("Apply application with non-existent image and wait for structured error")
			cond, err := applyBadImageAndWaitForError(harness, deviceId, ShortAppName, NonExistentImage, TIMEOUT)
			Expect(err).ToNot(HaveOccurred(), "failed to apply bad image and wait for error")
			Expect(cond.Message).ToNot(BeEmpty())
			GinkgoWriter.Printf("Structured error: %s\n", cond.Message)
			Expect(util.ValidateStructuredError(cond.Message, "Preparing", "prefetch", StatusMsgAuthFailed, StatusMsgNotFound, StatusMsgPermissionDenied, StatusMsgUnavailable, StatusMsgInternal)).To(Succeed(),
				"expected Preparing/prefetch with one of auth failed, not found, "+StatusMsgPermissionDenied+", service unavailable, or internal error")
			Expect(cond.Message).To(ContainSubstring(fmt.Sprintf("failed for %s", NonExistentImage)),
				"should contain 'failed for <image>'")

			By("Verifying non-retryable condition state")
			Expect(cond.Reason).To(Equal(ReasonError), "should set Reason to Error")
			Expect(cond.Status).To(Equal(v1beta1.ConditionStatusFalse), "should set Status to False")
		})

		It("Verifies element name truncation keeps message under MaxMessageLength", Label("87808", "sanity", "agent"), func() {
			By("Apply application with an image name exceeding MaxElementLength")
			Expect(len(LongImage)).To(BeNumerically(">", MaxElementLength),
				"test precondition: image name must exceed MaxElementLength")
			By("Apply application with long image and wait for error")
			cond, err := applyBadImageAndWaitForError(harness, deviceId, TruncationTestApp, LongImage, TIMEOUT)
			Expect(err).ToNot(HaveOccurred())
			msg := cond.Message
			GinkgoWriter.Printf("Truncated message (%d chars): %s\n", len(msg), msg)

			By("Verify message does not exceed MaxMessageLength")
			Expect(len(msg)).To(BeNumerically("<=", status.MaxMessageLength),
				fmt.Sprintf("message length %d should not exceed MaxMessageLength %d", len(msg), status.MaxMessageLength))

			By("Verify element name is truncated with '...' prefix and last 64 characters")
			truncatedSuffix := LongImage[len(LongImage)-MaxElementLength:]
			Expect(msg).To(ContainSubstring("..."+truncatedSuffix),
				"long element name should be truncated to '...' + last 64 characters")
			Expect(msg).ToNot(ContainSubstring(LongImage),
				"full element name should not appear when it exceeds MaxElementLength")

		})

		It("Verifies ApplyingUpdate phase and config component for config write failure", Label("87884", "sanity", "agent"), func() {
			By("Apply config with non-existent user to trigger config write failure")
			configSpec, err := util.BuildInlineConfigSpec(InvalidConfigName, InvalidConfigPath, "test-content", InvalidConfigUser)
			Expect(err).ToNot(HaveOccurred(), "failed to build inline config spec")
			err = harness.UpdateDeviceWithRetries(deviceId, e2e.SetDeviceConfig(configSpec))
			Expect(err).ToNot(HaveOccurred(), "failed to update device with invalid config")

			By("Wait for structured error")
			cond, err := waitForUpdatingErrorCondition(harness, deviceId, LONGTIMEOUT)
			Expect(err).ToNot(HaveOccurred(), "failed waiting for updating error condition")
			msg := cond.Message
			Expect(msg).ToNot(BeEmpty())
			GinkgoWriter.Printf("Config error: %s\n", msg)

			Expect(util.ValidateStructuredError(msg, "ApplyingUpdate", "config", StatusMsgNotFound)).To(Succeed())
		})

		It("Verifies prefetch error with Preparing phase and clears when fixed", Label("87885", "sanity", "agent"), func() {
			By("Apply application with non-existent image and wait for prefetch structured error")
			cond, err := applyBadImageAndWaitForError(harness, deviceId, ShortAppName, NonExistentImage, LONGTIMEOUT)
			Expect(err).ToNot(HaveOccurred())
			msg := cond.Message
			Expect(msg).ToNot(BeEmpty())

			Expect(util.ValidateStructuredError(msg, "Preparing", "prefetch", StatusMsgAuthFailed, StatusMsgNotFound, StatusMsgPermissionDenied, StatusMsgUnavailable, StatusMsgInternal)).To(Succeed())

			By("Fix by removing the failing application")
			err = harness.UpdateDeviceWithRetries(deviceId, e2e.ClearDeviceApplications)
			Expect(err).ToNot(HaveOccurred())

			By("Wait for device to become UpToDate")
			harness.WaitForDeviceContents(deviceId, WaitReasonUpToDate, e2e.IsDeviceUpToDate, LONGTIMEOUT)

			By("Verify Updating condition is no longer in error state")
			harness.WaitForDeviceContents(deviceId, WaitReasonNoErrorCondition, e2e.IsUpdatingConditionCleared, TIMEOUT)
		})

		It("Verifies hooks component structured error when beforeupdating hook fails", Label("87953", "sanity", "agent"), func() {
			By("Deploy a beforeupdating hook that always fails")
			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			hookConfigSpec, err := util.BuildInlineConfigSpec(FailingHookName, FailingHookPath, FailingHookContent, "")
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, e2e.SetDeviceConfig(hookConfigSpec))
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Trigger a second update so beforeUpdate executes the now-present failing hook")
			dummyConfigSpec, err := util.BuildInlineConfigSpec(DummyTriggerName, DummyTriggerPath, "trigger-content", "")
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, e2e.SetDeviceConfig(hookConfigSpec, dummyConfigSpec))
			Expect(err).ToNot(HaveOccurred())

			By("Wait for structured error from hook failure")
			cond, err := waitForUpdatingErrorCondition(harness, deviceId, TIMEOUT)
			Expect(err).ToNot(HaveOccurred())
			msg := cond.Message
			GinkgoWriter.Printf("Hook error: %s\n", msg)

			// Hook error chain contains both ErrExitCode (InvalidArgument) and ErrFailedToExecute (Unavailable);
			// ToCode() iterates a map so the resolved code varies between runs.
			Expect(util.ValidateStructuredError(msg, "Preparing", "hooks", StatusMsgInvalidArgument, StatusMsgUnavailable)).To(Succeed())
		})

		It("Verifies agent logs contain raw error matching the condition message", Label("87963", "sanity", "agent"), func() {
			By("Apply non-existent image and wait for structured error in device condition")
			cond, err := applyBadImageAndWaitForError(harness, deviceId, ShortAppName, NonExistentImage, TIMEOUT)
			Expect(err).ToNot(HaveOccurred())
			msg := cond.Message
			Expect(msg).ToNot(BeEmpty())

			By("Read agent logs and verify they contain the raw error for the same failure")
			logs, err := harness.ReadPrimaryVMAgentLogs("", util.FLIGHTCTL_AGENT_SERVICE)
			Expect(err).ToNot(HaveOccurred())

			Expect(logs).To(ContainSubstring("Failed to update to renderedVersion"),
				"logs should contain the raw sync error")
			Expect(logs).To(ContainSubstring("prefetch"),
				"logs should reference the same component as condition (prefetch)")
			Expect(logs).To(ContainSubstring(NonExistentImage),
				"logs should reference the same element as condition")
		})
	})
})

// waitForUpdatingErrorCondition uses the same pattern as agent_update_test: WaitForDeviceContents
// with ConditionExists for the updating-failure state, then returns the DeviceUpdating condition.
func waitForUpdatingErrorCondition(h *e2e.Harness, deviceID, timeout string) (*v1beta1.Condition, error) {
	h.WaitForDeviceContents(deviceID, "device status should indicate updating failure", func(device *v1beta1.Device) bool {
		return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError))
	}, timeout)
	device, err := h.GetDevice(deviceID)
	if err != nil {
		return nil, err
	}
	cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
	if cond == nil {
		return nil, fmt.Errorf("DeviceUpdating condition not found")
	}
	return cond, nil
}

// applyBadImageAndWaitForError applies an application with the given image and waits for
// the device to enter an updating error state. Returns the error condition for further assertions.
func applyBadImageAndWaitForError(h *e2e.Harness, deviceID, appName, image, timeout string) (*v1beta1.Condition, error) {
	if err := h.UpdateApplication(true, deviceID, appName, v1beta1.ImageApplicationProviderSpec{Image: image}, nil); err != nil {
		return nil, fmt.Errorf("applying application %s with image %s: %w", appName, image, err)
	}
	return waitForUpdatingErrorCondition(h, deviceID, timeout)
}
