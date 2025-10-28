package main

import (
	"fmt"
	"os"

	"github.com/flightctl/flightctl/pkg/template"
	"github.com/spf13/cobra"
)

func main() {
	command := NewGroundCrewCommand()
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func NewGroundCrewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flightctl-ground-crew [command]",
		Short: "Flight Control utility functions for quadlet services",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(0)
		},
	}
	cmd.AddCommand(NewRenderCommand())
	return cmd
}

type RenderOptions struct {
	Config     string
	InputFile  string
	OutputFile string
}

func NewRenderCommand() *cobra.Command {
	opts := &RenderOptions{}

	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render templates from config data",
		Long:  `Render templates using configuration data from a YAML file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return opts.Run()
		},
	}

	cmd.Flags().StringVar(&opts.Config, "config", "/etc/flightctl/service-config.yaml", "Path to the service configuration file")
	cmd.Flags().StringVar(&opts.InputFile, "input-file", "", "Input template file to render")
	cmd.Flags().StringVar(&opts.OutputFile, "output-file", "", "Output file path")

	_ = cmd.MarkFlagRequired("input-file")
	_ = cmd.MarkFlagRequired("output-file")

	return cmd
}

func (o *RenderOptions) Run() error {
	return template.Render(o.Config, o.InputFile, o.OutputFile)
}
