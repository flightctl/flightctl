package cli

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type DeleteOptions struct {
	GlobalOptions

	FleetName string
}

func DefaultDeleteOptions() *DeleteOptions {
	return &DeleteOptions{
		GlobalOptions: DefaultGlobalOptions(),
		FleetName:     "",
	}
}

func NewCmdDelete() *cobra.Command {
	o := DefaultDeleteOptions()
	cmd := &cobra.Command{
		Use:   "delete (TYPE | TYPE/NAME)",
		Short: "Delete resources by resources or owner.",
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

func (o *DeleteOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVarP(&o.FleetName, "fleetname", "f", o.FleetName, "Fleet name for accessing templateversions.")
}

func (o *DeleteOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	return nil
}

func (o *DeleteOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, _, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}
	if kind == TemplateVersionKind && len(o.FleetName) == 0 {
		return fmt.Errorf("fleetname must be specified when deleting templateversions")
	}
	return nil
}

func (o *DeleteOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}
	switch kind {
	case DeviceKind:
		if len(name) > 0 {
			response, err := c.DeleteDeviceWithResponse(ctx, name)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteDevicesWithResponse(ctx)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case EnrollmentRequestKind:
		if len(name) > 0 {
			response, err := c.DeleteEnrollmentRequestWithResponse(ctx, name)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteEnrollmentRequestsWithResponse(ctx)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case FleetKind:
		if len(name) > 0 {
			response, err := c.DeleteFleetWithResponse(ctx, name)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteFleetsWithResponse(ctx)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case TemplateVersionKind:
		if len(name) > 0 {
			response, err := c.DeleteTemplateVersionWithResponse(ctx, o.FleetName, name)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteTemplateVersionsWithResponse(ctx, o.FleetName)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case RepositoryKind:
		if len(name) > 0 {
			response, err := c.DeleteRepositoryWithResponse(ctx, name)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteRepositoriesWithResponse(ctx)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case ResourceSyncKind:
		if len(name) > 0 {
			response, err := c.DeleteResourceSyncWithResponse(ctx, name)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteResourceSyncsWithResponse(ctx)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return nil
}
