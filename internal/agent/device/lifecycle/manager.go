package lifecycle

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/identity"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/skip2/go-qrcode"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/cert"
)

const (
	// agent banner file
	BannerFile = "/etc/issue.d/flightctl-banner.issue"
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
	deviceReadWriter     fileio.ReadWriter

	enrollmentClient client.Enrollment
	defaultLabels    map[string]string
	enrollmentCSR    []byte
	statusManager    status.Manager
	systemdClient    *client.Systemd
	identityProvider identity.Provider

	backoff wait.Backoff
	log     *log.PrefixLogger
}

// Manager is responsible for managing the device lifecycle.
func NewManager(
	deviceName string,
	enrollmentUIEndpoint string,
	managementCertPath string,
	managementKeyPath string,
	deviceReadWriter fileio.ReadWriter,
	enrollmentClient client.Enrollment,
	enrollmentCSR []byte,
	defaultLabels map[string]string,
	statusManager status.Manager,
	systemdClient *client.Systemd,
	identityProvider identity.Provider,
	backoff wait.Backoff,
	log *log.PrefixLogger,
) *LifecycleManager {
	return &LifecycleManager{
		log:                  log,
		deviceName:           deviceName,
		enrollmentUIEndpoint: enrollmentUIEndpoint,
		managementCertPath:   managementCertPath,
		managementKeyPath:    managementKeyPath,
		deviceReadWriter:     deviceReadWriter,
		enrollmentClient:     enrollmentClient,
		enrollmentCSR:        enrollmentCSR,
		defaultLabels:        defaultLabels,
		backoff:              backoff,
		statusManager:        statusManager,
		systemdClient:        systemdClient,
		identityProvider:     identityProvider,
	}
}

// Initialize ensures the device is enrolled to the management service.
func (m *LifecycleManager) Initialize(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	if !m.IsInitialized() {
		if err := m.writeEnrollmentBanner(); err != nil {
			return err
		}

		if err := m.enrollmentRequest(ctx, status); err != nil {
			return err
		}

		m.log.Info("Waiting for enrollment to be approved")
		err := wait.ExponentialBackoffWithContext(ctx, m.backoff, func(ctx context.Context) (bool, error) {
			return m.verifyEnrollment(ctx)
		})
		if err != nil {
			return err
		}
	}

	// write the management banner
	return m.writeManagementBanner()
}

func (m *LifecycleManager) Sync(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error {
	// this controller currently does not implement a sync operation
	return nil
}

func (m *LifecycleManager) AfterUpdate(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error {
	var errs []error
	if current.Decommissioning == nil && desired.Decommissioning != nil {
		m.log.Warn("Detected decommissioning request from flightctl service")
		m.log.Warn("Updating Condition to decommissioning started")
		if err := m.updateWithStartedCondition(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to update status with decommission started Condition: %w", err))
			m.log.Warn("Unable to update Condition to decommissioning started")
		}

		// TODO: add support for additional decommissioning target types.
		// these are the steps that will take places between Started and Completed status

		if len(errs) == 0 {
			m.log.Warn("No errors during decommissioning prior to wiping key and cert; updating Condition to decommissioning completed")
			if err := m.updateWithCompletedCondition(ctx); err != nil {
				errs = append(errs, fmt.Errorf("failed to update status with decommission completed Condition: %w", err))
				m.log.Warn("Unable to update Condition to decommissioning completed")
			}
		} else {
			m.log.Warn("Errors encountered during decommissioning; updating Condition to decommission error")
			if err := m.updateWithErrorCondition(ctx, errs); err != nil {
				errs = append(errs, fmt.Errorf("failed to update status with decommission errored Condition: %w", err))
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
	updateErr := m.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.ConditionTypeDeviceDecommissioning,
		Status:  v1alpha1.ConditionStatusTrue,
		Reason:  string(v1alpha1.DecommissionStateStarted),
		Message: "Device started decommissioning",
	})
	if updateErr != nil {
		m.log.Warnf("Failed setting status: %v", updateErr)
		return fmt.Errorf("failed to update decommission started status: %w", updateErr)
	}
	return nil
}

func (m *LifecycleManager) updateWithCompletedCondition(ctx context.Context) error {
	updateErr := m.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.ConditionTypeDeviceDecommissioning,
		Status:  v1alpha1.ConditionStatusTrue,
		Reason:  string(v1alpha1.DecommissionStateComplete),
		Message: "Device completed decommissioning and will wipe its management certificate",
	})
	if updateErr != nil {
		m.log.Warnf("Failed setting status: %v", updateErr)
		return fmt.Errorf("failed to update decommission completed status: %w", updateErr)
	}
	return nil
}

func (m *LifecycleManager) updateWithErrorCondition(ctx context.Context, errs []error) error {
	updateErr := m.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.ConditionTypeDeviceDecommissioning,
		Status:  v1alpha1.ConditionStatusTrue,
		Reason:  string(v1alpha1.DecommissionStateError),
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

	// TODO: incorporate before-reboot hooks
	if err := m.systemdClient.Reboot(ctx); err != nil {
		errs = append(errs, fmt.Errorf("failed to initiate system reboot: %w", err))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
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

	return true, nil
}

func (m *LifecycleManager) writeEnrollmentBanner() error {
	if m.enrollmentUIEndpoint == "" {
		m.log.Warn("Flightctl enrollment UI endpoint is missing, skipping enrollment banner")
		return nil
	}
	url := fmt.Sprintf("%s/enroll/%s", m.enrollmentUIEndpoint, m.deviceName)
	if err := m.writeQRBanner("\nEnroll your device to flightctl by scanning\nthe above QR code or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("failed to write device enrollment banner: %w", err)
	}
	return nil
}

func (m *LifecycleManager) writeManagementBanner() error {
	// write a banner that explains that the device is enrolled
	if m.enrollmentUIEndpoint == "" {
		m.log.Warn("Flightctl enrollment UI endpoint is missing, skipping management banner")
		return nil
	}
	url := fmt.Sprintf("%s/manage/%s", m.enrollmentUIEndpoint, m.deviceName)
	if err := m.writeQRBanner("\nYour device is enrolled to flightctl,\nyou can manage your device scanning the above QR. or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("failed to write device management banner: %w", err)
	}
	return nil
}

func (m *LifecycleManager) writeQRBanner(message, url string) error {
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

	if err := m.sdNotify(); err != nil {
		m.log.Warnf("Failed to notify systemd: %v", err)
	}

	value := os.Getenv("FLIGHTCTL_DISABLE_CONSOLE_BANNER")
	if !(strings.EqualFold(value, "true") || value == "1") {
		fmt.Println(buffer.String())
	}
	return nil
}

func (m *LifecycleManager) enrollmentRequest(ctx context.Context, deviceStatus *v1alpha1.DeviceStatus) error {
	var csrString string
	if tpm.IsTCGCSRFormat(m.enrollmentCSR) {
		// TCG CSR is binary data, must be base64 encoded
		m.log.Debugf("Detected TCG CSR format, base64 encoding before transmission")
		csrString = base64.StdEncoding.EncodeToString(m.enrollmentCSR)
	} else {
		csrString = string(m.enrollmentCSR)
	}

	req := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1alpha1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: &m.deviceName,
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:          csrString,
			DeviceStatus: deviceStatus,
			Labels:       &m.defaultLabels,
		},
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

func (m *LifecycleManager) sdNotify() error {
	socketAddr := &net.UnixAddr{
		Name: os.Getenv("NOTIFY_SOCKET"),
		Net:  "unixgram",
	}

	// NOTIFY_SOCKET not set
	if socketAddr.Name == "" {
		m.log.Warning("NOTIFY_SOCKET not set, skipping systemd notification")
		return nil
	}
	conn, err := net.DialUnix(socketAddr.Net, nil, socketAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to systemd: %w", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("READY=1\n"))
	if err != nil {
		return fmt.Errorf("failed to write to systemd: %w", err)
	}
	return nil
}
