package client

import (
	"bytes"
	"context"
	"encoding/json"
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

func (m *Management) UpdateDeviceStatus(ctx context.Context, name string, req v1alpha1.DeviceStatus, rcb ...client.RequestEditorFn) (*v1alpha1.Device, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(req); err != nil {
		return nil, err
	}

	device := v1alpha1.Device{
		Metadata: v1alpha1.ObjectMeta{
			Name: &name,
		},
		Status: &req,
	}

	start := time.Now()
	resp, err := m.client.ReplaceDeviceStatusWithResponse(ctx, name, device, rcb...)
	if m.rpcMetricsCallbackFunc != nil {
		m.rpcMetricsCallbackFunc("update_device_status_duration", time.Since(start).Seconds(), err)
	}
	if err != nil {
		return nil, err
	}

	if resp.JSON200 == nil {
		return nil, ErrEmptyResponse
	}

	return resp.JSON200, nil
}
