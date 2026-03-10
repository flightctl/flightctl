package selinux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// SELinux policy file location
	flightctlPolicyPath = "/usr/share/selinux/packages/targeted/flightctl_agent.pp.bz2"

	// Module name
	flightctlModuleName = "flightctl_agent"

	// Timeout for SELinux operations
	selinuxTimeout = 30 * time.Second
)

// PolicyLoader handles SELinux policy loading for bootc environments
type PolicyLoader struct {
	log *log.PrefixLogger
}

// NewPolicyLoader creates a new SELinux policy loader
func NewPolicyLoader(log *log.PrefixLogger) *PolicyLoader {
	return &PolicyLoader{
		log: log,
	}
}

// EnsurePolicyLoaded ensures FlightCtl SELinux policies are loaded
// This addresses bootc environments (like EL10) where RPM post-install scripts
// don't execute properly, leaving policies unloaded despite package installation
func (p *PolicyLoader) EnsurePolicyLoaded(ctx context.Context) error {
	// Only attempt policy loading if needed
	if !p.needsPolicyLoading() {
		p.log.Debug("SELinux policy loading not needed")
		return nil
	}

	p.log.Info("SELinux policy loading required for bootc environment")

	// Check if we have the required capabilities
	if !p.hasRequiredCapabilities() {
		return fmt.Errorf("insufficient capabilities to load SELinux policies (requires CAP_MAC_ADMIN)")
	}

	// Load the policy
	if err := p.loadPolicy(ctx); err != nil {
		// Log error but don't fail agent startup - allow graceful degradation
		p.log.Errorf("Failed to load SELinux policy: %v", err)
		p.log.Warn("Agent will continue without custom SELinux policies")
		return nil // Don't fail agent startup
	}

	p.log.Info("Successfully loaded FlightCtl SELinux policies")
	return nil
}

// needsPolicyLoading determines if SELinux policy loading is required
func (p *PolicyLoader) needsPolicyLoading() bool {
	// Check if SELinux is enabled
	if !p.isSELinuxEnabled() {
		return false
	}

	// Check if FlightCtl module is already loaded
	if p.isFlightCtlModuleLoaded() {
		return false
	}

	// Check if policy file exists
	if !p.policyFileExists() {
		p.log.Debugf("FlightCtl SELinux policy file not found: %s", flightctlPolicyPath)
		return false
	}

	return true
}

// isSELinuxEnabled checks if SELinux is enabled and enforcing
func (p *PolicyLoader) isSELinuxEnabled() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "getenforce")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	status := strings.TrimSpace(string(output))
	return status == "Enforcing" || status == "Permissive"
}

// isFlightCtlModuleLoaded checks if FlightCtl SELinux module is loaded
func (p *PolicyLoader) isFlightCtlModuleLoaded() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "semodule", "-l")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	modules := string(output)
	return strings.Contains(modules, flightctlModuleName)
}

// policyFileExists checks if the FlightCtl policy file exists
func (p *PolicyLoader) policyFileExists() bool {
	_, err := os.Stat(flightctlPolicyPath)
	return err == nil
}

// hasRequiredCapabilities checks if we have CAP_MAC_ADMIN capability
func (p *PolicyLoader) hasRequiredCapabilities() bool {
	// Simple check: try to run semodule with a dry-run operation
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test with a harmless semodule operation
	cmd := exec.CommandContext(ctx, "semodule", "-l")
	err := cmd.Run()
	return err == nil
}

// loadPolicy loads the FlightCtl SELinux policy
func (p *PolicyLoader) loadPolicy(ctx context.Context) error {
	p.log.Infof("Loading FlightCtl SELinux policy from %s", flightctlPolicyPath)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, selinuxTimeout)
	defer cancel()

	// Load the policy module
	cmd := exec.CommandContext(ctx, "semodule", "-s", "targeted", "-i", flightctlPolicyPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to load SELinux policy: %w (output: %s)", err, string(output))
	}

	p.log.Debugf("semodule output: %s", string(output))

	// Verify the module was loaded
	if !p.isFlightCtlModuleLoaded() {
		return fmt.Errorf("policy loading appeared to succeed but module not found in loaded modules")
	}

	// Restore file contexts to apply the new policies
	if err := p.restoreFileContexts(ctx); err != nil {
		// Don't fail on restorecon errors - the policy is loaded
		p.log.Warnf("Failed to restore file contexts: %v", err)
	}

	return nil
}

// restoreFileContexts restores file contexts for FlightCtl files
func (p *PolicyLoader) restoreFileContexts(ctx context.Context) error {
	p.log.Debug("Restoring file contexts for FlightCtl files")

	// Restore contexts for key FlightCtl files
	filesToRestore := []string{
		"/usr/bin/flightctl-agent",
		"/var/lib/flightctl",
		"/var/log/flightctl",
		"/usr/lib/flightctl/custom-info.d",
	}

	for _, filePath := range filesToRestore {
		if _, err := os.Stat(filePath); err != nil {
			// Skip files that don't exist
			continue
		}

		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cmd := exec.CommandContext(ctx, "restorecon", "-R", filePath)
		output, err := cmd.CombinedOutput()
		cancel()

		if err != nil {
			p.log.Debugf("Failed to restore context for %s: %v (output: %s)", filePath, err, string(output))
		} else {
			p.log.Debugf("Restored context for %s", filePath)
		}
	}

	return nil
}
