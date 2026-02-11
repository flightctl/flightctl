package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type DeleteOptions struct {
	GlobalOptions

	FleetName   string
	CatalogName string
}

func DefaultDeleteOptions() *DeleteOptions {
	return &DeleteOptions{
		GlobalOptions: DefaultGlobalOptions(),
		FleetName:     "",
		CatalogName:   "",
	}
}

func NewCmdDelete() *cobra.Command {
	o := DefaultDeleteOptions()
	cmd := &cobra.Command{
		Use:       "delete (TYPE NAME [NAME...] | TYPE/NAME)",
		Short:     "Delete one or more resources by name.",
		Args:      cobra.MinimumNArgs(1),
		ValidArgs: getValidPluralResourceKinds(),
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

func (o *DeleteOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVarP(&o.FleetName, "fleetname", "f", o.FleetName, "Fleet name for accessing templateversions.")
	fs.StringVar(&o.CatalogName, "catalog", o.CatalogName, "Catalog name for accessing catalogitems.")
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

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}
	if len(name) == 0 && len(args) < 2 {
		return fmt.Errorf("name must be specified when deleting %s", kind)
	}
	if len(name) > 0 {
		if len(args) > 1 {
			return fmt.Errorf("invalid format: cannot mix TYPE/NAME syntax with additional resource names. Use either 'delete TYPE/NAME' or 'delete TYPE NAME [NAME...]'")
		}
		if errs := validation.ValidateResourceName(&name); len(errs) > 0 {
			return fmt.Errorf("invalid resource name: %s", errors.Join(errs...).Error())
		}
	}
	for _, resName := range args[1:] {
		if errs := validation.ValidateResourceName(&resName); len(errs) > 0 {
			return fmt.Errorf("invalid resource name: %s", errors.Join(errs...).Error())
		}
	}
	if kind == TemplateVersionKind && len(o.FleetName) == 0 {
		return fmt.Errorf("fleetname must be specified when deleting templateversions")
	}
	if kind == CatalogItemKind && len(o.CatalogName) == 0 {
		return fmt.Errorf("--catalog must be specified when deleting catalogitems")
	}
	return nil
}

func (o *DeleteOptions) Run(ctx context.Context, args []string) error {
	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	// ImageBuild and ImageExport resources use a separate API server
	if kind == ImageBuildKind {
		return o.runImageBuildDelete(ctx, args, kind, name)
	}
	if kind == ImageExportKind {
		return o.runImageExportDelete(ctx, args, kind, name)
	}

	c, err := o.BuildClient()
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	c.Start(ctx)
	defer c.Stop()

	if len(args) == 1 {
		response, err := o.deleteOne(ctx, c, kind, name)
		if err != nil {
			return err
		}
		if err := processDeletionReponse(response, err, kind, name); err != nil {
			return err
		}
		fmt.Printf("Deletion request for %s \"%s\" completed\n", kind, name)
		return nil
	}

	names := args[1:]

	return o.deleteMultiple(ctx, c, kind, names)
}

func (o *DeleteOptions) runImageBuildDelete(ctx context.Context, args []string, kind ResourceKind, name string) error {
	ibClient, err := o.BuildImageBuilderClient()
	if err != nil {
		return fmt.Errorf("creating imagebuilder client: %w", err)
	}
	ibClient.Start(ctx)
	defer ibClient.Stop()

	if len(args) == 1 {
		response, err := ibClient.DeleteImageBuildWithResponse(ctx, name)
		if err != nil {
			return err
		}
		if err := processDeletionReponse(response, nil, kind, name); err != nil {
			return err
		}
		fmt.Printf("Deletion request for %s \"%s\" completed\n", kind, name)
		return nil
	}

	names := args[1:]
	return o.deleteMultipleImageBuilds(ctx, ibClient, kind, names)
}

func (o *DeleteOptions) deleteMultipleImageBuilds(ctx context.Context, c *client.ImageBuilderClient, kind ResourceKind, names []string) error {
	var errorCount int

	for _, name := range names {
		response, deleteErr := c.DeleteImageBuildWithResponse(ctx, name)

		processErr := processDeletionReponse(response, deleteErr, kind, name)
		if processErr != nil {
			fmt.Printf("Error: %v\n", processErr)
			errorCount++
		} else {
			fmt.Printf("Deletion request for %s \"%s\" completed\n", kind, name)
		}
	}

	if errorCount > 0 {
		return fmt.Errorf("failed to delete %d %s(s)", errorCount, kind)
	}

	return nil
}

func (o *DeleteOptions) runImageExportDelete(ctx context.Context, args []string, kind ResourceKind, name string) error {
	ibClient, err := o.BuildImageBuilderClient()
	if err != nil {
		return fmt.Errorf("creating imagebuilder client: %w", err)
	}
	ibClient.Start(ctx)
	defer ibClient.Stop()

	if len(args) == 1 {
		response, err := ibClient.DeleteImageExportWithResponse(ctx, name)
		if err != nil {
			return err
		}
		if err := processDeletionReponse(response, nil, kind, name); err != nil {
			return err
		}
		fmt.Printf("Deletion request for %s \"%s\" completed\n", kind, name)
		return nil
	}

	names := args[1:]
	return o.deleteMultipleImageExports(ctx, ibClient, kind, names)
}

func (o *DeleteOptions) deleteMultipleImageExports(ctx context.Context, c *client.ImageBuilderClient, kind ResourceKind, names []string) error {
	var errorCount int

	for _, name := range names {
		response, deleteErr := c.DeleteImageExportWithResponse(ctx, name)

		processErr := processDeletionReponse(response, deleteErr, kind, name)
		if processErr != nil {
			fmt.Printf("Error: %v\n", processErr)
			errorCount++
		} else {
			fmt.Printf("Deletion request for %s \"%s\" completed\n", kind, name)
		}
	}

	if errorCount > 0 {
		return fmt.Errorf("failed to delete %d %s(s)", errorCount, kind)
	}

	return nil
}

func (o *DeleteOptions) deleteMultiple(ctx context.Context, c *client.Client, kind ResourceKind, names []string) error {
	var errorCount int

	for _, name := range names {
		response, deleteErr := o.deleteOne(ctx, c, kind, name)

		processErr := processDeletionReponse(response, deleteErr, kind, name)
		if processErr != nil {
			fmt.Printf("Error: %v\n", processErr)
			errorCount++
		} else {
			fmt.Printf("Deletion request for %s \"%s\" completed\n", kind, name)
		}
	}

	if errorCount > 0 {
		return fmt.Errorf("failed to delete %d %s(s)", errorCount, kind)
	}

	return nil
}

func (o *DeleteOptions) deleteOne(ctx context.Context, c *client.Client, kind ResourceKind, name string) (interface{}, error) {
	var response interface{}
	var err error

	switch kind {
	case DeviceKind:
		response, err = c.DeleteDeviceWithResponse(ctx, name)
	case EnrollmentRequestKind:
		response, err = c.DeleteEnrollmentRequestWithResponse(ctx, name)
	case FleetKind:
		response, err = c.DeleteFleetWithResponse(ctx, name)
	case TemplateVersionKind:
		response, err = c.DeleteTemplateVersionWithResponse(ctx, o.FleetName, name)
	case RepositoryKind:
		response, err = c.DeleteRepositoryWithResponse(ctx, name)
	case ResourceSyncKind:
		response, err = c.DeleteResourceSyncWithResponse(ctx, name)
	case CertificateSigningRequestKind:
		response, err = c.DeleteCertificateSigningRequestWithResponse(ctx, name)
	case AuthProviderKind:
		response, err = c.DeleteAuthProviderWithResponse(ctx, name)
	case CatalogKind:
		response, err = c.V1Alpha1().DeleteCatalogWithResponse(ctx, name)
	case CatalogItemKind:
		response, err = c.V1Alpha1().DeleteCatalogItemWithResponse(ctx, o.CatalogName, name)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return response, err
}

func processDeletionReponse(response interface{}, err error, kind ResourceKind, name string) error {
	errorPrefix := fmt.Sprintf("deleting %s", kind)
	if len(name) > 0 {
		errorPrefix = fmt.Sprintf("deleting %s/%s", kind, name)
	}

	if err != nil {
		return fmt.Errorf("%s: %w", errorPrefix, err)
	}

	httpResponse, err := responseField[*http.Response](response, "HTTPResponse")
	if err != nil {
		return fmt.Errorf("%s: %w", errorPrefix, err)
	}

	responseBody, err := responseField[[]byte](response, "Body")
	if err != nil {
		return fmt.Errorf("%s: %w", errorPrefix, err)
	}

	if err := validateHttpResponse(responseBody, httpResponse.StatusCode, http.StatusOK); err != nil {
		return fmt.Errorf("%s: %w", errorPrefix, err)
	}

	return nil
}
