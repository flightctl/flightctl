package cli

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/spf13/cobra"
)

type ApproveOptions struct {
	ApproveLabels []string
	ApproveRegion string
}

func NewCmdApprove() *cobra.Command {
	o := &ApproveOptions{}
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

	cmd.Flags().StringArrayVarP(&o.ApproveLabels, "label", "l", []string{}, "Labels to add to the device, as a comma-separated list of key=value.")
	cmd.Flags().StringVarP(&o.ApproveRegion, "region", "r", "default", "Region for the device.")
	return cmd
}

func (o *ApproveOptions) Complete(cmd *cobra.Command, args []string) error {
	return nil
}

func (o *ApproveOptions) Validate(args []string) error {
	return nil
}

func (o *ApproveOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(defaultClientConfigFile)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	enrollmentRequestName := args[0]
	labels := util.LabelArrayToMap(o.ApproveLabels)
	approval := api.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &labels,
		Region:   util.StrToPtr(o.ApproveRegion),
	}
	resp, err := c.CreateEnrollmentRequestApproval(ctx, enrollmentRequestName, approval)
	if err != nil {
		return fmt.Errorf("creating enrollmentrequestapproval: %w, http response: %+v", err, resp)
	}
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("creating enrollmentrequestapproval: %+v", resp)
	}
	return nil
}
