package certmanager

import (
	"context"
	"sync"
	"testing"
	"time"
)

func newFakeCert(name string) *certificate {
	return &certificate{
		Name: name,
	}
}

func TestCertificateProcessingQueue_BasicProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var processedCerts []string
	handler := func(ctx context.Context, providerName string, cert *certificate, attempt int) *time.Duration {
		processedCerts = append(processedCerts, cert.Name)
		return nil // No requeue
	}

	q := NewCertificateProcessingQueue(handler)
	q.Run(ctx)

	cert := newFakeCert("test-cert")
	providerName := "test"

	if err := q.Process(ctx, providerName, cert); err != nil {
		t.Fatalf("unexpected error on Process: %v", err)
	}

	// Wait a bit for the processing to complete
	time.Sleep(100 * time.Millisecond)

	if !contains(processedCerts, "test-cert") {
		t.Errorf("expected certificate to be processed")
	}

	if q.IsProcessing(providerName, "test-cert") {
		t.Errorf("expected certificate to be removed after processing")
	}

	if q.Len() != 0 {
		t.Errorf("expected Len() to be 0, got %d", q.Len())
	}
}

func TestCertificateProcessingQueue_ReplaceExisting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var attempts []string

	handler := func(ctx context.Context, providerName string, cert *certificate, attempt int) *time.Duration {
		mu.Lock()
		attempts = append(attempts, cert.Name)
		mu.Unlock()
		return nil
	}

	q := NewCertificateProcessingQueue(handler)
	q.Run(ctx)

	cert1 := newFakeCert("cert-A")
	cert2 := newFakeCert("cert-A") // same name, should replace

	providerName := "test"
	if err := q.Process(ctx, providerName, cert1); err != nil {
		t.Fatalf("unexpected error on first Process: %v", err)
	}

	// Immediately replace before it can process
	if err := q.Process(ctx, providerName, cert2); err != nil {
		t.Fatalf("unexpected error on replace Process: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !contains(attempts, "cert-A") {
		t.Errorf("expected cert-A to be processed at least once")
	}

	if q.IsProcessing(providerName, "cert-A") {
		t.Errorf("expected cert-A to be removed after processing")
	}
}

func TestCertificateProcessingQueue_Remove(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blocker := make(chan struct{})

	handler := func(ctx context.Context, providerName string, cert *certificate, attempt int) *time.Duration {
		// Block until remove is called
		<-blocker
		return nil
	}

	q := NewCertificateProcessingQueue(handler)
	q.Run(ctx)

	cert := newFakeCert("cert-B")

	providerName := "test"
	if err := q.Process(ctx, providerName, cert); err != nil {
		t.Fatalf("unexpected error on Process: %v", err)
	}

	if !q.IsProcessing(providerName, "cert-B") {
		t.Fatalf("expected cert-B to be marked as processing")
	}

	// Remove while blocked
	q.Remove(providerName, "cert-B")

	if q.IsProcessing(providerName, "cert-B") {
		t.Errorf("expected cert-B to be removed after Remove()")
	}

	close(blocker) // Unblock handler
}

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}
