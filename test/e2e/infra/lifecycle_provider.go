// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

import "time"

// ServiceLifecycleProvider abstracts service lifecycle management for different environments.
// K8s implementations use kubectl scale/delete, Quadlet implementations use systemctl.
type ServiceLifecycleProvider interface {
	// IsRunning checks if a service is currently running.
	// For K8s: checks pod phase is Running
	// For Quadlet: checks systemctl is-active
	IsRunning(serviceName string) (bool, error)

	// Start starts a stopped service.
	// For K8s: scales deployment to 1 replica
	// For Quadlet: systemctl start
	Start(serviceName string) error

	// Stop stops a running service.
	// For K8s: scales deployment to 0 replicas
	// For Quadlet: systemctl stop
	Stop(serviceName string) error

	// Restart restarts a service.
	// For K8s: deletes the pod to force restart
	// For Quadlet: systemctl restart
	Restart(serviceName string) error

	// WaitForReady waits for a service to be ready and healthy.
	// For K8s: waits for pod Ready condition
	// For Quadlet: waits for systemctl is-active and optionally health checks
	WaitForReady(serviceName string, timeout time.Duration) error

	// AreServicesHealthy checks if all flightctl services are healthy.
	// For K8s: checks all deployments have ready replicas
	// For Quadlet: checks all systemd services are active
	AreServicesHealthy() (bool, error)
}
