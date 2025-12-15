package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// Read the config file
	configData, err := os.ReadFile(o.Config)
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", o.Config, err)
	}

	// Unmarshal into a map to preserve all fields
	data := make(map[string]interface{})
	if err := yaml.Unmarshal(configData, &data); err != nil {
		return fmt.Errorf("failed to parse config YAML from %s: %w", o.Config, err)
	}

	// Apply defaults and completions to the config
	if err := o.completeConfig(data); err != nil {
		return err
	}

	// Validate the completed config
	if err := o.validateConfig(data); err != nil {
		return err
	}

	// Render template with the completed config data
	return template.RenderWithData(data, o.InputFile, o.OutputFile)
}

func (o *RenderTemplateOptions) completeConfig(data map[string]interface{}) error {
	// Navigate to global config
	global, ok := data["global"].(map[string]interface{})
	if !ok {
		global = make(map[string]interface{})
		data["global"] = global
	}

	// Default baseDomain if empty
	baseDomain, _ := global["baseDomain"].(string)
	if baseDomain == "" {
		hostname, err := o.getHostnameFQDN()
		if err != nil {
			return fmt.Errorf("failed to get hostname for baseDomain default: %w", err)
		}
		global["baseDomain"] = hostname
		fmt.Fprintf(os.Stderr, "global.baseDomain not set, defaulting to system hostname FQDN (%s)\n", hostname)
	}

	// Inject AAP OAuth client_id if file exists
	auth, ok := global["auth"].(map[string]interface{})
	if !ok {
		return nil
	}

	authType, _ := auth["type"].(string)
	if authType != "aap" {
		return nil
	}

	clientIDFile := "/etc/flightctl/pki/aap-client-id"
	clientIDData, err := os.ReadFile(clientIDFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("failed to read AAP client ID file %s: %w", clientIDFile, err)
	}

	clientID := strings.TrimSpace(string(clientIDData))
	if clientID == "" {
		return fmt.Errorf("AAP client ID file %s is empty", clientIDFile)
	}

	aap, ok := auth["aap"].(map[string]interface{})
	if !ok {
		aap = make(map[string]interface{})
		auth["aap"] = aap
	}
	aap["clientId"] = clientID
	fmt.Fprintf(os.Stderr, "Loaded AAP OAuth client_id from %s\n", clientIDFile)

	return nil
}

func (o *RenderTemplateOptions) getHostnameFQDN() (string, error) {
	// Try "hostname -f" first for FQDN
	cmd := exec.Command("hostname", "-f")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		hostname := strings.TrimSpace(string(output))
		if hostname != "" {
			return hostname, nil
		}
	}

	// Fallback to "hostname" (short name)
	cmd = exec.Command("hostname")
	output, err = cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute hostname command: %w", err)
	}

	hostname := strings.TrimSpace(string(output))
	if hostname == "" {
		return "", fmt.Errorf("hostname command returned empty value")
	}

	return hostname, nil
}

func (o *RenderTemplateOptions) validateConfig(data map[string]interface{}) error {
	// Marshal the map back to YAML and unmarshal to standalone.Config for validation
	configData, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal config for validation: %w", err)
	}

	var config standalone.Config
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return fmt.Errorf("failed to unmarshal config for validation: %w", err)
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
