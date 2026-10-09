// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

import (
	"context"
	"errors"
	"time"
)

// ErrChartDeploymentNotSupported is returned by ChartDeployer implementations
// for deployment types that do not install Helm charts (e.g. Quadlet). Callers
// skip instead of branching on the implementation type.
var ErrChartDeploymentNotSupported = errors.New("chart deployment is not supported for this deployment type")

// ChartRelease describes one Helm release a test suite installs alongside the
// Flight Control deployment under test (for example the catalog collector,
// which ships as its own chart rather than as part of the service chart).
type ChartRelease struct {
	// Name is the Helm release name.
	Name string
	// Namespace is the namespace the release is installed into. It is created
	// when missing.
	Namespace string
	// ChartPath is a path to the chart directory, relative to the repository
	// root or absolute.
	ChartPath string
	// ValuesFiles are rendered values files passed with --values, in order.
	ValuesFiles []string
	// SetValues are additional --set-string overrides.
	SetValues map[string]string
	// Timeout bounds the install, including --wait. Must be positive.
	Timeout time.Duration
}

// ChartDeployer installs auxiliary Helm charts into the cluster under test and
// supplies the cluster-side plumbing those charts need (namespaces, mounted
// Secrets and ConfigMaps, local image loading, readiness waits, diagnostics).
//
// Every method is a cluster operation, so the only implementation lives in
// test/e2e/infra/k8s. Suites consume this interface and never touch kubectl,
// helm, or the Kubernetes client directly.
type ChartDeployer interface {
	// EnsureNamespace creates the namespace when it does not exist.
	EnsureNamespace(ctx context.Context, namespace string) error

	// DeleteNamespace removes the namespace and waits for it to disappear.
	// A missing namespace is not an error.
	DeleteNamespace(ctx context.Context, namespace string, timeout time.Duration) error

	// ApplySecret creates or replaces an Opaque Secret from string data.
	ApplySecret(ctx context.Context, namespace, name string, data map[string]string) error

	// ApplyConfigMap creates or replaces a ConfigMap.
	ApplyConfigMap(ctx context.Context, namespace, name string, data map[string]string) error

	// LocalImageExists reports whether the image is present in the local
	// container runtime's image store.
	LocalImageExists(ctx context.Context, image string) (bool, error)

	// LoadLocalImage makes a locally built image available to cluster nodes.
	// For kind this loads the image into the node image store; other
	// environments return ErrChartDeploymentNotSupported.
	LoadLocalImage(ctx context.Context, image string) error

	// InstallChart installs or upgrades a Helm release and waits for it to
	// become ready within release.Timeout.
	InstallChart(ctx context.Context, release ChartRelease) error

	// UninstallChart removes a Helm release. A missing release is not an error.
	UninstallChart(ctx context.Context, name, namespace string, timeout time.Duration) error

	// ReleaseInstalled reports whether a Helm release exists in the namespace.
	ReleaseInstalled(ctx context.Context, name, namespace string) (bool, error)

	// WaitForWorkloadGone waits until no pod matching labelSelector remains in
	// the namespace. Used to assert that teardown actually removed the
	// workload rather than only the Helm release record.
	WaitForWorkloadGone(ctx context.Context, namespace, labelSelector string, timeout time.Duration) error

	// WorkloadDiagnostics returns a best-effort human-readable dump of pod
	// state, recent events, and container logs for the matching pods. It never
	// fails: diagnostics are emitted on a failing path where an error would
	// replace the information the caller needs.
	WorkloadDiagnostics(ctx context.Context, namespace, labelSelector string) string
}
