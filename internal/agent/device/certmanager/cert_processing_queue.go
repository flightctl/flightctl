package certmanager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// processHandlerFunc defines the function signature used to process a certificate.
type processHandlerFunc func(ctx context.Context, providerName string, cert *certificate, attempt int) *time.Duration

// certificateProcess represents an active certificate processing task.
type certificateProcess struct {
	id       string
	ctx      context.Context
	cancel   context.CancelFunc
	provider string
	cert     *certificate
}

// CertificateProcessingQueue manages and processes certificate provisioning and storage tasks.
type CertificateProcessingQueue struct {
	queue     *RetryQueue[*certificateProcess]
	inProcess map[string]*certificateProcess
	handler   processHandlerFunc
	mu        sync.RWMutex
}

// NewCertificateProcessingQueue creates a new CertificateProcessingQueue with the given handler.
func NewCertificateProcessingQueue(handler processHandlerFunc) *CertificateProcessingQueue {
	q := &CertificateProcessingQueue{
		inProcess: make(map[string]*certificateProcess),
		handler:   handler,
	}

	q.queue = NewRetryQueue(q.process)
	return q
}

func (q *CertificateProcessingQueue) Run(ctx context.Context) {
	go q.queue.RunWorker(ctx)
}

// Len returns the number of certificates currently being processed.
func (q *CertificateProcessingQueue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return len(q.inProcess)
}

// IsProcessing returns true if the certificate with the given name is currently being processed.
func (q *CertificateProcessingQueue) IsProcessing(providerName, certName string) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.isProcessing(providerName, certName)
}

// Remove stops and removes a certificate from the in-process map if it exists.
func (q *CertificateProcessingQueue) Remove(providerName, certName string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.remove(providerName, certName)
}

// Process adds a certificate to the processing queue using the provided context,
// or cancels and replaces an existing one if already in process.
func (q *CertificateProcessingQueue) Process(ctx context.Context, providerName string, cert *certificate) error {
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
		id:       uuid.New().String(),
		ctx:      ctx,
		cancel:   cancel,
		provider: providerName,
		cert:     cert,
	}

	q.inProcess[generateName(providerName, cert.Name)] = c
	q.queue.Add(c)
	return nil
}

// process is the internal callback invoked by the retry queue to process a certificate.
func (q *CertificateProcessingQueue) process(ctx context.Context, cp *certificateProcess, attempt int) *time.Duration {
	if q.handler == nil {
		return nil
	}

	if cp.ctx.Err() != nil {
		return nil
	}

	nextDelay := q.handler(ctx, cp.provider, cp.cert, attempt)

	if cp.ctx.Err() != nil {
		nextDelay = nil
	}

	if nextDelay == nil {
		q.mu.Lock()
		defer q.mu.Unlock()

		cp.cancel() // Ensure context is fully canceled before removing
		if exists := q.get(cp.provider, cp.cert.Name); exists != nil && exists.id == cp.id {
			q.remove(cp.provider, cp.cert.Name)
		}
	}

	return nextDelay
}

func (q *CertificateProcessingQueue) get(providerName, certName string) *certificateProcess {
	name := generateName(providerName, certName)
	return q.inProcess[name]
}

func (q *CertificateProcessingQueue) isProcessing(providerName, certName string) bool {
	name := generateName(providerName, certName)
	_, exists := q.inProcess[name]
	return exists
}

func (q *CertificateProcessingQueue) remove(providerName, certName string) {
	name := generateName(providerName, certName)
	if cp, exists := q.inProcess[name]; exists {
		cp.cancel()
		delete(q.inProcess, name)
	}
}

func generateName(providerName, certName string) string {
	return fmt.Sprintf("%s-%s", providerName, certName)
}
