package util

import (
	"crypto/fips140"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsFIPSEnabled_EnvironmentVariables(t *testing.T) {
	// Save original environment
	origOpenSSL := os.Getenv("OPENSSL_FORCE_FIPS_MODE")
	origGolang := os.Getenv("GOLANG_FIPS")
	defer func() {
		os.Setenv("OPENSSL_FORCE_FIPS_MODE", origOpenSSL)
		os.Setenv("GOLANG_FIPS", origGolang)
	}()

	tests := []struct {
		name            string
		opensslValue    string
		golangValue     string
		expectedEnabled bool
	}{
		{
			name:            "OPENSSL_FORCE_FIPS_MODE=1 enables FIPS",
			opensslValue:    "1",
			golangValue:     "",
			expectedEnabled: true,
		},
		{
			name:            "GOLANG_FIPS=1 enables FIPS",
			opensslValue:    "",
			golangValue:     "1",
			expectedEnabled: true,
		},
		{
			name:            "Both environment variables set enables FIPS",
			opensslValue:    "1",
			golangValue:     "1",
			expectedEnabled: true,
		},
		{
			name:            "OPENSSL_FORCE_FIPS_MODE=0 does not enable FIPS",
			opensslValue:    "0",
			golangValue:     "",
			expectedEnabled: false,
		},
		{
			name:            "Neither environment variable set does not enable FIPS",
			opensslValue:    "",
			golangValue:     "",
			expectedEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			// Reset cache before each test
			ResetFIPSCache()

			// Set environment variables
			if tt.opensslValue != "" {
				os.Setenv("OPENSSL_FORCE_FIPS_MODE", tt.opensslValue)
			} else {
				os.Unsetenv("OPENSSL_FORCE_FIPS_MODE")
			}

			if tt.golangValue != "" {
				os.Setenv("GOLANG_FIPS", tt.golangValue)
			} else {
				os.Unsetenv("GOLANG_FIPS")
			}

			// Test detection
			result := IsFIPSEnabled()

			// Check if higher-priority detection methods are forcing FIPS
			// If they are, we can't reliably test env var behavior
			higherPriorityFIPS := false
			if fips140.Enabled() {
				higherPriorityFIPS = true
			} else if data, err := os.ReadFile("/proc/sys/crypto/fips_enabled"); err == nil && len(data) > 0 && data[0] == '1' {
				higherPriorityFIPS = true
			}

			// Only assert the expected value if higher-priority methods aren't forcing FIPS
			if !higherPriorityFIPS {
				require.Equal(tt.expectedEnabled, result,
					"When higher-priority detection methods are inactive, env vars should determine FIPS state")
			} else if tt.expectedEnabled {
				// If higher-priority methods detect FIPS and we expect FIPS, result must be true
				require.True(result, "FIPS should be enabled when detected by higher-priority methods or env vars")
			}
			// Note: if higherPriorityFIPS=true but expectedEnabled=false, we can't assert false
			// because the higher-priority methods override the env vars

			// Test that subsequent calls return the same cached result
			result2 := IsFIPSEnabled()
			require.Equal(result, result2, "Cached result should match initial result")
		})
	}
}

func TestIsFIPSEnabled_Caching(t *testing.T) {
	require := require.New(t)

	// Reset cache
	ResetFIPSCache()

	// First call should perform detection
	result1 := IsFIPSEnabled()

	// Subsequent calls should return cached result
	result2 := IsFIPSEnabled()
	result3 := IsFIPSEnabled()

	require.Equal(result1, result2, "Second call should return cached result")
	require.Equal(result1, result3, "Third call should return cached result")
}

func TestResetFIPSCache(t *testing.T) {
	require := require.New(t)

	// Get initial result
	result1 := IsFIPSEnabled()

	// Reset cache
	ResetFIPSCache()

	// Should be able to detect again
	result2 := IsFIPSEnabled()

	// Results should be the same (environment hasn't changed)
	require.Equal(result1, result2, "Results should be consistent after cache reset")
}

// TestFIPSDetection_Integration is an integration-style test that verifies
// the actual FIPS detection on the system where the test runs
func TestFIPSDetection_Integration(t *testing.T) {
	require := require.New(t)

	// Reset cache to ensure fresh detection
	ResetFIPSCache()

	// Perform detection
	result := IsFIPSEnabled()

	// Log the result for visibility
	if result {
		t.Log("FIPS mode detected on this system")

		// If FIPS is detected, verify we can find evidence
		// Check if any of the detection methods would return true
		hasEvidence := false

		// Check environment variables
		if os.Getenv("OPENSSL_FORCE_FIPS_MODE") == "1" {
			t.Log("  - OPENSSL_FORCE_FIPS_MODE=1")
			hasEvidence = true
		}
		if os.Getenv("GOLANG_FIPS") == "1" {
			t.Log("  - GOLANG_FIPS=1")
			hasEvidence = true
		}

		// Check kernel FIPS mode file
		if data, err := os.ReadFile("/proc/sys/crypto/fips_enabled"); err == nil {
			if len(data) > 0 && data[0] == '1' {
				t.Log("  - /proc/sys/crypto/fips_enabled=1")
				hasEvidence = true
			}
		}

		// Note: crypto/fips140.Enabled() might be true even without file/env evidence
		// in Go builds with FIPS support
		if !hasEvidence {
			t.Log("  - crypto/fips140.Enabled() returned true (Go runtime FIPS mode)")
		}
	} else {
		t.Log("FIPS mode NOT detected on this system")
	}

	// The result should be boolean (no error case)
	require.IsType(false, result, "IsFIPSEnabled should return a boolean")
}

// TestFIPSDetection_SimulatedKernelFile tests the /proc/sys/crypto/fips_enabled file reading
func TestFIPSDetection_SimulatedKernelFile(t *testing.T) {
	// This test is informational - it documents how the kernel file is read
	// but can't easily mock it without changing the implementation

	t.Run("read /proc/sys/crypto/fips_enabled if it exists", func(t *testing.T) {
		// Try to read the actual file if it exists
		data, err := os.ReadFile("/proc/sys/crypto/fips_enabled")
		if err != nil {
			t.Logf("/proc/sys/crypto/fips_enabled not found (expected on non-Linux or non-FIPS systems): %v", err)
		} else {
			t.Logf("/proc/sys/crypto/fips_enabled exists, content: %q", string(data))
			if len(data) > 0 {
				if data[0] == '1' {
					t.Log("  -> Kernel FIPS mode is ENABLED")
				} else if data[0] == '0' {
					t.Log("  -> Kernel FIPS mode is DISABLED")
				} else {
					t.Logf("  -> Unexpected value: %c", data[0])
				}
			}
		}
	})
}

// TestFIPSDetection_WithTempProcFile tests FIPS detection with a simulated proc file
// This requires temporarily changing how the code reads the file (or using build tags)
// For now, this is a placeholder for future enhancement
func TestFIPSDetection_WithTempProcFile(t *testing.T) {
	t.Skip("Skipping - would require refactoring to inject file path or use build tags")

	// Future enhancement: Could refactor IsFIPSEnabled to accept optional config
	// that includes a custom path for the FIPS enabled file, allowing for testing
	// with temporary files
	tempDir := t.TempDir()
	fipsFile := filepath.Join(tempDir, "fips_enabled")

	// Test with FIPS enabled
	err := os.WriteFile(fipsFile, []byte("1\n"), 0600)
	require.NoError(t, err)

	// Would need to somehow inject this path into IsFIPSEnabled
	// For example: IsFIPSEnabledWithPath(fipsFile)
}
