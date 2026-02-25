package main

import (
	"fmt"

	"github.com/flightctl/flightctl/internal/pkg/flavors"
	"github.com/spf13/cobra"
)

func NewValidateCommand(flavorsFile, overrideFile *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate flavor configuration",
		Long:  "Validate the syntax and inheritance of flavor configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			var override string
			if overrideFile != nil && *overrideFile != "" {
				override = *overrideFile
			}

			flavorsPath := ""
			if flavorsFile != nil {
				flavorsPath = *flavorsFile
			}
			allFlavors, err := flavors.LoadFlavors(flavorsPath, override)
			if err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}

			fmt.Printf("Successfully validated %d flavors:\n", len(allFlavors))
			for name := range allFlavors {
				fmt.Printf("  %s\n", name)
			}

			return nil
		},
	}

	return cmd
}
