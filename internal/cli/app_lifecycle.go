package cli

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// AppLifecycleOptions holds the options shared by the stop/start/restart application commands.
type AppLifecycleOptions struct {
	GlobalOptions
	AppName string
	Yes     bool
}

func DefaultAppLifecycleOptions() *AppLifecycleOptions {
	return &AppLifecycleOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func (o *AppLifecycleOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.StringVar(&o.AppName, "app", o.AppName, "Application name to control (required)")
	fs.BoolVarP(&o.Yes, "yes", "y", o.Yes, "Skip the confirmation prompt")
}

func (o *AppLifecycleOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

// resolveDeviceName validates the positional args and required flags shared by all
// application lifecycle commands, returning the target device name.
func (o *AppLifecycleOptions) resolveDeviceName(args []string) (string, error) {
	kind, name, err := parseAndValidateKindNameFromArgsSingle(args)
	if err != nil {
		return "", err
	}
	if kind != DeviceKind {
		return "", fmt.Errorf("kind must be Device")
	}
	if len(name) == 0 {
		return "", fmt.Errorf("device name is required")
	}
	if o.AppName == "" {
		return "", fmt.Errorf("--app is required")
	}
	return name, nil
}

// confirm prompts the user for confirmation on stderr, returning an error if they decline.
// Skipped entirely when skip is true (--yes).
func confirm(prompt string, skip bool) error {
	if skip {
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s (y/N): ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read user input: %w", err)
	}
	response = strings.TrimSpace(strings.ToLower(response))
	if response != "y" && response != "yes" {
		return fmt.Errorf("cancelled")
	}
	return nil
}

func NewCmdStop() *cobra.Command {
	o := DefaultAppLifecycleOptions()
	cmd := &cobra.Command{
		Use:   "stop device/NAME --app APP",
		Short: "Stop an application running on a device.",
		Args:  cobra.MinimumNArgs(1),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{DeviceKind},
		}.ValidArgsFunction,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			deviceName, err := o.resolveDeviceName(args)
			if err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			return o.setDesiredState(ctx, deviceName, api.ApplicationDesiredStateStopped)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func NewCmdStart() *cobra.Command {
	o := DefaultAppLifecycleOptions()
	cmd := &cobra.Command{
		Use:   "start device/NAME --app APP",
		Short: "Start an application running on a device.",
		Args:  cobra.MinimumNArgs(1),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{DeviceKind},
		}.ValidArgsFunction,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			deviceName, err := o.resolveDeviceName(args)
			if err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			return o.setDesiredState(ctx, deviceName, api.ApplicationDesiredStateRunning)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func NewCmdRestartApplication() *cobra.Command {
	o := DefaultAppLifecycleOptions()
	cmd := &cobra.Command{
		Use:   "restart device/NAME --app APP",
		Short: "Restart an application running on a device.",
		Args:  cobra.MinimumNArgs(1),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{DeviceKind},
		}.ValidArgsFunction,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			deviceName, err := o.resolveDeviceName(args)
			if err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			return o.restart(ctx, deviceName)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *AppLifecycleOptions) setDesiredState(ctx context.Context, deviceName string, state api.ApplicationDesiredState) error {
	verb, verbPast := "start", "started"
	if state == api.ApplicationDesiredStateStopped {
		verb, verbPast = "stop", "stopped"
	}

	if err := confirm(fmt.Sprintf("%s application %q on device %q?", strings.ToUpper(verb[:1])+verb[1:], o.AppName, deviceName), o.Yes); err != nil {
		return err
	}

	c, err := o.BuildClient()
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	c.Start(ctx)
	defer c.Stop()

	body := api.DeviceApplicationDesiredStateRequest{DesiredState: state}
	response, err := c.SetDeviceApplicationDesiredStateWithResponse(ctx, deviceName, o.AppName, body)
	if err != nil {
		return fmt.Errorf("%sing application %s on device %s: %w", verb, o.AppName, deviceName, err)
	}

	if response.HTTPResponse != nil && response.HTTPResponse.StatusCode != http.StatusOK {
		return &CLIError{
			Context: fmt.Sprintf("%sing application %s on device %s: failed", verb, o.AppName, deviceName),
			Err:     &APIError{Status: ParseStatusFromBody(response.Body)},
		}
	}

	fmt.Printf("Application %q on device %q %s\n", o.AppName, deviceName, verbPast)
	return nil
}

func (o *AppLifecycleOptions) restart(ctx context.Context, deviceName string) error {
	if err := confirm(fmt.Sprintf("Restart application %q on device %q?", o.AppName, deviceName), o.Yes); err != nil {
		return err
	}

	c, err := o.BuildClient()
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	c.Start(ctx)
	defer c.Stop()

	response, err := c.RestartDeviceApplicationWithResponse(ctx, deviceName, o.AppName)
	if err != nil {
		return fmt.Errorf("restarting application %s on device %s: %w", o.AppName, deviceName, err)
	}

	if response.HTTPResponse != nil && response.HTTPResponse.StatusCode != http.StatusOK {
		return &CLIError{
			Context: fmt.Sprintf("restarting application %s on device %s: failed", o.AppName, deviceName),
			Err:     &APIError{Status: ParseStatusFromBody(response.Body)},
		}
	}

	fmt.Printf("Application %q on device %q restarted\n", o.AppName, deviceName)
	return nil
}
