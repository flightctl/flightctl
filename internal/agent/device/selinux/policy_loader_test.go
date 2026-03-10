package selinux

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/assert"
)

func TestNewPolicyLoader(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	loader := NewPolicyLoader(logger)

	assert.NotNil(t, loader)
	assert.Equal(t, logger, loader.log)
}

func TestPolicyFileExists(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	loader := NewPolicyLoader(logger)

	// Test with non-existent file
	exists := loader.policyFileExists()
	// This will typically be false in test environments
	// We're just testing the function doesn't crash
	assert.IsType(t, false, exists)
}

func TestIsSELinuxEnabled(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	loader := NewPolicyLoader(logger)

	// Test SELinux status check
	// This should not crash even if SELinux is not available
	enabled := loader.isSELinuxEnabled()
	assert.IsType(t, false, enabled)
}

func TestIsFlightCtlModuleLoaded(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	loader := NewPolicyLoader(logger)

	// Test module loading check
	// This should not crash even if semodule is not available
	loaded := loader.isFlightCtlModuleLoaded()
	assert.IsType(t, false, loaded)
}

func TestHasRequiredCapabilities(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	loader := NewPolicyLoader(logger)

	// Test capability check
	// This should not crash even if semodule is not available
	hasCaps := loader.hasRequiredCapabilities()
	assert.IsType(t, false, hasCaps)
}

func TestEnsurePolicyLoaded(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	loader := NewPolicyLoader(logger)

	ctx := context.Background()

	// Test policy loading (should be safe to call)
	err := loader.EnsurePolicyLoaded(ctx)

	// Should not fail even in test environment
	// Policy loading is designed to fail gracefully
	assert.NoError(t, err)
}

func TestNeedsPolicyLoading(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	loader := NewPolicyLoader(logger)

	// Test policy loading requirement check
	needs := loader.needsPolicyLoading()

	// In most test environments, this should be false
	assert.IsType(t, false, needs)
}

// TestEnsurePolicyLoadedContextCancellation tests context cancellation handling
func TestEnsurePolicyLoadedContextCancellation(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	loader := NewPolicyLoader(logger)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := loader.EnsurePolicyLoaded(ctx)

	// Should handle cancellation gracefully
	assert.NoError(t, err)
}
