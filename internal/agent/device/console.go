package device

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api_server/agentserver"
	"github.com/flightctl/flightctl/pkg/log"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/metadata"
)

type ConsoleController struct {
	grpcClient grpc_v1.RouterServiceClient
	log        *log.PrefixLogger
	deviceName string

	active           bool
	streamClient     grpc_v1.RouterService_StreamClient
	currentStreamID  string
	lastClosedStream string
}

func NewConsoleController(grpcClient grpc_v1.RouterServiceClient, deviceName string, log *log.PrefixLogger) *ConsoleController {
	return &ConsoleController{
		grpcClient: grpcClient,
		deviceName: deviceName,
		log:        log,
	}
}

func (c *ConsoleController) Sync(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	c.log.Debug("Syncing console status")
	defer c.log.Debug("Finished syncing console status")

	// do we have an open console stream, and we are supposed to close it?
	if desired.Console == nil {
		c.log.Debug("No desired console")
		if c.active {
			if c.streamClient != nil {
				err := c.streamClient.CloseSend()
				if err != nil {
					return fmt.Errorf("failed to close console stream: %w", err)
				}
			}
			c.active = false
		}
		return nil
	}

	// TO-DO: manage the situation where a new console is requested while the current one is still open
	// if we have an active console, and the session ID is the same, we should keep it open
	if c.active && c.streamClient != nil {
		c.log.Infof("active console on session %s", desired.Console.SessionID)
		return nil
	}

	if c.lastClosedStream == desired.Console.SessionID {
		c.log.Debugf("console session %s was closed, not opening again", desired.Console.SessionID)
		return nil
	}

	if c.grpcClient == nil {
		c.log.Errorf("no gRPC client available, cannot start console session to %s", desired.Console.SessionID)
		return nil
	}
	c.log.Infof("starting console for session %s", desired.Console.SessionID)
	// add key-value pairs of metadata to context, for now we are ignoring the Console.GRPCEndpoint
	ctx = metadata.AppendToOutgoingContext(ctx, agentserver.SessionIDKey, desired.Console.SessionID)
	ctx = metadata.AppendToOutgoingContext(ctx, agentserver.ClientNameKey, c.deviceName)

	stdin, stdout, err := c.bashProcess()
	if err != nil {
		return fmt.Errorf("error creating shell process: %w", err)
	}

	c.log.Info("console opening stream")
	// open a new console stream
	streamClient, err := c.grpcClient.Stream(ctx)
	if err != nil {
		return fmt.Errorf("error creating console stream client: %w", err)
	}
	c.streamClient = streamClient
	c.active = true
	c.currentStreamID = desired.Console.SessionID

	go func() {
		c.log.Info("starting console forwarding")
		err := c.startForwarding(ctx, stdin, stdout)
		// make sure that we wait for the command to finish, otherwise we will have a zombie process
		if err != nil {
			c.log.Errorf("error forwarding console ended for session %s: %v", desired.Console.SessionID, err)
		}
		c.log.Infof("console session %s ended", desired.Console.SessionID)
		// if the stream has been closed we won't try to re-open it until we find a
		// new session ID
		c.lastClosedStream = desired.Console.SessionID
		c.active = false
		c.streamClient = nil
	}()

	return nil
}

func (c *ConsoleController) startForwarding(ctx context.Context, stdin io.WriteCloser, stdout io.ReadCloser) error {
	stream := c.streamClient

	defer func() {
		stdin.Close()
		stdout.Close()
		// finally this should end the other forward function
		_ = stream.CloseSend()
		c.log.Infof("startForwarding: closing stream for console")

	}()
	g, _ := errgroup.WithContext(ctx)

	g.Go(func() error {
		defer stdout.Close() // close the other side to make the other forward function leave
		defer c.log.Infof("stream > bash: leaving forward loop")
		c.log.Infof("stream > bash: entering forward loop")
		for {
			msg, err := stream.Recv()
			if err == io.EOF || msg != nil && msg.Closed {
				c.log.Infof("stream > bash: connection closed")
				return nil
			}
			if err != nil {
				c.log.Errorf("stream > bash:error receiving message for stdin: %s", err)
				return fmt.Errorf("stream > bash:error receiving message for stdin: %w", err)
			}
			payload := msg.GetPayload()
			c.log.Infof("stream > bash: received: %s", (string)(payload))
			_, err = stdin.Write(payload)
			if errors.Is(err, io.ErrClosedPipe) {
				c.log.Infof("stream > bash: stdin closed")
				return nil
			}
			if err != nil {
				c.log.Infof("stream > bash: error writing to stdin: %s", err)
				return fmt.Errorf("stream > bash: error writing to stdin: %w", err)
			}
		}
	})

	g.Go(func() error {
		defer func() {
			// probably CloseSend is enough for the client, just in case...
			err := stream.Send(&grpc_v1.StreamRequest{
				Closed: true,
			})
			if err != nil {
				c.log.Errorf("bash > stream:: error sending close message to server: %s", err)
			}
			// finally this should end the other forward function
			_ = stream.CloseSend()
		}()
		defer c.log.Infof("bash > stream: leaving forward loop")
		c.log.Infof("bash > stream: entering forward loop")
		for {
			buffer := make([]byte, 4096)
			n, readErr := stdout.Read(buffer)
			// according to the docs, Read can return EOF and data at the same time, so
			// we should process the data first
			if n > 0 {
				err := stream.Send(&grpc_v1.StreamRequest{
					Payload: buffer[:n],
				})

				if err != nil {
					c.log.Errorf("bash > stream: error sending: %q, %s", (string)(buffer[:n]), err)
					return fmt.Errorf("bash > stream: error sending message for stdout: %w", err)
				}
			}

			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrClosedPipe) {
				c.log.Infof("bash > stream: stdout from bash ended")
				return nil
			}
			if readErr != nil {
				c.log.Infof("bash > stream: error reading from bash stdout: %s", readErr)
				return fmt.Errorf("bash > stream: error reading from stdout: %w", readErr)
			}

			c.log.Infof("bash > stream: sent: %q", (string)(buffer[:n]))

		}
	})

	return g.Wait()

}

func (c *ConsoleController) bashProcess() (io.WriteCloser, io.ReadCloser, error) {
	// TODO pty: this is how oci does a PTY:
	// https://github.com/cri-o/cri-o/blob/main/internal/oci/oci_unix.go
	//
	// set PS1 environment variable to make bash print the default prompt
	cmd := exec.Command("bash", "-i", "-l")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting stdout pipe: %w", err)
	}

	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("error starting bash process: %w", err)
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			c.log.Errorf("error waiting for bash process: %v", err)
		} else {
			c.log.Info("bash process exited succesfully")
		}
	}()
	return stdin, stdout, nil
}
