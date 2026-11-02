// Package vm runs image tasks inside disposable QEMU guests.
package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/util"
)

type Config struct {
	ExecutionTimeout util.Duration `json:"executionTimeout"`
	BaseImage        string        `json:"baseImage"`
	Accelerator      string        `json:"accelerator"`
	MemoryMiB        int           `json:"memoryMiB"`
	CPUs             int           `json:"cpus"`
	DiskSizeGiB      int           `json:"diskSizeGiB"`
	BootTimeout      util.Duration `json:"bootTimeout"`
}

func DefaultConfig() *Config {
	return &Config{ExecutionTimeout: util.Duration(2 * time.Hour), BaseImage: "/usr/share/flightctl-vm/guest.qcow2", Accelerator: "kvm", MemoryMiB: 4096, CPUs: 4, DiskSizeGiB: 24, BootTimeout: util.Duration(5 * time.Minute)}
}
func (c Config) Validate() error {
	if !filepath.IsAbs(c.BaseImage) || strings.ContainsAny(c.BaseImage, ",\n\r\x00") {
		return fmt.Errorf("vm.baseImage must be an absolute path without commas or control characters")
	}
	if c.Accelerator != "kvm" && c.Accelerator != "tcg" {
		return fmt.Errorf("vm.accelerator must be kvm or tcg")
	}
	if c.ExecutionTimeout < 0 {
		return fmt.Errorf("vm.executionTimeout must be positive")
	}
	if c.MemoryMiB < 512 || c.CPUs < 1 || c.DiskSizeGiB < 1 || time.Duration(c.BootTimeout) <= 0 {
		return fmt.Errorf("vm sizing and boot timeout must be positive (memory at least 512 MiB)")
	}
	return nil
}

// Session owns a guest; Close must finish before removing its input/output directories.
type Session struct {
	life                  context.Context
	work, outputDir, port string
	cancel                context.CancelFunc
	done                  chan struct{}
	once                  sync.Once
}

func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	configureProcess(cmd)
	return cmd
}
func run(ctx context.Context, name string, args ...string) error {
	out, err := command(ctx, name, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: %w: %s", name, err, out)
	}
	return nil
}

// Start boots a disposable overlay and scratch disk, sharing only the supplied directories.
func Start(ctx context.Context, cfg Config, inputDir, outputDir string) (_ *Session, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, fmt.Errorf("image task VMs currently support amd64 hosts only")
	}
	info, err := os.Stat(cfg.BaseImage)
	if err != nil {
		return nil, fmt.Errorf("guest base image: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("guest base image must be a regular qcow2 file")
	}
	inputDir, err = filepath.Abs(inputDir)
	if err != nil {
		return nil, err
	}
	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{inputDir, outputDir} {
		if strings.ContainsAny(p, ",\n\r\x00") {
			return nil, fmt.Errorf("VM share path contains unsupported characters")
		}
	}
	work, err := os.MkdirTemp("", "flightctl-vm-*")
	if err != nil {
		return nil, err
	}
	deadline := time.Duration(cfg.ExecutionTimeout)
	if deadline == 0 {
		deadline = 2 * time.Hour
	}
	life, cancel := context.WithTimeout(ctx, deadline)
	s := &Session{life: life, work: work, outputDir: outputDir, cancel: cancel, done: make(chan struct{})}
	started := false
	defer func() {
		if retErr != nil {
			cancel()
			if started {
				<-s.done
			}
			_ = os.RemoveAll(work)
		}
	}()
	if err := prepareGuestDisks(life, cfg, work); err != nil {
		return nil, err
	}
	seed := filepath.Join(work, "seed.iso")
	disk := filepath.Join(work, "guest.qcow2")
	scratch := filepath.Join(work, "scratch.raw")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.port = strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		return nil, err
	}
	cpu := "max"
	if cfg.Accelerator == "kvm" {
		cpu = "host"
	}
	args := []string{"-no-user-config", "-nodefaults", "-accel", cfg.Accelerator, "-cpu", cpu, "-m", strconv.Itoa(cfg.MemoryMiB), "-smp", strconv.Itoa(cfg.CPUs),
		"-drive", "file=" + disk + ",if=virtio,format=qcow2", "-drive", "file=" + scratch + ",if=virtio,format=raw", "-drive", "file=" + seed + ",if=virtio,format=raw,readonly=on",
		"-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:" + s.port + "-:22", "-device", "virtio-net-pci,netdev=net0",
		"-virtfs", "local,path=" + inputDir + ",mount_tag=build,security_model=none,readonly=on", "-virtfs", "local,path=" + outputDir + ",mount_tag=output,security_model=none",
		"-display", "none", "-serial", "file:" + filepath.Join(work, "console.log"), "-monitor", "none"}
	qemu := command(life, "qemu-system-x86_64", args...)
	logFile, err := os.OpenFile(filepath.Join(work, "qemu.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	qemu.Stdout = logFile
	qemu.Stderr = logFile
	if err := qemu.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	started = true
	go func() { _ = qemu.Wait(); _ = logFile.Close(); close(s.done) }()
	boot, bootCancel := context.WithTimeout(life, time.Duration(cfg.BootTimeout))
	defer bootCancel()
	for {
		if err := s.Run(boot, []string{"true"}, nil, nil, io.Discard); err == nil {
			break
		}
		select {
		case <-boot.Done():
			return nil, fmt.Errorf("guest boot: %w", boot.Err())
		case <-s.done:
			out, _ := os.ReadFile(filepath.Join(work, "qemu.log"))
			return nil, fmt.Errorf("QEMU exited during boot: %s", out)
		case <-time.After(time.Second):
		}
	}
	initScript := `# flightctl-vm-init
set -eu
mkdir -p /build /output /var/lib/containers
mkfs.ext4 -q -F /dev/vdb
mount /dev/vdb /var/lib/containers
mount -t 9p -o trans=virtio,version=9p2000.L,ro build /build
mount -t 9p -o trans=virtio,version=9p2000.L output /output
`
	if err := s.Run(boot, []string{"sh", "-s"}, nil, strings.NewReader(initScript), io.Discard); err != nil {
		return nil, fmt.Errorf("initialize guest: %w", err)
	}
	return s, nil
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Run streams guest output without logging arguments, environment or stdin.
func (s *Session) Run(ctx context.Context, args []string, env map[string]string, stdin io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.life, cancel)
	defer stop()
	select {
	case <-s.done:
		if s.life.Err() != nil {
			return s.life.Err()
		}
		return fmt.Errorf("guest is stopped")
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("guest command is empty")
	}
	words := []string{"exec", "env", "--"}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.ContainsAny(k, "=\x00") || k == "" {
			return fmt.Errorf("invalid guest environment name")
		}
		words = append(words, quote(k+"="+env[k]))
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("guest argument contains NUL")
		}
		words = append(words, quote(arg))
	}
	ssh := command(ctx, "ssh", "-i", filepath.Join(s.work, "key"), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "ConnectTimeout=2", "-o", "LogLevel=ERROR", "-p", s.port, "root@127.0.0.1", strings.Join(words, " "))
	ssh.Stdin = stdin
	ssh.Stdout = output
	ssh.Stderr = output
	if err := ssh.Run(); err != nil {
		if ctx.Err() != nil {
			if s.life.Err() != nil {
				return s.life.Err()
			}
			return ctx.Err()
		}
		return fmt.Errorf("guest command failed: %w", err)
	}
	return nil
}

// Close stops and reaps QEMU before releasing its disk and key, retaining caller-owned output.
func (s *Session) Close() error {
	s.once.Do(func() { s.cancel(); <-s.done; _ = os.RemoveAll(s.work) })
	return nil
}

func prepareGuestDisks(life context.Context, cfg Config, work string) error {
	if err := run(life, "ssh-keygen", "-q", "-t", "rsa", "-b", "3072", "-N", "", "-f", filepath.Join(work, "key")); err != nil {
		return err
	}
	pub, err := os.ReadFile(filepath.Join(work, "key.pub"))
	if err != nil {
		return err
	}
	data := "#cloud-config\nusers:\n  - name: root\n    ssh_authorized_keys:\n      - " + strings.TrimSpace(string(pub)) + "\ndisable_root: false\nssh_pwauth: false\n"
	if err := os.WriteFile(filepath.Join(work, "user-data"), []byte(data), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(work, "meta-data"), []byte("instance-id: "+filepath.Base(work)+"\n"), 0600); err != nil {
		return err
	}
	seed := filepath.Join(work, "seed.iso")
	if err := run(life, "genisoimage", "-quiet", "-output", seed, "-volid", "cidata", "-joliet", "-rock", filepath.Join(work, "user-data"), filepath.Join(work, "meta-data")); err != nil {
		return err
	}
	disk := filepath.Join(work, "guest.qcow2")
	if err := run(life, "qemu-img", "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", cfg.BaseImage, disk); err != nil {
		return err
	}
	scratch := filepath.Join(work, "scratch.raw")
	if err := run(life, "qemu-img", "create", "-q", "-f", "raw", scratch, fmt.Sprintf("%dG", cfg.DiskSizeGiB)); err != nil {
		return err
	}
	return nil
}
