package cli

import (
	"context"
	"fmt"
	"net/http"

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
		Use:   "approve enrollmentrequest/NAME",
		Short: "Approve an enrollment request.",
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

	return nil
}

func (o *ApproveOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	enrollmentRequestName := args[0]
	labels := util.LabelArrayToMap(o.ApproveLabels)
	approval := api.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &labels,
	}
	response, err := c.CreateEnrollmentRequestApprovalWithResponse(ctx, enrollmentRequestName, approval, getPrintHttpFn(&o.GlobalOptions))
	if err != nil {
		return fmt.Errorf("creating enrollmentrequestapproval: %w", err)
	}

	if o.VerboseHttp {
		printRawHttpResponse(response.HTTPResponse, response.Body)
	}

	if response.HTTPResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", response.HTTPResponse.Status)
	}
	return nil
}
