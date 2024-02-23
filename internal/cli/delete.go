package cli

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
)

func NewCmdDelete() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "delete resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseAndValidateKindName(args[0])
			if err != nil {
				return err
			}
			return RunDelete(kind, name)
		},
		SilenceUsage: true,
	}
	return cmd
}

func RunDelete(kind, name string) error {
	c, err := client.NewFromConfigFile(defaultClientConfigFile)
	if err != nil {
		return fmt.Errorf("creating client: %v", err)
	}

	switch kind {
	case "device":
		if len(name) > 0 {
			response, err := c.DeleteDeviceWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting device/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteDevicesWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting devices: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "enrollmentrequest":
		if len(name) > 0 {
			response, err := c.DeleteEnrollmentRequestWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting enrollmentrequest/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteEnrollmentRequestsWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting enrollmentrequests: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "fleet":
		if len(name) > 0 {
			response, err := c.DeleteFleetWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting fleet/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteFleetsWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting fleets: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "repository":
		if len(name) > 0 {
			response, err := c.DeleteRepositoryWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting repository/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteRepositoriesWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting repositories: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "resourcesync":
		if len(name) > 0 {
			response, err := c.DeleteResourceSyncWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting repository/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteResourceSyncsWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting repositories: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return nil
}
