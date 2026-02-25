package main

import (
	"os"

	"github.com/flightctl/flightctl/internal/pkg/flavors"
	"github.com/spf13/cobra"
)

func main() {
	command := NewFlavorCtlCommand()
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}

func NewFlavorCtlCommand() *cobra.Command {
	var flavorsFile string
	var overrideFile string

	cmd := &cobra.Command{
		Use:   "flavorctl [command]",
		Short: "flavorctl manages FlightCtl flavor configurations",
		Long: `flavorctl is a command-line tool for managing FlightCtl flavor configurations.
It provides commands to list, inspect, and validate flavor definitions.`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Auto-discover flavors file if not specified
			if flavorsFile == "" {
				foundFile, err := flavors.FindFlavorsFile()
				if err != nil {
					return err
				}
				flavorsFile = foundFile
			}
			// Store file paths in command context for subcommands
			cmd.SetContext(cmd.Context())
			return nil
		},
	}

	// Global flags
	cmd.PersistentFlags().StringVarP(&flavorsFile, "file", "f", "",
		"Path to flavors.yaml file (auto-discovered if not specified)")
	cmd.PersistentFlags().StringVar(&overrideFile, "override", "",
		"Path to override flavors file (for downstream customization)")

	// Add subcommands
	cmd.AddCommand(NewListCommand(&flavorsFile, &overrideFile))
	cmd.AddCommand(NewGetCommand(&flavorsFile, &overrideFile))
	cmd.AddCommand(NewValidateCommand(&flavorsFile, &overrideFile))
	cmd.AddCommand(NewImageCommand(&flavorsFile, &overrideFile))

	return cmd
}
