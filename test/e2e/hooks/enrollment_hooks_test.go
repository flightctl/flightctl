package hooks

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	enrollmentHookScenarioLabel             = "enrollment-hooks"
	enrollmentHookAgentLabel                = "agent"
	enrollmentHookSlowLabel                 = "slow"
	enrollmentHookDevLabel                  = "EDM-5713"
	enrollmentHookLabelKey                  = "enrollment-hook-test"
	enrollmentHookBeforeMarker              = "/run/flightctl/e2e-before-enrolling"
	enrollmentHookAfterMarker               = "/run/flightctl/e2e-after-enrolling"
	enrollmentHookReleaseFile               = "/run/flightctl/e2e-release-after-enrolling"
	enrollmentHookFleetName                 = "enrollment-hooks-fleet"
	enrollmentHookPolicyName                = "default"
	enrollmentHookReEnrollmentLabel         = "90609"
	enrollmentHookAbsenceObservationWindows = 2
)

type statusCodeGetter func(*e2e.Harness, string) (int, error)

var _ = Describe("Enrollment lifecycle hooks", Label(enrollmentHookScenarioLabel), Serial, func() {

	It("keeps a pending enrollment from running AfterEnrolling", Label(enrollmentHookDevLabel, "90612", enrollmentHookAgentLabel, enrollmentHookSlowLabel), func() {
		harness := e2e.GetWorkerHarness()

		statusCode, err := createEnrollmentHookPolicy(harness, newEnrollmentHookPolicy("", v1beta1.FailurePolicyBlock))
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusCreated))

		enrollmentID, err := waitForEnrollmentRequest(harness)
		Expect(err).ToNot(HaveOccurred())
		Expect(enrollmentID).ToNot(BeEmpty())

		Expect(waitForPathAbsenceAfterAgentPoll(harness, enrollmentHookAfterMarker)).To(Succeed())
	})

	It("does not run AfterEnrolling when an enrollment is denied", Label(enrollmentHookDevLabel, "90611", enrollmentHookAgentLabel, enrollmentHookSlowLabel), func() {
		harness := e2e.GetWorkerHarness()

		statusCode, err := createEnrollmentHookPolicy(harness, newEnrollmentHookPolicy("", v1beta1.FailurePolicyBlock))
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusCreated))

		enrollmentID, err := waitForEnrollmentRequest(harness)
		Expect(err).ToNot(HaveOccurred())

		statusCode, err = denyEnrollmentRequest(harness, enrollmentID)
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusOK))
		deviceStatus, err := waitForStatusCode(harness, enrollmentID, http.StatusNotFound, getDeviceStatusCode)
		Expect(err).ToNot(HaveOccurred())
		Expect(deviceStatus).To(Equal(http.StatusNotFound))
		Expect(waitForPathAbsenceAfterAgentPoll(harness, enrollmentHookAfterMarker)).To(Succeed())
	})

	It("blocks fleet ownership and rendering until AfterEnrolling succeeds", Label(enrollmentHookDevLabel, "90610", enrollmentHookAgentLabel, enrollmentHookSlowLabel), func() {
		harness := e2e.GetWorkerHarness()
		labels := enrollmentHookLabels(harness)

		statusCode, err := createEnrollmentHookPolicy(harness, newEnrollmentHookPolicy("", v1beta1.FailurePolicyBlock))
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusCreated))
		Expect(harness.CreateFleetWithSelector(enrollmentHookFleetName, labels)).To(Succeed())

		enrollmentID, err := waitForEnrollmentRequest(harness)
		Expect(err).ToNot(HaveOccurred())
		statusCode, err = approveEnrollmentRequest(harness, enrollmentID, labels)
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusOK))

		condition, err := waitForEnrollmentHooksCondition(harness, enrollmentID, v1beta1.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonPending)
		Expect(err).ToNot(HaveOccurred())
		Expect(condition.Status).To(Equal(v1beta1.ConditionStatusFalse))
		Expect(condition.Reason).To(Equal(v1beta1.EnrollmentHooksReasonPending))

		device, err := harness.GetDevice(enrollmentID)
		Expect(err).ToNot(HaveOccurred())
		Expect(device.Metadata.Owner).To(BeNil())
		renderedStatus, err := waitForStatusCode(harness, enrollmentID, http.StatusConflict, getRenderedDeviceStatusCode)
		Expect(err).ToNot(HaveOccurred())
		Expect(renderedStatus).To(Equal(http.StatusConflict))
		Expect(harness.PathDoesNotExistOnDevice(enrollmentHookAfterMarker)).To(Succeed())

		Expect(harness.WriteAgentFile(enrollmentHookReleaseFile, "")).To(Succeed())
		condition, err = waitForEnrollmentHooksCondition(harness, enrollmentID, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)
		Expect(err).ToNot(HaveOccurred())
		Expect(condition.Status).To(Equal(v1beta1.ConditionStatusTrue))
		Expect(condition.Reason).To(Equal(v1beta1.EnrollmentHooksReasonSucceeded))
		Expect(harness.PathExistsOnDevice(enrollmentHookAfterMarker)).To(Succeed())

		device, err = waitForDeviceOwner(harness, enrollmentID)
		Expect(err).ToNot(HaveOccurred())
		Expect(device.Metadata.Owner).ToNot(BeNil())
		Expect(*device.Metadata.Owner).To(ContainSubstring(enrollmentHookFleetName))
		renderedStatus, err = waitForStatusCode(harness, enrollmentID, http.StatusOK, getRenderedDeviceStatusCode)
		Expect(err).ToNot(HaveOccurred())
		Expect(renderedStatus).To(Equal(http.StatusOK))
	})

	It("runs the full hook cycle again after decommission and re-enrollment", Label(enrollmentHookDevLabel, enrollmentHookReEnrollmentLabel, enrollmentHookAgentLabel, enrollmentHookSlowLabel), func() {
		harness := e2e.GetWorkerHarness()
		labels := enrollmentHookLabels(harness)

		statusCode, err := createEnrollmentHookPolicy(harness, newEnrollmentHookPolicy("", v1beta1.FailurePolicyBlock))
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusCreated))
		Expect(harness.CreateFleetWithSelector(enrollmentHookFleetName, labels)).To(Succeed())
		Expect(harness.PathDoesNotExistOnDevice(enrollmentHookBeforeMarker)).To(Succeed())
		Expect(harness.PathDoesNotExistOnDevice(enrollmentHookAfterMarker)).To(Succeed())
		Expect(harness.StartAgentWithRetry()).To(Succeed())

		enrollmentID, err := waitForEnrollmentRequest(harness)
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.PathExistsOnDevice(enrollmentHookBeforeMarker)).To(Succeed())
		statusCode, err = approveEnrollmentRequest(harness, enrollmentID, labels)
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusOK))

		condition, err := waitForEnrollmentHooksCondition(harness, enrollmentID, v1beta1.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonPending)
		Expect(err).ToNot(HaveOccurred())
		Expect(condition.Reason).To(Equal(v1beta1.EnrollmentHooksReasonPending))
		device, err := harness.GetDevice(enrollmentID)
		Expect(err).ToNot(HaveOccurred())
		Expect(device.Status).ToNot(BeNil())
		Expect(device.Status.EnrollmentHooks).ToNot(BeNil())
		Expect(device.Status.EnrollmentHooks.Snapshot).ToNot(BeNil())
		Expect(harness.PathDoesNotExistOnDevice(enrollmentHookAfterMarker)).To(Succeed())

		Expect(harness.WriteAgentFile(enrollmentHookReleaseFile, "")).To(Succeed())
		condition, err = waitForEnrollmentHooksCondition(harness, enrollmentID, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)
		Expect(err).ToNot(HaveOccurred())
		Expect(condition.Reason).To(Equal(v1beta1.EnrollmentHooksReasonSucceeded))
		Expect(harness.PathExistsOnDevice(enrollmentHookAfterMarker)).To(Succeed())
	})
})

func prepareImageBackedReEnrollment(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	if auxSvcs == nil || auxSvcs.Registry == nil {
		return fmt.Errorf("registry auxiliary service is not initialized")
	}

	deviceID, device := harness.EnrollAndWaitForOnlineStatus()
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("enrollment returned an empty device ID")
	}
	if device == nil {
		return fmt.Errorf("enrollment returned a nil device for %s", deviceID)
	}
	if strings.TrimSpace(auxSvcs.Registry.Host) == "" || strings.TrimSpace(auxSvcs.Registry.Port) == "" {
		return fmt.Errorf("registry auxiliary service has an empty host or port")
	}
	nextVersion, err := harness.PrepareNextDeviceVersion(deviceID)
	if err != nil {
		return fmt.Errorf("prepare device %s image update: %w", deviceID, err)
	}
	imageRef := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V13)
	if err := harness.UpdateDeviceWithRetries(deviceID, func(device *v1beta1.Device) {
		device.Spec.Os = &v1beta1.DeviceOsSpec{Image: imageRef}
	}); err != nil {
		return fmt.Errorf("update device %s to enrollment-hook image: %w", deviceID, err)
	}
	if err := harness.WaitForDeviceNewRenderedVersionWithReboot(deviceID, nextVersion); err != nil {
		return fmt.Errorf("wait for enrollment-hook image on device %s: %w", deviceID, err)
	}
	decommissionOutput, err := harness.DecommissionDevice(deviceID)
	if err != nil {
		return fmt.Errorf("decommission device %s: %w", deviceID, err)
	}
	if !strings.Contains(decommissionOutput, fmt.Sprintf("%d", http.StatusOK)) {
		return fmt.Errorf("decommission device %s returned unexpected output: %q", deviceID, strings.TrimSpace(decommissionOutput))
	}
	registerDecommissionedDeviceCleanup(harness, deviceID)
	if err := harness.ResetAgentEnrollmentState(); err != nil {
		return fmt.Errorf("reset agent enrollment state after decommission: %w", err)
	}
	if err := removeEnrollmentHookMarkers(harness); err != nil {
		return err
	}
	return nil
}

func registerDecommissionedDeviceCleanup(harness *e2e.Harness, deviceID string) {
	DeferCleanup(func() {
		if harness == nil || strings.TrimSpace(deviceID) == "" {
			return
		}
		if err := harness.DeleteDeviceIgnoreNotFound(deviceID); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete decommissioned device %s: %v\n", deviceID, err)
		}
	})
}

func newEnrollmentHookPolicy(notifyURL string, failurePolicy v1beta1.FailurePolicyType) v1beta1.EnrollmentHookPolicy {
	policyName := enrollmentHookPolicyName
	policy := v1beta1.EnrollmentHookPolicy{
		ApiVersion: v1beta1.EnrollmentHookPolicyAPIVersion,
		Kind:       v1beta1.EnrollmentHookPolicyKind,
		Metadata:   v1beta1.ObjectMeta{Name: &policyName},
		Spec:       v1beta1.EnrollmentHookPolicySpec{AfterEnrolling: v1beta1.EnrollmentHookStageSpec{FailurePolicy: &failurePolicy}},
	}
	if notifyURL != "" {
		actions := []v1beta1.EnrollmentHookHttpAction{{Url: notifyURL}}
		policy.Spec.AfterEnrolling.ControlPlaneActions = &actions
	}
	return policy
}

func createEnrollmentHookPolicy(harness *e2e.Harness, policy v1beta1.EnrollmentHookPolicy) (int, error) {
	if harness == nil || harness.Client == nil {
		return 0, fmt.Errorf("harness or client is nil")
	}
	labels, err := harness.TestResourceLabels()
	if err != nil {
		return 0, fmt.Errorf("get policy labels: %w", err)
	}
	policy.Metadata.Labels = &labels
	response, err := harness.Client.CreateEnrollmentHookPolicyWithResponse(harness.Context, policy)
	if err != nil {
		return 0, fmt.Errorf("create enrollment hook policy: %w", err)
	}
	if response == nil {
		return 0, fmt.Errorf("create enrollment hook policy returned nil response")
	}
	if response.StatusCode() != http.StatusCreated {
		return response.StatusCode(), fmt.Errorf("create enrollment hook policy returned status %d: %s", response.StatusCode(), strings.TrimSpace(string(response.Body)))
	}
	return response.StatusCode(), nil
}

func waitForEnrollmentRequest(harness *e2e.Harness) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("harness is nil")
	}
	enrollmentID, err := harness.WaitForEnrollmentIDFromAgentLogs(TIMEOUT, POLLING)
	if err != nil {
		return "", fmt.Errorf("wait for enrollment ID: %w", err)
	}
	if enrollmentID == "" {
		return "", fmt.Errorf("agent returned an empty enrollment ID")
	}
	if _, err := harness.WaitForEnrollmentRequestResource(enrollmentID, TIMEOUT, POLLING); err != nil {
		return "", fmt.Errorf("wait for enrollment request %s: %w", enrollmentID, err)
	}
	return enrollmentID, nil
}

func approveEnrollmentRequest(harness *e2e.Harness, enrollmentID string, labels map[string]string) (int, error) {
	if harness == nil || harness.Client == nil {
		return 0, fmt.Errorf("harness or client is nil")
	}
	if enrollmentID == "" {
		return 0, fmt.Errorf("enrollment ID is empty")
	}
	response, err := harness.Client.ApproveEnrollmentRequestWithResponse(harness.Context, enrollmentID, v1beta1.EnrollmentRequestApproval{Approved: true, Labels: &labels})
	if err != nil {
		return 0, fmt.Errorf("approve enrollment request %s: %w", enrollmentID, err)
	}
	if response == nil {
		return 0, fmt.Errorf("approve enrollment request %s returned nil response", enrollmentID)
	}
	return response.StatusCode(), nil
}

func denyEnrollmentRequest(harness *e2e.Harness, enrollmentID string) (int, error) {
	if harness == nil || harness.Client == nil {
		return 0, fmt.Errorf("harness or client is nil")
	}
	if enrollmentID == "" {
		return 0, fmt.Errorf("enrollment ID is empty")
	}
	response, err := harness.Client.ApproveEnrollmentRequestWithResponse(harness.Context, enrollmentID, v1beta1.EnrollmentRequestApproval{Approved: false})
	if err != nil {
		return 0, fmt.Errorf("deny enrollment request %s: %w", enrollmentID, err)
	}
	if response == nil {
		return 0, fmt.Errorf("deny enrollment request %s returned nil response", enrollmentID)
	}
	return response.StatusCode(), nil
}

func waitForEnrollmentHooksCondition(harness *e2e.Harness, deviceID string, status v1beta1.ConditionStatus, reason string) (v1beta1.Condition, error) {
	if harness == nil {
		return v1beta1.Condition{}, fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(deviceID) == "" {
		return v1beta1.Condition{}, fmt.Errorf("device ID is empty")
	}
	deadline := time.Now().Add(util.DURATION_TIMEOUT)
	var lastErr error
	for time.Now().Before(deadline) {
		device, err := harness.GetDevice(deviceID)
		if err == nil {
			if condition, found := enrollmentHooksCondition(device); found && condition.Status == status && condition.Reason == reason {
				return condition, nil
			}
			lastErr = fmt.Errorf("device %s condition is not %s/%s", deviceID, status, reason)
		} else {
			lastErr = err
		}
		time.Sleep(util.POLLING)
	}
	return v1beta1.Condition{}, fmt.Errorf("timed out waiting for device %s EnrollmentHooks=%s/%s: %w", deviceID, status, reason, lastErr)
}

func waitForDeviceOwner(harness *e2e.Harness, deviceID string) (*v1beta1.Device, error) {
	if harness == nil {
		return nil, fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(deviceID) == "" {
		return nil, fmt.Errorf("device ID is empty")
	}
	deadline := time.Now().Add(util.DURATION_TIMEOUT)
	var lastErr error
	for time.Now().Before(deadline) {
		device, err := harness.GetDevice(deviceID)
		if err == nil && device != nil && device.Metadata.Owner != nil && *device.Metadata.Owner != "" {
			return device, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("device owner is empty")
		}
		time.Sleep(util.POLLING)
	}
	return nil, fmt.Errorf("timed out waiting for device %s owner: %w", deviceID, lastErr)
}

func getDeviceStatusCode(harness *e2e.Harness, deviceID string) (int, error) {
	if harness == nil || harness.Client == nil {
		return 0, fmt.Errorf("harness or client is nil")
	}
	if strings.TrimSpace(deviceID) == "" {
		return 0, fmt.Errorf("device ID is empty")
	}
	response, err := harness.Client.GetDeviceWithResponse(harness.Context, deviceID)
	if err != nil {
		return 0, err
	}
	if response == nil {
		return 0, fmt.Errorf("get device %s returned nil response", deviceID)
	}
	return response.StatusCode(), nil
}

func getRenderedDeviceStatusCode(harness *e2e.Harness, deviceID string) (int, error) {
	if harness == nil || harness.Client == nil {
		return 0, fmt.Errorf("harness or client is nil")
	}
	if strings.TrimSpace(deviceID) == "" {
		return 0, fmt.Errorf("device ID is empty")
	}
	response, err := harness.Client.GetRenderedDeviceWithResponse(harness.Context, deviceID, nil)
	if err != nil {
		return 0, err
	}
	if response == nil {
		return 0, fmt.Errorf("get rendered device %s returned nil response", deviceID)
	}
	return response.StatusCode(), nil
}

// waitForStatusCode polls a resource endpoint until it returns the expected HTTP status.
func waitForStatusCode(harness *e2e.Harness, deviceID string, expectedStatus int, getStatus statusCodeGetter) (int, error) {
	if harness == nil {
		return 0, fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(deviceID) == "" {
		return 0, fmt.Errorf("device ID is empty")
	}
	if getStatus == nil {
		return 0, fmt.Errorf("status getter is nil")
	}
	deadline := time.Now().Add(util.DURATION_TIMEOUT)
	var lastStatus int
	var lastErr error
	for time.Now().Before(deadline) {
		status, err := getStatus(harness, deviceID)
		if err == nil {
			lastStatus = status
			if status == expectedStatus {
				return status, nil
			}
		} else {
			lastErr = err
		}
		time.Sleep(util.POLLING)
	}
	if lastErr != nil {
		return lastStatus, fmt.Errorf("timed out waiting for device %s status %d; last status %d: %w", deviceID, expectedStatus, lastStatus, lastErr)
	}
	return lastStatus, fmt.Errorf("timed out waiting for device %s status %d; last status %d", deviceID, expectedStatus, lastStatus)
}

// waitForPathAbsenceAfterAgentPoll waits for an enrollment verification and observes sustained marker absence.
func waitForPathAbsenceAfterAgentPoll(harness *e2e.Harness, path string) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is empty")
	}

	agentPollInterval, err := enrollmentVerifyInterval(harness)
	if err != nil {
		return err
	}
	GinkgoWriter.Printf("Waiting %s for the agent to observe the enrollment before checking marker absence for %s\n", agentPollInterval, path)
	time.Sleep(agentPollInterval)
	deadline := time.Now().Add(time.Duration(enrollmentHookAbsenceObservationWindows) * agentPollInterval)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := harness.PathDoesNotExistOnDevice(path); err != nil {
			lastErr = err
			GinkgoWriter.Printf("Marker absence check for %s failed during observation: %v\n", path, err)
		}
		time.Sleep(util.POLLING)
	}
	if lastErr != nil {
		return fmt.Errorf("path %s was not absent for the full observation period: %w", path, lastErr)
	}
	return nil
}

func enrollmentHooksCondition(device *v1beta1.Device) (v1beta1.Condition, bool) {
	if device == nil || device.Status == nil {
		return v1beta1.Condition{}, false
	}
	for _, condition := range device.Status.Conditions {
		if condition.Type == v1beta1.ConditionTypeDeviceEnrollmentHooks {
			return condition, true
		}
	}
	return v1beta1.Condition{}, false
}

func enrollmentHookLabels(harness *e2e.Harness) map[string]string {
	if harness == nil {
		return map[string]string{enrollmentHookLabelKey: "invalid-harness"}
	}
	return map[string]string{enrollmentHookLabelKey: harness.GetTestIDFromContext()}
}

func removeEnrollmentHookMarkers(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	for _, path := range []string{enrollmentHookBeforeMarker, enrollmentHookAfterMarker, enrollmentHookReleaseFile} {
		if err := harness.RemoveAgentFile(path); err != nil {
			return fmt.Errorf("remove enrollment hook marker %s: %w", path, err)
		}
	}
	return nil
}

func isEnrollmentHookScenario(labels []string) bool {
	for _, label := range labels {
		if label == enrollmentHookScenarioLabel {
			return true
		}
	}
	return false
}

// isEnrollmentHookReEnrollmentScenario identifies the lifecycle spec that keeps the agent stopped during setup.
func isEnrollmentHookReEnrollmentScenario(labels []string) bool {
	for _, label := range labels {
		if label == enrollmentHookReEnrollmentLabel {
			return true
		}
	}
	return false
}

// enrollmentVerifyInterval reads the agent's configured enrollment verification interval.
func enrollmentVerifyInterval(harness *e2e.Harness) (time.Duration, error) {
	if harness == nil {
		return 0, fmt.Errorf("harness is nil")
	}
	agentConfig, err := harness.GetAgentConfig()
	if err != nil {
		return 0, fmt.Errorf("read agent config: %w", err)
	}
	if agentConfig == nil {
		return 0, fmt.Errorf("agent config is nil")
	}
	interval := time.Duration(agentConfig.EnrollmentVerifyInterval)
	if interval <= 0 {
		interval = time.Duration(agentcfg.DefaultEnrollmentVerifyInterval)
	}
	if interval <= 0 {
		return 0, fmt.Errorf("agent enrollment verify interval is not positive")
	}
	return interval, nil
}
