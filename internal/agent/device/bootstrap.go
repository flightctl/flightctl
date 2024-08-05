package device

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/skip2/go-qrcode"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/cert"
	"k8s.io/klog/v2"
)

var (
	ErrEnrollmentRequestFailed = fmt.Errorf("enrollment request failed")
	ErrEnrollmentRequestDenied = fmt.Errorf("enrollment request denied")
)

type Bootstrap struct {
	deviceName           string
	executer             executer.Executer
	deviceReadWriter     fileio.ReadWriter
	enrollmentClient     client.Enrollment
	enrollmentUIEndpoint string
	specManager          spec.ManagerNoClient
	statusManager        status.Manager
	bootcClient          *container.BootcCmd
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
	specManager spec.ManagerNoClient,
	statusManager status.Manager,
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
		enrollmentClient:        enrollmentClient,
		enrollmentUIEndpoint:    enrollmentUIEndpoint,
		managementServiceConfig: managementServiceConfig,
		bootcClient:             container.NewBootcCmd(executer),
		backoff:                 backoff,
		log:                     log,
		defaultLabels:           defaultLabels,
	}
}

func (b *Bootstrap) Initialize(ctx context.Context) error {
	b.log.Infof("Bootstrapping device: %s", b.deviceName)

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

	_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
		Status: v1alpha1.DeviceSummaryStatusOnline,
		Info:   util.StrToPtr("Bootstrap complete"),
	}))
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	b.log.Info("Bootstrap complete")
	return nil
}

func (b *Bootstrap) ensureSpecFiles() error {
	currentExists, err := b.specManager.Exists(spec.Current)
	if err != nil {
		return err
	}
	// If the current spec file does not exist and the device is not enrolled, we are in the initial bootstrap.
	if !currentExists {
		if b.isEnrolled() {
			// If the device is enrolled, we are in a recovery scenario, resetting current spec.
			b.log.Info("Device is enrolled, but current spec file does not exist. Recovering...")
			if err := b.specManager.Write(spec.Current, spec.NewRendered()); err != nil {
				return fmt.Errorf("writing current rendered spec: %w", err)
			}
		} else {
			if err := b.specManager.Initialize(); err != nil {
				return fmt.Errorf("initializing spec files: %w", err)
			}
		}
	}
	return nil
}

func (b *Bootstrap) ensureBootstrap(ctx context.Context) error {
	desiredSpec, err := b.specManager.Read(spec.Desired)
	if err != nil {
		return err
	}

	updating, err := b.specManager.IsOSUpdateInProgress()
	if err != nil {
		return err
	}
	if !updating {
		// We didn't change the OS image, so nothing else to do here
		return nil
	}

	bootcHost, err := b.bootcClient.Status(ctx)
	if err != nil {
		return fmt.Errorf("getting current bootc status: %w", err)
	}

	// note: empty desired os will return here
	imageReconciled, err := b.specManager.IsOsImageReconciled(bootcHost)
	if err != nil {
		return fmt.Errorf("checking if OS image is reconciled: %w", err)
	}

	if !imageReconciled {
		// We rebooted without applying the new OS image - something went wrong
		b.log.Warn("Started bootstrap with OS image not equal to desired image")
		_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
			Status: v1alpha1.DeviceSummaryStatusDegraded,
			Info:   util.StrToPtr(fmt.Sprintf("Booted image %s, expected %s", bootcHost.GetBootedImage(), desiredSpec.Os.Image)),
		}))
		if updateErr != nil {
			b.log.Warnf("Failed setting status: %v", updateErr)
		}
		return nil
	}

	// image is reconciled upgrade was a success update the current spec
	if err := b.specManager.Write(spec.Current, desiredSpec); err != nil {
		return fmt.Errorf("writing current rendered spec: %w", err)
	}

	updateFns := []status.UpdateStatusFn{
		status.SetOSImage(v1alpha1.DeviceOSStatus{
			Image: desiredSpec.Os.Image,
		}),
		status.SetConfig(v1alpha1.DeviceConfigStatus{
			RenderedVersion: desiredSpec.RenderedVersion,
		}),
	}

	_, updateErr := b.statusManager.Update(ctx, updateFns...)
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}
	return nil
}

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

// TODO: make more robust
func (b *Bootstrap) isEnrolled() bool {
	_, err := b.deviceReadWriter.ReadFile(b.managementServiceConfig.GetClientCertificatePath())
	return !os.IsNotExist(err)
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
			return false, fmt.Errorf("%w: reason: %v, message: %v", ErrEnrollmentRequestDenied, cond.Reason, cond.Message)
		}
		if cond.Type == "Failed" {
			return false, fmt.Errorf("%w: reason: %v, message: %v", ErrEnrollmentRequestFailed, cond.Reason, cond.Message)
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
	if err := b.deviceReadWriter.WriteFile("/etc/issue.d/flightctl-banner.issue", buffer.Bytes(), os.FileMode(0666)); err != nil {
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

	_, err = b.enrollmentClient.CreateEnrollmentRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create enrollment request: %w", err)
	}
	return nil
}

func (b *Bootstrap) setManagementClient() error {
	// management client already set up (for testing)
	if b.managementServiceConfig != nil {
		return nil
	}
	// create the management client
	managementHTTPClient, err := client.NewFromConfig(b.managementServiceConfig)
	if err != nil {
		return fmt.Errorf("create management client: %w", err)
	}
	b.managementClient = client.NewManagement(managementHTTPClient)
	b.statusManager.SetClient(b.managementClient)
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
