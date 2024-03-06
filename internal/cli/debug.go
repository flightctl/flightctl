package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var owner string
var templateVersion string

func NewCmdDebug() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "various commands that are useful for debuggiung",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}
	cmd.AddCommand(NewCmdDevSpec())

	return cmd
}

func NewCmdDevSpec() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devspec",
		Short: "devspec devname",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var knownOwner *string
			if cmd.Flags().Lookup("owner").Changed {
				knownOwner = &owner
			}
			var knownTV *string
			if cmd.Flags().Lookup("templateVersion").Changed {
				knownTV = &templateVersion
			}

			return RunGetDevSpec(cmd.Context(), args[0], knownOwner, knownTV)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVar(&owner, "owner", "", "device owner")
	cmd.Flags().StringVar(&templateVersion, "templateVersion", "", "device templateVersion")
	return cmd
}

func RunGetDevSpec(ctx context.Context, deviceName string, knownOwner, knownTV *string) error {
	c, err := client.NewFromConfigFile(defaultClientConfigFile)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	params := api.GetRenderedDeviceSpecParams{
		KnownOwner:           knownOwner,
		KnownTemplateVersion: knownTV,
	}
	resp, err := c.GetRenderedDeviceSpecWithResponse(ctx, deviceName, &params)
	if err != nil {
		return fmt.Errorf("creating enrollmentrequestapproval: %w, http response: %+v", err, resp)
	}

	if resp.HTTPResponse.StatusCode == http.StatusNoContent {
		fmt.Printf("<no change>\n")
		return nil
	}

	if resp.HTTPResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("getting device spec for %s failed: %d", deviceName, resp.HTTPResponse.StatusCode)
	}

	marshalled, err := yaml.Marshal(resp.JSON200)
	if err != nil {
		return fmt.Errorf("marshalling resource: %w", err)
	}

	fmt.Printf("%s\n", string(marshalled))
	return nil
}
