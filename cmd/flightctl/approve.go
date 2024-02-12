package main

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/spf13/cobra"
)

var approveLabels []string
var approveRegion string

func NewCmdApprove() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve",
		Short: "approve enrollment-request fleet-name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunApprove(args[0])
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringArrayVarP(&approveLabels, "label", "l", []string{}, "labels to add to the device")
	cmd.Flags().StringVarP(&approveRegion, "region", "r", "default", "region for the device")
	return cmd
}

func RunApprove(enrollmentRequestName string) error {
	c, err := client.NewFromConfigFile(clientConfigFile)
	if err != nil {
		return fmt.Errorf("creating client: %v", err)
	}
	labels := util.LabelArrayToMap(approveLabels)

	approval := api.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &labels,
		Region:   util.StrToPtr(approveRegion),
	}
	resp, err := c.CreateEnrollmentRequestApproval(context.Background(), enrollmentRequestName, approval)
	if err != nil {
		return fmt.Errorf("creating enrollmentrequestapproval: %w, http response: %+v", err, resp)
	}
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("creating enrollmentrequestapproval: %+v", resp)
	}
	return nil
}
