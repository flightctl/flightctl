package console

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/containers/storage/pkg/pools"
	"github.com/creack/pty"
	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
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
	executor         executer.Executer
}

type TerminalSize struct {
	Width  uint16
	Height uint16
}

func NewController(
	grpcClient grpc_v1.RouterServiceClient,
	deviceName string,
	executor executer.Executer,
	log *log.PrefixLogger,
) *ConsoleController {
	return &ConsoleController{
		grpcClient: grpcClient,
		deviceName: deviceName,
		executor:   executor,
		log:        log,
	}
}

func (c *ConsoleController) Sync(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	c.log.Debug("Syncing console status")
	defer c.log.Debug("Finished syncing console status")

	desiredConsoles := desired.GetConsoles()
	// do we have an open console stream, and we are supposed to close it?
	if len(desiredConsoles) == 0 {
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

	// TODO: handle multiple consoles
	console := desiredConsoles[0]

	// TO-DO: manage the situation where a new console is requested while the current one is still open
	// if we have an active console, and the session ID is the same, we should keep it open
	if c.active && c.streamClient != nil {
		c.log.Infof("active console on session %s", console.SessionID)
		return nil
	}

	if c.lastClosedStream == console.SessionID {
		c.log.Debugf("console session %s was closed, not opening again", console.SessionID)
		return nil
	}

	if c.grpcClient == nil {
		c.log.Errorf("no gRPC client available, cannot start console session to %s", console.SessionID)
		return nil
	}
	c.log.Infof("starting console for session %s", console.SessionID)
	// add key-value pairs of metadata to context
	ctx = metadata.AppendToOutgoingContext(ctx, consts.GrpcSessionIDKey, console.SessionID)
	ctx = metadata.AppendToOutgoingContext(ctx, consts.GrpcClientNameKey, c.deviceName)

	stdin, stdout, err := c.bashProcess(ctx)
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
	c.currentStreamID = console.SessionID

	go func() {
		c.log.Info("starting console forwarding")
		err := c.startForwarding(ctx, stdin, stdout)
		// make sure that we wait for the command to finish, otherwise we will have a zombie process
		if err != nil {
			c.log.Errorf("error forwarding console ended for session %s: %v", console.SessionID, err)
		}
		c.log.Infof("console session %s ended", console.SessionID)
		// if the stream has been closed we won't try to re-open it until we find a
		// new session ID
		c.lastClosedStream = console.SessionID
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
		c.log.Info("startForwarding: closing stream for console")

	}()
	g, _ := errgroup.WithContext(ctx)

	g.Go(func() error {
		defer stdout.Close() // close the other side to make the other forward function leave
		defer c.log.Debug("stream > bash: leaving forward loop")
		c.log.Debug("stream > bash: entering forward loop")
		for {
			msg, err := stream.Recv()
			if err == io.EOF || msg != nil && msg.Closed {
				c.log.Info("stream > bash: connection closed")
				return nil
			}
			if err != nil {
				c.log.Errorf("stream > bash:error receiving message for stdin: %s", err)
				return fmt.Errorf("stream > bash:error receiving message for stdin: %w", err)
			}
			payload := msg.GetPayload()
			c.log.Debugf("stream > bash: received: %s", (string)(payload))
			_, err = stdin.Write(payload)
			if errors.Is(err, io.ErrClosedPipe) {
				c.log.Error("stream > bash: stdin closed")
				return nil
			}
			if err != nil {
				c.log.Errorf("stream > bash: error writing to stdin: %s", err)
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
		defer c.log.Debug("bash > stream: leaving forward loop")
		c.log.Debug("bash > stream: entering forward loop")
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
				c.log.Debug("bash > stream: stdout from bash ended")
				return nil
			}
			if readErr != nil {
				c.log.Errorf("bash > stream: error reading from bash stdout: %s", readErr)
				return fmt.Errorf("bash > stream: error reading from stdout: %w", readErr)
			}

			c.log.Debugf("bash > stream: sent: %q", (string)(buffer[:n]))

		}
	})

	return g.Wait()

}

func (c *ConsoleController) bashProcess(ctx context.Context) (io.WriteCloser, io.ReadCloser, error) {

	// create a new bash process
	cmd := c.executor.CommandContext(ctx, "bash", "-i", "-l")
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")

	// create a new PTY
	p, err := pty.Start(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("error starting pty: %w", err)
	}

	c.log.Info("bash process started under pty")

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()

	var stdinErr, stdoutErr error

	go func() {
		// copy from stdinWriter to pty
		_, stdinErr = pools.Copy(p, stdinReader)
		_ = cmd.Process.Kill()
	}()
	go func() {
		// copy from pty to stdoutWriter
		_, stdoutErr = pools.Copy(stdoutWriter, p)
		stdoutWriter.Close()
		_ = cmd.Process.Kill()
	}()

	// set the initial terminal size until we receive a window resize event from the other side
	size := TerminalSize{Width: 80, Height: 25}
	if err := setSize(p.Fd(), size); err != nil {
		return nil, nil, fmt.Errorf("error setting terminal size: %w", err)
	}

	go func() {
		defer p.Close()
		err := cmd.Wait()
		if stdinErr != nil {
			c.log.Errorf("error copying to stdin: %v", stdinErr)
		}
		if stdoutErr != nil {
			c.log.Errorf("error copying from stdout: %v", stdoutErr)
		}
		if err != nil {
			c.log.Errorf("error waiting for bash process: %v", err)
		} else {
			c.log.Info("bash process exited successfully")
		}
	}()
	return stdinWriter, stdoutReader, nil
}

func setSize(fd uintptr, size TerminalSize) error {
	winsize := &unix.Winsize{Row: size.Height, Col: size.Width}
	return unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, winsize)
}
