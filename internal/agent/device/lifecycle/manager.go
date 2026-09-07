package lifecycle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	deviceos "github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/identity"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/skip2/go-qrcode"
	"github.com/stoewer/go-strcase"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/cert"
)

const (
	// agent banner file
	BannerFile           = "/etc/issue.d/flightctl-banner.issue"
	identityProofTimeout = 60 * time.Second
)

var (
	_ Manager     = (*LifecycleManager)(nil)
	_ Initializer = (*LifecycleManager)(nil)
)

// LifecycleManager struct needs to hold a reference to the management config
type LifecycleManager struct {
	deviceName           string
	enrollmentUIEndpoint string
	managementCertPath   string
	managementKeyPath    string
	dataDir              string
	deviceReadWriter     fileio.ReadWriter

	enrollmentClient    client.Enrollment
	defaultLabels       map[string]string
	labelFromSystemInfo map[string]string
	caps                deviceos.Capabilities
	enrollmentCSR       []byte
	statusManager       status.Manager
	systemdClient       *client.Systemd
	identityProvider    identity.Provider

	hookManager                hook.Manager
	preEnrollmentFailurePolicy string
	preEnrollmentBackoff       wait.Backoff

	backoff wait.Backoff
	log     *log.PrefixLogger
}

// preEnrollmentResult carries the outcome of pre-enrollment hooks for the ER.
type preEnrollmentResult struct {
	success    bool
	output     string
	hookLabels map[string]string
}

const maxPreEnrollmentOutput = 4096

// NewManager creates a LifecycleManager responsible for device enrollment.
func NewManager(
	deviceName string,
	enrollmentUIEndpoint string,
	managementCertPath string,
	managementKeyPath string,
	dataDir string,
	deviceReadWriter fileio.ReadWriter,
	enrollmentClient client.Enrollment,
	enrollmentCSR []byte,
	defaultLabels map[string]string,
	labelFromSystemInfo map[string]string,
	caps deviceos.Capabilities,
	statusManager status.Manager,
	systemdClient *client.Systemd,
	identityProvider identity.Provider,
	hookManager hook.Manager,
	preEnrollmentFailurePolicy string,
	backoff wait.Backoff,
	log *log.PrefixLogger,
) *LifecycleManager {
	return &LifecycleManager{
		log:                        log,
		deviceName:                 deviceName,
		enrollmentUIEndpoint:       enrollmentUIEndpoint,
		managementCertPath:         managementCertPath,
		managementKeyPath:          managementKeyPath,
		dataDir:                    dataDir,
		deviceReadWriter:           deviceReadWriter,
		enrollmentClient:           enrollmentClient,
		enrollmentCSR:              enrollmentCSR,
		defaultLabels:              defaultLabels,
		labelFromSystemInfo:        labelFromSystemInfo,
		caps:                       caps,
		backoff:                    backoff,
		statusManager:              statusManager,
		systemdClient:              systemdClient,
		identityProvider:           identityProvider,
		hookManager:                hookManager,
		preEnrollmentFailurePolicy: preEnrollmentFailurePolicy,
		preEnrollmentBackoff: wait.Backoff{
			Steps:    10,
			Duration: 5 * time.Second,
			Factor:   2.0,
			Cap:      5 * time.Minute,
		},
	}
}

// Initialize ensures the device is enrolled to the management service.
func (m *LifecycleManager) Initialize(ctx context.Context, status *v1beta1.DeviceStatus) error {
	if !m.IsInitialized() {
		preResult, err := m.runPreEnrollmentHooks(ctx, status)
		if err != nil {
			return err
		}

		if err := m.enrollmentRequest(ctx, status, preResult); err != nil {
			return err
		}

		if err := m.writeEnrollmentBanner(ctx); err != nil {
			return err
		}

		m.log.Info("Waiting for enrollment to be approved")
		err = wait.ExponentialBackoffWithContext(ctx, m.backoff, func(ctx context.Context) (bool, error) {
			return m.verifyEnrollment(ctx)
		})
		if err != nil {
			return err
		}
	}

	// write the management banner
	return m.writeManagementBanner(ctx)
}

func (m *LifecycleManager) Sync(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	// this controller currently does not implement a sync operation
	return nil
}

func (m *LifecycleManager) AfterUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	var errs []error
	if current.Decommissioning == nil && desired.Decommissioning != nil {
		m.log.Warn("Detected decommissioning request from flightctl service")
		m.log.Warn("Updating Condition to decommissioning started")
		if err := m.updateWithStartedCondition(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%w started Condition: %w", errors.ErrFailedToUpdateStatusWithDecommission, err))
			m.log.Warn("Unable to update Condition to decommissioning started")
		}

		// TODO: add support for additional decommissioning target types.
		// these are the steps that will take places between Started and Completed status

		if len(errs) == 0 {
			m.log.Warn("No errors during decommissioning prior to wiping key and cert; updating Condition to decommissioning completed")
			if err := m.updateWithCompletedCondition(ctx); err != nil {
				errs = append(errs, fmt.Errorf("%w completed Condition: %w", errors.ErrFailedToUpdateStatusWithDecommission, err))
				m.log.Warn("Unable to update Condition to decommissioning completed")
			}
		} else {
			m.log.Warn("Errors encountered during decommissioning; updating Condition to decommission error")
			if err := m.updateWithErrorCondition(ctx, errs); err != nil {
				errs = append(errs, fmt.Errorf("%w errored Condition: %w", errors.ErrFailedToUpdateStatusWithDecommission, err))
				m.log.Warn("Unable to update Condition to decommissioning error")
			}
		}

		// after this point the device will no longer be able to communicate with the management service
		m.log.Warn("Preparing to wipe agent certificate and keys and reboot")
		if err := m.wipeAndReboot(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (m *LifecycleManager) updateWithStartedCondition(ctx context.Context) error {
	updateErr := m.statusManager.UpdateCondition(ctx, v1beta1.Condition{
		Type:    v1beta1.ConditionTypeDeviceDecommissioning,
		Status:  v1beta1.ConditionStatusTrue,
		Reason:  string(v1beta1.DecommissionStateStarted),
		Message: "Device started decommissioning",
	})
	if updateErr != nil {
		m.log.Warnf("Failed setting status: %v", updateErr)
		return fmt.Errorf("failed to update decommission started status: %w", updateErr)
	}
	return nil
}

func (m *LifecycleManager) updateWithCompletedCondition(ctx context.Context) error {
	updateErr := m.statusManager.UpdateCondition(ctx, v1beta1.Condition{
		Type:    v1beta1.ConditionTypeDeviceDecommissioning,
		Status:  v1beta1.ConditionStatusTrue,
		Reason:  string(v1beta1.DecommissionStateComplete),
		Message: "Device completed decommissioning and will wipe its management certificate",
	})
	if updateErr != nil {
		m.log.Warnf("Failed setting status: %v", updateErr)
		return fmt.Errorf("failed to update decommission completed status: %w", updateErr)
	}
	return nil
}

func (m *LifecycleManager) updateWithErrorCondition(ctx context.Context, errs []error) error {
	updateErr := m.statusManager.UpdateCondition(ctx, v1beta1.Condition{
		Type:    v1beta1.ConditionTypeDeviceDecommissioning,
		Status:  v1beta1.ConditionStatusTrue,
		Reason:  string(v1beta1.DecommissionStateError),
		Message: fmt.Sprintf("Device encountered one or more errors during decommissioning: %v", errors.Join(errs...)),
	})
	if updateErr != nil {
		m.log.Warnf("Failed setting status: %v", updateErr)
		return fmt.Errorf("failed to update decommission errored status: %w", updateErr)
	}
	return nil
}

// point of no return - wipes management cert and keys
func (m *LifecycleManager) wipeAndReboot(ctx context.Context) error {
	var errs []error

	// Use identity provider to wipe credentials securely
	if err := m.identityProvider.WipeCredentials(); err != nil {
		m.log.Errorf("Failed to wipe credentials via identity provider: %v", err)
		errs = append(errs, fmt.Errorf("failed to wipe credentials via identity provider: %w", err))
	}

	// Clear sensitive data ahead of time in case reboot fails
	m.deviceName = ""
	m.enrollmentUIEndpoint = ""
	m.enrollmentClient = nil
	m.enrollmentCSR = nil

	// Delete all files in the data directory
	m.log.Warn("Deleting all files in data directory during decommissioning")
	if err := m.deleteAllDataDirFiles(); err != nil {
		m.log.Errorf("Failed to delete all data directory files: %v", err)
		errs = append(errs, fmt.Errorf("failed to delete all data directory files: %w", err))
	}

	// TODO: incorporate before-reboot hooks
	if err := m.systemdClient.Reboot(ctx); err != nil {
		errs = append(errs, fmt.Errorf("failed to initiate system reboot: %w", err))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// deleteAllDataDirFiles removes all files in the data directory recursively
func (m *LifecycleManager) deleteAllDataDirFiles() error {
	m.log.Infof("Deleting all files in data directory: %s", m.dataDir)

	// Read all top-level entries in the data directory
	entries, err := m.deviceReadWriter.ReadDir(m.dataDir)
	if err != nil {
		return fmt.Errorf("failed to read data directory: %w", err)
	}

	// Delete each entry (RemoveAll will recursively delete directories)
	var errs []error
	for _, entry := range entries {
		path := filepath.Join(m.dataDir, entry.Name())
		m.log.Debugf("Removing: %s", path)
		if err := m.deviceReadWriter.RemoveAll(path); err != nil {
			m.log.Warnf("Failed to remove %s: %v", path, err)
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to delete some files in data directory: %w", errors.Join(errs...))
	}

	m.log.Infof("Successfully deleted all files in data directory")
	return nil
}

func (m *LifecycleManager) IsInitialized() bool {
	// check if the identity provider has a certificate
	return m.identityProvider.HasCertificate()
}

func (m *LifecycleManager) verifyEnrollment(ctx context.Context) (bool, error) {
	enrollmentRequest, err := m.enrollmentClient.GetEnrollmentRequest(ctx, m.deviceName)
	if err != nil {
		m.log.Errorf("Error checking enrollment status: %v", err)
		return false, nil
	}

	// TODO: update schema to require condition in status, then remove this check
	if enrollmentRequest.Status == nil || enrollmentRequest.Status.Conditions == nil {
		return false, fmt.Errorf("enrollment request status or conditions field are nil")
	}

	approved := false
	for _, cond := range enrollmentRequest.Status.Conditions {
		if cond.Type == "Denied" {
			return false, fmt.Errorf("%w: reason: %v, message: %v", errors.ErrEnrollmentRequestDenied, cond.Reason, cond.Message)
		}
		if cond.Type == "Failed" {
			return false, fmt.Errorf("%w: reason: %v, message: %v", errors.ErrEnrollmentRequestFailed, cond.Reason, cond.Message)
		}
		if cond.Type == "Approved" {
			approved = true
		}
	}
	if !approved {
		// While pending approval, take the time to verify the identity. The provider
		// is responsible for determining whether proof is required
		ctx, cancel := context.WithTimeout(ctx, identityProofTimeout)
		defer cancel()
		if err := m.identityProvider.ProveIdentity(ctx, enrollmentRequest); err != nil {
			if errors.Is(err, identity.ErrIdentityProofFailed) {
				return false, fmt.Errorf("proving identity: %w", err)
			}
			m.log.Warnf("A retryable error occurred while proving the agent's identity: %v", err)
			return false, nil
		}
		m.log.Info("Enrollment request not yet approved")
		return false, nil
	}
	if enrollmentRequest.Status.Certificate == nil {
		m.log.Infof("Enrollment request approved, but certificate not yet issued")
		return false, nil
	}
	if len(*enrollmentRequest.Status.Certificate) == 0 {
		m.log.Infof("Enrollment request approved, but certificate not yet issued")
		return false, nil
	}
	m.log.Infof("Enrollment approved and certificate issued")

	if _, err = cert.ParseCertsPEM([]byte(*enrollmentRequest.Status.Certificate)); err != nil {
		return false, fmt.Errorf("parsing signed certificate: %v", err)
	}

	if err := m.identityProvider.StoreCertificate([]byte(*enrollmentRequest.Status.Certificate)); err != nil {
		return false, fmt.Errorf("failed to store certificate: %w", err)
	}

	// Clear the persisted CSR once certificate is obtained
	clientCSRPath := identity.GetCSRPath(m.dataDir)
	if _, found, err := identity.LoadCSR(m.deviceReadWriter, clientCSRPath); err == nil && found {
		m.log.Infof("Clearing persisted CSR after successful enrollment")
		if err := identity.StoreCSR(m.deviceReadWriter, clientCSRPath, nil); err != nil {
			m.log.Warnf("Failed to clear persisted CSR: %v", err)
		}
	}

	return true, nil
}

func (m *LifecycleManager) writeEnrollmentBanner(ctx context.Context) error {
	if m.enrollmentUIEndpoint == "" {
		m.log.Warn("Flightctl enrollment UI endpoint is missing, skipping enrollment banner")
		return nil
	}
	url := fmt.Sprintf("%s/enroll/%s", m.enrollmentUIEndpoint, m.deviceName)
	if err := m.writeQRBanner(ctx, "\nEnroll your device to flightctl by scanning\nthe above QR code or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("failed to write device enrollment banner: %w", err)
	}
	return nil
}

func (m *LifecycleManager) writeManagementBanner(ctx context.Context) error {
	// write a banner that explains that the device is enrolled
	if m.enrollmentUIEndpoint == "" {
		m.log.Warn("Flightctl enrollment UI endpoint is missing, skipping management banner")
		return nil
	}
	url := fmt.Sprintf("%s/manage/%s", m.enrollmentUIEndpoint, m.deviceName)
	if err := m.writeQRBanner(ctx, "\nYour device is enrolled to flightctl,\nyou can manage your device scanning the above QR. or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("failed to write device management banner: %w", err)
	}
	return nil
}

func (m *LifecycleManager) writeQRBanner(ctx context.Context, message, url string) error {
	qrCode, err := qrcode.New(url, qrcode.High)
	if err != nil {
		return fmt.Errorf("failed to generate new QR code: %w", err)
	}

	// convert the QR code to a string.
	qrString := qrCode.ToSmallString(false)

	// write a banner that explains that the device is enrolled
	buffer := bytes.NewBufferString("\n")
	buffer.WriteString(qrString)

	// write the QR code to the buffer
	fmt.Fprintf(buffer, message, url)

	// duplicate file to /etc/issue.d/flightctl-banner.issue
	if err := m.deviceReadWriter.WriteFile(BannerFile, buffer.Bytes(), fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("failed to write banner to disk: %w", err)
	}

	if err := m.systemdClient.SdNotify(ctx, "READY=1"); err != nil {
		m.log.Warnf("Failed to notify systemd: %v", err)
	}

	value := os.Getenv("FLIGHTCTL_DISABLE_CONSOLE_BANNER")
	if !(strings.EqualFold(value, "true") || value == "1") {
		fmt.Println(buffer.String())
	}
	return nil
}

func (m *LifecycleManager) runPreEnrollmentHooks(ctx context.Context, deviceStatus *v1beta1.DeviceStatus) (*preEnrollmentResult, error) {
	enrollCtx := &hook.EnrollmentContext{
		DeviceName:            m.deviceName,
		Labels:                make(map[string]string),
		ManagementCertificate: nil,
	}
	if deviceStatus != nil {
		sysInfoMap := make(map[string]interface{})
		sysInfoBytes, err := json.Marshal(deviceStatus.SystemInfo)
		if err == nil {
			_ = json.Unmarshal(sysInfoBytes, &sysInfoMap)
		}
		enrollCtx.SystemInfo = sysInfoMap
	}

	hookErr := m.hookManager.OnBeforeEnrolling(ctx, enrollCtx)

	result := &preEnrollmentResult{
		success:    enrollCtx.Success,
		output:     enrollCtx.Output,
		hookLabels: enrollCtx.HookLabels,
	}

	if hookErr != nil {
		result.success = false
		if m.preEnrollmentFailurePolicy == "Block" {
			m.log.Warnf("Pre-enrollment hook failed with Block policy, retrying with backoff: %v", hookErr)
			retryErr := wait.ExponentialBackoffWithContext(ctx, m.preEnrollmentBackoff, func(ctx context.Context) (bool, error) {
				retryCtx := &hook.EnrollmentContext{
					DeviceName:            enrollCtx.DeviceName,
					SystemInfo:            enrollCtx.SystemInfo,
					Labels:                make(map[string]string),
					ManagementCertificate: nil,
				}
				if err := m.hookManager.OnBeforeEnrolling(ctx, retryCtx); err != nil {
					m.log.Warnf("Pre-enrollment hook retry failed: %v", err)
					result.output = retryCtx.Output
					return false, nil
				}
				result.success = true
				result.output = retryCtx.Output
				result.hookLabels = retryCtx.HookLabels
				return true, nil
			})
			if retryErr != nil {
				return nil, fmt.Errorf("pre-enrollment hooks failed with Block policy (backoff exhausted): %w", retryErr)
			}
		} else {
			m.log.Warnf("Pre-enrollment hook failed with Continue policy, proceeding: %v", hookErr)
		}
	}

	// Redact and truncate output for persistence
	if result.output != "" {
		result.output = redactSecrets(result.output)
		result.output = truncateOutput(result.output, maxPreEnrollmentOutput)
	}

	return result, nil
}

func (m *LifecycleManager) enrollmentRequest(ctx context.Context, deviceStatus *v1beta1.DeviceStatus, preResult *preEnrollmentResult) error {
	var csrString string
	if tpm.IsTCGCSRFormat(m.enrollmentCSR) {
		// TCG CSR is binary data, must be base64 encoded
		m.log.Debugf("Detected TCG CSR format, base64 encoding before transmission")
		csrString = base64.StdEncoding.EncodeToString(m.enrollmentCSR)
	} else {
		csrString = string(m.enrollmentCSR)
	}

	// Try to read knownRenderedVersion from desired.json
	var knownRenderedVersion *string
	desiredPath := filepath.Join(m.dataDir, "desired.json")
	if desiredBytes, err := m.deviceReadWriter.ReadFile(desiredPath); err == nil {
		var desired v1beta1.Device
		if err := json.Unmarshal(desiredBytes, &desired); err == nil {
			if version := desired.Version(); version != "" {
				knownRenderedVersion = &version
				m.log.Debugf("Found knownRenderedVersion from desired.json: %s", version)
			}
		} else {
			m.log.Debugf("Failed to unmarshal desired.json: %v", err)
		}
	} else {
		m.log.Debugf("Failed to read desired.json: %v", err)
	}

	if deviceStatus != nil {
		deviceos.ApplyDeltaSystemInfo(&deviceStatus.SystemInfo, m.caps)
	}

	var hookLabels map[string]string
	if preResult != nil {
		hookLabels = preResult.hookLabels
	}
	enrollmentLabels := m.buildEnrollmentLabels(deviceStatus, hookLabels)

	req := v1beta1.EnrollmentRequest{
		ApiVersion: "v1beta1",
		Kind:       "EnrollmentRequest",
		Metadata: v1beta1.ObjectMeta{
			Name: &m.deviceName,
		},
		Spec: v1beta1.EnrollmentRequestSpec{
			Csr:                  csrString,
			DeviceStatus:         deviceStatus,
			Labels:               &enrollmentLabels,
			KnownRenderedVersion: knownRenderedVersion,
			OsMode:               &m.caps.OsMode,
		},
	}

	// Populate preEnrollment result if hooks ran
	if preResult != nil {
		pre := &v1beta1.PreEnrollmentResult{
			Success: preResult.success,
		}
		if preResult.output != "" {
			pre.Output = &preResult.output
		}
		req.Spec.PreEnrollment = pre
	}

	err := wait.ExponentialBackoffWithContext(ctx, m.backoff, func(ctx context.Context) (bool, error) {
		_, err := m.enrollmentClient.CreateEnrollmentRequest(ctx, req)
		if err != nil {
			m.log.Warnf("failed to create enrollment request: %v", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("creating enrollment request: %w", err)
	}

	return nil
}

// buildEnrollmentLabels creates the final label map for enrollment by:
// 1. Mapping systemInfo fields (built-in and customInfo) to labels based on labelFromSystemInfo config (sanitized)
// 2. Adding default alias=hostname if no "alias" label is configured (sanitized)
// 3. Merging with defaultLabels (validated but not sanitized - invalid labels are skipped)
// 4. Merging with hook labels from pre-enrollment hooks (validated, highest priority)
func (m *LifecycleManager) buildEnrollmentLabels(deviceStatus *v1beta1.DeviceStatus, hookLabels map[string]string) map[string]string {
	// Start with labels from systemInfo mappings
	labels := make(map[string]string, len(m.labelFromSystemInfo)+len(m.defaultLabels)+len(hookLabels)+1)

	// Extract and sanitize systemInfo field mappings (includes both built-in and customInfo fields)
	for labelName, fieldName := range m.labelFromSystemInfo {
		// Validate label key (user-configured in config file)
		if keyErrs := validation.ValidateLabelKey(labelName); len(keyErrs) > 0 {
			m.log.Errorf("Invalid label key %q in label-from-systeminfo: %v - skipping this label. Please fix your agent configuration.", labelName, keyErrs)
			continue
		}

		value := m.extractSystemInfoField(&deviceStatus.SystemInfo, fieldName)
		if value == "" {
			m.log.Warnf("Failed to extract systemInfo field %q for label %q", fieldName, labelName)
			continue
		}

		// Sanitize the value to ensure it's a valid Kubernetes label value
		// (systemInfo values like "CentOS Stream" need sanitization)
		sanitized := validation.SanitizeLabelValue(value)
		if sanitized == "" {
			m.log.Warnf("Failed to sanitize systemInfo field %q (original value: %q) for label %q - value cannot be made valid", fieldName, value, labelName)
			continue
		}

		if sanitized != value {
			m.log.Infof("Sanitized systemInfo field %q for label %q: %q -> %q", fieldName, labelName, value, sanitized)
		}
		labels[labelName] = sanitized
	}

	// Add default alias=hostname if no "alias" label is configured
	if _, hasAlias := m.labelFromSystemInfo["alias"]; !hasAlias {
		hostname := m.extractSystemInfoField(&deviceStatus.SystemInfo, "hostname")
		if isUsefulHostname(hostname) {
			// Sanitize hostname as well (it's auto-generated)
			sanitized := validation.SanitizeLabelValue(hostname)
			if sanitized != "" {
				if sanitized != hostname {
					m.log.Infof("Sanitized hostname for default alias: %q -> %q", hostname, sanitized)
				}
				labels["alias"] = sanitized
			} else {
				m.log.Warnf("Failed to sanitize hostname %q for default alias", hostname)
			}
		}
	}

	// Validate and apply defaultLabels (which take precedence on conflicts)
	// These are user-configured, so we validate but don't sanitize - invalid labels are skipped
	for labelName, labelValue := range m.defaultLabels {
		if err := validation.ValidateLabelValue(labelName, labelValue); len(err) > 0 {
			m.log.Errorf("Invalid default-label %q=%q: %v - skipping this label. Please fix your agent configuration.", labelName, labelValue, err)
			continue
		}
		labels[labelName] = labelValue
	}

	// Merge hook labels (highest priority — later wins on key conflict)
	for labelName, labelValue := range hookLabels {
		if keyErrs := validation.ValidateLabelKey(labelName); len(keyErrs) > 0 {
			m.log.Errorf("Invalid hook label key %q: %v - skipping", labelName, keyErrs)
			continue
		}
		if err := validation.ValidateLabelValue(labelName, labelValue); len(err) > 0 {
			m.log.Errorf("Invalid hook label %q=%q: %v - skipping", labelName, labelValue, err)
			continue
		}
		labels[labelName] = labelValue
	}

	return labels
}

// isUsefulHostname checks if a hostname is helpful for use as a device alias.
// Returns false for empty strings, "(none)", localhost variants, or loopback IPs.
func isUsefulHostname(hostname string) bool {
	if hostname == "" || hostname == "(none)" {
		return false
	}

	h := strings.ToLower(hostname)

	if strings.HasPrefix(h, "localhost") {
		return false
	}

	if h == "127.0.0.1" || h == "::1" {
		return false
	}

	return true
}

// extractSystemInfoField extracts a field from DeviceSystemInfo (built-in or customInfo)
// Supports both built-in fields (e.g., "hostname", "architecture") and customInfo fields
// (e.g., "customInfo.siteId", "customInfo.rackNumber").
func (m *LifecycleManager) extractSystemInfoField(systemInfo *v1beta1.DeviceSystemInfo, fieldName string) string {
	// Check if this is a customInfo field (e.g., "customInfo.siteId")
	const customInfoPrefix = "customInfo."
	if customFieldName, found := strings.CutPrefix(fieldName, customInfoPrefix); found {
		if systemInfo.CustomInfo != nil {
			if value, ok := (*systemInfo.CustomInfo)[customFieldName]; ok {
				return value
			}
		}
		m.log.Debugf("CustomInfo field %q not found or is empty", customFieldName)
		return ""
	}

	// Built-in field: use reflection to access struct fields
	// Convert to UpperCamelCase to match Go struct field naming
	structFieldName := strcase.UpperCamelCase(fieldName)
	v := reflect.ValueOf(*systemInfo)
	field := v.FieldByName(structFieldName)

	// If field exists and is a string type, return its value
	if field.IsValid() && field.Kind() == reflect.String {
		return field.String()
	}

	// If not found in struct fields, try AdditionalProperties
	if systemInfo.AdditionalProperties != nil {
		if value, ok := systemInfo.AdditionalProperties[fieldName]; ok {
			return value
		}
	}

	m.log.Debugf("SystemInfo field %q not found or is empty", fieldName)
	return ""
}
