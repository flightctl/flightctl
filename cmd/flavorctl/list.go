package main

import (
	"fmt"
	"sort"

	"github.com/flightctl/flightctl/internal/pkg/flavors"
	"github.com/spf13/cobra"
)

func NewListCommand(flavorsFile, overrideFile *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available flavors",
		Long:  "List all available flavor configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			var override string
			if overrideFile != nil && *overrideFile != "" {
				override = *overrideFile
			}
			flavorNames, err := flavors.ListFlavors(*flavorsFile, override)
			if err != nil {
				return err
			}

			if len(flavorNames) == 0 {
				fmt.Println("No flavors found")
				return nil
			}

			// Sort for consistent output
			sort.Strings(flavorNames)

			fmt.Println("Available flavors:")
			for _, name := range flavorNames {
				fmt.Printf("  %s\n", name)
			}

			return nil
		},
	}

	return cmd
}
