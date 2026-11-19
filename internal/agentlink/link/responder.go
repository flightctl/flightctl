package link

import (
	"encoding/json"
	"errors"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
)

// responder is the Responder handed to a handler. It is scoped to one
// request: a handler cannot reach the link or a sibling through it.
type responder struct {
	endpoint *Endpoint
	key      requestKey
	state    *requestState
}

var errOutcomeFixed = errors.New("link: request outcome is already fixed")

// Data enqueues one data message for this request.
//
// data and result are distinct kinds, so a failing result can never arrive
// fused to payload bytes.
func (r *responder) Data(payload json.RawMessage) error {
	if r.state.resultSent.Load() {
		return errOutcomeFixed
	}
	data, err := protocol.EncodeData(r.key.origin, r.key.id, payload)
	if err != nil {
		return err
	}
	return r.endpoint.enqueue(data)
}

// Result fixes this request's outcome. Only the first call takes effect.
func (r *responder) Result(reason, message string) error {
	return r.endpoint.sendResultOnce(r.key, r.state, reason, message)
}

// ringSet is a bounded set with first-in-first-out eviction. It holds the
// recently-completed identifiers against which a stale duplicate request is
// recognised.
//
// It is sized as an implementation constant, so the guarantee it supports is
// stated at the strength it holds: a request naming a currently active
// identifier is always detected, one naming an identifier still in this set
// is dropped as a stale duplicate, and one naming an evicted identifier is
// indistinguishable from a new request.
//
// It carries no lock of its own; the endpoint holds its mutex across every
// call.
type ringSet struct {
	capacity int
	order    []requestKey
	next     int
	members  map[requestKey]struct{}
}

func newRingSet(capacity int) *ringSet {
	if capacity < 1 {
		capacity = 1
	}
	return &ringSet{
		capacity: capacity,
		order:    make([]requestKey, 0, capacity),
		members:  make(map[requestKey]struct{}, capacity),
	}
}

func (s *ringSet) has(k requestKey) bool {
	_, ok := s.members[k]
	return ok
}

func (s *ringSet) add(k requestKey) {
	if s.has(k) {
		return
	}
	if len(s.order) < s.capacity {
		s.order = append(s.order, k)
		s.members[k] = struct{}{}
		return
	}
	delete(s.members, s.order[s.next])
	s.order[s.next] = k
	s.members[k] = struct{}{}
	s.next = (s.next + 1) % s.capacity
}

func (s *ringSet) len() int { return len(s.members) }
