package certmanager

import (
	"context"
	"time"
)

// Run is an OPTIONAL convenience helper that periodically calls Sync(ctx)
// until the context is canceled.
//
// Typical usage:
//
//	go cm.Run(ctx, 30*time.Second)
//
// Or manual control:
//
//	_ = cm.Sync(ctx)
//	_ = cm.SyncBundle(ctx, "default")
func (cm *CertManager) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		// Defensive: do nothing instead of panicking.
		cm.log.Warnf("certmanager.Run called with non-positive interval: %v", interval)
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run an initial sync immediately so callers don't have to wait for the first tick.
	cm.log.Debug("Starting certificate manager run loop (initial sync)")
	_ = cm.Sync(ctx)

	for {
		select {
		case <-ctx.Done():
			cm.log.Debug("Certificate manager run loop stopped (context canceled)")
			return

		case <-ticker.C:
			// Sync errors are logged internally; Run never terminates on sync failure.
			_ = cm.Sync(ctx)
		}
	}
}
