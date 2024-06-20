package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
)

var (
	ErrEmptyResponse = errors.New("empty response")
)

func NewManagement(
	client *client.ClientWithResponses,
) *Management {
	return &Management{
		client: client,
	}
}

type Management struct {
	client                 *client.ClientWithResponses
	rpcMetricsCallbackFunc func(operation string, durationSeconds float64, err error)
}

// UpdateDeviceStatus updates the status of the device with the given name.
func (m *Management) UpdateDeviceStatus(ctx context.Context, name string, device v1alpha1.Device, rcb ...client.RequestEditorFn) error {
	start := time.Now()
	resp, err := m.client.ReplaceDeviceStatusWithResponse(ctx, name, device, rcb...)
	if err != nil {
		return err
	}
	if resp.HTTPResponse != nil {
		defer resp.HTTPResponse.Body.Close()
	}

	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("update_device_status_duration", time.Since(start).Seconds(), err)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("update device status failed: %s", resp.Status())
	}

	return nil
}

// GetRenderedDeviceSpec returns the rendered device spec for the given device
// and the response code. If the server returns a 200, the rendered device spec
// is returned. If the server returns a 204, the rendered device spec is nil,
// and the response code is returned which should be evaluated but the caller.
func (m *Management) GetRenderedDeviceSpec(ctx context.Context, name string, params *v1alpha1.GetRenderedDeviceSpecParams, rcb ...client.RequestEditorFn) (*v1alpha1.RenderedDeviceSpec, int, error) {
	start := time.Now()
	resp, err := m.client.GetRenderedDeviceSpecWithResponse(ctx, name, params, rcb...)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if resp.HTTPResponse != nil {
		defer resp.HTTPResponse.Body.Close()
	}

	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("get_rendered_device_spec_duration", time.Since(start).Seconds(), err)
	}

	if resp.JSON200 != nil {
		return resp.JSON200, resp.StatusCode(), nil
	}

	// since there is no JSON204 to return, we have to let the caller evaluate
	// the status code

	return nil, resp.StatusCode(), nil
}
