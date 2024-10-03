package cli

import (
	"log"
	"os"
	"path/filepath"

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
	Context        string
}

func DefaultGlobalOptions() GlobalOptions {
	return GlobalOptions{
		ConfigFilePath: "",
		Context:        "",
	}
}

func (o *GlobalOptions) Bind(fs *pflag.FlagSet) {
	fs.StringVarP(&o.Context, "context", "c", o.Context, "Read client config from 'client_<context>.yaml' instead of 'client.yaml'.")
	fs.StringVarP(&o.ConfigFilePath, "config-path", "", o.ConfigFilePath, "Read client config located at <config-path>.")
}

func (o *GlobalOptions) Complete(cmd *cobra.Command, args []string) error {
	if o.ConfigFilePath == "" {
		o.ConfigFilePath = ConfigFilePath(o.Context)
	}
	return nil
}

func (o *GlobalOptions) Validate(args []string) error {
	return nil
}

func ConfigFilePath(context string) string {
	if len(context) > 0 && context != "default" {
		return filepath.Join(ConfigDir(), defaultConfigFileName+"_"+context+"."+defaultConfigFileExt)
	}
	return filepath.Join(ConfigDir(), defaultConfigFileName+"."+defaultConfigFileExt)
}

func ConfigDir() string {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		log.Fatal("Could not find the user config directory because ", err)
	}
	return filepath.Join(configRoot, appName)
}
