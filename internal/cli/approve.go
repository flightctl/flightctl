package cli

import (
	"context"
	"fmt"
	"net/http"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type ApproveOptions struct {
	GlobalOptions

	ApproveLabels []string
}

func DefaultApproveOptions() *ApproveOptions {
	return &ApproveOptions{
		GlobalOptions: DefaultGlobalOptions(),
		ApproveLabels: []string{},
	}
}

func NewCmdApprove() *cobra.Command {
	o := DefaultApproveOptions()
	cmd := &cobra.Command{
		Use:   "approve TYPE/NAME",
		Short: "Approve a request.",
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

func (o *ApproveOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringArrayVarP(&o.ApproveLabels, "label", "l", []string{}, "Labels to add to the device, as a comma-separated list of key=value.")
}

func (o *ApproveOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	return nil
}

func (o *ApproveOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	if kind != EnrollmentRequestKind && kind != CertificateSigningRequestKind {
		return fmt.Errorf("kind must be either %s or %s", EnrollmentRequestKind, CertificateSigningRequestKind)
	}

	if len(name) == 0 {
		return fmt.Errorf("specify a specific request resource to approve")
	}

	if len(o.ApproveLabels) > 0 && kind != EnrollmentRequestKind {
		return fmt.Errorf("labels only apply to %s approval", EnrollmentRequestKind)
	}

	return nil
}

func (o *ApproveOptions) Run(ctx context.Context, args []string) error {
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
	case kind == EnrollmentRequestKind:
		labels := util.LabelArrayToMap(o.ApproveLabels)
		approval := api.EnrollmentRequestApproval{
			Approved: true,
			Labels:   &labels,
		}
		response, err = c.ApproveEnrollmentRequest(ctx, name, approval)
	case kind == CertificateSigningRequestKind:
		response, err = c.ApproveCertificateSigningRequest(ctx, name)
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return processApprovalReponse(response, err, kind, name)
}

func processApprovalReponse(response interface{}, err error, kind string, name string) error {
	errorPrefix := fmt.Sprintf("approving %s/%s", kind, name)
	if err != nil {
		return fmt.Errorf("%s: %w", errorPrefix, err)
	}

	v := reflect.ValueOf(response).Elem()
	if v.FieldByName("StatusCode").Int() != http.StatusOK {
		return fmt.Errorf(errorPrefix+": %s (%d)", v.FieldByName("Status").String(), v.FieldByName("StatusCode").Int())
	}

	return nil
}
