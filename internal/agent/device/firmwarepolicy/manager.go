package firmwarepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
)

var ErrUnavailable = errors.New("retained firmware policy is unavailable")

type retained struct {
	view               *policy.View
	floor              *policy.View
	approved           map[string]string
	err                error
	historyUnavailable bool
}
type result struct {
	receipt policy.Receipt
	err     error
}
type pending struct {
	view   policy.View
	result chan result
}
type Option func(*options)
type options struct{ poll poll.Config }

func WithPollConfig(cfg poll.Config) Option { return func(o *options) { o.poll = cfg } }

type Manager struct {
	name, path string
	rw         fileio.ReadWriter
	client     client.Management
	log        *log.PrefixLogger
	poll       poll.Config
	pending    chan pending
	state      atomic.Pointer[retained]
	required   chan struct{}
	require    sync.Once
}

func New(name, dataDir string, rw fileio.ReadWriter, management client.Management, logger *log.PrefixLogger, opts ...Option) (*Manager, error) {
	if name == "" || !filepath.IsAbs(dataDir) || rw == nil || management == nil || logger == nil {
		return nil, errors.New("firmware policy requires identity, absolute data directory, fileio, management client and logger")
	}
	o := options{poll: poll.Config{BaseDelay: 5 * time.Second, Factor: 2, MaxDelay: 5 * time.Minute, JitterFactor: 0.2}}
	for _, opt := range opts {
		opt(&o)
	}
	o.poll.Rand = nil
	if err := o.poll.Validate(); err != nil {
		return nil, fmt.Errorf("validate firmware policy retry config: %w", err)
	}
	if o.poll.MaxDelay <= 0 {
		return nil, errors.New("firmware policy retry delay must be bounded")
	}
	m := &Manager{name: name, path: filepath.Join(dataDir, "firmware", "policy.json"), rw: rw, client: management, log: logger, poll: o.poll, pending: make(chan pending, 1), required: make(chan struct{})}
	state := &retained{}
	data, err := rw.ReadFileLimited(m.path, policy.MaxWireBytes)
	if !os.IsNotExist(err) {
		// Retained history may still authorize a recoverable attempt, even if
		// the current desired target has been removed or cannot be decoded.
		m.RequirePolicy()
	}
	if err != nil && !os.IsNotExist(err) {
		state.err = fmt.Errorf("%w: read retained firmware policy: %w", ErrUnavailable, err)
		state.historyUnavailable = true
	} else if err == nil {
		view, decodeErr := policy.ParseView(data)
		if decodeErr != nil {
			state.err = fmt.Errorf("%w: decode retained firmware policy: %w", ErrUnavailable, decodeErr)
			state.historyUnavailable = true
		} else {
			state, err = accepted(view)
			if err != nil {
				return nil, fmt.Errorf("accept retained firmware policy: %w", err)
			}
		}
	}
	m.state.Store(state)
	return m, nil
}

// RequirePolicy starts policy delivery once firmware work has been observed.
// It is intentionally irreversible: removing a target does not prove that a
// retained attempt can no longer execute and no longer needs revocations.
func (m *Manager) RequirePolicy() {
	m.require.Do(func() { close(m.required) })
}

func clone(view policy.View) policy.View {
	view.Document = append([]byte(nil), view.Document...)
	return view
}
func identity(name, version string) string { return name + "/" + version }
func accepted(view policy.View) (*retained, error) {
	document, err := policy.Decode(view)
	if err != nil {
		return nil, err
	}
	approved := map[string]string{}
	for _, b := range document.Bindings {
		approved[identity(b.Name, b.Version)] = b.DescriptorDigest
	}
	for _, t := range document.Tombstones {
		delete(approved, identity(t.Name, t.Version))
	}
	view = clone(view)
	return &retained{view: &view, approved: approved}, nil
}
func (m *Manager) Current() (*policy.View, error) {
	s := m.state.Load()
	if s.view == nil {
		if s.err != nil {
			return nil, s.err
		}
		return nil, ErrUnavailable
	}
	view := clone(*s.view)
	return &view, s.err
}
func (m *Manager) IsApproved(name, version, digest string) bool {
	s := m.state.Load()
	return s.err == nil && s.view != nil && digest != "" && s.approved[identity(name, version)] == digest
}
func (m *Manager) receipt() policy.Receipt {
	s := m.state.Load()
	if s.view == nil {
		return policy.Receipt{}
	}
	return policy.Receipt{Revision: s.view.Revision, Digest: s.view.Digest}
}

// Sync accepts at most one queued view under the agent's sequential update owner.
func (m *Manager) Sync(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case request := <-m.pending:
		receipt, err := m.accept(request.view)
		request.result <- result{receipt: receipt, err: err}
		return err
	default:
		return nil
	}
}

// Receive queues a complete policy from a delivery transport and waits for the
// sequential owner to validate and durably retain it through Sync. Only a
// successful receipt may be acknowledged to the service.
func (m *Manager) Receive(ctx context.Context, view policy.View) (policy.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return policy.Receipt{}, err
	}
	request := pending{view: clone(view), result: make(chan result, 1)}
	select {
	case m.pending <- request:
	case <-ctx.Done():
		return policy.Receipt{}, ctx.Err()
	}
	select {
	case outcome := <-request.result:
		return outcome.receipt, outcome.err
	case <-ctx.Done():
		return policy.Receipt{}, ctx.Err()
	}
}
func (m *Manager) accept(view policy.View) (policy.Receipt, error) {
	previous := m.state.Load()
	if previous.historyUnavailable {
		return policy.Receipt{}, previous.err
	}
	baseline := previous.view
	if previous.floor != nil {
		baseline = previous.floor
	}
	changed, err := policy.ValidateTransition(baseline, view)
	if err != nil {
		return policy.Receipt{}, err
	}
	if changed || previous.err != nil {
		next, err := accepted(view)
		if err != nil {
			return policy.Receipt{}, err
		}
		data, err := json.Marshal(view)
		if err != nil {
			return policy.Receipt{}, err
		}
		if err = m.rw.WriteFileDurable(m.path, data, 0700, 0600); err != nil {
			failed := *previous
			floor := clone(view)
			failed.floor = &floor
			failed.err = fmt.Errorf("%w: %w", ErrUnavailable, err)
			m.state.Store(&failed)
			return policy.Receipt{}, failed.err
		}
		m.state.Store(next)
	}
	return m.receipt(), nil
}
