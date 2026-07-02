package console

import (
	"context"
	"io"
	"sync"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/pkg/log"
)

// ExecStreamer opens an interactive exec session inside a named container,
// returning the process's stdin/stdout as an io.ReadWriteCloser.
// *client.Podman satisfies this interface in production; tests may inject a fake.
type ExecStreamer interface {
	ExecStream(ctx context.Context, containerName string, cmd ...string) (io.ReadWriteCloser, error)
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
		defer func() { _ = streamClient.CloseSend() }()
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
