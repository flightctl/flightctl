package device

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/skip2/go-qrcode"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/cert"
	"k8s.io/klog/v2"
)

const (
	// agent banner file
	BannerFile        = "/etc/issue.d/flightctl-banner.issue"
	BootstrapComplete = "Bootstrap complete"
)

type Bootstrap struct {
	deviceName           string
	executer             executer.Executer
	deviceReadWriter     fileio.ReadWriter
	enrollmentClient     client.Enrollment
	enrollmentUIEndpoint string
	specManager          spec.Manager
	statusManager        status.Manager
	hookManager          hook.Manager
	backoff              wait.Backoff

	managementServiceConfig *client.Config
	managementClient        client.Management

	enrollmentCSR []byte
	log           *log.PrefixLogger

	defaultLabels map[string]string
}

func NewBootstrap(
	deviceName string,
	executer executer.Executer,
	deviceReadWriter fileio.ReadWriter,
	enrollmentCSR []byte,
	specManager spec.Manager,
	statusManager status.Manager,
	hookManager hook.Manager,
	enrollmentClient client.Enrollment,
	enrollmentUIEndpoint string,
	managementServiceConfig *client.Config,
	backoff wait.Backoff,
	log *log.PrefixLogger,
	defaultLabels map[string]string,
) *Bootstrap {
	return &Bootstrap{
		deviceName:              deviceName,
		executer:                executer,
		deviceReadWriter:        deviceReadWriter,
		enrollmentCSR:           enrollmentCSR,
		specManager:             specManager,
		statusManager:           statusManager,
		hookManager:             hookManager,
		enrollmentClient:        enrollmentClient,
		enrollmentUIEndpoint:    enrollmentUIEndpoint,
		managementServiceConfig: managementServiceConfig,
		backoff:                 backoff,
		log:                     log,
		defaultLabels:           defaultLabels,
	}
}

func (b *Bootstrap) Initialize(ctx context.Context) error {
	b.log.Infof("Bootstrapping device: %s", b.deviceName)
	versionInfo := version.Get()
	b.log.Infof("System information: version=%s, go-version=%s, platform=%s, git-commit=%s",
		versionInfo.String(),
		versionInfo.GoVersion,
		versionInfo.Platform,
		versionInfo.GitCommit,
	)

	if err := b.ensureSpecFiles(); err != nil {
		return err
	}

	if err := b.ensureEnrollment(ctx); err != nil {
		return err
	}

	if err := b.setManagementClient(); err != nil {
		return err
	}

	if err := b.ensureBootstrap(ctx); err != nil {
		infoMsg := fmt.Sprintf("Bootstrap failed: %v", err)
		_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
			Status: v1alpha1.DeviceSummaryStatusError,
			Info:   util.StrToPtr(infoMsg),
		}))
		if updateErr != nil {
			b.log.Warnf("Failed setting status: %v", updateErr)
		}
		b.log.Error(infoMsg)

		return err
	}

	b.updateStatus(ctx)

	// unset NOTIFY_SOCKET on successful bootstrap to prevent subprocesses from
	// using it.
	// ref: https://bugzilla.redhat.com/show_bug.cgi?id=1781506
	os.Unsetenv("NOTIFY_SOCKET")

	b.log.Info(BootstrapComplete)
	return nil
}

func (b *Bootstrap) updateStatus(ctx context.Context) {
	updateFns := []status.UpdateStatusFn{
		status.SetConfig(v1alpha1.DeviceConfigStatus{
			RenderedVersion: b.specManager.RenderedVersion(spec.Current),
		}),
		status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
			Status: v1alpha1.DeviceSummaryStatusOnline,
			Info:   util.StrToPtr(BootstrapComplete),
		}),
	}

	_, updateErr := b.statusManager.Update(ctx, updateFns...)
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	updatingCondition := v1alpha1.Condition{
		Type: v1alpha1.DeviceUpdating,
	}

	if b.specManager.IsUpgrading() {
		updatingCondition.Status = v1alpha1.ConditionStatusTrue
		// TODO: only set rebooting in case where we are actually rebooting
		updatingCondition.Reason = string(v1alpha1.UpdateStateRebooting)
	} else {
		updatingCondition.Status = v1alpha1.ConditionStatusFalse
		updatingCondition.Reason = string(v1alpha1.UpdateStateUpdated)
	}

	updateErr = b.statusManager.UpdateCondition(ctx, updatingCondition)
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}
}

func (b *Bootstrap) ensureSpecFiles() error {
	if b.isEnrolled() {
		// it is unexpected to have a missing spec files when the device is
		// enrolled. reset the spec files to empty if they are missing to allow
		// us to make progress. on the next sync, the device will get the latest
		// desired spec and continue as expected.
		if err := b.specManager.Ensure(); err != nil {
			return fmt.Errorf("resetting spec files: %w", err)
		}
	} else {
		b.log.Info("Device is not enrolled, initializing spec files")
		if err := b.specManager.Initialize(); err != nil {
			return fmt.Errorf("initializing spec files: %w", err)
		}
	}

	return nil
}

func (b *Bootstrap) ensureBootstrap(ctx context.Context) error {
	if err := b.ensureBootedOS(ctx); err != nil {
		return err
	}

	// TODO: call if device has rebooted, rather than just agent restarted
	// b.hookManager.OnAfterRebooting(ctx)

	return nil
}

func (b *Bootstrap) ensureBootedOS(ctx context.Context) error {
	if !b.specManager.IsOSUpdate() {
		b.log.Info("No OS update in progress")
		// no change in OS image, so nothing else to do here
		return nil
	}

	return b.checkRollback(ctx)
}

func (b *Bootstrap) checkRollback(ctx context.Context) error {
	// check if the bootedOS image is expected
	bootedOS, reconciled, err := b.specManager.CheckOsReconciliation(ctx)
	if err != nil {
		return fmt.Errorf("checking if OS image is reconciled: %w", err)
	}

	if reconciled {
		b.log.Infof("Booted into desired OS image: %s", bootedOS)
		return nil
	}

	desiredOS := b.specManager.OSVersion(spec.Desired)
	// We rebooted without applying the new OS image - something potentially went wrong
	b.log.Warnf("Booted OS image (%s) does not match the desired OS image (%s)", bootedOS, desiredOS)

	_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
		Status: v1alpha1.DeviceSummaryStatusDegraded,
		Info:   util.StrToPtr(fmt.Sprintf("Booted image %s, expected %s", bootedOS, desiredOS)),
	}))
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	rollback, err := b.specManager.IsRollingBack(ctx)
	if err != nil {
		return fmt.Errorf("checking if rollback is in progress: %w", err)
	}

	if !rollback {
		// this is possible if device was rebooted before new image was applied
		b.log.Warn("No rollback in progress, continuing bootstrap to apply rollback spec")
		return nil
	}

	b.log.Warn("Starting spec rollback")
	if err := b.specManager.Rollback(); err != nil {
		return fmt.Errorf("failed spec rollback: %w", err)
	}
	b.log.Info("Spec rollback complete, resuming bootstrap")

	updateErr = b.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.DeviceUpdating,
		Status:  v1alpha1.ConditionStatusTrue,
		Reason:  string(v1alpha1.UpdateStateRollingBack),
		Message: fmt.Sprintf("The device is rolling back to template version: %s", b.specManager.RenderedVersion(spec.Desired)),
	})
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	return nil
}

// ensureEnrollment ensures the device is enrolled to the management service. the phase should ONLY rely on the enrollment client and the agent config.
func (b *Bootstrap) ensureEnrollment(ctx context.Context) error {
	if !b.isEnrolled() {
		if err := b.writeEnrollmentBanner(); err != nil {
			return err
		}

		if err := b.enrollmentRequest(ctx); err != nil {
			return err
		}

		b.log.Info("Waiting for enrollment to be approved")
		err := wait.ExponentialBackoffWithContext(ctx, b.backoff, func() (bool, error) {
			return b.verifyEnrollment(ctx)
		})
		if err != nil {
			return err
		}
	}

	// write the management banner
	return b.writeManagementBanner()
}

func (b *Bootstrap) isEnrolled() bool {
	exists, err := b.deviceReadWriter.PathExists(b.managementServiceConfig.GetClientCertificatePath())
	if err != nil {
		b.log.Warnf("Error checking if device is enrolled: %v", err)
		return false
	}
	return exists
}

func (b *Bootstrap) verifyEnrollment(ctx context.Context) (bool, error) {
	enrollmentRequest, err := b.enrollmentClient.GetEnrollmentRequest(ctx, b.deviceName)
	if err != nil {
		b.log.Errorf("Error checking enrollment status: %v", err)
		return false, nil
	}

	// TODO: update schema to require condition in status, then remove this check
	if enrollmentRequest.Status == nil || enrollmentRequest.Status.Conditions == nil {
		b.log.Fatal("Enrollment request status or conditions field are nil")
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
		b.log.Info("Enrollment request not yet approved")
		return false, nil
	}
	if len(*enrollmentRequest.Status.Certificate) == 0 {
		b.log.Infof("Enrollment request approved, but certificate not yet issued")
		return false, nil
	}
	b.log.Infof("Enrollment approved and certificate issued")

	if _, err = cert.ParseCertsPEM([]byte(*enrollmentRequest.Status.Certificate)); err != nil {
		return false, fmt.Errorf("parsing signed certificate: %v", err)
	}

	b.log.Infof("Writing signed certificate to %s", b.managementServiceConfig.GetClientCertificatePath())
	if err := b.deviceReadWriter.WriteFile(b.managementServiceConfig.GetClientCertificatePath(), []byte(*enrollmentRequest.Status.Certificate), os.FileMode(0600)); err != nil {
		return false, fmt.Errorf("writing signed certificate: %v", err)
	}

	return true, nil
}

// we want to look at the desired spec

func (b *Bootstrap) writeEnrollmentBanner() error {
	if b.enrollmentUIEndpoint == "" {
		b.log.Warn("Flightctl enrollment UI endpoint is missing, skipping enrollment banner")
		return nil
	}
	url := fmt.Sprintf("%s/enroll/%s", b.enrollmentUIEndpoint, b.deviceName)
	if err := b.writeQRBanner("\nEnroll your device to flightctl by scanning\nthe above QR code or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("failed to write device enrollment banner: %w", err)
	}
	return nil
}

func (b *Bootstrap) writeManagementBanner() error {
	// write a banner that explains that the device is enrolled
	if b.enrollmentUIEndpoint == "" {
		b.log.Warn("Flightctl enrollment UI endpoint is missing, skipping management banner")
		return nil
	}
	url := fmt.Sprintf("%s/manage/%s", b.enrollmentUIEndpoint, b.deviceName)
	if err := b.writeQRBanner("\nYour device is enrolled to flightctl,\nyou can manage your device scanning the above QR. or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("failed to write device management banner: %w", err)
	}
	return nil
}

func (b *Bootstrap) writeQRBanner(message, url string) error {
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
	if err := b.deviceReadWriter.WriteFile(BannerFile, buffer.Bytes(), os.FileMode(0666)); err != nil {
		return fmt.Errorf("failed to write banner to disk: %w", err)
	}

	if err := SdNotify("READY=1"); err != nil {
		b.log.Warnf("Failed to notify systemd: %v", err)
	}

	// additionally print the banner into the output console
	fmt.Println(buffer.String())
	return nil
}

func (b *Bootstrap) enrollmentRequest(ctx context.Context) error {
	err := b.statusManager.Collect(ctx)
	if err != nil {
		b.log.Warnf("Collecting device status: %v", err)
	}

	req := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1alpha1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: &b.deviceName,
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:          string(b.enrollmentCSR),
			DeviceStatus: b.statusManager.Get(ctx),
			Labels:       &b.defaultLabels,
		},
	}

	err = wait.ExponentialBackoffWithContext(ctx, b.backoff, func() (bool, error) {
		_, err := b.enrollmentClient.CreateEnrollmentRequest(ctx, req)
		if err != nil {
			b.log.Warnf("failed to create enrollment request: %v", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("creating enrollment request: %w", err)
	}

	return nil
}

func (b *Bootstrap) setManagementClient() error {
	managementCertExists, err := b.deviceReadWriter.PathExists(b.managementServiceConfig.GetClientCertificatePath())
	if err != nil {
		return fmt.Errorf("generated cert: %q: %w", b.managementServiceConfig.GetClientCertificatePath(), err)
	}

	if !managementCertExists {
		// TODO: we must re-enroll the device in this case
		return fmt.Errorf("management client certificate does not exist")
	}

	// create the management client
	managementHTTPClient, err := client.NewFromConfig(b.managementServiceConfig)
	if err != nil {
		return fmt.Errorf("create management client: %w", err)
	}
	b.managementClient = client.NewManagement(managementHTTPClient)

	// initialize the management client for spec and status managers
	b.statusManager.SetClient(b.managementClient)
	b.specManager.SetClient(b.managementClient)
	b.log.Info("Management client set")
	return nil
}

func SdNotify(state string) error {
	socketAddr := &net.UnixAddr{
		Name: os.Getenv("NOTIFY_SOCKET"),
		Net:  "unixgram",
	}

	// NOTIFY_SOCKET not set
	if socketAddr.Name == "" {
		klog.Warningf("NOTIFY_SOCKET not set, skipping systemd notification")
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
