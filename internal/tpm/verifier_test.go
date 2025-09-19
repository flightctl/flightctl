package tpm

import (
	"fmt"
	"sync"
	"testing"
	"time"

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
			verifier := NewCAVerifier(tt.initialPaths, tt.pathProvider)
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

func TestVerifierReloadCooldown(t *testing.T) {
	callCount := 0
	pathProvider := func() ([]string, error) {
		callCount++
		return []string{"/some/path"}, nil // Return valid format but invalid path
	}

	// Create verifier with short cooldown for testing
	v := &verifier{
		pathProvider:    pathProvider,
		paths:           []string{},            // Start with empty paths to ensure reload is attempted
		refreshInterval: 10 * time.Millisecond, // Very short for testing
	}

	// First call should attempt reload (cert pool is nil)
	err1 := v.VerifyChain([]byte("dummy"))
	assert.Error(t, err1)
	firstCallCount := callCount
	assert.GreaterOrEqual(t, firstCallCount, 1) // Should have called at least once

	// Immediate second call may or may not attempt reload depending on timing and cooldown behavior
	err2 := v.VerifyChain([]byte("dummy"))
	assert.Error(t, err2) // Still error due to cert loading failure

	// Wait for cooldown to expire
	time.Sleep(15 * time.Millisecond)

	// Third call should attempt reload again
	err3 := v.VerifyChain([]byte("dummy"))
	assert.Error(t, err3)
	thirdCallCount := callCount

	// Verify that at least some reload attempts happened
	assert.Greater(t, thirdCallCount, firstCallCount) // Should have increased
}

func TestVerifierConcurrentAccess(t *testing.T) {
	callCount := 0
	var mu sync.Mutex
	pathProvider := func() ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		return []string{}, fmt.Errorf("always fail")
	}

	v := &verifier{
		pathProvider:    pathProvider,
		refreshInterval: 1 * time.Millisecond,
	}

	// Run multiple goroutines concurrently
	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			err := v.VerifyChain([]byte("dummy"))
			assert.Error(t, err)
		}()
	}

	wg.Wait()

	// Should have attempted at least one reload
	mu.Lock()
	assert.GreaterOrEqual(t, callCount, 1)
	mu.Unlock()
}

func TestVerifierSuccessfulReload(t *testing.T) {
	reloadCount := 0
	pathProvider := func() ([]string, error) {
		reloadCount++
		if reloadCount == 1 {
			return []string{}, fmt.Errorf("first call fails")
		}
		// Second call succeeds but returns empty paths (still fails verification)
		return []string{}, nil
	}

	v := &verifier{
		pathProvider:    pathProvider,
		refreshInterval: 1 * time.Millisecond,
	}

	// First verification should fail and trigger reload
	err1 := v.VerifyChain([]byte("dummy"))
	assert.Error(t, err1)
	assert.Equal(t, 1, reloadCount)

	// Wait for cooldown
	time.Sleep(5 * time.Millisecond)

	// Second verification should fail again but trigger another reload
	err2 := v.VerifyChain([]byte("dummy"))
	assert.Error(t, err2)
	assert.Equal(t, 2, reloadCount)
}

func TestVerifierReloadError(t *testing.T) {
	pathProvider := func() ([]string, error) {
		return nil, fmt.Errorf("path provider error")
	}

	v := &verifier{
		pathProvider:    pathProvider,
		refreshInterval: 1 * time.Millisecond,
	}

	// Should handle reload errors gracefully
	err := v.VerifyChain([]byte("dummy"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "getting CA paths: path provider error")
}

func TestVerifierNilPathProvider(t *testing.T) {
	v := &verifier{
		pathProvider:    nil,
		refreshInterval: 1 * time.Millisecond,
	}

	err := v.VerifyChain([]byte("dummy"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no path provider configured")
}

func TestVerifierEmptyPaths(t *testing.T) {
	pathProvider := func() ([]string, error) {
		return []string{}, nil // Return empty but valid slice
	}

	v := &verifier{
		pathProvider:    pathProvider,
		refreshInterval: 1 * time.Millisecond,
	}

	err := v.VerifyChain([]byte("dummy"))
	assert.Error(t, err)
	// Should still fail because LoadCAsFromPaths with empty slice returns error
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
				return nil, fmt.Errorf("provider error")
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
			v := &verifier{
				pathProvider:    tt.pathProvider,
				refreshInterval: 1 * time.Millisecond,
			}

			_, err := v.reloadCertPool(0)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
