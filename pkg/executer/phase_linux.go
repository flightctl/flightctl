//go:build linux

package executer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// LinuxPhaseExecutor uses system-manager transient services to supervise native helpers.
type LinuxPhaseExecutor struct{}

// PhaseJob owns the supervisor connection and the output streams for one invocation.
type PhaseJob struct {
	spec PhaseExecSpec
	bus  *systemd.Conn
	unit string

	stdout, stderr     *os.File
	result, diagnostic *os.File
	started            bool
	submitted          bool
	acknowledged       bool
	deadline           time.Time
}

// PreparePhase validates the limits and establishes supervision without launching the updater.
func (*LinuxPhaseExecutor) PreparePhase(ctx context.Context, spec PhaseExecSpec) (*PhaseJob, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkPhaseHost(); err != nil {
		return nil, err
	}
	if err := validatePhaseSpec(spec); err != nil {
		return nil, err
	}
	bus, err := systemd.NewSystemConnectionContext(context.WithoutCancel(ctx))
	if err != nil {
		return nil, fmt.Errorf("%w: connect to system manager: %w", ErrPhaseUnsupported, err)
	}
	features, err := bus.GetManagerProperty("Features")
	if err != nil || !strings.Contains(features, "+SECCOMP ") {
		bus.Close()
		return nil, fmt.Errorf("%w: system manager must support seccomp", ErrPhaseUnsupported)
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		bus.Close()
		return nil, err
	}
	spec.Args = slices.Clone(spec.Args)
	return &PhaseJob{
		spec: spec, bus: bus, unit: "flightctl-updater-" + hex.EncodeToString(token) + ".service",
	}, nil
}

func checkPhaseHost() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%w: system-manager execution requires a privileged agent", ErrPhaseUnsupported)
	}
	if err := checkPhaseControllers("/sys/fs/cgroup/system.slice/cgroup.controllers"); err != nil {
		return err
	}
	if _, err := os.Stat("/proc/sys/kernel/seccomp/actions_avail"); err != nil {
		return fmt.Errorf("%w: syscall filtering is required", ErrPhaseUnsupported)
	}
	return nil
}

func checkPhaseControllers(path string) error {
	data, err := os.ReadFile(path)
	controllers := strings.Fields(string(data))
	if err != nil || !slices.Contains(controllers, "memory") || !slices.Contains(controllers, "pids") {
		return fmt.Errorf("%w: the execution slice must expose cgroup v2 memory and pids controllers", ErrPhaseUnsupported)
	}
	return nil
}

func validatePhaseSpec(spec PhaseExecSpec) error {
	if !filepath.IsAbs(spec.RootDir) || spec.RootDir != filepath.Clean(spec.RootDir) || spec.RootDir == "/" ||
		!filepath.IsAbs(spec.Entrypoint) || spec.Entrypoint != filepath.Clean(spec.Entrypoint) || spec.Entrypoint == "/" {
		return errors.New("invalid phase root or executable path")
	}
	if err := validatePhaseRoot(spec.RootDir); err != nil {
		return err
	}
	for _, arg := range append([]string{spec.Entrypoint}, spec.Args...) {
		if strings.ContainsRune(arg, '$') {
			return errors.New("phase argv cannot contain system-manager environment expansions")
		}
	}
	l := spec.Limits
	if l.ResultBytes <= 0 || l.StderrBytes <= 0 || l.ScratchBytes <= 0 || l.MemoryBytes <= 0 ||
		l.Processes <= 0 || l.Invocation < time.Microsecond || l.Cleanup < time.Microsecond {
		return errors.New("invalid phase limits")
	}
	if spec.WritableWork && l.ScratchBytes < int64(os.Getpagesize()) {
		return errors.New("phase scratch is smaller than the filesystem allocation unit")
	}
	if spec.SourceSocket && !spec.WritableWork {
		return errors.New("read-only phase cannot receive source access")
	}
	return nil
}

func validatePhaseRoot(path string) error {
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm()&0o022 != 0 || info.Sys().(*syscall.Stat_t).Uid != 0 {
			return errors.New("phase root and its parents must be root-owned directories without group or world write access")
		}
		if path == "/" {
			return nil
		}
		path = filepath.Dir(path)
	}
}

func phaseProperty(name string, value any) systemd.Property {
	return systemd.Property{Name: name, Value: dbus.MakeVariant(value)}
}

func (j *PhaseJob) properties() []systemd.Property {
	spec := j.spec
	l := spec.Limits
	addressFamilies := struct {
		Allow bool
		Names []string
	}{Allow: true}
	if spec.SourceSocket {
		addressFamilies.Names = []string{"AF_UNIX"}
	}
	props := []systemd.Property{
		systemd.PropType("exec"), phaseProperty("AddRef", true),
		phaseProperty("CollectMode", "inactive-or-failed"),
		systemd.PropExecStart(append([]string{spec.Entrypoint}, spec.Args...), false),
		systemd.PropSlice("system.slice"),
		phaseProperty("ExitType", "cgroup"), phaseProperty("Restart", "no"),
		phaseProperty("RootDirectory", spec.RootDir), phaseProperty("WorkingDirectory", "/"),
		phaseProperty("MountAPIVFS", false),
		phaseProperty("ProtectControlGroupsEx", "strict"),
		phaseProperty("InaccessiblePaths", []string{"/sys/fs/cgroup"}),
		phaseProperty("User", "65534"), phaseProperty("Group", "65534"),
		phaseProperty("NoNewPrivileges", true), phaseProperty("CapabilityBoundingSet", uint64(0)),
		phaseProperty("LimitCORE", uint64(0)), phaseProperty("LimitCORESoft", uint64(0)),
		phaseProperty("SystemCallErrorNumber", int32(unix.EPERM)),
		phaseProperty("SystemCallArchitectures", []string{"native"}),
		phaseProperty("AmbientCapabilities", uint64(0)),
		phaseProperty("RestrictNamespaces", uint64(0)),
		phaseProperty("PrivateNetwork", true), phaseProperty("PrivatePIDs", "yes"),
		phaseProperty("ProtectSystem", "strict"), phaseProperty("ReadOnlyPaths", []string{"/"}),
		phaseProperty("DevicePolicy", "closed"), phaseProperty("OOMPolicy", "kill"),
		phaseProperty("MemoryMax", uint64(l.MemoryBytes)), phaseProperty("MemorySwapMax", uint64(0)),
		phaseProperty("TasksMax", uint64(l.Processes)),
		phaseProperty("JobTimeoutUSec", uint64(l.Invocation.Microseconds())),
		phaseProperty("TimeoutStartUSec", uint64(l.Invocation.Microseconds())),
		phaseProperty("RuntimeMaxUSec", uint64(l.Invocation.Microseconds())),
		phaseProperty("TimeoutStopUSec", uint64(l.Cleanup.Microseconds())),
		phaseProperty("KillMode", "control-group"), phaseProperty("KillSignal", int32(unix.SIGKILL)),
		phaseProperty("StandardInput", "null"),
		phaseProperty("StandardOutputFileDescriptor", dbus.UnixFD(j.result.Fd())),
		phaseProperty("StandardErrorFileDescriptor", dbus.UnixFD(j.diagnostic.Fd())),
	}
	filter := struct {
		Allow bool
		Names []string
	}{Names: []string{"io_uring_setup", "io_uring_enter", "io_uring_register"}}
	if spec.SourceSocket {
		props = append(props, phaseProperty("RestrictAddressFamilies", addressFamilies))
	} else {
		filter.Names = append(filter.Names, "socket", "socketpair")
	}
	props = append(props, phaseProperty("SystemCallFilter", filter))
	type temporaryMount struct{ Path, Options string }
	const empty = "ro,nodev,nosuid,noexec,mode=000,size=65536,nr_inodes=32"
	mounts := []temporaryMount{{"/sys", empty}, {"/proc", empty}, {"/dev", empty}, {"/run", empty}}
	if spec.WritableWork {
		scratch := l.ScratchBytes / int64(os.Getpagesize()) * int64(os.Getpagesize())
		option := fmt.Sprintf("rw,nodev,nosuid,noexec,size=%d,nr_inodes=%d,mode=0700,uid=65534,gid=65534",
			scratch, scratch/int64(os.Getpagesize()))
		mounts = append(mounts, temporaryMount{"/work", option})
	}
	return append(props, phaseProperty("TemporaryFileSystem", mounts))
}

// Start launches the helper only under the configured service and waits for systemd's exec job.
func (j *PhaseJob) Start(ctx context.Context) error {
	if j.started || j.bus == nil {
		return errors.New("phase job has already started or closed")
	}
	j.started = true
	var err error
	j.stdout, j.result, err = os.Pipe()
	if err != nil {
		return err
	}
	j.stderr, j.diagnostic, err = os.Pipe()
	if err != nil {
		return err
	}
	j.deadline = time.Now().Add(j.spec.Limits.Invocation)
	startCtx, cancel := context.WithDeadline(ctx, j.deadline)
	defer cancel()
	started := make(chan string, 1)
	j.submitted = true
	_, err = j.bus.StartTransientUnitContext(startCtx, j.unit, "fail", j.properties(), started)
	_ = j.result.Close()
	_ = j.diagnostic.Close()
	if err != nil {
		return fmt.Errorf("%w: start phase scope: %w", ErrPhaseUnsupported, err)
	}
	j.acknowledged = true
	select {
	case status := <-started:
		if status != "done" {
			return fmt.Errorf("%w: start phase scope: %s", ErrPhaseUnsupported, status)
		}
		return nil
	case <-startCtx.Done():
		return startCtx.Err()
	}
}

// Wait returns output after the service has finished and its entire cgroup is empty.
func (j *PhaseJob) Wait(ctx context.Context) (PhaseExecOutput, error) {
	if j.bus == nil || !j.acknowledged || j.stdout == nil || j.stderr == nil {
		return PhaseExecOutput{}, errors.New("phase job was not started")
	}
	invokeCtx, cancel := context.WithDeadline(ctx, j.deadline)
	defer cancel()
	stdout := &phaseCapture{limit: j.spec.Limits.ResultBytes}
	stderr := &phaseCapture{limit: j.spec.Limits.StderrBytes}
	streams := make(chan error, 2)
	drain := func(capture *phaseCapture, pipe *os.File) {
		_, err := io.Copy(capture, pipe)
		streams <- err
		if err != nil {
			cancel()
		}
	}
	go drain(stdout, j.stdout)
	go drain(stderr, j.stderr)
	status, runErr := j.waitForService(invokeCtx)
	cleanupErr := j.stop()
	j.disconnect()
	var streamErr error
	drained := 0
	if runErr == nil && cleanupErr == nil {
		for drained < 2 && invokeCtx.Err() == nil {
			select {
			case err := <-streams:
				streamErr = errors.Join(streamErr, err)
				drained++
			case <-invokeCtx.Done():
			}
		}
		runErr = invokeCtx.Err()
	}
	j.release()
	for drained < 2 {
		streamErr = errors.Join(streamErr, <-streams)
		drained++
	}
	if cleanupErr != nil || runErr != nil || streamErr != nil {
		return PhaseExecOutput{Stderr: stderr.buffer.Bytes()}, errors.Join(runErr, cleanupErr, streamErr)
	}
	return PhaseExecOutput{Result: stdout.buffer.Bytes(), Stderr: stderr.buffer.Bytes(), ExitCode: status}, nil
}

func (j *PhaseJob) waitForService(ctx context.Context) (int, error) {
	timer := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	for {
		state, err := j.bus.GetUnitPropertyContext(ctx, j.unit, "SubState")
		if err != nil {
			return 0, err
		}
		switch state.Value.Value() {
		case "exited", "dead", "failed":
			props, err := j.bus.GetUnitTypePropertiesContext(ctx, j.unit, "Service")
			if err != nil {
				return 0, err
			}
			code, ok := props["ExecMainStatus"].(int32)
			if !ok || props["Result"] != "success" {
				return 0, fmt.Errorf("bounded firmware phase failed: %v (main status %d)", props["Result"], code)
			}
			return int(code), nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}

// Close stops the transient service, checks its cgroup and releases descriptors.
func (j *PhaseJob) Close() error {
	if j.bus == nil {
		return nil
	}
	defer j.release()
	return j.stop()
}

func (j *PhaseJob) release() {
	for _, fd := range []*os.File{j.stdout, j.stderr, j.result, j.diagnostic} {
		if fd != nil {
			_ = fd.Close()
		}
	}
	j.disconnect()
}

func (j *PhaseJob) disconnect() {
	if j.bus != nil {
		j.bus.Close()
		j.bus = nil
	}
}

func (j *PhaseJob) stop() error {
	if !j.submitted {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), j.spec.Limits.Cleanup)
	defer cancel()
	stopped := make(chan string, 1)
	_, err := j.bus.StopUnitContext(ctx, j.unit, "replace", stopped)
	if err != nil {
		var busError dbus.Error
		if !j.acknowledged || !errors.As(err, &busError) || busError.Name != "org.freedesktop.systemd1.NoSuchUnit" {
			return fmt.Errorf("%w: %w", ErrPhaseCleanup, err)
		}
	} else {
		select {
		case result := <-stopped:
			if result != "done" {
				return fmt.Errorf("%w: stop job %s", ErrPhaseCleanup, result)
			}
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrPhaseCleanup, ctx.Err())
		}
	}
	events, err := os.ReadFile(filepath.Join("/sys/fs/cgroup/system.slice", j.unit, "cgroup.events"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !strings.Contains(string(events), "populated 0\n") {
		return fmt.Errorf("%w: service cgroup is not confirmed empty", ErrPhaseCleanup)
	}
	return nil
}
