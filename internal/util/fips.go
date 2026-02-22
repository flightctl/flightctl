package util

import (
	"crypto/fips140"
	"os"
	"sync"
)

var (
	// Cache the FIPS detection result to avoid repeated filesystem/runtime checks
	fipsEnabled    bool
	fipsChecked    bool
	fipsCheckMutex sync.RWMutex
)

// IsFIPSEnabled detects if the system is running in FIPS mode using multiple detection methods.
// The result is cached after the first check for performance.
//
// Detection methods (in order of precedence):
//  1. Go 1.24's crypto/fips140.Enabled() - Runtime FIPS mode detection
//  2. /proc/sys/crypto/fips_enabled - Linux kernel FIPS mode indicator
//  3. OPENSSL_FORCE_FIPS_MODE environment variable - OpenSSL forced FIPS mode
//  4. GOLANG_FIPS environment variable - Go FIPS mode indicator
func IsFIPSEnabled() bool {
	// Fast path: return cached result if already checked
	fipsCheckMutex.RLock()
	if fipsChecked {
		result := fipsEnabled
		fipsCheckMutex.RUnlock()
		return result
	}
	fipsCheckMutex.RUnlock()

	// Slow path: perform detection and cache result
	fipsCheckMutex.Lock()
	defer fipsCheckMutex.Unlock()

	// Double-check in case another goroutine already did the check
	if fipsChecked {
		return fipsEnabled
	}

	// Method 1: Use Go 1.24's native crypto/fips140 package (primary detection)
	if fips140.Enabled() {
		fipsEnabled = true
		fipsChecked = true
		return true
	}

	// Method 2: Check /proc/sys/crypto/fips_enabled on Linux
	// This file contains "1" if FIPS mode is enabled at the kernel level
	if data, err := os.ReadFile("/proc/sys/crypto/fips_enabled"); err == nil {
		if len(data) > 0 && data[0] == '1' {
			fipsEnabled = true
			fipsChecked = true
			return true
		}
	}

	// Method 3: Check OPENSSL_FORCE_FIPS_MODE environment variable
	// This is used to force OpenSSL into FIPS mode and is useful for testing
	if os.Getenv("OPENSSL_FORCE_FIPS_MODE") == "1" {
		fipsEnabled = true
		fipsChecked = true
		return true
	}

	// Method 4: Check GOLANG_FIPS environment variable
	// Some Go FIPS builds use this environment variable
	if os.Getenv("GOLANG_FIPS") == "1" {
		fipsEnabled = true
		fipsChecked = true
		return true
	}

	// No FIPS mode detected
	fipsEnabled = false
	fipsChecked = true
	return false
}

// ResetFIPSCache clears the cached FIPS detection result.
// This is primarily useful for testing and should not be called in production code.
func ResetFIPSCache() {
	fipsCheckMutex.Lock()
	defer fipsCheckMutex.Unlock()
	fipsChecked = false
	fipsEnabled = false
}
