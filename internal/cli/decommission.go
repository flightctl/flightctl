package cli

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type DecommissionOptions struct {
	GlobalOptions
	DecommissionTarget string
}

func DefaultDecommissionOptions() *DecommissionOptions {
	return &DecommissionOptions{
		GlobalOptions:      DefaultGlobalOptions(),
		DecommissionTarget: "unenroll",
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
			return o.Run(cmd.Context(), args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *DecommissionOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.StringVarP(&o.DecommissionTarget, "target", "t", "unenroll", "Specify the type of decommissioning operation: unenroll, or factoryreset")
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

	// todo - update once this changes
	if o.DecommissionTarget == "Unenroll" {
		o.DecommissionTarget = "unenroll"
	}
	if o.DecommissionTarget != "unenroll" {
		return fmt.Errorf("Decommission target (type) requested must be 'unenroll' until other types are supported")
	}

	return nil
}

func (o *DecommissionOptions) Run(ctx context.Context, args []string) error {
	log := log.InitLogs()

	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	_, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	// todo - update once this changes
	if o.DecommissionTarget != "unenroll" {
		return fmt.Errorf("DecommissionTarget %s currently not supported; try 'unenroll'", o.DecommissionTarget)
	}

	body := api.DeviceDecommission{DecommissionTarget: "Unenroll"}
	response, err := c.DecommissionDeviceWithResponse(ctx, name, body)
	if err != nil {
		return fmt.Errorf("decommissioning device %s: %w", name, err)
	}

	var status string
	if response.HTTPResponse != nil {
		status = response.HTTPResponse.Status
		statuscode := response.HTTPResponse.StatusCode
		if statuscode != http.StatusOK {
			return fmt.Errorf("unsuccessful decommissioning device request %s: %s", name, string(response.Body))
		}
	}

	log.Infof("decommission request for %s: %s\n", status, name)
	return nil
}
