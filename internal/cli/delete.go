package cli

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/spf13/cobra"
)

type DeleteOptions struct {
	Owner     string
	FleetName string
}

func NewCmdDelete() *cobra.Command {
	o := &DeleteOptions{}
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
	cmd.Flags().StringVar(&o.Owner, "owner", o.Owner, "Filter by owner.")
	cmd.Flags().StringVarP(&o.FleetName, "fleetname", "f", o.FleetName, "Fleet name for accessing individual templateversions.")
	return cmd
}

func (o *DeleteOptions) Complete(cmd *cobra.Command, args []string) error {
	return nil
}

func (o *DeleteOptions) Validate(args []string) error {
	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}
	if len(o.Owner) > 0 && kind != TemplateVersionKind {
		return fmt.Errorf("owner can only be specified when deleting templateversions")
	}
	if kind == TemplateVersionKind && len(name) > 0 {
		if len(o.FleetName) == 0 {
			return fmt.Errorf("fleetname must be specified when fetching a specific templatevesion")
		}
	} else {
		if len(o.FleetName) > 0 {
			return fmt.Errorf("fleetname must only be specified when fetching a specific templatevesion")
		}
	}
	return nil
}

func (o *DeleteOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(defaultClientConfigFile)
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
			params := api.DeleteTemplateVersionsParams{
				Owner: util.StrToPtrWithNilDefault(o.Owner),
			}
			response, err := c.DeleteTemplateVersionsWithResponse(ctx, &params)
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
