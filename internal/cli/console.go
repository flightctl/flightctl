package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
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

func (o *ConsoleOptions) Run(ctx context.Context, args []string) error {
	config, err := client.ParseConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}

	_, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	err = o.connectViaWS(ctx, config, name, config.AuthInfo.Token)
	if err == io.EOF {
		fmt.Println("Connection closed")
		return nil
	}
	return err
}

func (o *ConsoleOptions) connectViaWS(ctx context.Context, config *client.Config, deviceName, token string) error {

	connURL := fmt.Sprintf("%s/ws/v1/devices/%s/console", config.Service.Server, deviceName)
	// replace https / http to wss / ws
	connURL = strings.Replace(connURL, "http", "ws", 1)
	headers := make(http.Header)
	headers.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	fmt.Printf("Connecting to device %s\n", deviceName)
	tlsConfig, err := client.CreateTLSConfigFromConfig(config)
	if err != nil {
		return fmt.Errorf("creating tls config: %w", err)
	}

	dialer := &websocket.Dialer{
		TLSClientConfig: tlsConfig,
	}

	conn, _, err := dialer.Dial(connURL, headers)
	if err != nil {
		return fmt.Errorf("dialing websocket: %w", err)
	}
	defer conn.Close()

	return forwardStdio(ctx, conn)
}

func forwardStdio(ctx context.Context, conn *websocket.Conn) error {
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
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second*5))
			conn.Close()
			stdout.Close()
			resetConsole()
		}()

		buffer := make([]byte, 1)
		ctrlBCount := 0
		for {
			select {
			case <-ctx.Done():
				return io.EOF

			case chr, isOpen := <-stdioChan:
				buffer[0] = chr

				if !isOpen { // the input STDIN has been closed
					return nil
				}

				err = conn.WriteMessage(websocket.BinaryMessage, buffer)
				if err != nil {
					return fmt.Errorf("writing to websocket: %w", err)
				}

				if chr == 2 {
					ctrlBCount++
					if ctrlBCount == 3 {
						return io.EOF
					}
				} else {
					ctrlBCount = 0
				}
			}
		}
	})

	g.Go(func() error {
		defer func() {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second*5))
			conn.Close()
			resetConsole()
		}()

		for {
			select {
			case <-ctx.Done():
				return io.EOF
			default:
				msgType, frame, err := conn.ReadMessage()
				if errors.Is(err, io.EOF) || websocket.IsCloseError(err, websocket.CloseNormalClosure) || errors.Is(err, net.ErrClosed) {
					return io.EOF
				}

				if err != nil {
					stdout.Write([]byte(err.Error()))
					return err
				}

				// if it's binary or text message, forward it to the console session
				if msgType == websocket.BinaryMessage || msgType == websocket.TextMessage {

					str := string(frame)
					// Probably we should use a pseudo tty on the other side
					// but this is good for now
					str = strings.ReplaceAll(str, "\n", "\n\r")
					_, err = stdout.Write([]byte(str))
					if err != nil {
						return err
					}
				}
			}
		}
	})

	return g.Wait()
}
