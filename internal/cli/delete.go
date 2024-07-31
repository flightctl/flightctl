package cli

import (
	"context"
	"fmt"
	"net/http"

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
		Use:       "delete (TYPE | TYPE/NAME)",
		Short:     "Delete resources by resources or owner.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: getValidResourceKinds(),
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
	var response any

	printHttpFn := getPrintHttpFn(&o.GlobalOptions)

	switch kind {
	case DeviceKind:
		if len(name) > 0 {
			response, err = c.DeleteDeviceWithResponse(ctx, name, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
		} else {
			response, err = c.DeleteDevicesWithResponse(ctx, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
		}
	case EnrollmentRequestKind:
		if len(name) > 0 {
			response, err = c.DeleteEnrollmentRequestWithResponse(ctx, name, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
		} else {
			response, err = c.DeleteEnrollmentRequestsWithResponse(ctx, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
		}
	case FleetKind:
		if len(name) > 0 {
			response, err = c.DeleteFleetWithResponse(ctx, name, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
		} else {
			response, err = c.DeleteFleetsWithResponse(ctx, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
		}
	case TemplateVersionKind:
		if len(name) > 0 {
			response, err = c.DeleteTemplateVersionWithResponse(ctx, o.FleetName, name, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
		} else {
			response, err = c.DeleteTemplateVersionsWithResponse(ctx, o.FleetName, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
		}
	case RepositoryKind:
		if len(name) > 0 {
			response, err = c.DeleteRepositoryWithResponse(ctx, name, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
		} else {
			response, err = c.DeleteRepositoriesWithResponse(ctx, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
		}
	case ResourceSyncKind:
		if len(name) > 0 {
			response, err = c.DeleteResourceSyncWithResponse(ctx, name, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s/%s: %w", kind, name, err)
			}
		} else {
			response, err = c.DeleteResourceSyncsWithResponse(ctx, printHttpFn)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", plural(kind), err)
			}
		}
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	httpResponse, body := reflectResponse(response)

	if o.VerboseHttp {
		printRawHttpResponse(httpResponse, body)
	}

	if httpResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", httpResponse.Status)
	}

	fmt.Printf("status %s\n", httpResponse.Status)

	return nil
}
