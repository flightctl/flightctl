package certmanager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
)

// processHandlerFunc defines the function signature used to process a certificate.
// It receives the certificate processing context and returns a retry delay if the operation
// should be retried, or nil if the operation completed successfully or should not be retried.
type processHandlerFunc func(ctx context.Context, providerName string, cert *certificate, cfg *provider.CertificateConfig) *time.Duration

// certificateProcess represents an active certificate processing task.
type certificateProcess struct {
	ctx      context.Context            // Context for this processing task, can be canceled
	cancel   context.CancelFunc         // Cancel function to stop processing
	provider string                     // Name of the configuration provider
	cert     *certificate               // Certificate being processed
	cfg      provider.CertificateConfig // Configuration for certificate processing
}

// CertificateProcessingQueue manages and processes certificate provisioning and storage tasks.
// It uses a retry queue to handle failed operations and tracks in-progress certificates
// to prevent duplicate processing.
type CertificateProcessingQueue struct {
	queue     *RetryQueue[*certificateProcess] // Underlying retry queue for processing tasks
	inProcess map[string]*certificateProcess   // Map of currently processing certificates
	handler   processHandlerFunc               // Function to handle certificate processing
	mu        sync.RWMutex                     // Mutex for thread-safe access to inProcess map
}

// NewCertificateProcessingQueue creates a new CertificateProcessingQueue with the given handler.
// The handler function will be called for each certificate that needs processing.
func NewCertificateProcessingQueue(handler processHandlerFunc) *CertificateProcessingQueue {
	q := &CertificateProcessingQueue{
		inProcess: make(map[string]*certificateProcess),
		handler:   handler,
	}

	q.queue = NewRetryQueue(q.process)
	return q
}

// Run starts the certificate processing queue worker.
// This method should be called in a goroutine as it runs until the context is canceled.
func (q *CertificateProcessingQueue) Run(ctx context.Context) {
	go q.queue.RunWorker(ctx)
}

// Len returns the number of certificates currently being processed.
// This is useful for monitoring and debugging queue status.
func (q *CertificateProcessingQueue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return len(q.inProcess)
}

// Get retrieves the certificate and configuration for a certificate currently being processed.
// Returns nil certificate and empty config if the certificate is not currently being processed.
func (q *CertificateProcessingQueue) Get(providerName, certName string) (*certificate, provider.CertificateConfig) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.get(providerName, certName)
}

// IsProcessing returns true if the certificate with the given name is currently being processed.
func (q *CertificateProcessingQueue) IsProcessing(providerName, certName string) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.isProcessing(providerName, certName)
}

// Remove stops and removes a certificate from the in-process map if it exists.
// This cancels the processing context and cleans up the tracking state.
func (q *CertificateProcessingQueue) Remove(providerName, certName string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.remove(providerName, certName)
}

// Process adds a certificate to the processing queue using the provided context,
// or cancels and replaces an existing one if already in process.
// This is the main entry point for certificate processing requests.
func (q *CertificateProcessingQueue) Process(ctx context.Context, providerName string, cert *certificate, cfg provider.CertificateConfig) error {
	if q.handler == nil {
		return fmt.Errorf("no handler configured for processing certificates")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.isProcessing(providerName, cert.Name) {
		q.remove(providerName, cert.Name)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)

	c := &certificateProcess{
		ctx:      ctx,
		cancel:   cancel,
		provider: providerName,
		cert:     cert,
		cfg:      cfg,
	}

	q.inProcess[generateName(providerName, cert.Name)] = c
	q.queue.Add(c)
	return nil
}

// process is the internal callback invoked by the retry queue to process a certificate.
// It calls the configured handler and manages the retry logic based on the returned delay.
func (q *CertificateProcessingQueue) process(ctx context.Context, cp *certificateProcess, attempt int) *time.Duration {
	if q.handler == nil || cp.ctx.Err() != nil {
		return nil
	}

	if retry := q.handler(ctx, cp.provider, cp.cert, &cp.cfg); retry != nil {
		return retry
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	// Remove from in-process map and cancel context automatically
	q.remove(cp.provider, cp.cert.Name)

	return nil
}

// get retrieves the certificate and configuration for a certificate currently being processed.
// This is an internal method that should be called with the mutex held.
// It returns the certificate and configuration if found, or nil and empty config if not processing.
func (q *CertificateProcessingQueue) get(providerName, certName string) (*certificate, provider.CertificateConfig) {
	name := generateName(providerName, certName)
	if cp, exists := q.inProcess[name]; exists {
		return cp.cert, cp.cfg
	}
	return nil, provider.CertificateConfig{}
}

// isProcessing checks if a certificate is currently being processed.
// This is an internal method that should be called with the mutex held.
func (q *CertificateProcessingQueue) isProcessing(providerName, certName string) bool {
	name := generateName(providerName, certName)
	_, exists := q.inProcess[name]
	return exists
}

// remove removes a certificate from the in-process map and cancels its context.
// This is an internal method that should be called with the mutex held.
func (q *CertificateProcessingQueue) remove(providerName, certName string) {
	name := generateName(providerName, certName)
	if cp, exists := q.inProcess[name]; exists {
		cp.cancel()
		delete(q.inProcess, name)
	}
}

// generateName creates a unique identifier for a certificate by combining provider and certificate names.
// This is used as a key in the in-process map to track processing state.
func generateName(providerName, certName string) string {
	return fmt.Sprintf("%s-%s", providerName, certName)
}
