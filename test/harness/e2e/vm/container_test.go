package vm

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func TestContainerDeviceUnsupportedSnapshots(t *testing.T) {
	device := NewContainerDevice(ContainerDeviceConfig{})
	for name, operation := range map[string]func(string) error{
		"create": device.CreateSnapshot,
		"delete": device.DeleteSnapshot,
		"revert": device.RevertToSnapshot,
	} {
		t.Run("When a container requests snapshot "+name+" it should report unsupported", func(t *testing.T) {
			if err := operation("pristine"); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("snapshot error = %v, want ErrUnsupported", err)
			}
		})
	}
	if exists, err := device.HasSnapshot("pristine"); exists || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("HasSnapshot() = %v, %v, want false, ErrUnsupported", exists, err)
	}
}

func TestContainerDeviceExecIdentity(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nondefault/podman/podman.sock")
	device := NewContainerDevice(ContainerDeviceConfig{Name: "device"})
	command := device.SSHCommand([]string{"echo", "$HOME"})
	want := []string{"podman", "--remote", "--url", "unix:///nondefault/podman/podman.sock", "exec", "-i", "-u", "user", "-e", "HOME=/home/user", "-e", "LC_ALL=C", "-w", "/", "device", "sh", "-c", `if [ -d "$HOME" ]; then cd "$HOME" || exit; fi; exec "$@"`, "sh", "sh", "-c", "echo $HOME"}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("Args = %v, want %v", command.Args, want)
	}
}

func TestContainerDeviceExecHomeFallback(t *testing.T) {
	for _, homeExists := range []bool{false, true} {
		t.Run("When executing a command it should use HOME only when its directory exists", func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			if homeExists {
				if err := os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			device := NewContainerDevice(ContainerDeviceConfig{Name: "device"})
			command := device.SSHCommand([]string{"pwd"})
			args := command.Args
			shellIndex := 0
			for index, arg := range args {
				if arg == "device" {
					shellIndex = index + 1
					break
				}
			}
			probe := exec.Command(args[shellIndex], args[shellIndex+1:]...)
			probe.Dir = "/"
			output, err := probe.Output()
			if err != nil {
				t.Fatal(err)
			}
			want := "/"
			if homeExists {
				want = home
			}
			if strings.TrimSpace(string(output)) != want {
				t.Fatalf("working directory = %q, want %q", output, want)
			}
		})
	}
}

func TestNestedOverlayProbe(t *testing.T) {
	for _, driver := range []string{"overlay", "vfs"} {
		t.Run("When probing "+driver+" it should memoize the result across concurrent calls", func(t *testing.T) {
			binDir := t.TempDir()
			callsPath := filepath.Join(binDir, "calls")
			script := "#!/bin/sh\necho probe >> '" + callsPath + "'\necho " + driver + "\n"
			if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
			var cache nestedOverlayProbe
			var workers sync.WaitGroup
			for range 16 {
				workers.Go(func() {
					if got := cache.get(context.Background()); got != (driver == "overlay") {
						t.Errorf("nested overlay = %v for driver %q", got, driver)
					}
				})
			}
			workers.Wait()
			calls, err := os.ReadFile(callsPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(calls) != "probe\n" {
				t.Fatalf("probe calls = %q, want exactly one", calls)
			}
		})
	}
	t.Run("When the parent deadline expires it should bound the probe", func(t *testing.T) {
		binDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\nexec sleep 10\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		var cache nestedOverlayProbe
		started := time.Now()
		if cache.get(ctx) {
			t.Fatal("failed probe enabled nested overlay")
		}
		if time.Since(started) > time.Second {
			t.Fatal("probe ignored the parent deadline")
		}
		if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\necho overlay\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if !cache.get(context.Background()) {
			t.Fatal("probe did not retry after the parent deadline expired")
		}
	})
	t.Run("When the runtime probe fails it should retry and cache only success", func(t *testing.T) {
		binDir := t.TempDir()
		runtimePath := filepath.Join(binDir, "docker")
		if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
		var cache nestedOverlayProbe
		if cache.get(context.Background()) {
			t.Fatal("failed probe enabled nested overlay")
		}
		if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\necho overlay\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if !cache.get(context.Background()) {
			t.Fatal("probe did not retry after a runtime failure")
		}
		if err := os.Remove(runtimePath); err != nil {
			t.Fatal(err)
		}
		if !cache.get(context.Background()) {
			t.Fatal("successful result was not cached")
		}
	})
}

func TestContainerDeviceExactNameProbes(t *testing.T) {
	t.Run("When only longer Docker container names exist it should not report this device present or running", func(t *testing.T) {
		binDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\nprintf 'device-10\\ndevice-11\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
		device := NewContainerDevice(ContainerDeviceConfig{Name: "device-1"})
		for name, probe := range map[string]func() (bool, error){"Exists": device.Exists, "IsRunning": device.IsRunning} {
			present, err := probe()
			if err != nil || present {
				t.Fatalf("%s() = %v, %v, want false, nil", name, present, err)
			}
		}
	})
}

func TestContainerDeviceRequestLifecycle(t *testing.T) {
	for _, nestedOverlay := range []bool{false, true} {
		t.Run("When building a device request it should gate agent startup and scope cleanup", func(t *testing.T) {
			device := NewContainerDevice(ContainerDeviceConfig{SessionID: "test-session"})
			device.nestedOverlay = nestedOverlay
			request := device.buildRequest()
			if request.Labels["flightctl.e2e.session"] != "test-session" {
				t.Fatal("missing session label")
			}
			storageConfigured, startupGated := false, false
			for _, file := range request.Files {
				if strings.HasPrefix(file.ContainerFilePath, "/etc/containers/storage.conf.d/") {
					t.Fatal("containers/storage does not support storage.conf drop-ins")
				}
				if file.ContainerFilePath == "/etc/containers/storage.conf" {
					content, err := io.ReadAll(file.Reader)
					if err != nil {
						t.Fatal(err)
					}
					if string(content) != NestedPodmanStorageConf {
						t.Fatalf("unexpected storage configuration: %s", content)
					}
					storageConfigured = true
				}
				if file.ContainerFilePath == "/etc/systemd/system/flightctl-agent.service.d/e2e-start.conf" {
					content, err := io.ReadAll(file.Reader)
					if err != nil {
						t.Fatal(err)
					}
					startupGated = strings.Contains(string(content), "ConditionPathExists=/run/flightctl-e2e-agent-enabled")
				}
			}
			if storageConfigured != nestedOverlay || !startupGated {
				t.Fatalf("storageConfigured = %v, startupGated = %v", storageConfigured, startupGated)
			}
		})
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
