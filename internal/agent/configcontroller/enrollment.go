package configcontroller

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/client"
	"github.com/skip2/go-qrcode"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/cert"
	"k8s.io/klog/v2"
)

func (c *ConfigController) ensureDeviceEnrollment(ctx context.Context, device *v1alpha1.Device) error {
	if c.isDeviceEnrolled() {
		return nil
	}
	if err := c.writeDeviceEnrollmentBanner(ctx, device); err != nil {
		return fmt.Errorf("failed to write enrollment banner: %w", err)
	}

	if err := c.deviceEnrollmentRequest(ctx, device); err != nil {
		return fmt.Errorf("failed to send enrollment request: %w", err)
	}

	klog.Infof("%swaiting for enrollment to be approved", c.logPrefix)
	err := wait.ExponentialBackoff(c.enrollmentVerifyBackoff, func() (bool, error) {
		return c.verifyDeviceEnrollment(ctx, device)
	})
	if err != nil {
		return fmt.Errorf("failed to verify device enrollment: %w", err)
	}

	// create the management client
	managementHTTPClient, err := client.NewWithResponses(c.managementEndpoint, c.caFilePath, c.managementCertFilePath, c.agentKeyFilePath)
	if err != nil {
		return fmt.Errorf("failed to create management client: %w", err)
	}

	c.managementClient = client.NewManagement(managementHTTPClient)

	return nil
}

func (c *ConfigController) isDeviceEnrolled() bool {
	_, err := os.Stat(c.managementCertFilePath)
	return !os.IsNotExist(err)
}

func (c *ConfigController) verifyDeviceEnrollment(ctx context.Context, device *v1alpha1.Device) (bool, error) {
	enrollmentRequest, err := c.enrollmentClient.GetEnrollmentRequest(ctx, *device.Metadata.Name)
	if err != nil {
		klog.Infof("%serror checking enrollment status: %v", c.logPrefix, err)
		return false, nil
	}

	// TODO: update schema to require condition in status, then remove this check
	if enrollmentRequest.Status == nil || enrollmentRequest.Status.Conditions == nil {
		klog.Fatalf("%senrollment request status or conditions field are nil", c.logPrefix)
	}

	approved := false
	for _, cond := range *enrollmentRequest.Status.Conditions {
		if cond.Type == "Denied" {
			return false, fmt.Errorf("%senrollment request is denied, reason: %v, message: %v", c.logPrefix, cond.Reason, cond.Message)
		}
		if cond.Type == "Failed" {
			return false, fmt.Errorf("%senrollment request failed, reason: %v, message: %v", c.logPrefix, cond.Reason, cond.Message)
		}
		if cond.Type == "Approved" {
			approved = true
		}
	}
	if !approved {
		klog.Infof("%senrollment request not yet approved", c.logPrefix)
		return false, nil
	}
	if len(*enrollmentRequest.Status.Certificate) == 0 {
		klog.Infof("%senrollment request approved, but certificate not yet issued", c.logPrefix)
		return false, nil
	}
	klog.Infof("%senrollment approved and certificate issued", c.logPrefix)

	if _, err = cert.ParseCertsPEM([]byte(*enrollmentRequest.Status.Certificate)); err != nil {
		return false, fmt.Errorf("%sparsing signed certificate: %v", c.logPrefix, err)
	}

	if err := os.WriteFile(filepath.Join(c.managementCertFilePath), []byte(*enrollmentRequest.Status.Certificate), os.FileMode(0600)); err != nil {
		return false, fmt.Errorf("%swriting signed certificate: %v", c.logPrefix, err)
	}

	return true, nil
}

func (c *ConfigController) writeDeviceEnrollmentBanner(_ context.Context, device *v1alpha1.Device) error {
	url := fmt.Sprintf("%s/enroll/%s", c.enrollmentEndpoint, *device.Metadata.Name)
	if err := c.writeQRBanner("\nEnroll your device to flightctl by scanning\nthe above QR code or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("failed to write enrollment banner: %w", err)
	}
	return nil
}

func (c *ConfigController) writeQRBanner(message, url string) error {
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
	if err := c.deviceWriter.WriteFile("/etc/issue.d/flightctl-banner.issue", buffer.Bytes(), os.FileMode(0666)); err != nil {
		return fmt.Errorf("failed to write banner to disk: %w", err)
	}

	// additionally print the banner into the output console
	klog.Info(buffer.String())
	return nil
}

func (c *ConfigController) deviceEnrollmentRequest(ctx context.Context, device *v1alpha1.Device) error {
	name := device.Metadata.Name
	req := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1alpha1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: name,
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:          string(c.enrollmentCSR),
			DeviceStatus: device.Status,
		},
	}

	_, err := c.enrollmentClient.CreateEnrollmentRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create enrollment request: %w", err)
	}
	return nil
}
