package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
	client "github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	appName               = "flightctl"
	defaultConfigFileName = "client"
	defaultConfigFileExt  = "yaml"
)

type GlobalOptions struct {
	ConfigDir      string
	ConfigFilePath string
	Context        string
	Organization   string
	RequestTimeout int
}

func DefaultGlobalOptions() GlobalOptions {
	return GlobalOptions{
		ConfigFilePath: filepath.Clean(ConfigFilePath("", "")),
		Context:        "",
		Organization:   "",
		RequestTimeout: 0,
	}
}

func (o *GlobalOptions) Bind(fs *pflag.FlagSet) {
	fs.StringVarP(&o.Organization, "org", "", o.Organization, "If present, use the specified organization for the request. This overrides the organization in the config file.")
	fs.StringVarP(&o.Context, "context", "c", o.Context, "Read client config from 'client_<context>.yaml' instead of 'client.yaml'.")
	fs.StringVarP(&o.ConfigDir, "config-dir", "", o.ConfigDir, "Specify the directory for client configuration files.")
	fs.IntVar(&o.RequestTimeout, "request-timeout", o.RequestTimeout, "Request Timeout in seconds (0 - use default OS timeout)")
}

func (o *GlobalOptions) Complete(cmd *cobra.Command, args []string) error {
	o.ConfigFilePath = ConfigFilePath(o.Context, o.ConfigDir)
	return nil
}

func (o *GlobalOptions) Validate(args []string) error {
	// 0 is a default value and is used as a flag to use a system-wide timeout
	if o.RequestTimeout < 0 {
		return fmt.Errorf("request-timeout must be greater than 0")
	}

	// If user provided --config-dir, validate it's actually a directory
	if o.ConfigDir != "" {
		stat, err := os.Stat(o.ConfigDir)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("config directory %q does not exist", o.ConfigDir)
			}
			return fmt.Errorf("failed to check config directory %q: %w", o.ConfigDir, err)
		}
		if !stat.IsDir() {
			return fmt.Errorf("config directory path %q exists but is not a directory", o.ConfigDir)
		}
	}

	if _, err := os.Stat(o.ConfigFilePath); errors.Is(err, os.ErrNotExist) {
		if o.Context != "" {
			return fmt.Errorf("context '%s' does not exist", o.Context)
		}
		return fmt.Errorf("you must log in to perform this operation. Please use the 'login' command to authenticate before proceeding")
	}
	return o.ValidateCmd(args)
}

// ValidateCmd Validates GlobalOptions without requiring ConfigFilePath to exist. This is useful for any CLI cmd that does not require user login.
func (o *GlobalOptions) ValidateCmd(args []string) error {
	if o.Organization != "" {
		if err := validateOrganizationID(o.Organization); err != nil {
			return fmt.Errorf("invalid organization ID %q: %w", o.Organization, err)
		}
	}
	return nil
}

// BuildClient constructs a FlightCTL API client using configuration derived
// from the global options (config file path, organization override, etc.).
func (o *GlobalOptions) BuildClient() (*apiclient.ClientWithResponses, error) {
	organization := o.GetEffectiveOrganization()
	return client.NewFromConfigFile(o.ConfigFilePath, client.WithOrganization(organization))
}

// GetEffectiveOrganization returns the organization ID to use for API requests.
func (o *GlobalOptions) GetEffectiveOrganization() string {
	if o.Organization != "" {
		return o.Organization
	}
	config, err := client.ParseConfigFile(o.ConfigFilePath)
	if err != nil {
		return ""
	}
	return config.Organization
}

func (o *GlobalOptions) WithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.RequestTimeout != 0 {
		return context.WithTimeout(ctx, time.Duration(o.RequestTimeout)*time.Second)
	}
	return ctx, func() {}
}

func ConfigFilePath(context string, configDirOverride string) string {
	baseDir := ConfigDir(configDirOverride)
	if len(context) > 0 && context != "default" {
		return filepath.Join(baseDir, defaultConfigFileName+"_"+context+"."+defaultConfigFileExt)
	}
	return filepath.Join(baseDir, defaultConfigFileName+"."+defaultConfigFileExt)
}

func ConfigDir(configDirOverride string) string {
	if configDirOverride != "" {
		return configDirOverride
	}
	baseDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatalf("could not get user config directory %v", err)
	}
	return filepath.Join(baseDir, appName)
}
