package cli

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	// todo - add api.FactoryReset once we support it
	allowedTargets = []string{string(api.DeviceDecommissionTargetTypeUnenroll)}
)

type DecommissionOptions struct {
	GlobalOptions
	DecommissionTarget string
}

func DefaultDecommissionOptions() *DecommissionOptions {
	return &DecommissionOptions{
		GlobalOptions:      DefaultGlobalOptions(),
		DecommissionTarget: string(api.DeviceDecommissionTargetTypeUnenroll),
	}
}

func NewCmdDecommission() *cobra.Command {
	o := DefaultDecommissionOptions()
	cmd := &cobra.Command{
		Use:   "decommission device/NAME",
		Short: "Decommission a device.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			return o.Run(ctx, args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *DecommissionOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.StringVarP(&o.DecommissionTarget, "target", "t", o.DecommissionTarget, "Specify the type of decommissioning operation: currently supports only 'unenroll'")
}

func (o *DecommissionOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	return nil
}

func (o *DecommissionOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	if kind != DeviceKind {
		return fmt.Errorf("kind must be Device")
	}

	if len(name) == 0 {
		return fmt.Errorf("specify a specific device to decommission")
	}

	if len(o.DecommissionTarget) > 0 && !slices.Contains(allowedTargets, o.DecommissionTarget) {
		uppercaseTarget := strings.ToUpper(string(o.DecommissionTarget[0])) + o.DecommissionTarget[1:]
		if !slices.Contains(allowedTargets, uppercaseTarget) {
			return fmt.Errorf("decommission target must be one of: (%s)", strings.Join(allowedTargets, ", "))
		}
	}

	return nil
}

func (o *DecommissionOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	_, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	var body api.DeviceDecommission
	switch o.DecommissionTarget {
	default:
		body = api.DeviceDecommission{Target: "Unenroll"}
	}
	response, err := c.DecommissionDeviceWithResponse(ctx, name, body)
	if err != nil {
		return fmt.Errorf("decommissioning device %s: %w", name, err)
	}

	if response.HTTPResponse != nil {
		if response.HTTPResponse.StatusCode != http.StatusOK {
			return fmt.Errorf("unsuccessful decommissioning device request %s: %s", name, string(response.Body))
		}
	}

	fmt.Printf("Device scheduled for decommissioning: %s: %s\n", response.HTTPResponse.Status, name)
	return nil
}
