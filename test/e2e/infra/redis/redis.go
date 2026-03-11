// Package redis provides Redis and queue helpers for e2e tests using infra providers.
// It uses the default providers (setup.GetDefaultProviders()) so tests do not pass context/namespace.
package redis

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

const (
	redisPolling     = 250 * time.Millisecond
	redisLongPolling = 2 * time.Second
	// K8s: must match Helm default (deploy/helm/.../flightctl-kv-secret.yaml, flightctl-kv-deployment.yaml).
	kvSecretName = "flightctl-kv-secret" //nolint:gosec // G101: secret name (K8s resource name), not a credential value
	kvSecretKey  = "password"
)

// QueueState represents the state of Redis queues.
type QueueState struct {
	Accessible       bool
	TaskQueueExists  bool
	HasConsumerGroup bool
	InFlightTasks    int
	FailedMessages   int
	QueueLength      int64
}

func getProviders() *infra.Providers {
	return setup.GetDefaultProviders()
}

// WaitForRedisReady waits for Redis to be ready after restart/start.
func WaitForRedisReady(timeout time.Duration) bool {
	p := getProviders()
	if p == nil || p.Lifecycle == nil {
		return false
	}
	err := p.Lifecycle.WaitForReady(infra.ServiceRedis, timeout)
	return err == nil
}

// VerifyRedisRecovery verifies that Redis and services have recovered after restart.
func VerifyRedisRecovery(timeout time.Duration) bool {
	p := getProviders()
	if p == nil {
		return false
	}
	logrus.Info("VerifyRedisRecovery: Waiting for Redis to be ready...")
	if !WaitForRedisReady(timeout) {
		logrus.Info("VerifyRedisRecovery: Redis not ready after timeout")
		return false
	}
	logrus.Info("VerifyRedisRecovery: Redis is ready, verifying connection...")
	if err := TryConnectToRedis(timeout); err != nil {
		logrus.Infof("VerifyRedisRecovery: Cannot connect to Redis: %v", err)
		return false
	}
	logrus.Info("VerifyRedisRecovery: Redis connection verified, checking queue state...")
	state := CheckQueueState()
	if !state.Accessible {
		logrus.Infof("VerifyRedisRecovery: Queue not accessible, state: %+v", state)
		return false
	}
	logrus.Info("VerifyRedisRecovery: Queue is accessible, checking service health...")
	healthy, err := p.Lifecycle.AreServicesHealthy()
	if err != nil || !healthy {
		logrus.Info("VerifyRedisRecovery: FlightCtl services not healthy yet")
		return false
	}
	logrus.Info("VerifyRedisRecovery: Success - Redis accessible, services healthy, queue accessible")
	return true
}

// IsRedisRunning returns whether Redis is running.
func IsRedisRunning() bool {
	p := getProviders()
	if p == nil || p.Lifecycle == nil {
		return false
	}
	ok, _ := p.Lifecycle.IsRunning(infra.ServiceRedis)
	return ok
}

// RestartRedis restarts Redis.
func RestartRedis() error {
	p := getProviders()
	if p == nil || p.Lifecycle == nil {
		return fmt.Errorf("no lifecycle provider")
	}
	GinkgoWriter.Printf("Restarting Redis...\n")
	return p.Lifecycle.Restart(infra.ServiceRedis)
}

// StopRedis stops Redis.
func StopRedis() error {
	p := getProviders()
	if p == nil || p.Lifecycle == nil {
		return fmt.Errorf("no lifecycle provider")
	}
	GinkgoWriter.Printf("Stopping Redis...\n")
	return p.Lifecycle.Stop(infra.ServiceRedis)
}

// StartRedis starts Redis.
func StartRedis() error {
	p := getProviders()
	if p == nil || p.Lifecycle == nil {
		return fmt.Errorf("no lifecycle provider")
	}
	GinkgoWriter.Printf("Starting Redis...\n")
	return p.Lifecycle.Start(infra.ServiceRedis)
}

// GetRedisPassword returns the Redis password from the infra Secrets provider (K8s secret or Podman secret via flightctl-kv container).
// Returns an error if the provider is unavailable or the secret cannot be read (no fallback).
func GetRedisPassword() (string, error) {
	p := getProviders()
	if p == nil || p.Secrets == nil {
		return "", fmt.Errorf("no secrets provider available for Redis password")
	}
	data, err := p.Secrets.GetSecretDataForService(context.Background(), infra.ServiceRedis, kvSecretName, kvSecretKey)
	if err != nil {
		return "", fmt.Errorf("get Redis secret %s key %q: %w", kvSecretName, kvSecretKey, err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("Redis secret %s key %q is empty", kvSecretName, kvSecretKey)
	}
	return strings.TrimSpace(string(data)), nil
}

// GetRedisClient returns a Redis client and a cleanup function. Caller must call cleanup when done.
// For K8s this port-forwards to Redis; for Quadlet uses direct host from provider.
func GetRedisClient() (*redis.Client, func(), error) {
	p := getProviders()
	if p == nil || p.Infra == nil {
		return nil, func() {}, fmt.Errorf("no infra provider")
	}
	password, err := GetRedisPassword()
	if err != nil {
		return nil, func() {}, err
	}
	urlStr, cleanup, err := p.Infra.ExposeService(infra.ServiceRedis, "redis")
	if err != nil {
		return nil, func() {}, err
	}
	u, err := url.Parse(urlStr)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	// Prefer IPv4 so "localhost" does not resolve to [::1] and get connection refused when Redis is on 127.0.0.1
	if host == "" || host == "localhost" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, port)

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})
	cleanupWithClose := func() {
		_ = client.Close()
		cleanup()
	}
	return client, cleanupWithClose, nil
}

// TryConnectToRedis attempts to connect to Redis with retries until timeout and returns the last error if all fail.
// Each retry invalidates the expose cache and creates a new port-forward so we connect to the current Redis instance.
func TryConnectToRedis(timeout time.Duration) error {
	const retryDelay = 2 * time.Second
	deadline := time.Now().Add(timeout)
	var lastErr error
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		if attempt > 0 {
			p := getProviders()
			if p != nil && p.Infra != nil {
				p.Infra.InvalidateExposeCache(infra.ServiceRedis)
			}
		}
		client, cleanup, err := GetRedisClient()
		if err != nil {
			lastErr = err
			if time.Now().Add(retryDelay).Before(deadline) {
				time.Sleep(retryDelay)
			}
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), redisLongPolling)
		err = client.Ping(ctx).Err()
		cancel()
		cleanup()
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().Add(retryDelay).Before(deadline) {
			time.Sleep(retryDelay)
		}
	}
	return lastErr
}

// CheckQueueState returns the current Redis queue state.
func CheckQueueState() QueueState {
	state := QueueState{Accessible: false}
	client, cleanup, err := GetRedisClient()
	if err != nil {
		return state
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), redisLongPolling)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return state
	}
	state.Accessible = true
	taskQueue := "task-queue"
	groupName := taskQueue + "-group"
	exists, err := client.Exists(ctx, taskQueue).Result()
	if err == nil && exists > 0 {
		state.TaskQueueExists = true
		length, _ := client.XLen(ctx, taskQueue).Result()
		state.QueueLength = length
	}
	groups, err := client.XInfoGroups(ctx, taskQueue).Result()
	if err == nil {
		for _, g := range groups {
			if g.Name == groupName {
				state.HasConsumerGroup = true
				break
			}
		}
	}
	tasks, _ := client.ZRange(ctx, "in_flight_tasks", 0, -1).Result()
	state.InFlightTasks = len(tasks)
	keys, _ := client.Keys(ctx, "failed_messages:*").Result()
	for _, key := range keys {
		n, _ := client.ZCard(ctx, key).Result()
		state.FailedMessages += int(n)
	}
	return state
}

// IsQueueInitialized returns whether the queue is ready (accessible and consumer group exists).
func IsQueueInitialized() bool {
	state := CheckQueueState()
	return state.Accessible && state.HasConsumerGroup
}

// IsQueueAccessible returns whether the Redis queue is accessible.
func IsQueueAccessible() bool {
	return CheckQueueState().Accessible
}

// TryQueueAccessible attempts to connect to Redis and ping it; returns an error describing the failure.
func TryQueueAccessible() error {
	client, cleanup, err := GetRedisClient()
	if err != nil {
		return fmt.Errorf("get redis client: %w", err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), redisLongPolling)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

// WaitForQueueAccessible waits for the Redis queue to become accessible.
func WaitForQueueAccessible(timeout, polling time.Duration, msg string) {
	Eventually(TryQueueAccessible, timeout, polling).
		Should(Succeed(), "Queue should be accessible %s", msg)
}

// WaitForResourcesAccessible waits for the check to succeed.
func WaitForResourcesAccessible(timeout, polling time.Duration, checkFn func() bool, msg string) {
	Eventually(checkFn, timeout, polling).Should(BeTrue(), "All resources should be accessible %s", msg)
}

// WaitForQueueInitializedAfterRestart creates a resource and verifies queue initialization after restart.
func WaitForQueueInitializedAfterRestart(
	timeout, polling time.Duration,
	createResourceFn func() error,
	verifyResourceFn func() bool,
) {
	Eventually(createResourceFn, timeout, polling).
		Should(Succeed(), "should create resource after restart")
	Eventually(func() bool { return IsQueueInitialized() }, timeout, polling).
		Should(BeTrue(), func() string {
			state, errs := AssertQueueState()
			return fmt.Sprintf("queue should be initialized after restart. Errors: %v, State: %+v", errs, state)
		})
	Eventually(verifyResourceFn, timeout, polling).Should(BeTrue(), "post-restart resource should be processed")
}

// VerifyQueueHealthy returns queue state and error if not healthy.
func VerifyQueueHealthy() (QueueState, error) {
	state := CheckQueueState()
	var errs []string
	if !state.Accessible {
		errs = append(errs, "queue is not accessible")
	}
	if !state.HasConsumerGroup {
		errs = append(errs, "consumer group does not exist (validates ensureConsumerGroup fix)")
	}
	if len(errs) > 0 {
		return state, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return state, nil
}

// AssertQueueState returns the queue state and any validation errors.
func AssertQueueState() (state QueueState, errors []string) {
	state = CheckQueueState()
	if !state.Accessible {
		errors = append(errors, "queue is not accessible")
	}
	if !state.HasConsumerGroup {
		errors = append(errors, "consumer group does not exist (validates ensureConsumerGroup fix)")
	}
	return state, errors
}

// HasQueueActivity returns whether the queue has in-flight tasks or pending items.
func HasQueueActivity() (state QueueState, hasActivity bool) {
	state = CheckQueueState()
	hasActivity = state.Accessible && (state.InFlightTasks > 0 || state.QueueLength > 0)
	return state, hasActivity
}
