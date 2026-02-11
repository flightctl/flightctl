package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type CancelOptions struct {
	GlobalOptions
}

func DefaultCancelOptions() *CancelOptions {
	return &CancelOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func NewCmdCancel() *cobra.Command {
	o := DefaultCancelOptions()
	cmd := &cobra.Command{
		Use:   "cancel (TYPE/NAME | TYPE NAME)",
		Short: "Cancel a running resource operation.",
		Long:  "Cancel a running resource operation. Currently supports imagebuild and imageexport resources.",
		Example: `  # Cancel a running imagebuild
  flightctl cancel imagebuild/my-build

  # Cancel a running imageexport
  flightctl cancel imageexport/my-export

  # Cancel using type and name as separate arguments
  flightctl cancel imagebuild my-build
  flightctl cancel imageexport my-export

  # Cancel using short names
  flightctl cancel ib/my-build
  flightctl cancel ie/my-export`,
		Args: cobra.RangeArgs(1, 2),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{ImageBuildKind, ImageExportKind},
		}.ValidArgsFunction,
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

func (o *CancelOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
}

func (o *CancelOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *CancelOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseResourceArgs(args)
	if err != nil {
		return err
	}

	// Only imagebuild and imageexport are supported for cancel
	if kind != ImageBuildKind && kind != ImageExportKind {
		return fmt.Errorf("cancel command only supports imagebuild and imageexport resources, got: %s", kind)
	}

	if name == "" {
		return fmt.Errorf("resource name is required")
	}

	return nil
}

func (o *CancelOptions) Run(ctx context.Context, args []string) error {
	kind, name, err := parseResourceArgs(args)
	if err != nil {
		return err
	}

	switch kind {
	case ImageBuildKind:
		return o.cancelImageBuild(ctx, name)
	case ImageExportKind:
		return o.cancelImageExport(ctx, name)
	default:
		return fmt.Errorf("cancel not supported for resource kind: %s", kind)
	}
}

func (o *CancelOptions) cancelImageBuild(ctx context.Context, name string) error {
	ibClient, err := o.BuildImageBuilderClient()
	if err != nil {
		return fmt.Errorf("creating imagebuilder client: %w", err)
	}

	response, err := ibClient.CancelImageBuildWithResponse(ctx, name)
	if err != nil {
		return fmt.Errorf("canceling imagebuild %s: %w", name, err)
	}

	if err := validateImageBuilderResponse(response); err != nil {
		return err
	}

	fmt.Printf("ImageBuild cancellation requested: %s\n", name)
	return nil
}

func (o *CancelOptions) cancelImageExport(ctx context.Context, name string) error {
	ibClient, err := o.BuildImageBuilderClient()
	if err != nil {
		return fmt.Errorf("creating imagebuilder client: %w", err)
	}

	response, err := ibClient.CancelImageExportWithResponse(ctx, name)
	if err != nil {
		return fmt.Errorf("canceling imageexport %s: %w", name, err)
	}

	if err := validateImageBuilderResponse(response); err != nil {
		return err
	}

	fmt.Printf("ImageExport cancellation requested: %s\n", name)
	return nil
}
