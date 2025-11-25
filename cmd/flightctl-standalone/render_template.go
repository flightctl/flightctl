package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/config/standalone"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/template"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
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
	if err := o.validateConfig(); err != nil {
		return err
	}

	return template.Render(o.Config, o.InputFile, o.OutputFile)
}

func (o *RenderTemplateOptions) validateConfig() error {
	configData, err := os.ReadFile(o.Config)
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", o.Config, err)
	}

	var config standalone.Config
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return fmt.Errorf("failed to parse config YAML from %s: %w", o.Config, err)
	}

	if errs := validation.ValidateStandaloneConfig(&config); len(errs) > 0 {
		errMsgs := make([]string, len(errs))
		for i, err := range errs {
			errMsgs[i] = err.Error()
		}
		return fmt.Errorf("configuration validation failed:\n  - %s", strings.Join(errMsgs, "\n  - "))
	}

	return nil
}
