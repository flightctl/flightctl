package client

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/client"
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

func (m *Management) GetDevice(ctx context.Context, name string, rcb ...client.RequestEditorFn) (*v1alpha1.Device, error) {
	start := time.Now()
	resp, err := m.client.ReadDeviceWithResponse(ctx, name, rcb...)
	if err != nil {
		return nil, err
	}
	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("get_device_duration", time.Since(start).Seconds(), err)
	}

	if resp.JSON200 == nil {
		return nil, ErrEmptyResponse
	}

	return resp.JSON200, nil
}

func (m *Management) UpdateDevice(ctx context.Context, name string, req v1alpha1.Device, rcb ...client.RequestEditorFn) (*v1alpha1.Device, error) {
	device := v1alpha1.Device{
		Metadata: v1alpha1.ObjectMeta{
			Name: &name,
		},
		Spec: req.Spec,
	}

	start := time.Now()
	resp, err := m.client.ReplaceDeviceWithResponse(ctx, name, device, rcb...)
	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("update_device_duration", time.Since(start).Seconds(), err)
	}
	if err != nil {
		return nil, err
	}

	if resp.JSON200 == nil {
		return nil, ErrEmptyResponse
	}

	return resp.JSON200, nil
}

func (m *Management) UpdateDeviceStatus(ctx context.Context, name string, buf *bytes.Buffer, rcb ...client.RequestEditorFn) error {
	start := time.Now()
	resp, err := m.client.ReplaceDeviceStatusWithBodyWithResponse(ctx, name, "application/json", buf, rcb...)
	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("update_device_status_duration", time.Since(start).Seconds(), err)
	}
	if err != nil {
		return err
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("update device status failed: %s", resp.Status())
	}

	return nil
}

// GetRenderedDeviceSpec returns the rendered device spec for the given device and the response code.
func (m *Management) GetRenderedDeviceSpec(ctx context.Context, name string, params *v1alpha1.GetRenderedDeviceSpecParams, rcb ...client.RequestEditorFn) (*v1alpha1.RenderedDeviceSpec, int, error) {
	start := time.Now()
	resp, err := m.client.GetRenderedDeviceSpecWithResponse(ctx, name, params, rcb...)
	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("get_rendered_device_spec_duration", time.Since(start).Seconds(), err)
	}
	if err != nil {
		return nil, 0, err
	}

	if resp.JSON200 != nil {
		return resp.JSON200, resp.StatusCode(), nil
	}

	return nil, resp.StatusCode(), nil
}