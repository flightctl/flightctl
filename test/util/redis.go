package util

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	. "github.com/onsi/ginkgo/v2"
)

// QueueState represents the state of Redis queues
type QueueState struct {
	Accessible       bool
	TaskQueueExists  bool
	HasConsumerGroup bool
	InFlightTasks    int
	FailedMessages   int
	QueueLength      int64 // Number of messages in the task-queue stream
}

// DetectDeploymentMode detects whether we're running in podman or kubernetes mode
func DetectDeploymentMode() string {
	// Check for podman systemd unit first
	exists, err := SystemdUnitExists("flightctl-kv.service")
	if err == nil && exists {
		return "podman"
	}

	// Check for kubernetes deployment
	cmd := exec.Command("kubectl", "get", "deployment", "flightctl-kv", "-n", "flightctl-internal", "--ignore-not-found")
	err = cmd.Run()
	if err == nil {
		// Check if deployment actually exists
		cmd = exec.Command("kubectl", "get", "deployment", "flightctl-kv", "-n", "flightctl-internal")
		if cmd.Run() == nil {
			return "kubernetes"
		}
	}

	// Default to podman if we can't detect
	return "podman"
}

// WaitForRedisReady waits for Redis to be ready after restart/start
func WaitForRedisReady(mode, namespace string, timeout time.Duration) bool {
	if mode == "kubernetes" {
		return WaitForRedisPodReady(namespace, timeout)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if IsRedisRunning() {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// VerifyRedisRecovery verifies that Redis and services have recovered after restart
func VerifyRedisRecovery(mode, namespace string) bool {
	// Wait for Redis to be ready
	if !WaitForRedisReady(mode, namespace, 2*time.Minute) {
		return false
	}
	// Verify connection
	if !CanConnectToRedis() {
		return false
	}
	// Verify services are healthy
	if !AreFlightCtlServicesHealthy() {
		return false
	}
	// Verify queue is accessible
	queueState := CheckQueueState()
	return queueState.Accessible && queueState.TaskQueueExists
}

// IsRedisRunning checks if Redis is running
func IsRedisRunning() bool {
	mode := DetectDeploymentMode()
	if mode == "podman" {
		return IsRedisRunningPodman()
	}
	return IsRedisRunningKubernetes("flightctl-internal")
}

// IsRedisRunningPodman checks if Redis is running in podman mode
func IsRedisRunningPodman() bool {
	cmd := exec.Command("sudo", "systemctl", "is-active", "flightctl-kv.service")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "active"
}

// IsRedisRunningKubernetes checks if Redis is running in kubernetes mode
func IsRedisRunningKubernetes(namespace string) bool {
	cmd := exec.Command("kubectl", "get", "pod", "-n", namespace, "-l", "flightctl.service=flightctl-kv", "-o", "jsonpath={.items[0].status.phase}")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "Running"
}

// RestartRedis restarts Redis based on deployment mode
func RestartRedis(mode, namespace string) error {
	GinkgoWriter.Printf("Restarting Redis in %s mode...\n", mode)

	if mode == "podman" {
		return RestartRedisPodman()
	}
	return RestartRedisKubernetes(namespace)
}

// RestartRedisPodman restarts Redis in podman mode
func RestartRedisPodman() error {
	// Restart the systemd service
	cmd := exec.Command("sudo", "systemctl", "restart", "flightctl-kv.service")
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to restart Redis podman service: %w", err)
	}
	GinkgoWriter.Printf("✓ Redis podman service restart command executed\n")
	return nil
}

// RestartRedisKubernetes restarts Redis in kubernetes mode
func RestartRedisKubernetes(namespace string) error {
	// Delete the pod to force restart
	cmd := exec.Command("kubectl", "delete", "pod", "-n", namespace, "-l", "flightctl.service=flightctl-kv", "--wait=false")
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to delete Redis pod: %w", err)
	}
	GinkgoWriter.Printf("✓ Redis kubernetes pod deletion command executed\n")

	// Wait a moment for the pod to start terminating
	time.Sleep(2 * time.Second)

	return nil
}

// StopRedis stops Redis based on deployment mode
func StopRedis(mode, namespace string) error {
	GinkgoWriter.Printf("Stopping Redis in %s mode...\n", mode)

	if mode == "podman" {
		return StopRedisPodman()
	}
	return StopRedisKubernetes(namespace)
}

// StopRedisPodman stops Redis in podman mode
func StopRedisPodman() error {
	cmd := exec.Command("sudo", "systemctl", "stop", "flightctl-kv.service")
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to stop Redis podman service: %w", err)
	}
	GinkgoWriter.Printf("✓ Redis podman service stop command executed\n")
	return nil
}

// StopRedisKubernetes stops Redis in kubernetes mode
func StopRedisKubernetes(namespace string) error {
	// Scale down the deployment to 0 replicas
	cmd := exec.Command("kubectl", "scale", "deployment", "flightctl-kv", "-n", namespace, "--replicas=0")
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to scale down Redis deployment: %w", err)
	}
	GinkgoWriter.Printf("✓ Redis kubernetes deployment scaled down\n")
	return nil
}

// StartRedis starts Redis based on deployment mode
func StartRedis(mode, namespace string) error {
	GinkgoWriter.Printf("Starting Redis in %s mode...\n", mode)

	if mode == "podman" {
		return StartRedisPodman()
	}
	return StartRedisKubernetes(namespace)
}

// StartRedisPodman starts Redis in podman mode
func StartRedisPodman() error {
	cmd := exec.Command("sudo", "systemctl", "start", "flightctl-kv.service")
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to start Redis podman service: %w", err)
	}
	GinkgoWriter.Printf("✓ Redis podman service start command executed\n")
	return nil
}

// StartRedisKubernetes starts Redis in kubernetes mode
func StartRedisKubernetes(namespace string) error {
	// Scale up the deployment to 1 replica
	cmd := exec.Command("kubectl", "scale", "deployment", "flightctl-kv", "-n", namespace, "--replicas=1")
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to scale up Redis deployment: %w", err)
	}
	GinkgoWriter.Printf("✓ Redis kubernetes deployment scaled up\n")
	return nil
}

// WaitForRedisPodReady waits for Redis pod to be ready in kubernetes
func WaitForRedisPodReady(namespace string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("kubectl", "get", "pod", "-n", namespace, "-l", "flightctl.service=flightctl-kv", "-o", "jsonpath={.items[0].status.phase}")
		output, err := cmd.Output()
		if err == nil && strings.TrimSpace(string(output)) == "Running" {
			// Also check ready condition
			cmd = exec.Command("kubectl", "get", "pod", "-n", namespace, "-l", "flightctl.service=flightctl-kv", "-o", "jsonpath={.items[0].status.conditions[?(@.type=='Ready')].status}")
			output, err = cmd.Output()
			if err == nil && strings.TrimSpace(string(output)) == "True" {
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// AreFlightCtlServicesHealthy checks if FlightCtl services are healthy
func AreFlightCtlServicesHealthy() bool {
	mode := DetectDeploymentMode()
	if mode == "podman" {
		return AreFlightCtlServicesHealthyPodman()
	}
	return AreFlightCtlServicesHealthyKubernetes("flightctl-internal")
}

// AreFlightCtlServicesHealthyPodman checks if FlightCtl services are healthy in podman mode
func AreFlightCtlServicesHealthyPodman() bool {
	// Check key services
	services := []string{
		"flightctl-api.service",
		"flightctl-worker.service",
	}

	for _, service := range services {
		cmd := exec.Command("sudo", "systemctl", "is-active", service)
		output, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(output)) != "active" {
			return false
		}
	}
	return true
}

// AreFlightCtlServicesHealthyKubernetes checks if FlightCtl services are healthy in kubernetes mode
func AreFlightCtlServicesHealthyKubernetes(namespace string) bool {
	// Check key deployments
	deployments := []string{
		"flightctl-api",
		"flightctl-worker",
	}

	for _, deployment := range deployments {
		cmd := exec.Command("kubectl", "get", "deployment", deployment, "-n", namespace, "-o", "jsonpath={.status.readyReplicas}")
		output, err := cmd.Output()
		if err != nil {
			return false
		}
		readyReplicas := strings.TrimSpace(string(output))
		if readyReplicas == "" || readyReplicas == "0" {
			return false
		}

		// Also check that desired replicas match ready replicas
		cmd = exec.Command("kubectl", "get", "deployment", deployment, "-n", namespace, "-o", "jsonpath={.spec.replicas}")
		output, err = cmd.Output()
		if err == nil {
			desiredReplicas := strings.TrimSpace(string(output))
			if desiredReplicas != "" && desiredReplicas != readyReplicas {
				return false
			}
		}
	}
	return true
}

// GetRedisClient creates a Redis client connection
func GetRedisClient() *redis.Client {
	// Try to get Redis connection details from environment or defaults
	// For e2e tests, Redis is typically on localhost:6379 with password from config
	addr := "localhost:6379"
	password := GetRedisPassword()

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})

	return client
}

// GetRedisPassword gets Redis password from environment or default
func GetRedisPassword() string {
	// Try to get from environment first
	if pwd := os.Getenv("REDIS_PASSWORD"); pwd != "" {
		return pwd
	}
	// Default password used in test environments
	return "adminpass"
}

// CanConnectToRedis checks if we can connect to Redis
func CanConnectToRedis() bool {
	client := GetRedisClient()
	if client == nil {
		return false
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Ping(ctx).Err()
	return err == nil
}

// CheckQueueState checks the state of Redis queues
func CheckQueueState() QueueState {
	state := QueueState{
		Accessible: false,
	}

	// Try to connect to Redis
	redisClient := GetRedisClient()
	if redisClient == nil {
		return state
	}
	defer redisClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test connection
	if err := redisClient.Ping(ctx).Err(); err != nil {
		return state
	}
	state.Accessible = true

	// Check if task-queue stream exists
	taskQueue := "task-queue"
	exists, err := redisClient.Exists(ctx, taskQueue).Result()
	if err == nil && exists > 0 {
		state.TaskQueueExists = true

		// Get the length of the stream (number of messages)
		length, err := redisClient.XLen(ctx, taskQueue).Result()
		if err == nil {
			state.QueueLength = length
		}
	}

	// Check consumer group
	groupName := taskQueue + "-group"
	groups, err := redisClient.XInfoGroups(ctx, taskQueue).Result()
	if err == nil {
		for _, group := range groups {
			if group.Name == groupName {
				state.HasConsumerGroup = true
				break
			}
		}
	}

	// Check in-flight tasks
	tasks, err := redisClient.ZRange(ctx, "in_flight_tasks", 0, -1).Result()
	if err == nil {
		state.InFlightTasks = len(tasks)
	}

	// Check failed messages (check for any failed_messages keys)
	keys, err := redisClient.Keys(ctx, "failed_messages:*").Result()
	if err == nil {
		totalFailed := 0
		for _, key := range keys {
			count, err := redisClient.ZCard(ctx, key).Result()
			if err == nil {
				totalFailed += int(count)
			}
		}
		state.FailedMessages = totalFailed
	}

	return state
}

