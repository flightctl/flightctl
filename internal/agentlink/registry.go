// Package agentlink tracks which replica holds each device's live agent link.
//
// The registration gates new starts; it does not gate running work. Replacing
// it must not cancel, fence or re-home any request still running on the
// previous link: a superseded link is not closed on supersession, it simply
// stops being reachable for new starts.
package agentlink

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	// DispatchTTL is how long a registration survives without a refresh. A
	// replica that dies stops refreshing and its registration expires, after
	// which the device resolves to no link until it redials.
	DispatchTTL = 60 * time.Second
	// DispatchRefreshInterval is how often the holding replica refreshes its
	// own registration. It is comfortably below DispatchTTL so an ordinary
	// scheduling delay or a slow round trip does not expire a healthy link.
	DispatchRefreshInterval = 20 * time.Second
)

// Registration is the value stored under a device's dispatch key.
//
// LinkID stays internal: it never appears in any JSON message on the
// agent-facing wire. It exists so the owner can fence a dispatch against the
// exact connection it was resolved for.
type Registration struct {
	ReplicaID string `json:"replicaId"`
	LinkID    string `json:"linkId"`
}

// Registry registers and resolves device link ownership.
type Registry struct {
	kv        kvstore.KVStore
	replicaID string
	log       logrus.FieldLogger
}

// NewRegistry returns a registry for one replica. replicaID identifies this
// process and must be stable for its lifetime.
func NewRegistry(kv kvstore.KVStore, replicaID string, log logrus.FieldLogger) *Registry {
	return &Registry{kv: kv, replicaID: replicaID, log: log}
}

func dispatchKey(orgID uuid.UUID, deviceName string) string {
	k := kvstore.AgentLinkDispatchKey{OrgID: orgID, DeviceName: deviceName}
	return k.ComposeKey()
}

// Resolve returns the registration for a device, or nil when none is current.
//
// Absence does not distinguish an offline device from one whose agent is
// redialling, and it proves nothing about which methods the agent implements:
// a registered link proves only that the agent completed the handshake and
// speaks the envelope.
func (r *Registry) Resolve(ctx context.Context, orgID uuid.UUID, deviceName string) (*Registration, error) {
	raw, err := r.kv.Get(ctx, dispatchKey(orgID, deviceName))
	if err != nil {
		return nil, fmt.Errorf("resolving agent link for %s: %w", deviceName, err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var reg Registration
	if err := json.Unmarshal(raw, &reg); err != nil {
		return nil, fmt.Errorf("decoding agent link registration for %s: %w", deviceName, err)
	}
	return &reg, nil
}

// Lease is a live registration held by this replica for one connection.
type Lease struct {
	registry   *Registry
	key        string
	value      []byte
	deviceName string
	linkID     string
	// refreshInterval is indirected so tests need not wait a real tick.
	refreshInterval time.Duration
}

// LinkID returns the unique identifier of the connection this lease covers.
func (l *Lease) LinkID() string { return l.linkID }

// Register claims the dispatch registration for a device's new link.
//
// The newest link wins: a replacement supersedes the previous registration,
// which is why the previous holder's refresh will fail rather than resurrect
// it. The previous link is left open and its running requests untouched.
func (r *Registry) Register(ctx context.Context, orgID uuid.UUID, deviceName, linkID string) (*Lease, error) {
	value, err := json.Marshal(Registration{ReplicaID: r.replicaID, LinkID: linkID})
	if err != nil {
		return nil, fmt.Errorf("encoding agent link registration for %s: %w", deviceName, err)
	}
	key := dispatchKey(orgID, deviceName)
	if err := r.kv.Set(ctx, key, value, DispatchTTL); err != nil {
		return nil, fmt.Errorf("registering agent link for %s: %w", deviceName, err)
	}
	return &Lease{
		registry:        r,
		key:             key,
		value:           value,
		deviceName:      deviceName,
		linkID:          linkID,
		refreshInterval: DispatchRefreshInterval,
	}, nil
}

// Refresh extends the lease. It reports false once the registration has been
// superseded or has expired, at which point this link is no longer reachable
// for new starts -- though the requests already running on it continue, which
// is why losing the lease is not a reason to close the connection.
func (l *Lease) Refresh(ctx context.Context) (bool, error) {
	return l.registry.kv.ExpireIfMatch(ctx, l.key, l.value, DispatchTTL)
}

// Release removes the registration, but only while this lease still holds it.
// A lease that has been superseded removes nothing, so a closing link can
// never delete its replacement's registration.
func (l *Lease) Release(ctx context.Context) error {
	removed, err := l.registry.kv.DeleteIfMatch(ctx, l.key, l.value)
	if err != nil {
		return fmt.Errorf("releasing agent link for %s: %w", l.deviceName, err)
	}
	if !removed {
		l.registry.log.Debugf("Agent link registration for %s was already superseded", l.deviceName)
	}
	return nil
}

// KeepAlive refreshes the lease until ctx is cancelled or the registration is
// superseded. It is meant to run for the life of one connection.
//
// Losing the registration ends only the refresh loop. The link stays open and
// keeps serving the requests already accepted on it.
func (l *Lease) KeepAlive(ctx context.Context) {
	ticker := time.NewTicker(l.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			held, err := l.Refresh(ctx)
			if err != nil {
				l.registry.log.Warnf("Agent link: failed to refresh registration for %s: %v", l.deviceName, err)
				continue
			}
			if !held {
				l.registry.log.Infof("Agent link registration for %s was superseded; this link remains open for its running requests", l.deviceName)
				return
			}
		}
	}
}
