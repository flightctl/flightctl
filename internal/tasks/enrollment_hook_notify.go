package tasks

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	enrollmenthooknotifysecrets "github.com/flightctl/flightctl/internal/store/enrollmenthooknotifysecrets"
	"github.com/flightctl/flightctl/internal/webhookdelivery"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// enrollmentApprovedPayload is the JSON body POSTed to webhook endpoints.
type enrollmentApprovedPayload struct {
	APIVersion        string             `json:"apiVersion"`
	Kind              string             `json:"kind"`
	DeviceName        string             `json:"deviceName"`
	Labels            *map[string]string `json:"labels,omitempty"`
	CertificateSerial string             `json:"certificateSerial,omitempty"`
}

// buildEnrollmentApprovedPayload constructs the JSON payload for the
// EnrollmentApproved webhook notification.
func buildEnrollmentApprovedPayload(device *domain.Device, erName string, certSerial string) ([]byte, error) {
	payload := enrollmentApprovedPayload{
		APIVersion:        "v1beta1",
		Kind:              "EnrollmentApproved",
		DeviceName:        erName,
		CertificateSerial: certSerial,
	}
	if device != nil && device.Metadata.Labels != nil {
		payload.Labels = device.Metadata.Labels
	}
	return json.Marshal(payload)
}

// parseCertificateSerial extracts the certificate serial number from a
// PEM-encoded certificate and returns it as colon-separated uppercase hex.
func parseCertificateSerial(certPEM string) (string, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse certificate: %w", err)
	}
	bytes := cert.SerialNumber.Bytes()
	if len(bytes) == 0 {
		return "00", nil
	}
	parts := make([]string, len(bytes))
	for i, b := range bytes {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":"), nil
}

// deliveryConfigFromAction maps an enrollment-hook snapshot action onto the
// shared webhook delivery config (string durations → time.Duration).
func deliveryConfigFromAction(action domain.EnrollmentHookSnapshotAction) webhookdelivery.Config {
	cfg := webhookdelivery.Config{URL: action.Url}

	if action.Timeout != nil {
		if d, err := time.ParseDuration(*action.Timeout); err == nil && d > 0 {
			cfg.Timeout = d
		}
	}
	if action.Retry == nil {
		return cfg
	}

	r := action.Retry
	if r.MaxAttempts != nil && *r.MaxAttempts > 0 {
		cfg.MaxAttempts = *r.MaxAttempts
	}
	if r.Deadline != nil {
		if d, err := time.ParseDuration(*r.Deadline); err == nil && d > 0 {
			cfg.Deadline = d
		}
	}
	if r.MaxBackoff != nil {
		if d, err := time.ParseDuration(*r.MaxBackoff); err == nil && d > 0 {
			cfg.MaxBackoff = d
		}
	}
	if r.BackoffDelay != nil {
		if d, err := time.ParseDuration(*r.BackoffDelay); err == nil && d > 0 {
			cfg.BackoffDelay = d
		}
	}
	return cfg
}

func executeNotifyAction(ctx context.Context, action domain.EnrollmentHookSnapshotAction, bearerToken string, payload []byte, deliveryID string, log logrus.FieldLogger, httpClient *http.Client) error {
	return webhookdelivery.Deliver(ctx, httpClient, webhookdelivery.Delivery{
		Config:      deliveryConfigFromAction(action),
		BearerToken: bearerToken,
		Payload:     payload,
		DeliveryID:  deliveryID,
	}, log)
}

// enrollmentHookNotify is the main worker task that executes enrollment
// webhook notifications for all control-plane actions in the device's snapshot.
func enrollmentHookNotify(
	ctx context.Context,
	orgId uuid.UUID,
	event domain.Event,
	deviceSvc deviceservice.Service,
	enrollmentRequestSvc enrollmentrequestservice.Service,
	notifySecretsStore enrollmenthooknotifysecrets.Store,
	eventSvc eventservice.Service,
	log logrus.FieldLogger,
) error {
	httpClient := webhookdelivery.NewClient()
	defer httpClient.CloseIdleConnections()
	return enrollmentHookNotifyWithClient(ctx, orgId, event, deviceSvc, enrollmentRequestSvc, notifySecretsStore, eventSvc, log, httpClient)
}

// enrollmentHookNotifyWithClient is the same as enrollmentHookNotify but allows
// injecting an HTTP client (used by tests with httptest TLS servers).
func enrollmentHookNotifyWithClient(
	ctx context.Context,
	orgId uuid.UUID,
	event domain.Event,
	deviceSvc deviceservice.Service,
	enrollmentRequestSvc enrollmentrequestservice.Service,
	notifySecretsStore enrollmenthooknotifysecrets.Store,
	eventSvc eventservice.Service,
	log logrus.FieldLogger,
	httpClient *http.Client,
) error {
	// ctx is the worker root context from the consumer (not the per-event
	// EventProcessingTimeout). Apply the notify deadline here so delivery can
	// run up to the retry policy while still stopping on worker shutdown.
	notifyCtx, cancelNotify := context.WithTimeout(ctx, EnrollmentHookNotifyProcessingTimeout)
	defer cancelNotify()

	erName := event.InvolvedObject.Name

	// Load the device to get the enrollment hooks snapshot.
	device, status := deviceSvc.GetDevice(notifyCtx, orgId, erName)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get device %s: %s", erName, status.Message)
	}

	// Idempotency guard: only proceed if the condition is NotifyPending.
	if device.Status == nil || device.Status.EnrollmentHooks == nil || device.Status.EnrollmentHooks.Snapshot == nil {
		log.Infof("enrollment hook notify: device %s has no enrollment hooks snapshot, skipping", erName)
		return nil
	}

	cond := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceEnrollmentHooks)
	if cond == nil || cond.Reason != domain.EnrollmentHooksReasonNotifyPending {
		log.Infof("enrollment hook notify: device %s condition is not NotifyPending (reason=%s), skipping",
			erName, condReasonOrEmpty(cond))
		return nil
	}

	snapshot := device.Status.EnrollmentHooks.Snapshot
	if snapshot.ControlPlaneActions == nil || len(*snapshot.ControlPlaneActions) == 0 {
		log.Infof("enrollment hook notify: device %s has no control plane actions, marking Pending", erName)
		return setNotifyCondition(notifyCtx, deviceSvc, orgId, erName, domain.EnrollmentHooksReasonPending, "", log)
	}

	// Load the enrollment request for the certificate serial.
	certSerial := ""
	er, erStatus := enrollmentRequestSvc.GetEnrollmentRequest(notifyCtx, orgId, erName)
	if erStatus.Code == http.StatusOK && er.Status != nil && er.Status.Certificate != nil {
		serial, err := parseCertificateSerial(*er.Status.Certificate)
		if err != nil {
			log.WithError(err).Warnf("enrollment hook notify: failed to parse certificate serial for %s", erName)
		} else {
			certSerial = serial
		}
	}

	// Build the payload.
	payload, err := buildEnrollmentApprovedPayload(device, erName, certSerial)
	if err != nil {
		return fmt.Errorf("failed to build payload for %s: %w", erName, err)
	}

	// Load secrets (bearer tokens).
	secrets, err := notifySecretsStore.ListByDevice(notifyCtx, orgId, erName)
	if err != nil {
		return fmt.Errorf("failed to load notify secrets for %s: %w", erName, err)
	}
	secretMap := make(map[int]string, len(secrets))
	for _, s := range secrets {
		secretMap[s.ActionIndex] = s.BearerToken
	}

	// Execute each action with fail-fast on first failure.
	for _, action := range *snapshot.ControlPlaneActions {
		deliveryID := fmt.Sprintf("%s/%d", erName, action.Index)
		bearerToken := secretMap[action.Index]

		log.Infof("enrollment hook notify: executing action %s for device %s", deliveryID, erName)
		if err := executeNotifyAction(notifyCtx, action, bearerToken, payload, deliveryID, log, httpClient); err != nil {
			log.WithError(err).Errorf("enrollment hook notify: action %s failed for device %s", deliveryID, erName)
			return handleNotifyFailure(notifyCtx, deviceSvc, eventSvc, orgId, erName, snapshot.FailurePolicy, err, log)
		}
	}

	// All actions succeeded: transition to Pending.
	log.Infof("enrollment hook notify: all actions succeeded for device %s", erName)
	return setNotifyCondition(notifyCtx, deviceSvc, orgId, erName, domain.EnrollmentHooksReasonPending, "", log)
}

// handleNotifyFailure updates the device condition based on the failure policy.
func handleNotifyFailure(
	ctx context.Context,
	deviceSvc deviceservice.Service,
	eventSvc eventservice.Service,
	orgId uuid.UUID,
	deviceName string,
	failurePolicy domain.FailurePolicyType,
	notifyErr error,
	log logrus.FieldLogger,
) error {
	switch failurePolicy {
	case domain.FailurePolicyBlock:
		// Block + failure: SetDeviceServiceConditions(Failed) ONLY.
		// EnrollmentHookFailed event is auto-emitted by diffAndEmitConditionEvents.
		if err := setNotifyCondition(ctx, deviceSvc, orgId, deviceName, domain.EnrollmentHooksReasonFailed, notifyErr.Error(), log); err != nil {
			return err
		}
		return notifyErr

	case domain.FailurePolicyContinue:
		// Continue + failure: SetDeviceServiceConditions(Pending) FIRST,
		// then CreateEvent(NotifyFailed). Condition-first for redelivery safety.
		if err := setNotifyCondition(ctx, deviceSvc, orgId, deviceName, domain.EnrollmentHooksReasonPending, "", log); err != nil {
			return err
		}
		eventSvc.CreateEvent(ctx, orgId, common.GetEnrollmentHookNotifyFailedEvent(ctx, deviceName, notifyErr.Error()))
		return nil // Continue policy: return nil so enrollment proceeds

	default:
		// Default to Block behavior for unknown policies.
		if err := setNotifyCondition(ctx, deviceSvc, orgId, deviceName, domain.EnrollmentHooksReasonFailed, notifyErr.Error(), log); err != nil {
			return err
		}
		return notifyErr
	}
}

// setNotifyCondition updates the EnrollmentHooks condition on the device.
func setNotifyCondition(
	ctx context.Context,
	deviceSvc deviceservice.Service,
	orgId uuid.UUID,
	deviceName string,
	reason string,
	message string,
	log logrus.FieldLogger,
) error {
	condition := domain.Condition{
		Type:    domain.ConditionTypeDeviceEnrollmentHooks,
		Status:  domain.ConditionStatusFalse,
		Reason:  reason,
		Message: message,
	}
	status := deviceSvc.SetDeviceServiceConditions(ctx, orgId, deviceName, []domain.Condition{condition})
	if status.Code != http.StatusOK {
		log.Errorf("enrollment hook notify: failed to set condition for device %s: %s", deviceName, status.Message)
		return fmt.Errorf("failed to set EnrollmentHooks condition for device %s: %s", deviceName, status.Message)
	}
	return nil
}

// condReasonOrEmpty safely returns the reason from a condition, or empty string if nil.
func condReasonOrEmpty(cond *domain.Condition) string {
	if cond == nil {
		return ""
	}
	return cond.Reason
}
