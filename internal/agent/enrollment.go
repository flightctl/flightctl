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
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/cert"
	"k8s.io/klog/v2"
)

func (a *DeviceAgent) requestAndWaitForEnrollment(ctx context.Context) error {
	a.sendEnrollmentRequest(ctx)

	klog.Infof("%swaiting for enrollment to be approved", a.name)
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
		Metadata:   api.ObjectMeta{Name: a.fingerprint},
		Spec: &api.EnrollmentRequestSpec{
			Csr:          string(csr),
			DeviceStatus: a.device.Status,
		},
	}
	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(req)
	if err != nil {
		return err
	}

	_, err = a.enrollmentClient.CreateEnrollmentRequestWithBodyWithResponse(ctx, "application/json", &buf)
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

	response, err := a.enrollmentClient.ReadEnrollmentRequestStatusWithResponse(ctx, a.fingerprint)
	if err != nil {
		klog.Infof("%serror checking enrollment status: %v", a.name, err)
		return false, nil
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		klog.Infof("%serror checking enrollment status: %v", a.name, response.Status())
		return false, nil
	}
	enrollmentRequest := response.JSON200

	// TODO: update schema to require condition in status, then remove this check
	if enrollmentRequest.Status == nil || enrollmentRequest.Status.Conditions == nil {
		klog.Fatalf("%senrollment request status or conditions field are nil", a.name)
	}

	approved := false
	for _, c := range *enrollmentRequest.Status.Conditions {
		if c.Type == "Denied" {
			return false, fmt.Errorf("%senrollment request is denied, reason: %v, message: %v", a.name, c.Reason, c.Message)
		}
		if c.Type == "Failed" {
			return false, fmt.Errorf("%senrollment request failed, reason: %v, message: %v", a.name, c.Reason, c.Message)
		}
		if c.Type == "Approved" {
			approved = true
		}
	}
	if !approved {
		klog.Infof("%senrollment request not yet approved", a.name)
		return false, nil
	}
	if len(*enrollmentRequest.Status.Certificate) == 0 {
		klog.Infof("%senrollment request approved, but certificate not yet issued", a.name)
		return false, nil
	}
	klog.Infof("%senrollment approved and certificate issued", a.name)

	if _, err = cert.ParseCertsPEM([]byte(*enrollmentRequest.Status.Certificate)); err != nil {
		return false, fmt.Errorf("%sparsing signed certificate: %v", a.name, err)
	}

	if err := os.WriteFile(filepath.Join(a.certDir, clientCertFile), []byte(*enrollmentRequest.Status.Certificate), os.FileMode(0600)); err != nil {
		return false, fmt.Errorf("%swriting signed certificate: %v", a.name, err)
	}

	return true, nil
}
