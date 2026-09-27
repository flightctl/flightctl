package vm

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

func TestContainerDeviceBuildRequestAddsConfiguredExtraHosts(t *testing.T) {
	want := []string{"e2e-registry:2001:db8::10"}
	device := NewContainerDevice(ContainerDeviceConfig{ExtraHosts: want})
	request := device.buildRequest()

	hostConfig := &container.HostConfig{}
	request.HostConfigModifier(hostConfig)
	if !reflect.DeepEqual(hostConfig.ExtraHosts, want) {
		t.Fatalf("HostConfig.ExtraHosts = %v, want %v", hostConfig.ExtraHosts, want)
	}
}

type lifecycleTestContainer struct {
	testcontainers.Container
	running    bool
	startCalls int
	stopCalls  int
}

type blockingTerminateTestContainer struct {
	testcontainers.Container
}

func (c *blockingTerminateTestContainer) Terminate(ctx context.Context, _ ...testcontainers.TerminateOption) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c *lifecycleTestContainer) IsRunning() bool {
	return c.running
}

func (c *lifecycleTestContainer) Start(context.Context) error {
	c.startCalls++
	c.running = true
	return nil
}

func (c *lifecycleTestContainer) Stop(context.Context, *time.Duration) error {
	c.stopCalls++
	c.running = false
	return nil
}

func TestContainerDeviceRunRestartsAfterShutdown(t *testing.T) {
	t.Run("When a shut-down container is run it should restart the same container", func(t *testing.T) {
		container := &lifecycleTestContainer{running: true}
		device := NewContainerDevice(ContainerDeviceConfig{Name: "test-device"})
		device.container = container
		device.started = true

		if err := device.Shutdown(); err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
		if container.stopCalls != 1 {
			t.Fatalf("Stop() calls = %d, want 1", container.stopCalls)
		}

		if err := device.Run(); err != nil {
			t.Fatalf("Run() after Shutdown() error = %v", err)
		}
		if container.startCalls != 1 {
			t.Errorf("Start() calls = %d, want 1", container.startCalls)
		}
		if !container.running {
			t.Error("container is not running after Run()")
		}
		if device.container != container {
			t.Error("Run() replaced the stopped container instead of restarting it")
		}
	})
}

func TestContainerDeviceContextCancellation(t *testing.T) {
	t.Run("When startup context is canceled it should return before starting the container", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		device := NewContainerDevice(ContainerDeviceConfig{Name: "canceled-device"})

		err := device.RunAndWaitForSSHContext(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunAndWaitForSSHContext() error = %v, want context.Canceled", err)
		}
		if device.container != nil {
			t.Fatal("container was created with a canceled context")
		}
	})

	t.Run("When readiness context is canceled it should return without probing the container", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		device := NewContainerDevice(ContainerDeviceConfig{Name: "canceled-device"})

		err := device.WaitForSSHToBeReadyContext(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitForSSHToBeReadyContext() error = %v, want context.Canceled", err)
		}
	})
}

func TestContainerDeviceForceDeleteContext(t *testing.T) {
	t.Run("When termination reaches its deadline it should retain the handle for a cleanup retry", func(t *testing.T) {
		container := &blockingTerminateTestContainer{}
		device := NewContainerDevice(ContainerDeviceConfig{Name: "slow-cleanup-device"})
		device.container = container
		device.started = true

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := device.ForceDeleteContext(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ForceDeleteContext() error = %v, want context.DeadlineExceeded", err)
		}
		if device.container != container {
			t.Fatal("container handle was discarded after failed termination")
		}
		if !device.started {
			t.Fatal("started state was cleared after failed termination")
		}
	})
}
