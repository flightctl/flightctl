// certificate_reconciler.go
package certmanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// reconcileFunc is invoked for a single certificate reconcile attempt.
// If it returns a non-nil duration, the item will be retried after that delay.
// If it returns nil, processing is considered complete and the item is removed.
type reconcileFunc func(
	ctx context.Context,
	bundleName string,
	providerKey string, // expected to already be bundle-namespaced (i.e. a stable unique key)
	cert *certificate,
	cfg *CertificateConfig,
	attempt int,
) *time.Duration

// reconcileItem represents an active reconcile task for a single (providerKey, certName).
//
// Note: retries for the *same* item are represented by re-enqueueing this same pointer.
// If an item is replaced via Enqueue, the old item is canceled and removed from tracking;
// any outstanding scheduled retries for the old item may still fire, but will be dropped
// because ctx.Err() != nil.
type reconcileItem struct {
	ctx    context.Context
	cancel context.CancelFunc

	bundleName  string
	providerKey string
	cert        *certificate
	cfg         CertificateConfig
}

// CertificateReconciler manages certificate reconcile tasks and retries,
// and prevents duplicate concurrent processing for the same (providerKey, certName).
//
// Semantics:
//   - Key is (providerKey, cert.Name).
//   - Enqueue replaces any existing in-flight item for the same key (cancel+drop).
//   - The underlying RetryQueue worker is sequential; handler is not called in parallel.
type CertificateReconciler struct {
	mu sync.Mutex

	queue     *RetryQueue[*reconcileItem]
	inProcess map[string]*reconcileItem
	handler   reconcileFunc

	// baseCtx is set by Run() and used as parent for per-item contexts.
	// This ensures canceling the Run() ctx cancels all outstanding items.
	baseCtx context.Context
}

// newCertificateReconciler creates a reconciler with the given handler.
func newCertificateReconciler(handler reconcileFunc) *CertificateReconciler {
	r := &CertificateReconciler{
		inProcess: make(map[string]*reconcileItem),
		handler:   handler,
	}
	r.queue = newRetryQueue(r.process)
	return r
}

// Run starts the underlying worker and binds the reconciler lifetime to ctx.
// Call in a goroutine.
func (r *CertificateReconciler) Run(ctx context.Context) {
	r.mu.Lock()
	r.baseCtx = ctx
	r.mu.Unlock()

	r.queue.RunWorker(ctx)
}

// Len returns the number of currently tracked in-flight items.
func (r *CertificateReconciler) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inProcess)
}

// Get returns the currently tracked certificate/config for a key.
// If not present, returns (nil, CertificateConfig{}).
func (r *CertificateReconciler) Get(providerKey, certName string) (*certificate, CertificateConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cp, ok := r.inProcess[reconcileKey(providerKey, certName)]
	if !ok {
		return nil, CertificateConfig{}
	}
	return cp.cert, cp.cfg
}

// IsProcessing reports whether (providerKey, certName) is currently tracked.
func (r *CertificateReconciler) IsProcessing(providerKey, certName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.inProcess[reconcileKey(providerKey, certName)]
	return ok
}

// Remove cancels and removes the tracked item if it exists.
func (r *CertificateReconciler) Remove(providerKey, certName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(providerKey, certName)
}

// Enqueue schedules (or replaces) a reconcile item.
//
// providerKey is expected to already be stable and bundle-namespaced.
// bundleName is retained for capability boundary / lookup in the handler.
func (r *CertificateReconciler) Enqueue(bundleName, providerKey string, cert *certificate, cfg CertificateConfig) error {
	if r.handler == nil {
		return fmt.Errorf("no handler configured")
	}
	if cert == nil {
		return fmt.Errorf("certificate is nil")
	}
	bundleName = strings.TrimSpace(bundleName)
	if bundleName == "" {
		return fmt.Errorf("bundle name is empty")
	}
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		return fmt.Errorf("provider key is empty")
	}
	if strings.TrimSpace(cert.Name) == "" {
		return fmt.Errorf("certificate name is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Replace any existing item for this key.
	if _, ok := r.inProcess[reconcileKey(providerKey, cert.Name)]; ok {
		r.removeLocked(providerKey, cert.Name)
	}

	base := r.baseCtx
	if base == nil {
		// Defensive fallback: if Run() wasn't called yet, still function.
		// Prefer calling Run() first so cancellation is properly wired.
		base = context.Background()
	}

	ctx, cancel := context.WithCancel(base)
	cp := &reconcileItem{
		ctx:         ctx,
		cancel:      cancel,
		bundleName:  bundleName,
		providerKey: providerKey,
		cert:        cert,
		cfg:         cfg,
	}

	r.inProcess[reconcileKey(providerKey, cert.Name)] = cp
	r.queue.Add(cp)
	return nil
}

// process is the RetryQueue handler.
// It delegates to r.handler and controls remove/retry semantics.
func (r *CertificateReconciler) process(_ context.Context, it *reconcileItem, attempt int) *time.Duration {
	// Fast path: if canceled, drop and untrack.
	if it == nil || it.ctx.Err() != nil || r.handler == nil {
		if it != nil && it.cert != nil {
			r.Remove(it.providerKey, it.cert.Name)
		}
		return nil
	}

	delay := r.handler(it.ctx, it.bundleName, it.providerKey, it.cert, &it.cfg, attempt)

	// If handler requested retry, keep tracking. Otherwise remove.
	if delay != nil {
		if it.ctx.Err() != nil {
			r.Remove(it.providerKey, it.cert.Name)
			return nil
		}
		return delay
	}

	r.Remove(it.providerKey, it.cert.Name)
	return nil
}

func (r *CertificateReconciler) removeLocked(providerKey, certName string) {
	key := reconcileKey(providerKey, certName)
	if it, ok := r.inProcess[key]; ok {
		it.cancel()
		delete(r.inProcess, key)
	}
}

// reconcileKey creates a stable unique identifier for inProcess tracking.
// providerKey is expected to already be bundle-namespaced.
func reconcileKey(providerKey, certName string) string {
	return providerKey + "\x00" + certName
}
