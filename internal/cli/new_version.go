package cli

import (
	"context"
	"fmt"
	"os"

	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/cli/display"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type NewVersionOptions struct {
	GlobalOptions

	// Name for the new ImageBuild resource (required).
	Name string
	// SourceImageTag overrides spec.source.imageTag of the parent.
	SourceImageTag string
	// DestinationImageTag overrides spec.destination.imageTag of the parent.
	DestinationImageTag string
	// Output format: json, yaml, or empty.
	Output string
}

func DefaultNewVersionOptions() *NewVersionOptions {
	return &NewVersionOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func NewCmdNewVersion() *cobra.Command {
	o := DefaultNewVersionOptions()
	cmd := &cobra.Command{
		Use:   "new-version (imagebuild/NAME | imagebuild NAME)",
		Short: "Create a new ImageBuild derived from an existing one.",
		Long: `Create a new ImageBuild by copying the spec of an existing parent build,
with optional source and destination image tag overrides.

The new build records its lineage via the 'flightctl.io/new-version-from'
annotation set to the parent's name.`,
		Example: `  # Create a new version of an imagebuild using slash format
  flightctl new-version imagebuild/<existing-build-name> --name <new-build-name>

  # With tag overrides
  flightctl new-version imagebuild/<existing-build-name> \
    --name <new-build-name> \
    --source-image-tag <source-tag> \
    --destination-image-tag <destination-tag>

  # Output as JSON
  flightctl new-version ib/<existing-build-name> --name <new-build-name> -o json`,
		Args: cobra.RangeArgs(1, 2),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{ImageBuildKind},
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

func (o *NewVersionOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.StringVar(&o.Name, "name", "", "Name for the new ImageBuild resource (required).")
	fs.StringVar(&o.SourceImageTag, "source-image-tag", "", "Override for spec.source.imageTag. If omitted, the parent's tag is used.")
	fs.StringVar(&o.DestinationImageTag, "destination-image-tag", "", "Override for spec.destination.imageTag. If omitted, the parent's tag is used.")
	fs.StringVarP(&o.Output, "output", "o", "", "Output format. One of: json|yaml.")
}

func (o *NewVersionOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *NewVersionOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseResourceArgs(args)
	if err != nil {
		return err
	}

	if kind != ImageBuildKind {
		return fmt.Errorf("new-version command only supports imagebuild resources, got: %s", kind)
	}

	if name == "" {
		return fmt.Errorf("parent imagebuild name is required")
	}

	if o.Name == "" {
		return fmt.Errorf("--name is required: provide a name for the new ImageBuild resource")
	}

	if o.Output != "" && o.Output != string(display.JSONFormat) && o.Output != string(display.YAMLFormat) {
		return fmt.Errorf("output format must be one of (json, yaml), got: %s", o.Output)
	}

	return nil
}

func (o *NewVersionOptions) Run(ctx context.Context, args []string) error {
	_, parentName, err := parseResourceArgs(args)
	if err != nil {
		return err
	}

	ibClient, err := o.BuildImageBuilderClient()
	if err != nil {
		return fmt.Errorf("creating imagebuilder client: %w", err)
	}

	req := api.ImageBuildNewVersionRequest{
		Name: o.Name,
	}
	if o.SourceImageTag != "" {
		req.SourceImageTag = lo.ToPtr(o.SourceImageTag)
	}
	if o.DestinationImageTag != "" {
		req.DestinationImageTag = lo.ToPtr(o.DestinationImageTag)
	}

	response, err := ibClient.CreateImageBuildNewVersionWithResponse(ctx, parentName, req)
	if err != nil {
		return fmt.Errorf("creating new version of imagebuild %s: %w", parentName, err)
	}

	if err := validateImageBuilderResponse(response); err != nil {
		return err
	}

	if response.JSON201 == nil {
		return fmt.Errorf("unexpected empty response from server")
	}

	newName := lo.FromPtr(response.JSON201.Metadata.Name)

	if o.Output == "" {
		fmt.Printf("imagebuild/%s created\n", newName)
		return nil
	}

	formatter := display.NewFormatter(display.OutputFormat(o.Output))
	return formatter.Format(response.JSON201, display.FormatOptions{
		Kind:   string(api.ResourceKindImageBuild),
		Name:   newName,
		Writer: os.Stdout,
	})
}
