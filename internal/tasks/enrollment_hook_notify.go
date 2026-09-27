package tasks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	enrollmenthooknotifysecrets "github.com/flightctl/flightctl/internal/store/enrollmenthooknotifysecrets"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	maxResponseBodySize = 4096 // 4 KiB cap for empty-body validation

	// Default retry policy values (matching API-level defaults).
	defaultMaxAttempts  = 5
	defaultDeadline     = 10 * time.Minute
	defaultMaxBackoff   = 2 * time.Minute
	defaultBackoffDelay = 2 * time.Second
	defaultTimeout      = 30 * time.Second
)

// enrollmentApprovedPayload is the JSON body POSTed to webhook endpoints.
type enrollmentApprovedPayload struct {
	APIVersion        string             `json:"apiVersion"`
	Kind              string             `json:"kind"`
	DeviceName        string             `json:"deviceName"`
	Labels            *map[string]string `json:"labels,omitempty"`
	CertificateSerial string             `json:"certificateSerial,omitempty"`
}

// classifyHTTPStatus determines whether an HTTP response status code is
// retryable and returns a human-readable description.
func classifyHTTPStatus(statusCode int) (retryable bool, message string) {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return false, ""
	case statusCode == http.StatusBadRequest,
		statusCode == http.StatusUnauthorized,
		statusCode == http.StatusForbidden,
		statusCode == http.StatusNotFound:
		return false, fmt.Sprintf("non-retryable HTTP %d", statusCode)
	case statusCode == http.StatusTooManyRequests:
		return true, fmt.Sprintf("retryable HTTP %d", statusCode)
	case statusCode >= 500:
		return true, fmt.Sprintf("retryable HTTP %d", statusCode)
	default:
		return false, fmt.Sprintf("non-retryable HTTP %d", statusCode)
	}
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

// resolveRetryConfig extracts retry parameters from a snapshot action,
// falling back to defaults where unset.
func resolveRetryConfig(action domain.EnrollmentHookSnapshotAction) (maxAttempts int, deadline, maxBackoff, backoffDelay, timeout time.Duration) {
	maxAttempts = defaultMaxAttempts
	deadline = defaultDeadline
	maxBackoff = defaultMaxBackoff
	backoffDelay = defaultBackoffDelay
	timeout = defaultTimeout

	if action.Timeout != nil {
		if d, err := time.ParseDuration(*action.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}

	if action.Retry == nil {
		return
	}

	r := action.Retry
	if r.MaxAttempts != nil && *r.MaxAttempts > 0 {
		maxAttempts = *r.MaxAttempts
	}
	if r.Deadline != nil {
		if d, err := time.ParseDuration(*r.Deadline); err == nil && d > 0 {
			deadline = d
		}
	}
	if r.MaxBackoff != nil {
		if d, err := time.ParseDuration(*r.MaxBackoff); err == nil && d > 0 {
			maxBackoff = d
		}
	}
	if r.BackoffDelay != nil {
		if d, err := time.ParseDuration(*r.BackoffDelay); err == nil && d > 0 {
			backoffDelay = d
		}
	}

	return
}

// executeNotifyAction performs a single webhook POST with retry.
// It returns nil on 2xx success and an error otherwise.
func executeNotifyAction(ctx context.Context, action domain.EnrollmentHookSnapshotAction, bearerToken string, payload []byte, deliveryID string, log logrus.FieldLogger, httpClient *http.Client) error {
	// HTTPS-only runtime guard (reject http, missing scheme, and non-https schemes).
	parsedURL, err := url.Parse(action.Url)
	if err != nil || parsedURL.Scheme != "https" {
		return &nonRetryableError{fmt.Errorf("insecure or invalid URL rejected: %s", action.Url)}
	}

	maxAttempts, deadline, maxBackoff, backoffDelay, timeout := resolveRetryConfig(action)
	deadlineCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := deadlineCtx.Err(); err != nil {
			return fmt.Errorf("deadline exceeded after %d attempts: %w", attempt-1, err)
		}

		reqCtx, reqCancel := context.WithTimeout(deadlineCtx, timeout)
		err = doNotifyRequest(reqCtx, httpClient, action.Url, bearerToken, payload, deliveryID, attempt, log)
		reqCancel()

		if err == nil {
			return nil
		}

		// Check if this is a non-retryable error
		if isNonRetryableError(err) {
			return err
		}

		if attempt < maxAttempts {
			backoff := computeBackoff(attempt, backoffDelay, maxBackoff)
			log.Infof("notify action %s: attempt %d/%d failed (%v), retrying in %s", deliveryID, attempt, maxAttempts, err, backoff)

			select {
			case <-deadlineCtx.Done():
				return fmt.Errorf("deadline exceeded during backoff after %d attempts: %w", attempt, deadlineCtx.Err())
			case <-time.After(backoff):
			}
		} else {
			return fmt.Errorf("all %d attempts exhausted: %w", maxAttempts, err)
		}
	}

	return fmt.Errorf("all %d attempts exhausted", maxAttempts)
}

// nonRetryableError wraps an error to signal that it should not be retried.
type nonRetryableError struct {
	err error
}

func (e *nonRetryableError) Error() string { return e.err.Error() }
func (e *nonRetryableError) Unwrap() error { return e.err }

func isNonRetryableError(err error) bool {
	var nre *nonRetryableError
	return errors.As(err, &nre)
}

// doNotifyRequest performs a single HTTP POST and returns nil on 2xx.
func doNotifyRequest(ctx context.Context, client *http.Client, url, bearerToken string, payload []byte, deliveryID string, attempt int, log logrus.FieldLogger) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return &nonRetryableError{fmt.Errorf("failed to create request: %w", err)}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flightctl-Delivery-Id", deliveryID)
	req.Header.Set("X-Flightctl-Delivery-Attempt", fmt.Sprintf("%d", attempt))
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		// Network errors and TLS errors are retryable
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Cap response body read at 4 KiB
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodySize))

	retryable, msg := classifyHTTPStatus(resp.StatusCode)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Infof("notify action %s: attempt %d succeeded with HTTP %d", deliveryID, attempt, resp.StatusCode)
		return nil
	}
	if !retryable {
		return &nonRetryableError{fmt.Errorf("%s from %s", msg, url)}
	}
	return fmt.Errorf("%s from %s", msg, url)
}

// computeBackoff calculates exponential backoff with a cap.
func computeBackoff(attempt int, baseDelay, maxBackoff time.Duration) time.Duration {
	backoff := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt-1)))
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff
}

// newNotifyHTTPClient creates an HTTP client configured for webhook delivery:
// TLS verification enabled, redirects disabled.
func newNotifyHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
	return enrollmentHookNotifyWithClient(ctx, orgId, event, deviceSvc, enrollmentRequestSvc, notifySecretsStore, eventSvc, log, newNotifyHTTPClient())
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
	erName := event.InvolvedObject.Name

	// Load the device to get the enrollment hooks snapshot.
	device, status := deviceSvc.GetDevice(ctx, orgId, erName)
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
		return setNotifyCondition(ctx, deviceSvc, orgId, erName, domain.EnrollmentHooksReasonPending, "", log)
	}

	// Load the enrollment request for the certificate serial.
	certSerial := ""
	er, erStatus := enrollmentRequestSvc.GetEnrollmentRequest(ctx, orgId, erName)
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
	secrets, err := notifySecretsStore.ListByDevice(ctx, orgId, erName)
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
		if err := executeNotifyAction(ctx, action, bearerToken, payload, deliveryID, log, httpClient); err != nil {
			log.WithError(err).Errorf("enrollment hook notify: action %s failed for device %s", deliveryID, erName)
			return handleNotifyFailure(ctx, deviceSvc, eventSvc, orgId, erName, snapshot.FailurePolicy, err, log)
		}
	}

	// All actions succeeded: transition to Pending.
	log.Infof("enrollment hook notify: all actions succeeded for device %s", erName)
	return setNotifyCondition(ctx, deviceSvc, orgId, erName, domain.EnrollmentHooksReasonPending, "", log)
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
