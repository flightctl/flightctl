// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

import (
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
