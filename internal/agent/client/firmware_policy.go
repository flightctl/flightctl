package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	agentapi "github.com/flightctl/flightctl/api/agent/v1beta1"
	agentclient "github.com/flightctl/flightctl/internal/api/client/agent"
	"github.com/flightctl/flightctl/internal/firmwarepolicy"
)

func (m *management) GetFirmwarePolicy(ctx context.Context, name string, known firmwarepolicy.Receipt, rcb ...agentclient.RequestEditorFn) (view *firmwarepolicy.View, status int, err error) {
	start := time.Now()
	defer func() {
		if m.rpcMetricsCallbackFunc != nil {
			m.rpcMetricsCallbackFunc("get_firmware_policy_duration", time.Since(start).Seconds(), err)
		}
	}()
	params := &agentapi.GetFirmwarePolicyParams{}
	if known.Revision != 0 || known.Digest != "" {
		if err := known.Validate(); err != nil {
			return nil, 0, err
		}
		params.KnownRevision = &known.Revision
		params.KnownDigest = &known.Digest
	}
	response, err := m.client.GetFirmwarePolicy(ctx, name, params, rcb...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, firmwarepolicy.MaxWireBytes+1))
	if err != nil {
		return nil, response.StatusCode, err
	}
	parsed, err := firmwarepolicy.ParseView(data)
	if err != nil {
		return nil, response.StatusCode, err
	}
	return &parsed, response.StatusCode, nil
}

func (m *management) AcknowledgeFirmwarePolicy(ctx context.Context, name string, receipt firmwarepolicy.Receipt, rcb ...agentclient.RequestEditorFn) (status int, err error) {
	start := time.Now()
	defer func() {
		if m.rpcMetricsCallbackFunc != nil {
			m.rpcMetricsCallbackFunc("acknowledge_firmware_policy_duration", time.Since(start).Seconds(), err)
		}
	}()
	if err := receipt.Validate(); err != nil {
		return 0, err
	}
	response, err := m.client.AcknowledgeFirmwarePolicy(ctx, name, agentapi.FirmwarePolicyReceipt{Revision: receipt.Revision, Digest: receipt.Digest}, rcb...)
	if err != nil {
		return 0, err
	}
	if response == nil {
		return 0, errors.New("empty policy acknowledgement response")
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, nil
}
