package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/api_server/agentserver"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
	"google.golang.org/grpc/metadata"
)

type ConsoleOptions struct {
	GlobalOptions
}

func DefaultConsoleOptions() *ConsoleOptions {
	return &ConsoleOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func NewConsoleCmd() *cobra.Command {
	o := DefaultConsoleOptions()

	cmd := &cobra.Command{
		Use:   "console device/NAME",
		Short: "Connect a console to the remote device through the server.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), args)
		},
		SilenceUsage: true,
	}

	o.Bind(cmd.Flags())

	return cmd
}

func (o *ConsoleOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
}

func (o *ConsoleOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *ConsoleOptions) Validate(args []string) error {
	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	if kind != DeviceKind {
		return fmt.Errorf("only devices can be connected to a console")
	}

	if len(name) == 0 {
		return fmt.Errorf("device name is required")
	}
	return nil
}

func (o *ConsoleOptions) Run(ctx context.Context, args []string) error { // nolint: gocyclo
	config, err := client.ParseConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}
	c, err := client.NewFromConfig(config)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	_, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}
	console, err := c.RequestConsoleWithResponse(ctx, name)

	if err != nil {
		return fmt.Errorf("error requesting console: %w", err)
	}

	if console.HTTPResponse.StatusCode != 200 {
		return fmt.Errorf("error requesting console: %s, %+v", console.HTTPResponse.Status, console.HTTPResponse.Body)
	}

	grpcEndpoint := console.JSON200.GRPCEndpoint
	sessionID := console.JSON200.SessionID

	err = o.connectViaGRPC(ctx, grpcEndpoint, sessionID, config.AuthInfo.Token)
	if err == io.EOF {
		fmt.Println("Connection closed")
		return nil
	}
	return err
}

// TODO: Move this to a websocket call instead later, the console endpoint will redirect to a ws method
func (o *ConsoleOptions) connectViaGRPC(ctx context.Context, grpcEndpoint, sessionID string, token string) error {
	//grpcEndpoint = "grpcs://192.168.1.10:7444"
	grpcEndpoint = strings.TrimRight(grpcEndpoint, "/")
	fmt.Printf("Connecting to %s with session id %s\n", grpcEndpoint, sessionID)
	client, err := client.NewGrpcClientFromConfigFile(o.ConfigFilePath, grpcEndpoint)
	if err != nil {
		return fmt.Errorf("creating grpc client: %w", err)
	}
	// add key-value pairs of metadata to context
	ctx = metadata.AppendToOutgoingContext(ctx, agentserver.SessionIDKey, sessionID)
	ctx = metadata.AppendToOutgoingContext(ctx, agentserver.ClientNameKey, "flightctl-cli")
	ctx = metadata.AppendToOutgoingContext(ctx, common.AuthHeader, fmt.Sprintf("Bearer %s", token))

	stream, err := client.Stream(ctx)
	if err != nil {
		return fmt.Errorf("error creating stream: %w", err)
	}

	return forwardStdio(ctx, stream)

}

func forwardStdio(ctx context.Context, stream grpc_v1.RouterService_StreamClient) error {
	g, _ := errgroup.WithContext(ctx)
	stdout := os.Stdout
	consoleIsRaw := true

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error making terminal raw: %s\n", err)
		consoleIsRaw = false
	}

	fmt.Printf("Use CTRL+B 3 times to exit console\r\n")

	resetConsole := func() {
		if consoleIsRaw {
			err := term.Restore(int(os.Stdin.Fd()), oldState)
			consoleIsRaw = false
			// reset terminal and clear screen
			if err != nil {
				fmt.Printf("error restoring terminal: %v", err)
			}
		}
		fmt.Print("\033c\033[2J\033[H")
		os.Exit(0)
	}

	stdioChan := make(chan byte, 8)
	go func() {
		buffer := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buffer)
			if err != nil {
				close(stdioChan)
				return
			}
			if n == 0 {
				continue
			}
			stdioChan <- buffer[0]
		}
	}()

	g.Go(func() error {
		defer func() {
			_ = stream.Send(&grpc_v1.StreamRequest{
				Closed: true,
			})
			stdout.Close()
			_ = stream.CloseSend()
			// we need to allow some time for gRPC to complete
			// the send close before reset console will exit proccess.
			time.Sleep(1 * time.Second)
			resetConsole()
		}()

		buffer := make([]byte, 1)
		ctrlBCount := 0
		for {
			chr, isOpen := <-stdioChan
			buffer[0] = chr

			if !isOpen {
				return nil
			}

			err = stream.Send(&grpc_v1.StreamRequest{Payload: buffer})
			if err != nil {
				return err
			}

			// CTRL+B 3 times to exit console
			if chr == 2 {
				ctrlBCount++
				if ctrlBCount == 3 {
					return io.EOF
				}
			} else {
				ctrlBCount = 0
			}
		}
	})

	g.Go(func() error {
		defer func() {
			_ = stream.Send(&grpc_v1.StreamRequest{
				Closed: true,
			})
			_ = stream.CloseSend()
			// we need to allow gRPC to send the Close
			time.Sleep(1 * time.Second)
			resetConsole()
		}()

		for {
			frame, err := stream.Recv()
			if errors.Is(err, io.EOF) || frame != nil && frame.Closed {
				return io.EOF
			}

			if err != nil {
				stdout.Write([]byte(err.Error()))
				return err
			}
			str := string(frame.Payload)
			// Probably we should use a pseudo tty on the other side
			// but this is good for now
			str = strings.ReplaceAll(str, "\n", "\n\r")
			_, err = stdout.Write([]byte(str))
			if err != nil {
				return err
			}
		}
	})

	return g.Wait()
}
