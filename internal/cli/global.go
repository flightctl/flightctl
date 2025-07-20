package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	appName               = "flightctl"
	defaultConfigFileName = "client"
	defaultConfigFileExt  = "yaml"
)

type GlobalOptions struct {
	ConfigFilePath string
	ConfigDir      string
	Context        string
	RequestTimeout int
}

func DefaultGlobalOptions() GlobalOptions {
	return GlobalOptions{
		ConfigFilePath: ConfigFilePath("", ""),
		Context:        "",
		RequestTimeout: 0,
	}
}

func (o *GlobalOptions) Bind(fs *pflag.FlagSet) {
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

	if o.ConfigDir != "" {
		path := filepath.Clean(o.ConfigDir)
		ext := filepath.Ext(path)
		if ext != "" {
			return fmt.Errorf("config-dir should specify a directory path")
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

// Validates GlobalOptions without requiring ConfigFilePath to exist. This is useful for any CLI cmd that does not require user login.
func (o *GlobalOptions) ValidateCmd(args []string) error {
	return nil
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
