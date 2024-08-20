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

type DenyOptions struct {
	GlobalOptions
}

func DefaultDenyOptions() *DenyOptions {
	return &DenyOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func NewCmdDeny() *cobra.Command {
	o := DefaultDenyOptions()
	cmd := &cobra.Command{
		Use:   "deny TYPE/NAME",
		Short: "Deny a request.",
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

func (o *DenyOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
}

func (o *DenyOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	return nil
}

func (o *DenyOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	if kind != CertificateSigningRequestKind {
		return fmt.Errorf("kind must be %s", CertificateSigningRequestKind)
	}

	if len(name) == 0 {
		return fmt.Errorf("specify a specific request resource to deny")
	}

	return nil
}

func (o *DenyOptions) Run(ctx context.Context, args []string) error {
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
	case kind == CertificateSigningRequestKind:
		response, err = c.DenyCertificateSigningRequest(ctx, name)
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return processDenyReponse(response, err, kind, name)
}

func processDenyReponse(response interface{}, err error, kind string, name string) error {
	errorPrefix := fmt.Sprintf("denying %s/%s", kind, name)
	if err != nil {
		return fmt.Errorf("%s: %w", errorPrefix, err)
	}

	v := reflect.ValueOf(response).Elem()
	if v.FieldByName("StatusCode").Int() != http.StatusOK {
		return fmt.Errorf(errorPrefix+": %d", v.FieldByName("StatusCode").Int())
	}

	return nil
}
