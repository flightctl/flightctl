package main

import (
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/quadlet/renderer"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func main() {
	command := NewRenderQuadletsCommand()
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func NewRenderQuadletsCommand() *cobra.Command {
	var cfgFile string
	config := renderer.NewRendererConfig()

	cmd := &cobra.Command{
		Use:   "render-quadlets",
		Short: "Render FlightCtl service quadlet files",
		Long:  "A tool to render FlightCtl service quadlet files and systemd units from templates",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return initConfig(cfgFile, config)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return renderer.RenderQuadlets(config)
		},
	}

	cmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (required)")

	cmd.Flags().StringVar(&config.ReadOnlyConfigOutputDir, "readonly-config-dir", config.ReadOnlyConfigOutputDir, "Read-only config output directory")
	cmd.Flags().StringVar(&config.WriteableConfigOutputDir, "writeable-config-dir", config.WriteableConfigOutputDir, "Writeable config output directory")
	cmd.Flags().StringVar(&config.QuadletFilesOutputDir, "quadlet-dir", config.QuadletFilesOutputDir, "Quadlet files output directory")
	cmd.Flags().StringVar(&config.SystemdUnitOutputDir, "systemd-dir", config.SystemdUnitOutputDir, "Systemd unit output directory")
	cmd.Flags().StringVar(&config.BinOutputDir, "bin-dir", config.BinOutputDir, "Binary output directory")
	cmd.Flags().StringVar(&config.FlightctlServicesTagOverride, "flightctl-services-tag-override", "", "Override image tags for all FlightCtl services")

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
