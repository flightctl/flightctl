package tpm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCAVerifier(t *testing.T) {
	tests := []struct {
		name         string
		initialPaths []string
		pathProvider CAPathProvider
		expectError  bool
	}{
		{
			name:         "empty initial paths",
			initialPaths: []string{},
			pathProvider: func() ([]string, error) { return []string{}, nil },
			expectError:  true, // should fail verification due to no certs
		},
		{
			name:         "invalid initial paths",
			initialPaths: []string{"/nonexistent/path"},
			pathProvider: func() ([]string, error) { return []string{}, nil },
			expectError:  true, // should fail verification due to no valid certs
		},
		{
			name:         "nil path provider",
			initialPaths: []string{},
			pathProvider: nil,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			logger := logrus.New()
			logger.SetLevel(logrus.ErrorLevel) // Reduce noise in tests

			verifier := NewCAVerifier(ctx, tt.initialPaths, tt.pathProvider, logger)
			require.NotNil(t, verifier)

			// Test with dummy CSR bytes
			err := verifier.VerifyChain([]byte("dummy"))
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVerifyChain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	// Test with empty cert pool
	verifier := NewCAVerifier(ctx, []string{}, nil, logger)
	err := verifier.VerifyChain([]byte("dummy"))
	assert.Error(t, err)
	assert.Equal(t, ErrManufacturerCACertsNotConfigured, err)
}

func TestPeriodicReload(t *testing.T) {
	callCount := 0
	pathProvider := func() ([]string, error) {
		callCount++
		return []string{"/some/path"}, nil // Return valid format but invalid path
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	v := &verifier{
		pathProvider:    pathProvider,
		log:             logger,
		paths:           []string{},
		refreshInterval: 50 * time.Millisecond,
		ctx:             ctx,
	}

	go v.periodicReload()

	// Wait for a few reload cycles
	time.Sleep(200 * time.Millisecond)

	// Verify that periodic reloading is happening
	assert.Greater(t, callCount, 2) // Should have called multiple times

	// VerifyChain should not trigger additional reloads
	initialCallCount := callCount
	err := v.VerifyChain([]byte("dummy"))
	assert.Error(t, err)                         // Should error due to cert loading failure
	assert.Equal(t, initialCallCount, callCount) // Should not have triggered additional reload
}

func TestReloadOnPathChanges(t *testing.T) {
	paths := []string{"/path1"}
	pathProvider := func() ([]string, error) {
		return paths, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	v := &verifier{
		pathProvider:    pathProvider,
		log:             logger,
		paths:           []string{"/initial/path"},
		refreshInterval: 50 * time.Millisecond,
		ctx:             ctx,
	}

	go v.periodicReload()

	// Wait for initial reload
	time.Sleep(100 * time.Millisecond)

	// Verify paths were updated
	v.mu.Lock()
	currentPaths := v.paths
	v.mu.Unlock()

	assert.Equal(t, []string{"/path1"}, currentPaths)

	// Change paths
	paths = []string{"/path1", "/path2"}

	// Wait for reload to pick up changes
	time.Sleep(100 * time.Millisecond)

	v.mu.Lock()
	updatedPaths := v.paths
	v.mu.Unlock()

	assert.Equal(t, []string{"/path1", "/path2"}, updatedPaths)
}

func TestReloadCertPool(t *testing.T) {
	tests := []struct {
		name         string
		pathProvider CAPathProvider
		expectError  bool
	}{
		{
			name:         "nil path provider",
			pathProvider: nil,
			expectError:  true,
		},
		{
			name: "path provider returns error",
			pathProvider: func() ([]string, error) {
				return nil, assert.AnError
			},
			expectError: true,
		},
		{
			name: "empty paths",
			pathProvider: func() ([]string, error) {
				return []string{}, nil
			},
			expectError: true,
		},
		{
			name: "invalid paths",
			pathProvider: func() ([]string, error) {
				return []string{"/invalid/path"}, nil
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			logger := logrus.New()
			logger.SetLevel(logrus.ErrorLevel)

			v := &verifier{
				pathProvider:    tt.pathProvider,
				log:             logger,
				paths:           []string{},
				refreshInterval: time.Minute,
				ctx:             ctx,
			}

			err := v.reloadCertPool()
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConcurrentAccess(t *testing.T) {
	var callCount int
	var mu sync.Mutex
	pathProvider := func() ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		return []string{"/some/path"}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	v := &verifier{
		pathProvider:    pathProvider,
		log:             logger,
		paths:           []string{},
		refreshInterval: 50 * time.Millisecond,
		ctx:             ctx,
	}

	go v.periodicReload()

	// Wait for initial reload
	time.Sleep(100 * time.Millisecond)

	// Run multiple VerifyChain calls concurrently
	const numGoroutines = 10
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer func() { done <- true }()
			for j := 0; j < 5; j++ {
				err := v.VerifyChain([]byte("dummy"))
				assert.Error(t, err) // Should error due to cert loading failure
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Should have attempted at least some reloads
	mu.Lock()
	count := callCount
	mu.Unlock()
	assert.Greater(t, count, 0)
}

func TestPathSorting(t *testing.T) {
	unsortedPaths := []string{"/path/z", "/path/a", "/path/m"}
	pathProvider := func() ([]string, error) {
		return unsortedPaths, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	v := &verifier{
		pathProvider:    pathProvider,
		log:             logger,
		paths:           []string{},
		refreshInterval: 50 * time.Millisecond,
		ctx:             ctx,
	}

	go v.periodicReload()

	// Wait for reload
	time.Sleep(100 * time.Millisecond)

	v.mu.Lock()
	sortedPaths := v.paths
	v.mu.Unlock()

	// Paths should be sorted
	expected := []string{"/path/a", "/path/m", "/path/z"}
	assert.Equal(t, expected, sortedPaths)
}
