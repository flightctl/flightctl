package vm_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/imagebuilder_worker/vm"
	"github.com/stretchr/testify/require"
)

func TestRejectSpecialArtifact(t *testing.T) {
	t.Run("When a guest returns a FIFO it should reject it without blocking", func(t *testing.T) {
		cfg := fakeRuntime(t)
		dir := t.TempDir()
		fifo := filepath.Join(dir, "disk.qcow2")
		require.NoError(t, syscall.Mkfifo(fifo, 0600))
		s, err := vm.Start(t.Context(), cfg, t.TempDir(), dir)
		require.NoError(t, err)
		defer s.Close()
		done := make(chan error, 1)
		go func() { _, err := s.VerifyArtifact(context.Background(), "disk.qcow2"); done <- err }()
		select {
		case err := <-done:
			require.Error(t, err)
		case <-time.After(200 * time.Millisecond):
			// Release a blocked open so the regression itself never leaks a goroutine.
			f, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
			require.NoError(t, err)
			defer f.Close()
			<-done
			t.Error("artifact validation blocked on a guest FIFO")
		}
	})
}
