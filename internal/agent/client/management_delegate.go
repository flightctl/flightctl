package client

import (
	"context"
	"fmt"
	"sync/atomic"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	agentclient "github.com/flightctl/flightctl/internal/api/client/agent"
)

// Reloadable is an optional interface that callers can use to force recreating
// the underlying client (e.g., after rotating client TLS certs).
type Reloadable interface {
	Reload() error
}

var _ Management = (*ManagementDelegate)(nil)
var _ Reloadable = (*ManagementDelegate)(nil)

// ManagementFactory creates a new underlying Management client.
type ManagementFactory func() (Management, error)

type managementHolder struct {
	mgmt Management
}

// ManagementDelegate delegates Management calls to a swappable underlying client.
type ManagementDelegate struct {
	factory ManagementFactory
	current atomic.Pointer[managementHolder]

	// Keep the latest callback so it survives Reload().
	rpcCB atomic.Value // stores RPCMetricsCallback
}

// NewManagementDelegate creates a Management client wrapper that delegates all
// calls to an underlying Management instance produced by the given factory.
//
// The delegate allows the underlying client to be atomically replaced at
// runtime (via Reload), which is useful for scenarios such as client TLS
// certificate rotation, transport reconfiguration, or full client rebuilds.
//
// Callers should treat the returned Management as long-lived and stable,
// while the delegate transparently swaps the actual client implementation
// underneath without requiring re-wiring of consumers.
func NewManagementDelegate(factory ManagementFactory) (*ManagementDelegate, error) {
	if factory == nil {
		return nil, fmt.Errorf("management delegate: factory is nil")
	}

	d := &ManagementDelegate{factory: factory}
	if err := d.Reload(); err != nil {
		return nil, err
	}
	return d, nil
}

// Reload forces re-creation of the underlying client (e.g., after cert rotation).
func (d *ManagementDelegate) Reload() error {
	if d.factory == nil {
		return fmt.Errorf("management delegate: factory is nil")
	}

	newMgmt, err := d.factory()
	if err != nil {
		return err
	}

	// Publish the new client first so readers can observe it immediately.
	d.current.Store(&managementHolder{mgmt: newMgmt})

	// Best-effort: apply the latest metrics callback to the new client.
	// This covers races with SetRPCMetricsCallback().
	if v := d.rpcCB.Load(); v != nil {
		if cb, ok := v.(RPCMetricsCallback); ok {
			newMgmt.SetRPCMetricsCallback(cb)
		}
	}

	return nil
}

// TryReload is a best-effort helper: if m supports Reloadable it reloads it.
// It returns (true, err) when Reload was attempted, and (false, nil) otherwise.
func TryReload(m Management) (bool, error) {
	r, ok := m.(Reloadable)
	if !ok {
		return false, nil
	}
	return true, r.Reload()
}

func (d *ManagementDelegate) mgmt() (Management, error) {
	h := d.current.Load()
	if h == nil || h.mgmt == nil {
		return nil, fmt.Errorf("management client not initialized")
	}
	return h.mgmt, nil
}

// ---- Management passthrough ----

func (d *ManagementDelegate) SetRPCMetricsCallback(cb RPCMetricsCallback) {
	// Persist for future Reload() calls.
	d.rpcCB.Store(cb)

	m, err := d.mgmt()
	if err != nil {
		// Not initialized yet; callback will be applied on Reload().
		return
	}
	m.SetRPCMetricsCallback(cb)
}

func (d *ManagementDelegate) UpdateDeviceStatus(
	ctx context.Context,
	name string,
	device api.Device,
	rcb ...agentclient.RequestEditorFn,
) error {
	m, err := d.mgmt()
	if err != nil {
		return err
	}
	return m.UpdateDeviceStatus(ctx, name, device, rcb...)
}

func (d *ManagementDelegate) GetRenderedDevice(
	ctx context.Context,
	name string,
	params *api.GetRenderedDeviceParams,
	rcb ...agentclient.RequestEditorFn,
) (*api.Device, int, error) {
	m, err := d.mgmt()
	if err != nil {
		return nil, 0, err
	}
	return m.GetRenderedDevice(ctx, name, params, rcb...)
}

func (d *ManagementDelegate) PatchDeviceStatus(
	ctx context.Context,
	name string,
	patch api.PatchRequest,
	rcb ...agentclient.RequestEditorFn,
) error {
	m, err := d.mgmt()
	if err != nil {
		return err
	}
	return m.PatchDeviceStatus(ctx, name, patch, rcb...)
}

func (d *ManagementDelegate) CreateCertificateSigningRequest(
	ctx context.Context,
	csr api.CertificateSigningRequest,
	rcb ...agentclient.RequestEditorFn,
) (*api.CertificateSigningRequest, int, error) {
	m, err := d.mgmt()
	if err != nil {
		return nil, 0, err
	}
	return m.CreateCertificateSigningRequest(ctx, csr, rcb...)
}

func (d *ManagementDelegate) GetCertificateSigningRequest(
	ctx context.Context,
	name string,
	rcb ...agentclient.RequestEditorFn,
) (*api.CertificateSigningRequest, int, error) {
	m, err := d.mgmt()
	if err != nil {
		return nil, 0, err
	}
	return m.GetCertificateSigningRequest(ctx, name, rcb...)
}
