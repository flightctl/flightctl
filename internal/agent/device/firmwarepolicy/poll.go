package firmwarepolicy

import (
	"context"
	"net/http"
	"time"

	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/poll"
)

// Run performs network I/O only; queued views become authoritative through Sync.
func (m *Manager) Run(ctx context.Context) {
	select {
	case <-m.required:
	case <-ctx.Done():
		return
	}
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
				receipt, err := m.Receive(ctx, *view)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					failed = true
					m.log.WithError(err).Warn("firmware policy was not accepted")
				} else {
					unacknowledged = &receipt
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
