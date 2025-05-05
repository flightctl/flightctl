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

func runTest(t *testing.T, dir, expected string) {
	mu.Lock()
	defer mu.Unlock()
	log := log.NewPrefixLogger("test")
	configFile := filepath.Join(dir, "config.yaml")
	reloader := NewManager(configFile, dir, log)
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
	const testDir = "testdata"
	t.Run("No level definition fallback to default", func(t *testing.T) {
		runTest(t, filepath.Join(testDir, "t1"), "info")
	})
	t.Run("Only top level definition", func(t *testing.T) {
		runTest(t, filepath.Join(testDir, "t2"), "warning")
	})
	t.Run("With single override", func(t *testing.T) {
		runTest(t, filepath.Join(testDir, "t3"), "debug")
	})
	t.Run("With 2 overrides", func(t *testing.T) {
		runTest(t, filepath.Join(testDir, "t4"), "info")
	})
	t.Run("With illegal file name", func(t *testing.T) {
		runTest(t, filepath.Join(testDir, "t5"), "warning")
	})
}
