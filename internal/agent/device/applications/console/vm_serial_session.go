package console

import (
	"context"
	"fmt"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/pkg/log"
)

// vmSerialSocketPath is the fixed Unix socket path inside the virt-launcher compute container
// where libvirt exposes the VM's serial console (created by virt-launcher at startup).
const vmSerialSocketPath = "/var/run/kubevirt-private/default/virt-serial0"

// vmSerialSession implements Session for VM serial console.
type vmSerialSession struct {
	containerName string
	exec          ExecStreamer
	log           *log.PrefixLogger
}

// NewVMSerialSession returns a Session that bridges the VM's serial socket to the gRPC stream.
// exec must not be nil; in production pass a *client.Podman, in tests pass a mock ExecStreamer.
func NewVMSerialSession(containerName string, exec ExecStreamer, log *log.PrefixLogger) Session {
	return &vmSerialSession{
		containerName: containerName,
		exec:          exec,
		log:           log,
	}
}

// Run implements Session. It dials the container's serial socket and bridges it to the gRPC stream.
func (s *vmSerialSession) Run(ctx context.Context, streamClient grpc_v1.RouterService_StreamClient) {
	s.log.Debugf("vm serial console session started for container %s", s.containerName)
	defer s.log.Debugf("vm serial console session finished for container %s", s.containerName)

	conn, err := s.exec.ExecStream(ctx, s.containerName, "nc", "-U", vmSerialSocketPath)
	if err != nil {
		sendErrorOverStream(streamClient, fmt.Sprintf("failed to connect to serial console for %s: %v", s.containerName, err))
		return
	}
	defer conn.Close()

	// Send an initial CR to wake up agetty, which waits for the first character
	// before displaying the login prompt (baud-rate detection on real hardware).
	// Best-effort: bridging proceeds regardless of whether the CR succeeds.
	// Run asynchronously so a synchronous connection (e.g. net.Pipe in tests)
	// does not block bridge startup.
	go func() {
		if _, err := conn.Write([]byte("\r")); err != nil {
			s.log.Debugf("failed to send initial CR to serial console: %v", err)
		}
	}()

	bridgeConn(ctx, "serial", conn, streamClient, s.log)
}
