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
}

func DefaultGlobalOptions() GlobalOptions {
	return GlobalOptions{
		ConfigFilePath: ConfigFilePath(),
	}
}

func (o *GlobalOptions) Bind(fs *pflag.FlagSet) {
}

func (o *GlobalOptions) Complete(cmd *cobra.Command, args []string) error {
	return nil
}

func (o *GlobalOptions) Validate(args []string) error {
	return nil
}

func ConfigFilePath() string {
	return filepath.Join(ConfigDir(), defaultConfigFileName+"."+defaultConfigFileExt)
}

func ConfigDir() string {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		log.Fatal("Could not find the user config directory because ", err)
	}
	return filepath.Join(configRoot, appName)
}
