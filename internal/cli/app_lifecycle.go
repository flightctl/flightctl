package cli

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
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

// markAppFlagRequired declares --app required on cmd so cobra rejects a missing
// value immediately and documents the requirement in --help.
func markAppFlagRequired(cmd *cobra.Command) {
	_ = cmd.MarkFlagRequired("app")
}

func (o *AppLifecycleOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

// resolveDeviceName validates the positional args and required flags shared by the
// device-only restart command, returning the target device name.
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

// resolveTarget validates the positional args and required flags shared by the stop/start
// application commands, returning the target's kind (Device or Fleet) and name.
func (o *AppLifecycleOptions) resolveTarget(args []string) (ResourceKind, string, error) {
	kind, name, err := parseAndValidateKindNameFromArgsSingle(args)
	if err != nil {
		return "", "", err
	}
	if kind != DeviceKind && kind != FleetKind {
		return "", "", fmt.Errorf("kind must be Device or Fleet")
	}
	if len(name) == 0 {
		return "", "", fmt.Errorf("%s name is required", kind)
	}
	if o.AppName == "" {
		return "", "", fmt.Errorf("--app is required")
	}
	return kind, name, nil
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
		if response == "" {
			return fmt.Errorf("failed to read confirmation from stdin: %w; use --yes to skip the prompt", err)
		}
		// Partial read (e.g. "y" without trailing newline); proceed to check the response.
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
		Use:   "stop (device/NAME | fleet/NAME) --app APP",
		Short: "Stop an application running on a device, or on every device owned by a fleet.",
		Long: "Stop an application running on a device, or on every device owned by a fleet.\n\n" +
			"Stopping on a fleet sets a fleet-wide default: it applies to every device currently " +
			"owned by the fleet, as well as any device that joins the fleet later, but a device's " +
			"own stop/start command still takes precedence over the fleet-wide default for that " +
			"device.",
		Example: `  # Stop an application on a single device
  flightctl stop device/my-device --app my-app

  # Stop an application on every device in a fleet
  flightctl stop fleet/my-fleet --app my-app`,
		Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{DeviceKind, FleetKind},
		}.ValidArgsFunction,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			kind, name, err := o.resolveTarget(args)
			if err != nil {
				return err
			}
			if err := confirm(stopStartConfirmPrompt("Stop", o.AppName, kind, name), o.Yes); err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			c, err := o.BuildClient()
			if err != nil {
				return fmt.Errorf("creating client: %w", err)
			}
			c.Start(ctx)
			defer c.Stop()
			if kind == FleetKind {
				return runStopFleet(ctx, c.ClientWithResponses, name, o.AppName)
			}
			return runStop(ctx, c.ClientWithResponses, name, o.AppName)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	markAppFlagRequired(cmd)
	return cmd
}

func NewCmdStart() *cobra.Command {
	o := DefaultAppLifecycleOptions()
	cmd := &cobra.Command{
		Use:   "start (device/NAME | fleet/NAME) --app APP",
		Short: "Start an application running on a device, or on every device owned by a fleet.",
		Long: "Start an application running on a device, or on every device owned by a fleet.\n\n" +
			"Starting on a fleet sets a fleet-wide default: it applies to every device currently " +
			"owned by the fleet, as well as any device that joins the fleet later, but a device's " +
			"own stop/start command still takes precedence over the fleet-wide default for that " +
			"device.",
		Example: `  # Start an application on a single device
  flightctl start device/my-device --app my-app

  # Start an application on every device in a fleet
  flightctl start fleet/my-fleet --app my-app`,
		Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{DeviceKind, FleetKind},
		}.ValidArgsFunction,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			kind, name, err := o.resolveTarget(args)
			if err != nil {
				return err
			}
			if err := confirm(stopStartConfirmPrompt("Start", o.AppName, kind, name), o.Yes); err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			c, err := o.BuildClient()
			if err != nil {
				return fmt.Errorf("creating client: %w", err)
			}
			c.Start(ctx)
			defer c.Stop()
			if kind == FleetKind {
				return runStartFleet(ctx, c.ClientWithResponses, name, o.AppName)
			}
			return runStart(ctx, c.ClientWithResponses, name, o.AppName)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	markAppFlagRequired(cmd)
	return cmd
}

// stopStartConfirmPrompt builds the confirmation prompt for the stop/start commands, calling
// out the fleet-wide blast radius when the target is a fleet rather than a single device.
func stopStartConfirmPrompt(verb, appName string, kind ResourceKind, name string) string {
	if kind == FleetKind {
		return fmt.Sprintf("%s application %q on every device in fleet %q?", verb, appName, name)
	}
	return fmt.Sprintf("%s application %q on device %q?", verb, appName, name)
}

func NewCmdRestart() *cobra.Command {
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
			if err := confirm(fmt.Sprintf("Restart application %q on device %q?", o.AppName, deviceName), o.Yes); err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			c, err := o.BuildClient()
			if err != nil {
				return fmt.Errorf("creating client: %w", err)
			}
			c.Start(ctx)
			defer c.Stop()
			return runRestart(ctx, c.ClientWithResponses, deviceName, o.AppName)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	markAppFlagRequired(cmd)
	return cmd
}

func runStop(ctx context.Context, c *apiclient.ClientWithResponses, deviceName, appName string) error {
	response, err := c.StopDeviceApplicationWithResponse(ctx, deviceName, appName)
	if err != nil {
		return fmt.Errorf("stopping application %s on device %s: %w", appName, deviceName, err)
	}
	if err := checkLifecycleResponse(response.HTTPResponse, response.Body, "stopping", appName, "device", deviceName); err != nil {
		return err
	}
	fmt.Printf("Requested stop of application %q on device %q\n", appName, deviceName)
	return nil
}

func runStart(ctx context.Context, c *apiclient.ClientWithResponses, deviceName, appName string) error {
	response, err := c.StartDeviceApplicationWithResponse(ctx, deviceName, appName)
	if err != nil {
		return fmt.Errorf("starting application %s on device %s: %w", appName, deviceName, err)
	}
	if err := checkLifecycleResponse(response.HTTPResponse, response.Body, "starting", appName, "device", deviceName); err != nil {
		return err
	}
	fmt.Printf("Requested start of application %q on device %q\n", appName, deviceName)
	return nil
}

func runRestart(ctx context.Context, c *apiclient.ClientWithResponses, deviceName, appName string) error {
	response, err := c.RestartDeviceApplicationWithResponse(ctx, deviceName, appName)
	if err != nil {
		return fmt.Errorf("restarting application %s on device %s: %w", appName, deviceName, err)
	}
	if err := checkLifecycleResponse(response.HTTPResponse, response.Body, "restarting", appName, "device", deviceName); err != nil {
		return err
	}
	fmt.Printf("Requested restart of application %q on device %q\n", appName, deviceName)
	return nil
}

func runStopFleet(ctx context.Context, c *apiclient.ClientWithResponses, fleetName, appName string) error {
	response, err := c.StopFleetApplicationWithResponse(ctx, fleetName, appName)
	if err != nil {
		return fmt.Errorf("stopping application %s on fleet %s: %w", appName, fleetName, err)
	}
	if err := checkLifecycleResponse(response.HTTPResponse, response.Body, "stopping", appName, "fleet", fleetName); err != nil {
		return err
	}
	fmt.Printf("Requested stop of application %q on every device in fleet %q\n", appName, fleetName)
	return nil
}

func runStartFleet(ctx context.Context, c *apiclient.ClientWithResponses, fleetName, appName string) error {
	response, err := c.StartFleetApplicationWithResponse(ctx, fleetName, appName)
	if err != nil {
		return fmt.Errorf("starting application %s on fleet %s: %w", appName, fleetName, err)
	}
	if err := checkLifecycleResponse(response.HTTPResponse, response.Body, "starting", appName, "fleet", fleetName); err != nil {
		return err
	}
	fmt.Printf("Requested start of application %q on every device in fleet %q\n", appName, fleetName)
	return nil
}

// checkLifecycleResponse returns a CLIError if the HTTP response status is not 200 OK.
func checkLifecycleResponse(resp *http.Response, body []byte, verbGerund, appName, targetKind, targetName string) error {
	if resp != nil && resp.StatusCode != http.StatusOK {
		return &CLIError{
			Context: fmt.Sprintf("%s application %s on %s %s: failed", verbGerund, appName, targetKind, targetName),
			Err:     &APIError{Status: ParseStatusFromBody(body)},
		}
	}
	return nil
}
