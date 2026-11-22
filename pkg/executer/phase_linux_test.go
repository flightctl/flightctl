//go:build linux

package executer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == PhaseLauncherCommand {
		if _, err := os.Stat("/.launcher-proof-fail"); err == nil {
			actual, err := CurrentPhaseNamespaceIdentity()
			if err != nil {
				os.Exit(21)
			}
			args := append(phaseNamespaceLauncherArgs(actual), os.Args[8:]...)
			proofErr := RunPhaseLauncher(args)
			if proofErr == nil || !strings.Contains(proofErr.Error(), "required phase namespace isolation is not active") {
				os.Exit(22)
			}
			os.Exit(20)
		}
		if _, err := os.Stat("/.launcher-delay"); err == nil {
			time.Sleep(5 * time.Second)
		}
		if RunPhaseLauncher(os.Args[1:]) != nil {
			os.Exit(20)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPhaseOutput(t *testing.T) {
	t.Run("When streams use different budgets it should cap them independently", func(t *testing.T) {
		output := phaseCapture{limit: 4}
		stderr := phaseCapture{limit: 2}
		_, err := output.Write([]byte("1234"))
		require.NoError(t, err)
		_, err = stderr.Write([]byte("abc"))
		require.ErrorIs(t, err, ErrPhaseOutputLimit)
		require.Equal(t, "1234", output.buffer.String())
		require.Equal(t, "ab", stderr.buffer.String())
	})
	t.Run("When another write crosses the bound it should stop at the allowance", func(t *testing.T) {
		capture := phaseCapture{limit: 3}
		_, err := capture.Write([]byte("ab"))
		require.NoError(t, err)
		n, err := capture.Write([]byte("cd"))
		require.Equal(t, 1, n)
		require.True(t, errors.Is(err, ErrPhaseOutputLimit))
		require.Equal(t, "abc", capture.buffer.String())
	})
}

func TestPhaseLauncherAndManagerTimeouts(t *testing.T) {
	host, err := CurrentPhaseNamespaceIdentity()
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		namespaces PhaseNamespaceIdentity
		wantError  bool
	}{
		{name: "When the launcher sees an unchanged namespace it should fail closed", namespaces: host, wantError: true},
		{name: "When every private namespace differs it should allow the main command", namespaces: changedPhaseNamespaces(host)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePhaseNamespaceIsolation(tc.namespaces, host)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	queue, startup, runtime := phaseManagerTimeouts(2 * time.Second)
	require.Positive(t, queue)
	require.Positive(t, startup)
	require.Positive(t, runtime)
	require.LessOrEqual(t, queue+startup+runtime, 2*time.Second)
	require.Equal(t, time.Duration(0), queue+startup+runtime-(2*time.Second))
}

func changedPhaseNamespaces(host PhaseNamespaceIdentity) PhaseNamespaceIdentity {
	host.PID.Inode++
	host.Network.Inode++
	host.IPC.Inode++
	return host
}

func TestLinuxPhaseRejectsUnavailableProfile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged caller")
	}
	_, err := (&LinuxPhaseExecutor{}).PreparePhase(t.Context(), PhaseExecSpec{})
	require.ErrorIs(t, err, ErrPhaseUnsupported)
}

func TestPhaseControllers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cgroup.controllers")
	require.ErrorIs(t, checkPhaseControllers(path), ErrPhaseUnsupported)
	require.NoError(t, os.WriteFile(path, []byte("cpu pids"), 0o600))
	require.ErrorIs(t, checkPhaseControllers(path), ErrPhaseUnsupported)
	require.NoError(t, os.WriteFile(path, []byte("cpu memory pids"), 0o600))
	require.NoError(t, checkPhaseControllers(path))
}

func TestLinuxPhaseSetupHonorsDeadline(t *testing.T) {
	if os.Getenv("FLIGHTCTL_TEST_PHASE_BOUNDARY") != "1" {
		t.Skip("requires an explicit privileged Linux profile test")
	}
	require.NoError(t, checkPhaseHost())
	root, err := os.MkdirTemp("/var/lib", "flightctl-phase-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	executable, err := os.Executable()
	require.NoError(t, err)
	data, err := os.ReadFile(executable)
	require.NoError(t, err)
	launcher := writePhaseLauncherFixture(t, data)
	hostNamespaces, err := CurrentHostPhaseNamespaceIdentity()
	require.NoError(t, err)
	socket := filepath.Join(root, "bus")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	// The socket accepts the connection but never answers authentication.
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+socket)
	_, err = (&LinuxPhaseExecutor{}).PreparePhase(t.Context(), PhaseExecSpec{
		RootDir: root, Entrypoint: "/updater", LauncherPath: launcher, HostNamespaces: hostNamespaces,
		Limits: PhaseExecLimits{ResultBytes: 1, StderrBytes: 1, ScratchBytes: 1,
			MemoryBytes: 1, Processes: 1, Invocation: 100 * time.Millisecond, Cleanup: time.Second},
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestLinuxPhaseBoundary(t *testing.T) {
	if os.Getenv("FLIGHTCTL_TEST_PHASE_BOUNDARY") != "1" {
		t.Skip("requires an explicit privileged Linux profile test")
	}
	require.Equal(t, 0, os.Geteuid(), "system-manager boundary must be tested as root")
	executable, err := os.Executable()
	require.NoError(t, err)
	data, err := os.ReadFile(executable)
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		mode      string
		writable  bool
		source    bool
		limit     int64
		wantError bool
		cancel    bool
		abandon   bool
		proofFail bool
		delay     bool
	}{
		{name: "When verify runs it should see only read-only inputs", mode: "verify"},
		{name: "When prepare has no grant it should not access the source", mode: "ungranted", writable: true},
		{name: "When prepare has a grant it should use bounded scratch and the admitted source", mode: "prepare", writable: true, source: true},
		{name: "When stdout overflows it should fail", mode: "output", limit: 2, wantError: true},
		{name: "When stderr overflows it should fail independently of stdout", mode: "stderr", wantError: true},
		{name: "When a descendant starts a new session it should be stopped within the invocation budget", mode: "descendant", wantError: true},
		{name: "When a descendant inherits output after the main process exits it should not retain the scope", mode: "orphan"},
		{name: "When namespace proof fails it should never execute the updater", mode: "proof-fail", writable: true, proofFail: true, wantError: true},
		{name: "When the caller cancels it should stop the owned scope", mode: "linger", wantError: true, cancel: true},
		{name: "When the caller disappears the manager should enforce the runtime limit", mode: "linger", abandon: true},
		{name: "When the launcher delays and the caller disappears the manager should enforce the invocation limit", mode: "delay-launch", abandon: true, delay: true},
		{name: "When helper tasks exhaust the memory limit the unit should fail", mode: "memory", wantError: true},
		{name: "When helper tasks hit the process limit it should deny additional tasks", mode: "pids"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := os.MkdirTemp("/var/lib", "flightctl-phase-test-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
			require.NoError(t, os.Chmod(root, 0o755))
			updater := filepath.Join(root, "updater")
			writePhaseFixture(t, updater, data, 0o555)
			launcher := writePhaseLauncherFixture(t, data)
			if _, err := os.Stat("/sys/fs/selinux/enforce"); err == nil {
				output, err := exec.CommandContext(t.Context(), "chcon", "--reference=/usr/bin/true", updater).CombinedOutput()
				require.NoError(t, err, "%s", output)
			}
			require.NoError(t, os.Mkdir(filepath.Join(root, "work"), 0o755))
			writePhaseFixture(t, filepath.Join(root, "input"), []byte("approved"), 0o444)
			hostNamespaces, err := CurrentHostPhaseNamespaceIdentity()
			require.NoError(t, err)
			agentNamespaces, err := CurrentPhaseNamespaceIdentity()
			require.NoError(t, err)
			require.Equal(t, hostNamespaces, agentNamespaces)
			namespaceBytes, err := json.Marshal(hostNamespaces)
			require.NoError(t, err)
			writePhaseFixture(t, filepath.Join(root, "host-namespaces.json"), namespaceBytes, 0o444)
			if tc.proofFail {
				writePhaseFixture(t, filepath.Join(root, ".launcher-proof-fail"), nil, 0o444)
			}
			if tc.delay {
				writePhaseFixture(t, filepath.Join(root, ".launcher-delay"), nil, 0o444)
			}
			var source *os.File
			if tc.source {
				var writer *os.File
				source, writer, err = os.Pipe()
				require.NoError(t, err)
				_, err = writer.Write([]byte("artifact"))
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				t.Cleanup(func() { require.NoError(t, source.Close()) })
			}
			limit := tc.limit
			if limit == 0 {
				limit = 32
			}
			processes := int64(32)
			diagnosticLimit := int64(128)
			if tc.mode == "pids" {
				processes = 12
			}
			if tc.mode == "stderr" {
				diagnosticLimit = 32
			}
			job, err := (&LinuxPhaseExecutor{}).PreparePhase(t.Context(), PhaseExecSpec{
				RootDir: root, Entrypoint: "/updater", Args: []string{"-test.run=^TestPhaseFixture", "--", tc.mode},
				WritableWork: tc.writable, SourceFD: source, LauncherPath: launcher, HostNamespaces: hostNamespaces,
				Limits: PhaseExecLimits{ResultBytes: limit, StderrBytes: diagnosticLimit, ScratchBytes: 32 << 10,
					MemoryBytes: 64 << 20, Processes: processes, Invocation: 2 * time.Second, Cleanup: 3 * time.Second},
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, job.Close()) })
			require.NoError(t, job.Start(t.Context()))
			if tc.abandon {
				startedAt := time.Now()
				require.NoError(t, job.release())
				require.Eventually(t, func() bool {
					_, err := os.Stat(filepath.Join("/sys/fs/cgroup/system.slice", job.unit))
					return errors.Is(err, os.ErrNotExist)
				}, 3*time.Second, 20*time.Millisecond)
				if tc.delay {
					require.LessOrEqual(t, time.Since(startedAt), 3*time.Second)
				}
				return
			}
			waitCtx := t.Context()
			if tc.cancel {
				cancelled, cancel := context.WithCancel(waitCtx)
				cancel()
				waitCtx = cancelled
			}
			out, err := job.Wait(waitCtx)
			if tc.wantError {
				require.Error(t, err)
				require.NotErrorIs(t, err, ErrPhaseCleanup)
				if tc.cancel {
					require.ErrorIs(t, err, context.Canceled)
				}
				if tc.mode == "output" || tc.mode == "stderr" {
					require.ErrorIs(t, err, ErrPhaseOutputLimit)
				}
				if tc.mode == "descendant" {
					require.ErrorContains(t, err, "timeout", "%s", out.Stderr)
				}
				if tc.mode == "memory" {
					require.ErrorContains(t, err, "oom-kill")
				}
				if tc.proofFail {
					_, statErr := os.Stat(filepath.Join(root, "work", "updater-ran"))
					require.ErrorIs(t, statErr, os.ErrNotExist)
				}
			} else {
				require.NoError(t, err, "%s", out.Stderr)
				require.Equal(t, "ok", string(out.Result))
			}
			_, err = os.Stat(filepath.Join("/sys/fs/cgroup/system.slice", job.unit))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestPhaseFixture(t *testing.T) {
	if filepath.Base(os.Args[0]) != "updater" {
		return
	}
	checkPhaseFixtureNamespaces()
	mode := os.Args[len(os.Args)-1]
	_, _, errno := syscall.RawSyscall(unix.SYS_IO_URING_SETUP, 0, 0, 0)
	if errno != syscall.EPERM && errno != syscall.ENOSYS {
		os.Exit(16)
	}
	switch mode {
	case "verify", "ungranted":
		checkPhaseFixtureReadOnly(mode == "ungranted")
	case "prepare":
		checkPhaseFixtureSource()
	case "proof-fail":
		if err := os.WriteFile("/work/updater-ran", []byte("ran"), 0o600); err != nil {
			os.Exit(17)
		}
	case "output":
		fmt.Print("oversized")
		os.Exit(0)
	case "stderr":
		fmt.Fprint(os.Stderr, "123456789012345678901234567890123")
		os.Exit(0)
	case "descendant", "orphan":
		child := exec.Command("/updater", "-test.run=^TestPhaseFixture", "--", "linger")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(11)
		}
		if mode == "descendant" {
			time.Sleep(30 * time.Second)
			os.Exit(0)
		}
	case "linger":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "memory":
		mem := make([]byte, 128<<20)
		for i := 0; i < len(mem); i += 4096 {
			mem[i] = 1
		}
		fmt.Print(mem[len(mem)-4096])
		os.Exit(12)
	case "pids":
		var children []*exec.Cmd
		for i := 0; i < 40; i++ {
			child := exec.Command("/updater", "-test.run=^TestPhaseFixture", "--", "linger")
			child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				if !errors.Is(err, syscall.EAGAIN) {
					fmt.Fprint(os.Stderr, err)
					os.Exit(13)
				}
				// Stop the previously started children before reporting success.
				break
			}
			children = append(children, child)
		}
		if len(children) == 0 || len(children) == 40 {
			os.Exit(14)
		}
		for _, child := range children {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	default:
		os.Exit(15)
	}
	fmt.Print("ok")
	os.Exit(0)
}

func writePhaseFixture(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	require.NoError(t, os.Chmod(path, mode))
}

func writePhaseLauncherFixture(t *testing.T, contents []byte) string {
	t.Helper()
	dir, err := os.MkdirTemp("/usr/libexec", "flightctl-phase-launcher-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o755))
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	path := filepath.Join(dir, "phase-launcher")
	writePhaseFixture(t, path, contents, 0o555)
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err == nil {
		output, err := exec.CommandContext(t.Context(), "chcon", "--reference=/usr/bin/true", path).CombinedOutput()
		require.NoError(t, err, "%s", output)
	}
	return path
}

func checkPhaseFixtureReadOnly(writable bool) {
	for _, name := range []string{"/etc/passwd", "/dev/kvm", "/run/systemd/private", "/sys/firmware/efi/efivars"} {
		if _, err := os.ReadFile(name); err == nil {
			fmt.Fprint(os.Stderr, name)
			os.Exit(3)
		}
	}
	if _, err := os.Readlink("/proc/self/ns/pid"); err != nil {
		os.Exit(3)
	}
	if _, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		os.Exit(3)
	}
	const processes = "/sys/fs/cgroup/cgroup.procs"
	if fd, err := os.OpenFile(processes, os.O_WRONLY, 0); err == nil {
		_ = fd.Close()
		os.Exit(3)
	}
	if data, err := os.ReadFile(processes); err == nil {
		for _, pid := range strings.Fields(string(data)) {
			if pid != strconv.Itoa(os.Getpid()) {
				fmt.Fprint(os.Stderr, "foreign process in cgroup")
				os.Exit(3)
			}
		}
	}
	err := os.WriteFile("/work/fixture", []byte("x"), 0o600)
	if (err == nil) != writable {
		os.Exit(4)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil || len(input) != 0 {
		os.Exit(5)
	}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		_ = syscall.Close(fd)
		os.Exit(5)
	}
}

func checkPhaseFixtureNamespaces() {
	expectedBytes, err := os.ReadFile("/host-namespaces.json")
	if err != nil {
		os.Exit(3)
	}
	var expected PhaseNamespaceIdentity
	if json.Unmarshal(expectedBytes, &expected) != nil {
		os.Exit(3)
	}
	actual, err := CurrentPhaseNamespaceIdentity()
	if err != nil || validatePhaseNamespaceIsolation(actual, expected) != nil {
		os.Exit(3)
	}
}

func checkPhaseFixtureSource() {
	artifact, err := io.ReadAll(os.Stdin)
	if err != nil || string(artifact) != "artifact" {
		os.Exit(6)
	}
	if err = os.WriteFile("/work/item", make([]byte, 8192), 0o600); err != nil {
		os.Exit(8)
	}
	if err = os.WriteFile("/work/over-limit", make([]byte, 32768), 0o600); !errors.Is(err, syscall.ENOSPC) {
		os.Exit(9)
	}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		_ = syscall.Close(fd)
		os.Exit(10)
	}
}
