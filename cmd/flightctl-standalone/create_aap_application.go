package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	standaloneconfig "github.com/flightctl/flightctl/internal/config/standalone"
	"github.com/flightctl/flightctl/internal/quadlet/renderer"
	"github.com/flightctl/flightctl/internal/standalone"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

type CreateAAPApplicationOptions struct {
	Config       string
	OutputFile   string
	CACertFile   string
	AppName      string
	Organization int
}

func NewAAPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aap [command]",
		Short: "AAP integration commands",
		Long:  "Commands for integrating with Ansible Automation Platform (AAP)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(NewCreateAAPApplicationCommand())

	return cmd
}

func NewCreateAAPApplicationCommand() *cobra.Command {
	opts := &CreateAAPApplicationOptions{}

	cmd := &cobra.Command{
		Use:   "create-oauth-application",
		Short: "Create an OAuth application in AAP",
		Long: `Create an OAuth application in AAP Gateway for Flight Control.

This command reads the service configuration to get AAP settings and creates
an OAuth application if one is not already configured. Requires setting the aap.apiUrl and aap.token in the service configuration. 
The resulting client_id is written to the specified output file.

The command is idempotent: it will skip creation if:
- A clientId is already set in the service configuration
- The output file already exists`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return opts.Run()
		},
	}

	cmd.Flags().StringVar(&opts.Config, "config", renderer.DefaultServiceConfigPath, "Path to the service configuration file")
	cmd.Flags().StringVar(&opts.OutputFile, "output-file", renderer.DefaultAAPClientIDPath, "Output file path for the client_id")
	cmd.Flags().StringVar(&opts.CACertFile, "ca-cert-file", renderer.DefaultAuthCACertPath, "Path to CA certificate file for AAP TLS verification")
	cmd.Flags().StringVar(&opts.AppName, "app-name", "Flight Control", "Name for the OAuth application")
	cmd.Flags().IntVar(&opts.Organization, "organization", aap.DefaultOrganizationID, "AAP organization ID")

	return cmd
}

func (o *CreateAAPApplicationOptions) Run() error {
	logger := logrus.New()

	if _, err := os.Stat(o.OutputFile); err == nil {
		logger.Infof("AAP OAuth client_id file already exists at %s - skipping creation", o.OutputFile)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to check if output file exists: %w", err)
	}

	configData, err := os.ReadFile(o.Config)
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", o.Config, err)
	}

	var config standaloneconfig.Config
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return fmt.Errorf("failed to parse config YAML from %s: %w", o.Config, err)
	}

	// Default baseDomain if empty
	if config.Global.BaseDomain == "" {
		hostname, err := standalone.GetHostnameFQDN()
		if err != nil {
			return fmt.Errorf("failed to get hostname for baseDomain default: %w", err)
		}
		config.Global.BaseDomain = hostname
	}

	if errs := validation.ValidateStandaloneConfig(&config); len(errs) > 0 {
		for _, err := range errs {
			logger.Errorf("Config validation error: %s", err)
		}
		return fmt.Errorf("config validation failed with %d error(s)", len(errs))
	}

	if config.Global.Auth.Type != standaloneconfig.AuthTypeAAP {
		logger.Infof("Auth type is %s not 'aap', skipping AAP application creation", config.Global.Auth.Type)
		return nil
	}

	aapConfig := config.Global.Auth.AAP
	if aapConfig == nil {
		logger.Infof("No AAP config found, skipping AAP application creation")
		return nil
	}

	if aapConfig.ClientID != "" {
		logger.Infof("AAP clientId is already configured, skipping creation")
		return nil
	}

	if aapConfig.ApiURL == "" {
		return fmt.Errorf("AAP apiUrl is required but not configured")
	}

	if aapConfig.Token == "" {
		return fmt.Errorf("AAP token is required but not configured")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := standalone.CreateAAPClient(standalone.CreateAAPClientOptions{
		AAPConfig:       aapConfig,
		BaseDomain:      config.Global.BaseDomain,
		InsecureSkipTLS: config.Global.Auth.InsecureSkipTlsVerify,
		CACertFile:      o.CACertFile,
		Logger:          logger,
	})
	if err != nil {
		return fmt.Errorf("failed to create AAP client: %w", err)
	}

	return standalone.CreateAAPApplication(ctx, standalone.CreateAAPApplicationOptions{
		AAPConfig:    aapConfig,
		BaseDomain:   config.Global.BaseDomain,
		Client:       client,
		Logger:       logger,
		AppName:      o.AppName,
		Organization: o.Organization,
		OutputFile:   o.OutputFile,
	})
}
