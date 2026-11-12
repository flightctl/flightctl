package firmwarepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	m := &Manager{name: name, path: filepath.Join(dataDir, "firmware", "policy.json"), rw: rw, client: management, log: logger, poll: o.poll, pending: make(chan pending, 1)}
	state := &retained{}
	data, err := rw.ReadFileLimited(m.path, policy.MaxWireBytes)
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

// Run performs network I/O only; queued views become authoritative through Sync.
func (m *Manager) Run(ctx context.Context) {
	var unacknowledged *policy.Receipt
	if current := m.receipt(); current.Revision > 0 {
		unacknowledged = &current
	}
	failures := 0
	for ctx.Err() == nil {
		failed := false
		if unacknowledged != nil {
			callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			status, err := m.client.AcknowledgeFirmwarePolicy(callCtx, m.name, *unacknowledged)
			cancel()
			if err == nil && (status == http.StatusNoContent || status == http.StatusConflict) {
				unacknowledged = nil
			} else {
				failed = true
				if ctx.Err() == nil {
					m.log.Warn("firmware policy acknowledgement unavailable; retained view preserved")
				}
			}
		}
		callCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		view, status, err := m.client.GetFirmwarePolicy(callCtx, m.name, m.receipt())
		cancel()
		if err != nil || (status != http.StatusOK && status != http.StatusNoContent) {
			failed = true
			if ctx.Err() == nil {
				m.log.Warn("firmware policy refresh unavailable; retained view preserved")
			}
		} else if status == http.StatusOK {
			if view == nil {
				failed = true
			} else {
				request := pending{view: clone(*view), result: make(chan result, 1)}
				select {
				case m.pending <- request:
				case <-ctx.Done():
					return
				}
				select {
				case outcome := <-request.result:
					if outcome.err != nil {
						failed = true
						m.log.WithError(outcome.err).Warn("firmware policy was not accepted")
					} else {
						receipt := outcome.receipt
						unacknowledged = &receipt
					}
				case <-ctx.Done():
					return
				}
			}
		}
		delay := m.poll.BaseDelay
		if failed {
			if failures < 20 {
				failures++
			}
			delay = poll.CalculateBackoffDelay(&m.poll, failures)
		} else {
			failures = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
