package console

import (
	"context"
	"fmt"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/pkg/log"
)

// vmVNCSocketPath is the fixed Unix socket path inside the virt-launcher compute container
// where libvirt exposes the VM's VNC server.
const vmVNCSocketPath = "/var/run/kubevirt-private/default/virt-vnc"

// vmVNCSession implements Session for VM VNC console.
type vmVNCSession struct {
	containerName string
	exec          ExecStreamer
	log           *log.PrefixLogger
}

// NewVMVNCSession returns a Session that bridges the VM's VNC socket to the gRPC stream.
// exec must not be nil; in production pass a *client.Podman, in tests pass a mock ExecStreamer.
func NewVMVNCSession(containerName string, exec ExecStreamer, log *log.PrefixLogger) Session {
	return &vmVNCSession{
		containerName: containerName,
		exec:          exec,
		log:           log,
	}
}

// Run implements Session. It dials the container's VNC socket and bridges it to the gRPC stream.
// No initial byte is sent — VNC clients initiate the RFB handshake themselves.
func (s *vmVNCSession) Run(ctx context.Context, streamClient grpc_v1.RouterService_StreamClient) {
	s.log.Debugf("vm vnc console session started for container %s", s.containerName)
	defer s.log.Debugf("vm vnc console session finished for container %s", s.containerName)

	if s.exec == nil {
		sendErrorOverStream(streamClient, fmt.Sprintf("vnc session misconfigured: no exec streamer for %s", s.containerName))
		return
	}

	conn, err := s.exec.ExecStream(ctx, s.containerName, "nc", "-U", vmVNCSocketPath)
	if err != nil {
		sendErrorOverStream(streamClient, fmt.Sprintf("failed to connect to VNC console for %s: %v", s.containerName, err))
		return
	}
	defer conn.Close()

	bridgeConn(ctx, "vnc", conn, streamClient, s.log)
}
