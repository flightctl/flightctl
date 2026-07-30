package console

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

type recordingCloser struct {
	closed bool
}

func (c *recordingCloser) Read([]byte) (int, error)    { return 0, io.EOF }
func (c *recordingCloser) Write(p []byte) (int, error) { return len(p), nil }
func (c *recordingCloser) Close() error {
	c.closed = true
	return nil
}

func TestDialVMUnixSocket_WhenExecIsNilItShouldReturnError(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	conn, err := dialVMUnixSocket(context.Background(), nil, "compute", vmSerialSocketPath, logger)
	require.Error(err)
	require.Nil(conn)
	require.Contains(err.Error(), "no exec streamer")
}

func TestDialVMUnixSocket_WhenOpeningItShouldReapLeftoverNCBeforeDial(t *testing.T) {
	require := require.New(t)
	closer := &recordingCloser{}
	exec := &mockExecStreamer{conn: closer}
	logger := log.NewPrefixLogger("test")
	socketPath := "/var/run/kubevirt-private/default/virt-serial0"

	conn, err := dialVMUnixSocket(context.Background(), exec, "compute", socketPath, logger)
	require.NoError(err)
	require.NotNil(conn)

	require.Len(exec.execCalls, 1, "expected a pre-dial cleanup Exec call")
	require.Contains(strings.Join(exec.execCalls[0], " "), socketPath)
	require.Contains(strings.Join(exec.execCalls[0], " "), "pkill")
	require.Equal(1, exec.streamN)

	require.NoError(conn.Close())
	require.True(closer.closed)
	require.Len(exec.execCalls, 1, "Close must not pkill (avoids killing a --force successor's nc)")
}

func TestDialVMUnixSocket_WhenDialFailsItShouldRetryUpToThreeTimes(t *testing.T) {
	require := require.New(t)
	closer := &recordingCloser{}
	exec := &mockExecStreamer{
		conn: closer,
		streamErrs: []error{
			fmt.Errorf("busy"),
			fmt.Errorf("busy"),
			nil,
		},
	}
	logger := log.NewPrefixLogger("test")

	start := time.Now()
	conn, err := dialWithRetryDelay(t, context.Background(), exec, "compute", vmSerialSocketPath, logger, time.Millisecond)
	require.NoError(err)
	require.NotNil(conn)
	require.Equal(3, exec.streamN)
	require.Len(exec.execCalls, 3, "each attempt reaps before dial")
	require.Less(time.Since(start), time.Second)
	require.NoError(conn.Close())
}

func TestDialVMUnixSocket_WhenAllDialAttemptsFailItShouldReturnError(t *testing.T) {
	require := require.New(t)
	exec := &mockExecStreamer{
		streamErrs: []error{
			fmt.Errorf("busy"),
			fmt.Errorf("busy"),
			fmt.Errorf("still busy"),
		},
	}
	logger := log.NewPrefixLogger("test")

	conn, err := dialWithRetryDelay(t, context.Background(), exec, "compute", vmSerialSocketPath, logger, time.Millisecond)
	require.Error(err)
	require.Nil(conn)
	require.Contains(err.Error(), "after 3 attempts")
	require.Equal(3, exec.streamN)
	require.Len(exec.execCalls, 3)
}

func TestDialVMUnixSocket_WhenOldSessionClosesAfterNewDialItShouldNotReap(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")
	socketPath := vmSerialSocketPath
	shared := &mockExecStreamer{conn: &recordingCloser{}}

	oldConn, err := dialVMUnixSocket(context.Background(), shared, "compute", socketPath, logger)
	require.NoError(err)
	require.Len(shared.execCalls, 1)

	// Simulate --force: new dial starts while old session still open.
	shared.conn = &recordingCloser{}
	newConn, err := dialVMUnixSocket(context.Background(), shared, "compute", socketPath, logger)
	require.NoError(err)
	require.Len(shared.execCalls, 2, "new dial issues its own pre-dial pkill")

	require.NoError(oldConn.Close())
	require.Len(shared.execCalls, 2, "old Close must not issue another pkill")
	require.NoError(newConn.Close())
	require.Len(shared.execCalls, 2)
}

func TestKillNCSocketHolders_WhenPatternBuiltItShouldNotMatchLogSuffix(t *testing.T) {
	require := require.New(t)
	exec := &mockExecStreamer{}
	logger := log.NewPrefixLogger("test")

	killNCSocketHolders(context.Background(), exec, "compute", vmSerialSocketPath, logger)
	require.Len(exec.execCalls, 1)
	script := strings.Join(exec.execCalls[0], " ")
	require.Contains(script, "^nc -U "+vmSerialSocketPath+"$")
	require.NotContains(script, "virt-serial0-log")
}

// dialWithRetryDelay runs dialVMUnixSocket with a shortened retry delay for tests.
func dialWithRetryDelay(t *testing.T, ctx context.Context, exec ExecStreamer, containerName, socketPath string, logger *log.PrefixLogger, delay time.Duration) (io.ReadWriteCloser, error) {
	t.Helper()
	prev := dialRetryDelay
	dialRetryDelay = delay
	t.Cleanup(func() { dialRetryDelay = prev })
	return dialVMUnixSocket(ctx, exec, containerName, socketPath, logger)
}
