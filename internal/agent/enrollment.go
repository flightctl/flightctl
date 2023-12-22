package agent

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/skip2/go-qrcode"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/cert"
	"k8s.io/klog/v2"
)

func (a *DeviceAgent) requestAndWaitForEnrollment(ctx context.Context) error {

	if err := a.writeEnrollmentBanner(); err != nil {
		return fmt.Errorf("requestAndWaitForEnrollment: %w", err)
	}
	a.sendEnrollmentRequest(ctx)

	klog.Infof("%swaiting for enrollment to be approved", a.logPrefix)
	backoff := wait.Backoff{
		Cap:      3 * time.Minute,
		Duration: 10 * time.Second,
		Factor:   1.5,
		Steps:    24,
	}
	return wait.ExponentialBackoff(backoff, func() (bool, error) {
		return a.checkEnrollment(ctx)
	})
}

func (a *DeviceAgent) writeEnrollmentBanner() error {
	if a.enrollmentUiUrl == "" {
		klog.Warningf("%sflightctl enrollment UI URL is missing, skipping enrollment banner", a.logPrefix)
		return nil
	}
	url := a.enrollmentUiUrl + "/enroll/" + a.fingerprint
	if err := a.writeQRBanner("\nEnroll your device to flightctl by scanning\nthe above QR code or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("writeEnrollmentBanner: %w", err)
	}
	return nil
}

func (a *DeviceAgent) writeManagementBanner() error {
	// write a banner that explains that the device is enrolled
	if a.enrollmentUiUrl == "" {
		klog.Warningf("%sflightctl enrollment UI URL is missing, skipping enrollment banner", a.logPrefix)
		return nil
	}
	url := a.enrollmentUiUrl + "/manage/" + a.fingerprint
	if err := a.writeQRBanner("\nYour device is enrolled to flightctl,\nyou can manage your device scanning the above QR. or following this URL:\n%s\n\n", url); err != nil {
		return fmt.Errorf("writeManagementBanner: %w", err)
	}
	return nil
}

func (a *DeviceAgent) writeQRBanner(message, url string) error {
	qrCode, err := qrcode.New(url, qrcode.High)
	if err != nil {
		return fmt.Errorf("writeQRBanner: %w", err)
	}

	// Convert the QR code to a string.
	qrString := qrCode.ToSmallString(false)

	// write a banner that explains that the device is enrolled
	buffer := bytes.NewBufferString("\n")
	buffer.WriteString(qrString)

	// write the QR code to the buffer
	fmt.Fprintf(buffer, message, url)

	// duplicate file to /etc/issue.d/flightctl-banner.issue
	if err := os.WriteFile("/etc/issue.d/flightctl-banner.issue", buffer.Bytes(), os.FileMode(0666)); err != nil {
		return fmt.Errorf("writeQRBanner: %w", err)
	}

	// additionally print the banner into the output console
	klog.Info(buffer.String())
	return nil
}

func (a *DeviceAgent) sendEnrollmentRequest(ctx context.Context) error {
	if err := a.ensureEnrollmentClient(); err != nil {
		return err
	}

	csr, err := fccrypto.MakeCSR((*a.key).(crypto.Signer), a.fingerprint)
	if err != nil {
		return err
	}

	req := &api.EnrollmentRequest{
		ApiVersion: "v1alpha1",
		Kind:       "EnrollmentRequest",
		Metadata:   api.ObjectMeta{Name: &a.fingerprint},
		Spec: api.EnrollmentRequestSpec{
			Csr:          string(csr),
			DeviceStatus: a.device.Status,
		},
	}
	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(req)
	if err != nil {
		return err
	}

	t0 := time.Now()
	_, err = a.enrollmentClient.CreateEnrollmentRequestWithBodyWithResponse(ctx, "application/json", &buf)
	if a.rpcMetricsCallbackFunc != nil {
		a.rpcMetricsCallbackFunc("create_enrollmentrequest", time.Since(t0).Seconds(), err)
	}
	if err != nil {
		return err
	}
	return nil
}

func (a *DeviceAgent) ensureEnrollmentClient() error {
	if a.enrollmentClient == nil {
		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs:      a.caCertPool,
					Certificates: []tls.Certificate{*a.enrollmentClientCert},
				},
			},
		}
		c, err := client.NewClientWithResponses(a.enrollmentServerUrl, client.WithHTTPClient(httpClient))
		if err != nil {
			return err
		}
		a.enrollmentClient = c
	}
	return nil
}

func (a *DeviceAgent) checkEnrollment(ctx context.Context) (bool, error) {
	if err := a.ensureEnrollmentClient(); err != nil {
		return false, err
	}

	t0 := time.Now()
	response, err := a.enrollmentClient.ReadEnrollmentRequestStatusWithResponse(ctx, a.fingerprint)
	if a.rpcMetricsCallbackFunc != nil {
		a.rpcMetricsCallbackFunc("get_enrollmentrequest_status", time.Since(t0).Seconds(), err)
	}
	if err != nil {
		klog.Infof("%serror checking enrollment status: %v", a.logPrefix, err)
		return false, nil
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		klog.Infof("%serror checking enrollment status: %v", a.logPrefix, response.Status())
		return false, nil
	}
	enrollmentRequest := response.JSON200

	// TODO: update schema to require condition in status, then remove this check
	if enrollmentRequest.Status == nil || enrollmentRequest.Status.Conditions == nil {
		klog.Fatalf("%senrollment request status or conditions field are nil", a.logPrefix)
	}

	approved := false
	for _, c := range *enrollmentRequest.Status.Conditions {
		if c.Type == "Denied" {
			return false, fmt.Errorf("%senrollment request is denied, reason: %v, message: %v", a.logPrefix, c.Reason, c.Message)
		}
		if c.Type == "Failed" {
			return false, fmt.Errorf("%senrollment request failed, reason: %v, message: %v", a.logPrefix, c.Reason, c.Message)
		}
		if c.Type == "Approved" {
			approved = true
		}
	}
	if !approved {
		klog.Infof("%senrollment request not yet approved", a.logPrefix)
		return false, nil
	}
	if len(*enrollmentRequest.Status.Certificate) == 0 {
		klog.Infof("%senrollment request approved, but certificate not yet issued", a.logPrefix)
		return false, nil
	}
	klog.Infof("%senrollment approved and certificate issued", a.logPrefix)

	if _, err = cert.ParseCertsPEM([]byte(*enrollmentRequest.Status.Certificate)); err != nil {
		return false, fmt.Errorf("%sparsing signed certificate: %v", a.logPrefix, err)
	}

	if err := os.WriteFile(filepath.Join(a.certDir, clientCertFile), []byte(*enrollmentRequest.Status.Certificate), os.FileMode(0600)); err != nil {
		return false, fmt.Errorf("%swriting signed certificate: %v", a.logPrefix, err)
	}

	return true, nil
}
