package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
)

var _ Management = (*management)(nil)

var (
	ErrEmptyResponse  = errors.New("empty response")
	ErrDeviceNotFound = errors.New("device not found - certificate should be wiped and agent restarted")
)

func NewManagement(
	client *client.ClientWithResponses, cb RPCMetricsCallback,
) Management {
	return &management{
		client:                 client,
		rpcMetricsCallbackFunc: cb,
	}
}

type management struct {
	client                 *client.ClientWithResponses
	rpcMetricsCallbackFunc RPCMetricsCallback
}

// SetRPCMetricsCallback sets the callback function to be called when a RPC
// request is made. The callback function is called with the operation name,
// the duration of the request in seconds, and the error if any.
func (m *management) SetRPCMetricsCallback(cb RPCMetricsCallback) {
	m.rpcMetricsCallbackFunc = cb
}

// UpdateDeviceStatus updates the status of the device with the given name.
func (m *management) UpdateDeviceStatus(ctx context.Context, name string, device v1beta1.Device, rcb ...client.RequestEditorFn) error {
	start := time.Now()
	resp, err := m.client.ReplaceDeviceStatusWithResponse(ctx, name, device, rcb...)

	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("update_device_status_duration", time.Since(start).Seconds(), err)
	}

	if err != nil {
		return err
	}
	if resp.HTTPResponse != nil {
		defer func() { _ = resp.HTTPResponse.Body.Close() }()
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("update device status failed: %s", resp.Status())
	}

	return nil
}

func (m *management) PatchDeviceStatus(ctx context.Context, name string, patch v1beta1.PatchRequest, rcb ...client.RequestEditorFn) error {
	start := time.Now()
	resp, err := m.client.PatchDeviceStatusWithApplicationJSONPatchPlusJSONBodyWithResponse(ctx, name, patch, rcb...)

	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("patch_device_status_duration", time.Since(start).Seconds(), err)
	}

	if err != nil {
		return err
	}
	if resp.HTTPResponse != nil {
		defer resp.HTTPResponse.Body.Close()
	}

	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return fmt.Errorf("patch device status failed: %s", resp.Status())
	}

	return nil
}

// GetRenderedDevice returns the rendered device spec for the given device
// and the response code. If the server returns a 200, the rendered device spec
// is returned. If the server returns a 204, the rendered device spec is nil,
// and the response code is returned which should be evaluated but the caller.
func (m *management) GetRenderedDevice(ctx context.Context, name string, params *v1beta1.GetRenderedDeviceParams, rcb ...client.RequestEditorFn) (*v1beta1.Device, int, error) {
	start := time.Now()

	resp, err := m.client.GetRenderedDeviceWithResponse(ctx, name, params, rcb...)

	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("get_rendered_device_spec_duration", time.Since(start).Seconds(), err)
	}

	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if resp.HTTPResponse != nil {
		defer func() { _ = resp.HTTPResponse.Body.Close() }()
	}

	if resp.JSON200 != nil {
		return resp.JSON200, resp.StatusCode(), nil
	}

	// Check for 404 device not found - only if body contains specific content
	if resp.StatusCode() == http.StatusNotFound {
		// Check the response body for specific device not found content
		// The generated client already reads the body into resp.Body
		if len(resp.Body) > 0 {
			// Check for the exact device not found error pattern from the server
			// Server returns: "Device of name \"<device-name>\" not found"
			if strings.Contains(string(resp.Body), "Device of name") && strings.Contains(string(resp.Body), "not found") {
				return nil, resp.StatusCode(), ErrDeviceNotFound
			}
		}
		// 404 but not the specific device not found case - return generic 404
		return nil, resp.StatusCode(), nil
	}

	// since there is no JSON204 to return, we have to let the caller evaluate
	// the status code

	return nil, resp.StatusCode(), nil
}

// CreateCertificateSigningRequest submits a new CSR to the management server for certificate approval.
// It handles the initial CSR submission and returns the created CSR object with server-assigned metadata.
// The CSR will be processed asynchronously by the server's certificate approval workflow.
func (m *management) CreateCertificateSigningRequest(ctx context.Context, csr v1beta1.CertificateSigningRequest, rcb ...client.RequestEditorFn) (*v1beta1.CertificateSigningRequest, int, error) {
	start := time.Now()
	resp, err := m.client.CreateCertificateSigningRequestWithResponse(ctx, csr, rcb...)

	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("create_certificate_signing_request_duration", time.Since(start).Seconds(), err)
	}

	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if resp.HTTPResponse != nil {
		defer func() { _ = resp.HTTPResponse.Body.Close() }()
	}

	if resp.JSON400 != nil {
		return nil, resp.StatusCode(), fmt.Errorf("create certificate signing request failed: %s", resp.JSON400.Message)
	}

	if resp.JSON201 != nil {
		return resp.JSON201, resp.StatusCode(), nil
	}

	return nil, resp.StatusCode(), nil
}

// GetCertificateSigningRequest retrieves the current status of a CSR from the management server.
// This method is used to poll for CSR approval status and retrieve the issued certificate when ready.
// The CSR status includes approval/denial state and the signed certificate when approved.
func (m *management) GetCertificateSigningRequest(ctx context.Context, name string, rcb ...client.RequestEditorFn) (*v1beta1.CertificateSigningRequest, int, error) {
	start := time.Now()
	resp, err := m.client.GetCertificateSigningRequestWithResponse(ctx, name, rcb...)

	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("get_certificate_signing_request_duration", time.Since(start).Seconds(), err)
	}

	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if resp.HTTPResponse != nil {
		defer func() { _ = resp.HTTPResponse.Body.Close() }()
	}

	if resp.JSON200 != nil {
		return resp.JSON200, resp.StatusCode(), nil
	}

	return nil, resp.StatusCode(), nil
}
