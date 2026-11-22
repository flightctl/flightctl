//go:build linux

package executer

import (
	"context"
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
	}{
		{name: "When verify runs it should see only read-only inputs", mode: "verify"},
		{name: "When prepare has no grant it should not access the source", mode: "ungranted", writable: true},
		{name: "When prepare has a grant it should use bounded scratch and the admitted source", mode: "prepare", writable: true, source: true},
		{name: "When stdout overflows it should fail", mode: "output", limit: 2, wantError: true},
		{name: "When stderr overflows it should fail independently of stdout", mode: "stderr", wantError: true},
		{name: "When a descendant starts a new session it should be stopped at the invocation deadline", mode: "descendant", wantError: true},
		{name: "When a descendant inherits output after the main process exits it should not retain the scope", mode: "orphan"},
		{name: "When the caller cancels it should stop the owned scope", mode: "linger", wantError: true, cancel: true},
		{name: "When the caller disappears the manager should enforce the runtime limit", mode: "linger", abandon: true},
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
			if _, err := os.Stat("/sys/fs/selinux/enforce"); err == nil {
				output, err := exec.CommandContext(t.Context(), "chcon", "--reference=/usr/bin/true", updater).CombinedOutput()
				require.NoError(t, err, "%s", output)
			}
			require.NoError(t, os.Mkdir(filepath.Join(root, "work"), 0o755))
			writePhaseFixture(t, filepath.Join(root, "input"), []byte("approved"), 0o444)
			hostPIDNS, err := os.Readlink("/proc/self/ns/pid")
			require.NoError(t, err)
			writePhaseFixture(t, filepath.Join(root, "host-pid-namespace"), []byte(hostPIDNS), 0o444)
			servePhaseFixture(t, root)
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
				WritableWork: tc.writable, SourceSocket: tc.source,
				Limits: PhaseExecLimits{ResultBytes: limit, StderrBytes: diagnosticLimit, ScratchBytes: 32 << 10,
					MemoryBytes: 64 << 20, Processes: processes, Invocation: 800 * time.Millisecond, Cleanup: 3 * time.Second},
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, job.Close()) })
			require.NoError(t, job.Start(t.Context()))
			if tc.abandon {
				job.release()
				require.Eventually(t, func() bool {
					_, err := os.Stat(filepath.Join("/sys/fs/cgroup/system.slice", job.unit))
					return errors.Is(err, os.ErrNotExist)
				}, 3*time.Second, 20*time.Millisecond)
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
					require.ErrorContains(t, err, "deadline exceeded", "%s", out.Stderr)
				}
				if tc.mode == "memory" {
					require.ErrorContains(t, err, "oom-kill")
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

func servePhaseFixture(t *testing.T, root string) {
	t.Helper()
	socket := filepath.Join(root, "source")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	require.NoError(t, err)
	require.NoError(t, os.Chmod(socket, 0o666))
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = conn.Write([]byte("artifact"))
	}()
	t.Cleanup(func() {
		require.NoError(t, listener.Close())
		<-done
	})
}

func TestPhaseFixture(t *testing.T) {
	if filepath.Base(os.Args[0]) != "updater" {
		return
	}
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

func checkPhaseFixtureReadOnly(writable bool) {
	for _, name := range []string{"/etc/passwd", "/dev/kvm", "/run/systemd/private", "/sys/firmware/efi/efivars"} {
		if _, err := os.ReadFile(name); err == nil {
			fmt.Fprint(os.Stderr, name)
			os.Exit(3)
		}
	}
	hostPIDNS, err := os.ReadFile("/host-pid-namespace")
	if err != nil {
		os.Exit(3)
	}
	if ns, err := os.Readlink("/proc/self/ns/pid"); err == nil && ns == string(hostPIDNS) {
		fmt.Fprint(os.Stderr, "host PID namespace")
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
	err = os.WriteFile("/work/fixture", []byte("x"), 0o600)
	if (err == nil) != writable {
		os.Exit(4)
	}
	if conn, err := net.DialTimeout("unix", "/source", time.Second); err == nil {
		_ = conn.Close()
		os.Exit(5)
	}
}

func checkPhaseFixtureSource() {
	conn, err := net.DialTimeout("unix", "/source", time.Second)
	if err != nil {
		os.Exit(6)
	}
	var artifact [8]byte
	_, err = io.ReadFull(conn, artifact[:])
	_ = conn.Close()
	if err != nil || string(artifact[:]) != "artifact" {
		os.Exit(7)
	}
	if err = os.WriteFile("/work/item", make([]byte, 8192), 0o600); err != nil {
		os.Exit(8)
	}
	if err = os.WriteFile("/work/over-limit", make([]byte, 32768), 0o600); !errors.Is(err, syscall.ENOSPC) {
		os.Exit(9)
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err == nil {
		_ = syscall.Close(fd)
		os.Exit(10)
	}
}
