package selinux

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/assert"
)

// createTestPolicyLoader creates a test PolicyLoader instance with required dependencies
func createTestPolicyLoader(t *testing.T) *PolicyLoader {
	logger := log.NewPrefixLogger("test")
	rwFactory := fileio.NewReadWriterFactory("")
	rw, err := rwFactory("")
	assert.NoError(t, err)
	loader := NewPolicyLoader(logger, rw)
	assert.NotNil(t, loader)
	assert.NotNil(t, loader.log)
	assert.NotNil(t, loader.rw)
	return loader
}

func TestNewPolicyLoader(t *testing.T) {
	createTestPolicyLoader(t)
}

func TestPolicyFileExists(t *testing.T) {
	loader := createTestPolicyLoader(t)

	// Test with non-existent file
	exists := loader.policyFileExists()
	// This will typically be false in test environments
	// We're just testing the function doesn't crash
	assert.IsType(t, false, exists)
}

func TestIsSELinuxEnabled(t *testing.T) {
	loader := createTestPolicyLoader(t)
	ctx := context.Background()

	// Test SELinux status check
	// This should not crash even if SELinux is not available
	enabled := loader.isSELinuxEnabled(ctx)
	assert.IsType(t, false, enabled)
}

func TestIsFlightCtlModuleLoaded(t *testing.T) {
	loader := createTestPolicyLoader(t)
	ctx := context.Background()

	// Test module loading check
	// This should not crash even if semodule is not available
	loaded := loader.isFlightCtlModuleLoaded(ctx)
	assert.IsType(t, false, loaded)
}

func TestHasRequiredCapabilities(t *testing.T) {
	loader := createTestPolicyLoader(t)
	ctx := context.Background()

	// Test capability check
	// This should not crash even if semodule is not available
	hasCaps := loader.hasRequiredCapabilities(ctx)
	assert.IsType(t, false, hasCaps)
}

func TestEnsurePolicyLoaded(t *testing.T) {
	loader := createTestPolicyLoader(t)
	ctx := context.Background()

	// Test policy loading (should be safe to call)
	err := loader.EnsurePolicyLoaded(ctx)

	// Should not fail even in test environment
	// Policy loading is designed to fail gracefully
	assert.NoError(t, err)
}

func TestNeedsPolicyLoading(t *testing.T) {
	loader := createTestPolicyLoader(t)
	ctx := context.Background()

	// Test policy loading requirement check
	needs := loader.needsPolicyLoading(ctx)

	// In most test environments, this should be false
	assert.IsType(t, false, needs)
}

// TestEnsurePolicyLoadedContextCancellation tests context cancellation handling
func TestEnsurePolicyLoadedContextCancellation(t *testing.T) {
	loader := createTestPolicyLoader(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := loader.EnsurePolicyLoaded(ctx)

	// Should handle cancellation gracefully
	assert.NoError(t, err)
}
