package console

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/pkg/log"
)

// ExecStreamer opens an interactive exec session inside a named container,
// returning the process's stdin/stdout as an io.ReadWriteCloser.
// *client.Podman satisfies this interface in production; tests may inject a fake.
type ExecStreamer interface {
	ExecStream(ctx context.Context, containerName string, cmd ...string) (io.ReadWriteCloser, error)
}

// evictionReasonKey is the context key under which Start stashes a flag that bridgeConn
// checks, right before its own CloseSend, to decide whether to report a reason for tearing
// the stream down.
type evictionReasonKey struct{}

// withEvictionReason attaches replaced to ctx so bridgeConn's send-side goroutine — the sole
// owner of Send() calls on the stream — can fold a final "replaced" notice into its normal
// teardown sequence. This avoids a separate goroutine calling Send() concurrently, which is
// not safe on a gRPC client stream.
func withEvictionReason(ctx context.Context, replaced *atomic.Bool) context.Context {
	return context.WithValue(ctx, evictionReasonKey{}, replaced)
}

// evictionReasonFromContext returns the message to send before closing, or "" if this
// teardown was not caused by a forced takeover.
func evictionReasonFromContext(ctx context.Context) string {
	replaced, ok := ctx.Value(evictionReasonKey{}).(*atomic.Bool)
	if !ok || !replaced.Load() {
		return ""
	}
	return "console session replaced by a new connection"
}

// bridgeConn copies data bidirectionally between conn and streamClient until
// either side closes or ctx is canceled. label is used only for debug logging.
func bridgeConn(ctx context.Context, label string, conn io.ReadWriteCloser, streamClient grpc_v1.RouterService_StreamClient, logger *log.PrefixLogger) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// conn → gRPC stream
	go func() {
		defer wg.Done()
		defer cancel()
		// Signal the server that no more data will be sent. This causes the server
		// to close its send side, which unblocks the Recv() call in the other goroutine.
		// If this teardown was triggered by a forced takeover, report that reason first —
		// this goroutine is the only one that ever calls Send(), so no locking is needed.
		defer func() {
			if reason := evictionReasonFromContext(ctx); reason != "" {
				if sendErr := streamClient.Send(&grpc_v1.StreamRequest{Error: reason}); sendErr != nil {
					logger.Debugf("%s: failed to send takeover notice over gRPC stream: %v", label, sendErr)
				}
			}
			if closeErr := streamClient.CloseSend(); closeErr != nil {
				logger.Debugf("%s: failed to close send side of gRPC stream: %v", label, closeErr)
			}
		}()
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				logger.Debugf("%s→gRPC: %d bytes", label, n)
				if sendErr := streamClient.Send(&grpc_v1.StreamRequest{Payload: buf[:n]}); sendErr != nil {
					logger.Debugf("send to gRPC stream failed: %v", sendErr)
					return
				}
			}
			if err != nil {
				logger.Debugf("%s connection read error: %v", label, err)
				return
			}
		}
	}()

	// gRPC stream → conn
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			msg, err := streamClient.Recv()
			if err == io.EOF || (msg != nil && msg.Closed) {
				return
			}
			if err != nil {
				logger.Debugf("recv from gRPC stream failed: %v", err)
				return
			}
			if len(msg.Payload) > 0 {
				logger.Debugf("gRPC→%s: %d bytes", label, len(msg.Payload))
				remaining := msg.Payload
				for len(remaining) > 0 {
					n, writeErr := conn.Write(remaining)
					if writeErr != nil {
						logger.Debugf("write to %s connection failed: %v", label, writeErr)
						return
					}
					remaining = remaining[n:]
				}
			}
		}
	}()

	wg.Wait()
}
