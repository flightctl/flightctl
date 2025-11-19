package main

import (
	"github.com/flightctl/flightctl/pkg/template"
	"github.com/spf13/cobra"
)

type RenderTemplateOptions struct {
	Config     string
	InputFile  string
	OutputFile string
}

func NewRenderTemplateCommand() *cobra.Command {
	opts := &RenderTemplateOptions{}

	cmd := &cobra.Command{
		Use:   "template",
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

func (o *RenderTemplateOptions) Run() error {
	return template.Render(o.Config, o.InputFile, o.OutputFile)
}
