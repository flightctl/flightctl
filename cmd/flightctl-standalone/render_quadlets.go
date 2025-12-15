package main

import (
	"fmt"

	"github.com/flightctl/flightctl/internal/quadlet/renderer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func NewRenderQuadletsCommand() *cobra.Command {
	var cfgFile string
	config := renderer.NewRendererConfig()

	cmd := &cobra.Command{
		Use:   "quadlets",
		Short: "Render Flight Control service quadlet files",
		Long:  "A tool to render Flight Control service quadlet files and systemd units from templates",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return initConfig(cfgFile, config)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := log.InitLogs()
			return renderer.RenderQuadlets(config, logger)
		},
	}

	cmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (required)")

	cmd.Flags().StringVar(&config.ReadOnlyConfigOutputDir, "readonly-config-dir", config.ReadOnlyConfigOutputDir, "Read-only config output directory")
	cmd.Flags().StringVar(&config.WriteableConfigOutputDir, "writeable-config-dir", config.WriteableConfigOutputDir, "Writeable config output directory")
	cmd.Flags().StringVar(&config.QuadletFilesOutputDir, "quadlet-dir", config.QuadletFilesOutputDir, "Quadlet files output directory")
	cmd.Flags().StringVar(&config.SystemdUnitOutputDir, "systemd-dir", config.SystemdUnitOutputDir, "Systemd unit output directory")
	cmd.Flags().StringVar(&config.BinOutputDir, "bin-dir", config.BinOutputDir, "Binary output directory")
	cmd.Flags().StringVar(&config.FlightctlServicesTagOverride, "flightctl-services-tag-override", "", "Override image tags for all FlightCtl services")
	cmd.Flags().BoolVar(&config.FlightctlUiTagOverride, "flightctl-ui-tag-override", false, "Apply tag override to UI service")

	_ = viper.BindPFlags(cmd.Flags())

	return cmd
}

func initConfig(cfgFile string, config *renderer.RendererConfig) error {
	if cfgFile == "" {
		return fmt.Errorf("config file is required (use --config flag)")
	}

	viper.SetConfigFile(cfgFile)

	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if err := viper.Unmarshal(config); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return nil
}
