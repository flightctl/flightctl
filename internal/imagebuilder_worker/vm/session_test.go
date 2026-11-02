//go:build linux

package vm_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/imagebuilder_worker/vm"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/stretchr/testify/require"
)

func TestCanceledStart(t *testing.T) {
	t.Run("When a task is canceled it should not launch a guest", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := vm.Start(ctx, vm.Config{}, t.TempDir(), t.TempDir())
		require.ErrorIs(t, err, context.Canceled)
	})
}

func fakeRuntime(t *testing.T) vm.Config {
	t.Helper()
	bin := t.TempDir()
	scripts := map[string]string{
		"qemu-img":           "exit 0",
		"genisoimage":        "exit 0",
		"ssh-keygen":         "while [ $# -gt 0 ]; do if [ \"$1\" = -f ]; then shift; printf 'ssh-ed25519 test\\n' > \"$1.pub\"; fi; shift; done",
		"qemu-system-x86_64": "exec sleep 300",
		"ssh": `for arg do cmd=$arg; done
case "$cmd" in *sha256sum*) printf "%s  disk.qcow2\n" "$VM_TEST_CHECKSUM"; exit 0;; *"'sh' '-s'"*) cat >/dev/null; exit 0;; esac
exec /bin/sh -c "$cmd"`,
	}
	for name, body := range scripts {
		//nolint:gosec // G306: The temporary executable fixture is private to this test.
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700))
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	base := filepath.Join(t.TempDir(), "guest.qcow2")
	require.NoError(t, os.WriteFile(base, []byte("base disk"), 0600))
	return vm.Config{BaseImage: base, Accelerator: "tcg", MemoryMiB: 1024, CPUs: 1, DiskSizeGiB: 4, BootTimeout: util.Duration(5 * time.Second)}
}

func TestGuestCommands(t *testing.T) {
	t.Run("When guest arguments contain shell syntax it should preserve their literal values", func(t *testing.T) {
		cfg := fakeRuntime(t)
		session, err := vm.Start(t.Context(), cfg, t.TempDir(), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, session.Close()) })
		var out bytes.Buffer
		err = session.Run(t.Context(), []string{"printf", "%s", "a'; $(echo injected)"}, nil, nil, &out)
		require.NoError(t, err)
		require.Equal(t, "a'; $(echo injected)", out.String())
	})
}

func TestArtifactReceipt(t *testing.T) {
	t.Run("When an export checksum differs it should refuse the artifact", func(t *testing.T) {
		cfg := fakeRuntime(t)
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "disk.qcow2"), []byte("hello"), 0600))
		t.Setenv("VM_TEST_CHECKSUM", strings.Repeat("0", 64))
		s, err := vm.Start(t.Context(), cfg, t.TempDir(), dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, s.Close()) })
		_, err = s.VerifyArtifact(t.Context(), "disk.qcow2")
		require.ErrorContains(t, err, "checksum mismatch")
	})
}

func TestClosedSession(t *testing.T) {
	t.Run("When a guest has been closed it should refuse further execution", func(t *testing.T) {
		s, err := vm.Start(t.Context(), fakeRuntime(t), t.TempDir(), t.TempDir())
		require.NoError(t, err)
		require.NoError(t, s.Close())
		require.ErrorIs(t, s.Run(t.Context(), []string{"true"}, nil, nil, io.Discard), context.Canceled)
		require.NoError(t, s.Close())
	})
}

// TestRealGuest is an opt-in check of boot, SSH and 9p using a prepared local disk.
func TestRealGuest(t *testing.T) {
	image := os.Getenv("FLIGHTCTL_VM_SMOKE_IMAGE")
	if image == "" {
		t.Skip("set FLIGHTCTL_VM_SMOKE_IMAGE to a prepared qcow2")
	}
	cfg := vm.DefaultConfig()
	cfg.BaseImage = image
	cfg.CPUs = 1
	cfg.MemoryMiB = 1024
	cfg.DiskSizeGiB = 1
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	s, err := vm.Start(ctx, *cfg, t.TempDir(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	var out bytes.Buffer
	require.NoError(t, s.Run(ctx, []string{"sh", "-c", "printf hello > /output/probe; id -u; podman info --format '{{.Store.GraphRoot}}'; findmnt -n -o FSTYPE /var/lib/containers"}, nil, nil, &out))
	require.Equal(t, "0\n/var/lib/containers/storage\next4\n", out.String())
	path, err := s.VerifyArtifact(ctx, "probe")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
}

func TestTaskCancellation(t *testing.T) {
	t.Run("When a task is canceled it should stop an active guest command", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s, err := vm.Start(ctx, fakeRuntime(t), t.TempDir(), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, s.Close()) })
		r, w := io.Pipe()
		defer r.Close()
		defer w.Close()
		done := make(chan error, 1)
		go func() { done <- s.Run(t.Context(), []string{"sh", "-c", "printf ready; exec sleep 300"}, nil, nil, w) }()
		ready := make([]byte, 5)
		_, err = io.ReadFull(r, ready)
		require.NoError(t, err)
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(3 * time.Second):
			t.Fatal("guest command survived task cancellation")
		}
	})
}

func TestExecutionDeadline(t *testing.T) {
	t.Run("When a VM exceeds its execution deadline it should stop the guest command", func(t *testing.T) {
		cfg := fakeRuntime(t)
		cfg.ExecutionTimeout = util.Duration(time.Second)
		s, err := vm.Start(t.Context(), cfg, t.TempDir(), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, s.Close()) })
		err = s.Run(t.Context(), []string{"sleep", "300"}, nil, nil, io.Discard)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestValidatedArtifact(t *testing.T) {
	t.Run("When guest and host checksums match it should return the artifact", func(t *testing.T) {
		cfg := fakeRuntime(t)
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "disk.qcow2"), []byte("hello"), 0600))
		t.Setenv("VM_TEST_CHECKSUM", "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824")
		s, err := vm.Start(t.Context(), cfg, t.TempDir(), dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, s.Close()) })
		path, err := s.VerifyArtifact(t.Context(), "disk.qcow2")
		require.NoError(t, err)
		require.Equal(t, filepath.Join(dir, "disk.qcow2"), path)
	})
	t.Run("When an artifact is a symlink it should refuse publication", func(t *testing.T) {
		cfg := fakeRuntime(t)
		dir := t.TempDir()
		require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "disk.qcow2")))
		s, err := vm.Start(t.Context(), cfg, t.TempDir(), dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, s.Close()) })
		_, err = s.VerifyArtifact(t.Context(), "disk.qcow2")
		require.ErrorContains(t, err, "symlink")
	})
}

func TestRestrictedCryptoPolicy(t *testing.T) {
	t.Run("When the host disallows Ed25519 it should still boot a guest", func(t *testing.T) {
		cfg := fakeRuntime(t)
		keygen := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "ssh-keygen")
		script := `#!/bin/sh
while [ $# -gt 0 ]; do
 if [ "$1" = ed25519 ]; then exit 1; fi
 if [ "$1" = -f ]; then shift; printf 'ssh-rsa test\n' > "$1.pub"; fi
 shift
done
`
		//nolint:gosec // G306: The temporary executable fixture is private to this test.
		require.NoError(t, os.WriteFile(keygen, []byte(script), 0700))
		s, err := vm.Start(t.Context(), cfg, t.TempDir(), t.TempDir())
		require.NoError(t, err)
		require.NoError(t, s.Close())
	})
}

func TestBoundedArtifactRead(t *testing.T) {
	t.Run("When guest metadata exceeds its limit it should refuse the read", func(t *testing.T) {
		cfg := fakeRuntime(t)
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "push-digest.txt"), []byte("untrusted oversized metadata"), 0600))
		s, err := vm.Start(t.Context(), cfg, t.TempDir(), dir)
		require.NoError(t, err)
		defer s.Close()
		_, err = s.ReadArtifact(t.Context(), "push-digest.txt", 8)
		require.ErrorContains(t, err, "size limit")
	})
}
