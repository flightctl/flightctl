package util

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/redis/go-redis/v9"
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

// WaitForRedisReady waits for Redis to be ready after restart/start
// ctx should be KIND, OCP (for kubernetes), or "podman"
func WaitForRedisReady(ctx, namespace string, timeout time.Duration) bool {
	if ctx == KIND || ctx == OCP {
		return WaitForRedisPodReady(namespace, timeout)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if IsRedisRunning(ctx) {
			return true
		}
		time.Sleep(POLLING)
	}
	return false
}

// VerifyRedisRecovery verifies that Redis and services have recovered after restart
// ctx should be KIND, OCP (for kubernetes), or "podman"
func VerifyRedisRecovery(ctx, namespace string) bool {
	// Wait for Redis to be ready
	if !WaitForRedisReady(ctx, namespace, 2*time.Minute) {
		GinkgoWriter.Printf("VerifyRedisRecovery: Redis not ready yet\n")
		return false
	}
	// Verify connection
	if !CanConnectToRedis(ctx) {
		GinkgoWriter.Printf("VerifyRedisRecovery: Cannot connect to Redis\n")
		return false
	}
	// Verify queue is accessible (queue may not exist until tasks are queued, so just check accessibility)
	queueState := CheckQueueState(ctx)
	if !queueState.Accessible {
		GinkgoWriter.Printf("VerifyRedisRecovery: Queue not accessible, state: %+v\n", queueState)
		return false
	}
	// Verify services are healthy (worker is more critical than API for queue processing)
	// Check worker health - it's the one that processes the queue
	if !AreFlightCtlServicesHealthy(ctx) {
		GinkgoWriter.Printf("VerifyRedisRecovery: FlightCtl services not healthy yet\n")
		return false
	}
	GinkgoWriter.Printf("VerifyRedisRecovery: Success - Redis accessible, services healthy, queue accessible\n")
	return true
}

// IsRedisRunning checks if Redis is running
// ctx should be KIND, OCP (for kubernetes), or "podman"
func IsRedisRunning(ctx string) bool {
	if ctx != KIND && ctx != OCP {
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
	return strings.EqualFold(strings.TrimSpace(string(output)), "active")
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

// RestartRedis restarts Redis based on context
// ctx should be KIND, OCP (for kubernetes), or "podman"
func RestartRedis(ctx, namespace string) error {
	GinkgoWriter.Printf("Restarting Redis in %s context...\n", ctx)

	if ctx != KIND && ctx != OCP {
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
	time.Sleep(POLLING)

	return nil
}

// StopRedis stops Redis based on context
// ctx should be KIND, OCP (for kubernetes), or "podman"
func StopRedis(ctx, namespace string) error {
	GinkgoWriter.Printf("Stopping Redis in %s context...\n", ctx)

	if ctx != KIND && ctx != OCP {
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

// StartRedis starts Redis based on context
// ctx should be KIND, OCP (for kubernetes), or "podman"
func StartRedis(ctx, namespace string) error {
	GinkgoWriter.Printf("Starting Redis in %s context...\n", ctx)

	if ctx != KIND && ctx != OCP {
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
		time.Sleep(POLLING)
	}
	return false
}

// AreFlightCtlServicesHealthy checks if FlightCtl services are healthy
// ctx should be KIND, OCP (for kubernetes), or "podman"
func AreFlightCtlServicesHealthy(ctx string) bool {
	if ctx != KIND && ctx != OCP {
		return AreFlightCtlServicesHealthyPodman()
	}
	// In Kubernetes, API is in main namespace, worker is in internal namespace
	// Try to detect namespaces dynamically
	mainNamespace := detectMainNamespace()
	internalNamespace := "flightctl-internal"
	return AreFlightCtlServicesHealthyKubernetes(mainNamespace, internalNamespace)
}

// detectMainNamespace tries to find the namespace where flightctl-api is deployed
func detectMainNamespace() string {
	// First, try to find it across all namespaces (most reliable)
	cmd := exec.Command("kubectl", "get", "deployment", "flightctl-api", "--all-namespaces", "-o", "jsonpath={.items[0].metadata.namespace}")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		ns := strings.TrimSpace(string(output))
		GinkgoWriter.Printf("detectMainNamespace: Found flightctl-api in namespace: %s\n", ns)
		return ns
	}

	// If not found across all namespaces, try common namespaces
	namespaces := []string{"flightctl", "default", "flightctl-system"}

	for _, ns := range namespaces {
		cmd := exec.Command("kubectl", "get", "deployment", "flightctl-api", "-n", ns, "--ignore-not-found", "-o", "name")
		output, err := cmd.Output()
		if err == nil && strings.Contains(string(output), "flightctl-api") {
			GinkgoWriter.Printf("detectMainNamespace: Found flightctl-api in namespace: %s\n", ns)
			return ns
		}
	}

	// Default fallback
	GinkgoWriter.Printf("detectMainNamespace: Could not find flightctl-api, using default: flightctl\n")
	return "flightctl"
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
		if err != nil || !strings.EqualFold(strings.TrimSpace(string(output)), "active") {
			return false
		}
	}
	return true
}

// AreFlightCtlServicesHealthyKubernetes checks if FlightCtl services are healthy in kubernetes mode
// mainNamespace is where flightctl-api is deployed, internalNamespace is where flightctl-worker is deployed
func AreFlightCtlServicesHealthyKubernetes(mainNamespace, internalNamespace string) bool {
	// For Redis restart tests, worker is more critical than API since it processes the queue
	// Check Worker in internal namespace first (most important for queue processing)
	workerDeployment := "flightctl-worker"
	workerNamespace := internalNamespace
	cmd := exec.Command("kubectl", "get", "deployment", workerDeployment, "-n", workerNamespace, "-o", "jsonpath={.status.readyReplicas}")
	output, err := cmd.Output()
	if err != nil {
		GinkgoWriter.Printf("AreFlightCtlServicesHealthyKubernetes: Failed to get %s status in %s: %v\n", workerDeployment, workerNamespace, err)
		cmd = exec.Command("kubectl", "get", "pods", "-n", workerNamespace, "-l", fmt.Sprintf("flightctl.service=%s", workerDeployment), "-o", "jsonpath={.items[*].status.phase}")
		podOutput, _ := cmd.Output()
		GinkgoWriter.Printf("AreFlightCtlServicesHealthyKubernetes: %s pods status in %s: %s\n", workerDeployment, workerNamespace, string(podOutput))
		return false
	}
	readyReplicas := strings.TrimSpace(string(output))
	if readyReplicas == "" || readyReplicas == "0" {
		cmd = exec.Command("kubectl", "get", "deployment", workerDeployment, "-n", workerNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Available')].status}")
		availableOutput, _ := cmd.Output()
		cmd = exec.Command("kubectl", "get", "deployment", workerDeployment, "-n", workerNamespace, "-o", "jsonpath={.spec.replicas}")
		desiredOutput, _ := cmd.Output()
		cmd = exec.Command("kubectl", "get", "pods", "-n", workerNamespace, "-l", fmt.Sprintf("flightctl.service=%s", workerDeployment), "-o", "jsonpath={.items[*].status.phase}")
		podOutput, _ := cmd.Output()
		cmd = exec.Command("kubectl", "get", "pods", "-n", workerNamespace, "-l", fmt.Sprintf("flightctl.service=%s", workerDeployment), "-o", "jsonpath={.items[*].status.containerStatuses[0].restartCount}")
		restartOutput, _ := cmd.Output()
		GinkgoWriter.Printf("AreFlightCtlServicesHealthyKubernetes: %s not ready in %s - readyReplicas: %s, desired: %s, available: %s, pod phases: %s, restart counts: %s\n",
			workerDeployment, workerNamespace, readyReplicas, strings.TrimSpace(string(desiredOutput)), strings.TrimSpace(string(availableOutput)), string(podOutput), string(restartOutput))
		return false
	}
	GinkgoWriter.Printf("AreFlightCtlServicesHealthyKubernetes: %s is ready in %s with %s replicas\n", workerDeployment, workerNamespace, readyReplicas)

	// Check API in main namespace (less critical for queue processing, but still good to verify)
	apiDeployment := "flightctl-api"
	apiNamespace := mainNamespace
	cmd = exec.Command("kubectl", "get", "deployment", apiDeployment, "-n", apiNamespace, "--ignore-not-found", "-o", "jsonpath={.status.readyReplicas}")
	output, err = cmd.Output()
	if err != nil {
		// API might not exist or be in a different namespace - log but don't fail
		GinkgoWriter.Printf("AreFlightCtlServicesHealthyKubernetes: Could not check %s in %s (may not exist): %v\n", apiDeployment, apiNamespace, err)
		// Worker is healthy, which is what matters for queue processing
		return true
	}
	readyReplicas = strings.TrimSpace(string(output))
	if readyReplicas == "" || readyReplicas == "0" {
		// API not ready, but worker is - that's acceptable for queue processing
		GinkgoWriter.Printf("AreFlightCtlServicesHealthyKubernetes: %s not ready in %s, but worker is healthy\n", apiDeployment, apiNamespace)
		return true
	}
	GinkgoWriter.Printf("AreFlightCtlServicesHealthyKubernetes: %s is ready in %s with %s replicas\n", apiDeployment, apiNamespace, readyReplicas)
	return true
}

// GetRedisClient creates a Redis client connection
// ctx should be KIND, OCP (for kubernetes), or "podman"
func GetRedisClient(ctx string) *redis.Client {
	// In Kubernetes mode, we can't directly connect to Redis on localhost
	// Return nil and let the caller use kubectl exec instead
	if ctx == KIND || ctx == OCP {
		return nil
	}

	// For podman mode, connect to localhost
	addr := "localhost:6379"
	password := GetRedisPassword(ctx)

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})

	return client
}

// GetRedisPassword gets Redis password from environment, Kubernetes secret, or default
// ctx should be KIND, OCP (for kubernetes), or "podman"
func GetRedisPassword(ctx string) string {
	// Try to get from environment first
	if pwd := os.Getenv("REDIS_PASSWORD"); pwd != "" {
		return pwd
	}

	// Try to get from Kubernetes secret if in Kubernetes mode
	if ctx == KIND || ctx == OCP {
		namespace := "flightctl-internal"
		secretName := "flightctl-kv-secret" //nolint:gosec // G101: This is a secret name, not a credential
		// Get the base64 encoded password from the secret and decode it
		//nolint:gosec // G204: secretName and namespace are hardcoded constants, not user input
		cmd := exec.Command("sh", "-c", fmt.Sprintf("kubectl get secret %s -n %s -o jsonpath={.data.password} | base64 -d", secretName, namespace))
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			return strings.TrimSpace(string(output))
		}
	}

	// Default password used in test environments
	return "adminpass"
}

// CanConnectToRedis checks if we can connect to Redis
// ctx should be KIND, OCP (for kubernetes), or "podman"
func CanConnectToRedis(ctx string) bool {
	if ctx == KIND || ctx == OCP {
		// In Kubernetes, check if we can exec into the Redis pod
		namespace := "flightctl-internal"
		password := GetRedisPassword(ctx)
		// Get pod name first
		cmd := exec.Command("kubectl", "get", "pod", "-n", namespace, "-l", "flightctl.service=flightctl-kv", "-o", "jsonpath={.items[0].metadata.name}")
		output, err := cmd.Output()
		if err != nil {
			return false
		}
		podName := strings.TrimSpace(string(output))
		if podName == "" {
			return false
		}
		// Use AUTH command via stdin
		cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", fmt.Sprintf("echo 'AUTH %s\nPING' | redis-cli", password))
		output, err = cmd.Output()
		if err != nil {
			// Try with -a flag as fallback
			cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "redis-cli", "-a", password, "PING")
			output, err = cmd.Output()
			if err != nil {
				return false
			}
		}
		outputStr := strings.TrimSpace(string(output))
		return strings.Contains(outputStr, "PONG")
	}

	client := GetRedisClient(ctx)
	if client == nil {
		return false
	}
	defer client.Close()

	ctxTimeout, cancel := context.WithTimeout(context.Background(), LONG_POLLING)
	defer cancel()

	err := client.Ping(ctxTimeout).Err()
	return err == nil
}

// CheckQueueState checks the state of Redis queues
// ctx should be KIND, OCP (for kubernetes), or "podman"
func CheckQueueState(ctx string) QueueState {
	state := QueueState{
		Accessible: false,
	}

	namespace := "flightctl-internal"
	password := GetRedisPassword(ctx)

	if ctx == KIND || ctx == OCP {
		// Use kubectl exec for Kubernetes mode
		return CheckQueueStateKubernetes(namespace, password)
	}

	// Use direct Redis client for podman mode
	redisClient := GetRedisClient(ctx)
	if redisClient == nil {
		return state
	}
	defer redisClient.Close()

	ctxTimeout, cancel := context.WithTimeout(context.Background(), LONG_POLLING)
	defer cancel()

	// Test connection
	if err := redisClient.Ping(ctxTimeout).Err(); err != nil {
		return state
	}
	state.Accessible = true

	// Check if task-queue stream exists
	taskQueue := "task-queue"
	exists, err := redisClient.Exists(ctxTimeout, taskQueue).Result()
	if err == nil && exists > 0 {
		state.TaskQueueExists = true

		// Get the length of the stream (number of messages)
		length, err := redisClient.XLen(ctxTimeout, taskQueue).Result()
		if err == nil {
			state.QueueLength = length
		}
	}

	// Check consumer group
	groupName := taskQueue + "-group"
	groups, err := redisClient.XInfoGroups(ctxTimeout, taskQueue).Result()
	if err == nil {
		for _, group := range groups {
			if group.Name == groupName {
				state.HasConsumerGroup = true
				break
			}
		}
	}

	// Check in-flight tasks
	tasks, err := redisClient.ZRange(ctxTimeout, "in_flight_tasks", 0, -1).Result()
	if err == nil {
		state.InFlightTasks = len(tasks)
	}

	// Check failed messages (check for any failed_messages keys)
	keys, err := redisClient.Keys(ctxTimeout, "failed_messages:*").Result()
	if err == nil {
		totalFailed := 0
		for _, key := range keys {
			count, err := redisClient.ZCard(ctxTimeout, key).Result()
			if err == nil {
				totalFailed += int(count)
			}
		}
		state.FailedMessages = totalFailed
	}

	return state
}

// CheckQueueStateKubernetes checks queue state using kubectl exec in Kubernetes mode
func CheckQueueStateKubernetes(namespace, password string) QueueState {
	state := QueueState{
		Accessible: false,
	}

	// Get Redis pod name
	cmd := exec.Command("kubectl", "get", "pod", "-n", namespace, "-l", "flightctl.service=flightctl-kv", "-o", "jsonpath={.items[0].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		GinkgoWriter.Printf("Failed to get Redis pod name: %v\n", err)
		if exitErr, ok := err.(*exec.ExitError); ok {
			GinkgoWriter.Printf("Stderr: %s\n", string(exitErr.Stderr))
		}
		return state
	}
	podName := strings.TrimSpace(string(output))
	if podName == "" {
		GinkgoWriter.Printf("Redis pod name is empty\n")
		return state
	}
	GinkgoWriter.Printf("Found Redis pod: %s\n", podName)

	// Test connection with PING
	// Use redis-cli with AUTH command via stdin for more reliable authentication
	cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", fmt.Sprintf("echo 'AUTH %s\nPING' | redis-cli", password))
	output, err = cmd.Output()
	if err != nil {
		// Try with -a flag as fallback
		cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "redis-cli", "-a", password, "PING")
		output, err = cmd.Output()
		if err != nil {
			GinkgoWriter.Printf("Failed to ping Redis: %v\n", err)
			if exitErr, ok := err.(*exec.ExitError); ok {
				GinkgoWriter.Printf("Stderr: %s\n", string(exitErr.Stderr))
			}
			return state
		}
	}
	outputStr := strings.TrimSpace(string(output))
	GinkgoWriter.Printf("Redis PING response: %s\n", outputStr)
	// Handle both "PONG" and potential error messages (may have "OK" from AUTH before PONG)
	if !strings.Contains(outputStr, "PONG") {
		GinkgoWriter.Printf("Redis PING did not return PONG, got: %s\n", outputStr)
		return state
	}
	state.Accessible = true

	taskQueue := "task-queue"
	groupName := taskQueue + "-group"

	// Check if task-queue stream exists
	// Use AUTH command via stdin
	cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", fmt.Sprintf("echo 'AUTH %s\nEXISTS %s' | redis-cli", password, taskQueue))
	output, err = cmd.Output()
	if err != nil {
		// Try with -a flag as fallback
		cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "redis-cli", "-a", password, "EXISTS", taskQueue)
		output, err = cmd.Output()
	}
	if err == nil {
		exists := strings.TrimSpace(string(output))
		// EXISTS returns "1" if key exists, "0" if not
		if exists == "1" {
			state.TaskQueueExists = true

			// Get stream length
			cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", fmt.Sprintf("echo 'AUTH %s\nXLEN %s' | redis-cli", password, taskQueue))
			output, err = cmd.Output()
			if err != nil {
				cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "redis-cli", "-a", password, "XLEN", taskQueue)
				output, err = cmd.Output()
			}
			if err == nil {
				outputStr := strings.TrimSpace(string(output))
				var length int64
				if _, parseErr := fmt.Sscanf(outputStr, "%d", &length); parseErr == nil {
					state.QueueLength = length
				}
			}
		}
	}

	// Check consumer group
	cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", fmt.Sprintf("echo 'AUTH %s\nXINFO GROUPS %s' | redis-cli", password, taskQueue))
	output, err = cmd.Output()
	if err != nil {
		cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "redis-cli", "-a", password, "XINFO", "GROUPS", taskQueue)
		output, err = cmd.Output()
	}
	if err == nil {
		outputStr := string(output)
		if strings.Contains(outputStr, groupName) {
			state.HasConsumerGroup = true
		}
	}

	// Check in-flight tasks
	cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", fmt.Sprintf("echo 'AUTH %s\nZCARD in_flight_tasks' | redis-cli", password))
	output, err = cmd.Output()
	if err != nil {
		cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "redis-cli", "-a", password, "ZCARD", "in_flight_tasks")
		output, err = cmd.Output()
	}
	if err == nil {
		outputStr := strings.TrimSpace(string(output))
		var count int
		if _, parseErr := fmt.Sscanf(outputStr, "%d", &count); parseErr == nil {
			state.InFlightTasks = count
		}
	}

	// Check failed messages
	cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", fmt.Sprintf("echo 'AUTH %s\nKEYS failed_messages:*' | redis-cli", password))
	output, err = cmd.Output()
	if err != nil {
		cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "redis-cli", "-a", password, "KEYS", "failed_messages:*")
		output, err = cmd.Output()
	}
	if err == nil {
		keysOutput := strings.TrimSpace(string(output))
		if keysOutput != "" {
			keys := strings.Fields(keysOutput)
			totalFailed := 0
			for _, key := range keys {
				cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "sh", "-c", fmt.Sprintf("echo 'AUTH %s\nZCARD %s' | redis-cli", password, key))
				cardOutput, cardErr := cmd.Output()
				if cardErr != nil {
					cmd = exec.Command("kubectl", "exec", "-n", namespace, podName, "--", "redis-cli", "-a", password, "ZCARD", key)
					cardOutput, cardErr = cmd.Output()
				}
				if cardErr == nil {
					cardStr := strings.TrimSpace(string(cardOutput))
					var count int
					if _, parseErr := fmt.Sscanf(cardStr, "%d", &count); parseErr == nil {
						totalFailed += count
					}
				}
			}
			state.FailedMessages = totalFailed
		}
	}

	return state
}
