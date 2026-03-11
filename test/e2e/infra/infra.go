// Package infra provides E2E test infrastructure in two separate layers:
//
//  1. Aux services (test/e2e/infra/aux): Shared testcontainers that run the same
//     for all deployments — registry, git server, prometheus. Use auxiliary.Get(ctx).
//
//  2. Deployment providers (this package + k8s/, quadlet/): Where Flight Control runs. Interfaces
//     (InfraProvider, ServiceLifecycleProvider, RBACProvider, SecretsProvider) and implementations
//     for K8s and Quadlet. Create once at harness creation via the harness's NewProvidersForEnvironment;
//     then use the interface methods without further environment checks.
package infra
