package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/sirupsen/logrus"
	"github.com/skip2/go-qrcode"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/cert"
	"k8s.io/klog/v2"
)

type Bootstrap struct {
	deviceName           string
	executer             executer.Executer
	deviceWriter         *fileio.Writer
	deviceReader         *fileio.Reader
	enrollmentClient     *client.Enrollment
	enrollmentUIEndpoint string
	statusManager        status.Manager
	backoff              wait.Backoff

	currentRenderedFile string
	desiredRenderedFile string

	managementClient            *client.Management
	managementEndpoint          string
	managementGeneratedCertFile string
	caFile                      string
	keyFile                     string

	enrollmentCSR []byte
	log           *logrus.Logger
	// The log prefix used for testing
	logPrefix string
}

func NewBootstrap(
	deviceName string,
	executer executer.Executer,
	deviceWriter *fileio.Writer,
	deviceReader *fileio.Reader,
	enrollmentCSR []byte,
	statusManager status.Manager,
	enrollmentClient *client.Enrollment,
	managementEndpoint string,
	enrollmentUIEndpoint string,
	caFile string,
	keyFile string,
	managementGeneratedCertFile string,
	backoff wait.Backoff,
	currentRenderedFile string,
	desiredRenderedFile string,
	log *logrus.Logger,
	logPrefix string,
) *Bootstrap {
	return &Bootstrap{
		deviceName:                  deviceName,
		executer:                    executer,
		deviceWriter:                deviceWriter,
		deviceReader:                deviceReader,
		enrollmentCSR:               enrollmentCSR,
		statusManager:               statusManager,
		enrollmentClient:            enrollmentClient,
		managementEndpoint:          managementEndpoint,
		enrollmentUIEndpoint:        enrollmentUIEndpoint,
		caFile:                      caFile,
		keyFile:                     keyFile,
		managementGeneratedCertFile: managementGeneratedCertFile,
		backoff:                     backoff,
		currentRenderedFile:         currentRenderedFile,
		desiredRenderedFile:         desiredRenderedFile,
		log:                         log,
		logPrefix:                   logPrefix,
	}
}

func (b *Bootstrap) Initialize(ctx context.Context) error {
	b.log.Infof("%sbootstrapping device", b.logPrefix)
	if err := b.ensureEnrollment(ctx); err != nil {
		return err
	}

	if err := b.setManagementClient(); err != nil {
		return err
	}

	if err := b.ensureCurrentRenderedSpecUpToDate(ctx); err != nil {
		return err
	}

	if err := b.ensureRenderedSpec(ctx); err != nil {
		if updateErr := b.statusManager.UpdateConditionError(ctx, "BootstrapFailed", err); updateErr != nil {
			b.log.Errorf("Failed to update condition: %v", updateErr)
		}
		return err
	}

	// TODO: allow multiple conditions to be set with retry
	if err := b.statusManager.UpdateCondition(ctx, v1alpha1.DeviceAvailable, v1alpha1.ConditionStatusTrue, "AsExpected", "All is good"); err != nil {
		b.log.Errorf("Failed to update condition: %v", err)
	}
	if err := b.statusManager.UpdateCondition(ctx, v1alpha1.DeviceProgressing, v1alpha1.ConditionStatusFalse, "AsExpected", "All is good"); err != nil {
		b.log.Errorf("Failed to update condition: %v", err)
	}

	b.log.Infof("%sbootstrap complete", b.logPrefix)
	return nil
}

func (b *Bootstrap) ensureCurrentRenderedSpecUpToDate(ctx context.Context) error {
	currentSpec, err := spec.ReadRenderedSpecFromFile(b.deviceReader, b.currentRenderedFile)
	if err != nil {
		// During the initial bootstrap it is expected that the spec does not exist yet.  This assumption is validated later.
		if errors.Is(err, spec.ErrMissingRenderedSpec) {
			return nil
		}
		return fmt.Errorf("getting current rendered spec: %w", err)
	}

	desiredSpec, err := spec.ReadRenderedSpecFromFile(b.deviceReader, b.desiredRenderedFile)
	if err != nil {
		// During the initial bootstrap it is expected that the spec does not exist yet.  This assumption is validated later.
		if errors.Is(err, spec.ErrMissingRenderedSpec) {
			return nil
		}
		return fmt.Errorf("getting desired rendered spec: %w", err)
	}

	if !isOsImageInTransition(&currentSpec, &desiredSpec) {
		// We didn't change the OS image, so nothing to do here
		return nil
	}

	bootcCmd := container.NewBootcCmd(b.executer)
	bootcHost, err := bootcCmd.Status(ctx)
	if err != nil {
		return fmt.Errorf("getting current bootc status: %w", err)
	}

	if container.IsOsImageReconciled(bootcHost, &desiredSpec) {
		err = spec.WriteRenderedSpecToFile(b.deviceWriter, &desiredSpec, b.currentRenderedFile)
		if err != nil {
			return fmt.Errorf("writing rendered spec to file: %w", err)
		}
	} else {
		// We rebooted without applying the new OS image - something went wrong
		b.log.Warnf("%sstarted bootstrap with OS image not equal to desired image", b.logPrefix)
		condErr := fmt.Errorf("booted image %s, expected %s", container.GetImage(bootcHost), desiredSpec.Os.Image)
		err = b.statusManager.UpdateConditionError(ctx, BootedWithUnexpectedImage, condErr)
		if err != nil {
			b.log.Warnf("%sfailed setting error condition: %v", b.logPrefix, err)
		}
		return nil
	}

	return nil
}

func isOsImageInTransition(current *v1alpha1.RenderedDeviceSpec, desired *v1alpha1.RenderedDeviceSpec) bool {
	currentImage := ""
	if current.Os != nil {
		currentImage = current.Os.Image
	}
	desiredImage := ""
	if desired.Os != nil {
		desiredImage = desired.Os.Image
	}
	return currentImage != desiredImage
}

func (b *Bootstrap) ensureRenderedSpec(ctx context.Context) error {
	if err := b.deviceReader.CheckPathExists(b.managementGeneratedCertFile); err != nil {
		return fmt.Errorf("generated cert: %q: %w", b.managementGeneratedCertFile, err)
	}

	// create the management client
	managementHTTPClient, err := client.NewWithResponses(b.managementEndpoint,
		b.deviceReader.PathFor(b.caFile),
		b.deviceReader.PathFor(b.managementGeneratedCertFile),
		b.deviceReader.PathFor(b.keyFile))
	if err != nil {
		return fmt.Errorf("create management client: %w", err)
	}
	managementClient := client.NewManagement(managementHTTPClient)

	_, err = spec.EnsureDesiredRenderedSpec(ctx, b.log, b.logPrefix, b.deviceWriter, b.deviceReader, managementClient, b.deviceName, b.desiredRenderedFile, b.backoff)
	if err != nil {
		return fmt.Errorf("ensure desired rendered spec: %w", err)
	}

	_, err = spec.EnsureCurrentRenderedSpec(ctx, b.log, b.logPrefix, b.deviceWriter, b.deviceReader, b.currentRenderedFile)
	if err != nil {
		return fmt.Errorf("ensure current rendered spec: %w", err)
	}

	return nil
}

func (b *Bootstrap) ensureEnrollment(ctx context.Context) error {
	if !b.isBootstrapComplete() {
		if err := b.writeEnrollmentBanner(); err != nil {
			return err
		}

		if err := b.enrollmentRequest(ctx); err != nil {
			return err
		}

		b.log.Infof("%swaiting for enrollment to be approved", b.logPrefix)
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
func (b *Bootstrap) isBootstrapComplete() bool {
	_, err := b.deviceReader.ReadFile(b.managementGeneratedCertFile)
	return !os.IsNotExist(err)
}

func (b *Bootstrap) verifyEnrollment(ctx context.Context) (bool, error) {
	enrollmentRequest, err := b.enrollmentClient.GetEnrollmentRequest(ctx, b.deviceName)
	if err != nil {
		b.log.Infof("%serror checking enrollment status: %v", b.logPrefix, err)
		return false, nil
	}

	// TODO: update schema to require condition in status, then remove this check
	if enrollmentRequest.Status == nil || enrollmentRequest.Status.Conditions == nil {
		b.log.Fatalf("%senrollment request status or conditions field are nil", b.logPrefix)
	}

	approved := false
	for _, cond := range *enrollmentRequest.Status.Conditions {
		if cond.Type == "Denied" {
			return false, fmt.Errorf("enrollment request is denied, reason: %v, message: %v", cond.Reason, cond.Message)
		}
		if cond.Type == "Failed" {
			return false, fmt.Errorf("enrollment request failed, reason: %v, message: %v", cond.Reason, cond.Message)
		}
		if cond.Type == "Approved" {
			approved = true
		}
	}
	if !approved {
		b.log.Infof("%senrollment request not yet approved", b.logPrefix)
		return false, nil
	}
	if len(*enrollmentRequest.Status.Certificate) == 0 {
		b.log.Infof("%senrollment request approved, but certificate not yet issued", b.logPrefix)
		return false, nil
	}
	b.log.Infof("%senrollment approved and certificate issued", b.logPrefix)

	if _, err = cert.ParseCertsPEM([]byte(*enrollmentRequest.Status.Certificate)); err != nil {
		return false, fmt.Errorf("parsing signed certificate: %v", err)
	}

	b.log.Infof("%swriting signed certificate to %s", b.logPrefix, b.managementGeneratedCertFile)
	if err := b.deviceWriter.WriteFile(b.managementGeneratedCertFile, []byte(*enrollmentRequest.Status.Certificate), os.FileMode(0600)); err != nil {
		return false, fmt.Errorf("writing signed certificate: %v", err)
	}

	return true, nil
}

func (b *Bootstrap) writeEnrollmentBanner() error {
	if b.enrollmentUIEndpoint == "" {
		b.log.Warningf("%sflightctl enrollment UI endpoint is missing, skipping enrollment banner", b.logPrefix)
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
		b.log.Warningf("%sflightctl enrollment UI endpoint is missing, skipping management banner", b.logPrefix)
		return nil
	}
	url := fmt.Sprintf("%s/manage/%s", b.enrollmentUIEndpoint, b.deviceName)
	if err := b.writeQRBanner("\nYour device is enrolled to flightctl,\nyou can manage your device scanning the above QR. or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("%sfailed to write device management banner: %w", b.logPrefix, err)
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
	if err := b.deviceWriter.WriteFile("/etc/issue.d/flightctl-banner.issue", buffer.Bytes(), os.FileMode(0666)); err != nil {
		return fmt.Errorf("failed to write banner to disk: %w", err)
	}

	if err := SdNotify("READY=1"); err != nil {
		b.log.Warningf("failed to notify systemd: %v", err)
	}

	// additionally print the banner into the output console
	fmt.Println(buffer.String())
	return nil
}

func (b *Bootstrap) enrollmentRequest(ctx context.Context) error {
	status, err := b.statusManager.Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to get device status: %w", err)
	}

	req := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1alpha1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: &b.deviceName,
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:          string(b.enrollmentCSR),
			DeviceStatus: status,
		},
	}

	_, err = b.enrollmentClient.CreateEnrollmentRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create enrollment request: %w", err)
	}
	return nil
}

func (b *Bootstrap) setManagementClient() error {
	if err := b.deviceReader.CheckPathExists(b.managementGeneratedCertFile); err != nil {
		return fmt.Errorf("generated cert: %q: %w", b.managementGeneratedCertFile, err)
	}

	// create the management client
	managementHTTPClient, err := client.NewWithResponses(b.managementEndpoint,
		b.deviceReader.PathFor(b.caFile),
		b.deviceReader.PathFor(b.managementGeneratedCertFile),
		b.deviceReader.PathFor(b.keyFile))
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
