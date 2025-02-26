package cli

import (
	"context"
	"fmt"
	"net/http"
	"reflect"

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

func (o *DeleteOptions) Run(ctx context.Context, args []string) error { //nolint:gocyclo
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	var response interface{}

	switch {
	case kind == DeviceKind && len(name) > 0:
		response, err = c.DeleteDeviceWithResponse(ctx, name)
	case kind == DeviceKind && len(name) == 0:
		response, err = c.DeleteDevicesWithResponse(ctx)
	case kind == EnrollmentRequestKind && len(name) > 0:
		response, err = c.DeleteEnrollmentRequestWithResponse(ctx, name)
	case kind == EnrollmentRequestKind && len(name) == 0:
		response, err = c.DeleteEnrollmentRequestsWithResponse(ctx)
	case kind == FleetKind && len(name) > 0:
		response, err = c.DeleteFleetWithResponse(ctx, name)
	case kind == FleetKind && len(name) == 0:
		response, err = c.DeleteFleetsWithResponse(ctx)
	case kind == TemplateVersionKind && len(name) > 0:
		response, err = c.DeleteTemplateVersionWithResponse(ctx, o.FleetName, name)
	case kind == TemplateVersionKind && len(name) == 0:
		response, err = c.DeleteTemplateVersionsWithResponse(ctx, o.FleetName)
	case kind == RepositoryKind && len(name) > 0:
		response, err = c.DeleteRepositoryWithResponse(ctx, name)
	case kind == RepositoryKind && len(name) == 0:
		response, err = c.DeleteRepositoriesWithResponse(ctx)
	case kind == ResourceSyncKind && len(name) > 0:
		response, err = c.DeleteResourceSyncWithResponse(ctx, name)
	case kind == ResourceSyncKind && len(name) == 0:
		response, err = c.DeleteResourceSyncsWithResponse(ctx)
	case kind == CertificateSigningRequestKind && len(name) > 0:
		response, err = c.DeleteCertificateSigningRequestWithResponse(ctx, name)
	case kind == CertificateSigningRequestKind && len(name) == 0:
		response, err = c.DeleteCertificateSigningRequestsWithResponse(ctx)
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return processDeletionReponse(response, err, kind, name)
}

func processDeletionReponse(response interface{}, err error, kind string, name string) error {
	errorPrefix := fmt.Sprintf("deleting %s", kind)
	if len(name) > 0 {
		errorPrefix = fmt.Sprintf("deleting %s/%s", kind, name)
	}

	if err != nil {
		return fmt.Errorf("%s: %w", errorPrefix, err)
	}

	v := reflect.ValueOf(response).Elem()
	if v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int() != http.StatusOK {
		return fmt.Errorf(errorPrefix+": %s (%d)", v.FieldByName("HTTPResponse").Elem().FieldByName("Status").String(), v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int())
	}

	return nil
}
