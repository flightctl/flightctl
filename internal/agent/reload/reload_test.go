package reload

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

var mu sync.Mutex

func sendSignal(t *testing.T) {
	// Simulate sending a SIGHUP signal
	signal := syscall.SIGHUP
	err := syscall.Kill(os.Getpid(), signal)
	require.NoError(t, err, "Failed to send SIGHUP signal")
}

func runTest(t *testing.T, rw fileio.ReadWriter, testdataSubdir, expected string) {
	mu.Lock()
	defer mu.Unlock()

	src := filepath.Join("testdata", testdataSubdir)
	workDir := copyTestdata(t, rw, src)
	configFile := filepath.Join(workDir, "config.yaml")

	t.Setenv("FLIGHTCTL_TEST_ROOT_DIR", workDir)
	writeDummyCerts(t, workDir)

	log := log.NewPrefixLogger("test")
	reloader := NewManager(configFile, log)

	var (
		called   atomic.Bool
		received atomic.Pointer[string]
	)
	reloader.Register(func(ctx context.Context, config *config.Config) error {
		received.Store(&config.LogLevel)
		called.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go reloader.Run(ctx)

	time.Sleep(5 * time.Millisecond)
	sendSignal(t)

	require.Eventually(t, func() bool { return called.Load() }, time.Second, 5*time.Millisecond)
	require.Equal(t, expected, lo.FromPtr(received.Load()))
}

func TestReloader(t *testing.T) {
	t.Run("No level definition fallback to default", func(t *testing.T) {
		tmpDir := t.TempDir()
		rw := fileio.NewReadWriter()
		rw.SetRootdir(tmpDir)
		runTest(t, rw, "t1", "info")
	})
	t.Run("Only top level definition", func(t *testing.T) {
		tmpDir := t.TempDir()
		rw := fileio.NewReadWriter()
		rw.SetRootdir(tmpDir)
		runTest(t, rw, "t2", "warning")
	})
	t.Run("With single override", func(t *testing.T) {
		tmpDir := t.TempDir()
		rw := fileio.NewReadWriter()
		rw.SetRootdir(tmpDir)
		runTest(t, rw, "t3", "debug")
	})
	t.Run("With 2 overrides", func(t *testing.T) {
		tmpDir := t.TempDir()
		rw := fileio.NewReadWriter()
		rw.SetRootdir(tmpDir)
		runTest(t, rw, "t4", "info")
	})
	t.Run("With illegal file name", func(t *testing.T) {
		tmpDir := t.TempDir()
		rw := fileio.NewReadWriter()
		rw.SetRootdir(tmpDir)
		runTest(t, rw, "t5", "warning")
	})
}

func writeDummyCerts(t *testing.T, root string) {
	t.Helper()
	for _, rel := range []string{
		"etc/flightctl/certs/ca.crt",
		"var/lib/flightctl/certs/client-enrollment.crt",
		"var/lib/flightctl/certs/client-enrollment.key",
	} {
		full := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte("dummy"), 0600))
	}
}

func copyTestdata(t *testing.T, rw fileio.ReadWriter, src string) string {
	t.Helper()
	src = filepath.Clean(src)
	dstRoot := rw.PathFor(".")
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		require.NoError(t, err)

		rel, err := filepath.Rel(src, path)
		require.NoError(t, err)

		dstPath := filepath.Join(rw.PathFor(rel))
		if info.IsDir() {
			return rw.MkdirAll(dstPath, 0755)
		}

		data, err := os.ReadFile(path)
		require.NoError(t, err)

		return rw.WriteFile(dstPath, data, info.Mode())
	})
	require.NoError(t, err)

	return dstRoot
}
