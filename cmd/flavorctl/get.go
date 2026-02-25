package main

import (
	"fmt"

	"github.com/flightctl/flightctl/internal/pkg/flavors"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func NewGetCommand(flavorsFile, overrideFile *string) *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "get FLAVOR_NAME",
		Short: "Get flavor configuration",
		Long:  "Get the complete configuration for a specific flavor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flavorName := args[0]

			var override string
			if overrideFile != nil && *overrideFile != "" {
				override = *overrideFile
			}

			flavorsPath := ""
			if flavorsFile != nil {
				flavorsPath = *flavorsFile
			}
			flavor, err := flavors.GetFlavor(flavorName, flavorsPath, override)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "yaml", "yml":
				data, err := yaml.Marshal(flavor)
				if err != nil {
					return fmt.Errorf("failed to marshal flavor to YAML: %w", err)
				}
				fmt.Print(string(data))
			case "name":
				fmt.Println(flavor.Name)
			case "description":
				fmt.Println(flavor.Description)
			default:
				return fmt.Errorf("unsupported output format: %s", outputFormat)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFormat, "output", "o", "yaml",
		"Output format (yaml, name, description)")

	return cmd
}
