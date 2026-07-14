// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
)

// SkipIfNotK8s skips the current test if not running in a Kubernetes environment.
// Use this for tests that require Kubernetes-specific features (RBAC, etc.).
func SkipIfNotK8s(reason ...string) {
	if !IsK8sEnvironment() {
		msg := "Test requires Kubernetes environment"
		if len(reason) > 0 {
			msg = reason[0]
		}
		Skip(fmt.Sprintf("%s (current environment: %s)", msg, DetectEnvironment()))
	}
}

// SkipIfNotQuadlet skips the current test if not running in a Quadlet environment.
// Use this for tests that require Quadlet-specific features.
func SkipIfNotQuadlet(reason ...string) {
	if !IsQuadletEnvironment() {
		msg := "Test requires Quadlet environment"
		if len(reason) > 0 {
			msg = reason[0]
		}
		Skip(fmt.Sprintf("%s (current environment: %s)", msg, DetectEnvironment()))
	}
}

// SkipIfRBACNotSupported skips the current test if RBAC provider is not available.
// Both K8s and PAM environments support RBAC, so this only checks for nil provider.
func SkipIfRBACNotSupported(rbacProvider RBACProvider) {
	if rbacProvider == nil {
		Skip("RBAC provider not available")
	}
}

// SkipIfObservabilityNotConfigured skips the suite when infra cannot provide ServicePrometheus.
// Kind uses the auxiliary testcontainer instead and is not checked here.
func SkipIfObservabilityNotConfigured(ctx context.Context, providers *Providers) {
	switch providers.Infra.GetEnvironmentType() {
	case EnvironmentQuadlet, EnvironmentOCP:
		exists, err := providers.Infra.ServiceExists(ctx, ServicePrometheus)
		if err != nil {
			Fail(fmt.Sprintf("unable to check observability prometheus: %v", err))
		}
		if !exists {
			Skip(observabilityPrometheusSkipMessage(providers.Infra.GetEnvironmentType()))
		}
	}
}

func observabilityPrometheusSkipMessage(envType string) string {
	switch envType {
	case EnvironmentQuadlet:
		return "flightctl-observability stack not running (flightctl-prometheus is not active); " +
			"install flightctl-observability and run: systemctl enable --now flightctl-observability.target"
	case EnvironmentOCP:
		return "COO MonitoringStack not found (flightctl-monitoring-stack-prometheus service missing); " +
			"install flightctl-monitoring-stack"
	default:
		return fmt.Sprintf("observability prometheus not configured for %s deployment", envType)
	}
}

// SkipIfEnvironment skips the current test if running in the specified environment.
func SkipIfEnvironment(envType string, reason ...string) {
	currentEnv := DetectEnvironment()
	if currentEnv == envType {
		msg := fmt.Sprintf("Test not supported in %s environment", envType)
		if len(reason) > 0 {
			msg = reason[0]
		}
		Skip(msg)
	}
}

// RequireEnvironment fails the test if not running in the specified environment.
func RequireEnvironment(envType string) {
	currentEnv := DetectEnvironment()
	if currentEnv != envType {
		Fail(fmt.Sprintf("Test requires %s environment, but running in %s", envType, currentEnv))
	}
}
