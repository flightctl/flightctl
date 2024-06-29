package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type GlobalOptions struct {
}

func DefaultGlobalOptions() GlobalOptions {
	return GlobalOptions{}
}

func (o *GlobalOptions) Bind(fs *pflag.FlagSet) {
}

func (o *GlobalOptions) Complete(cmd *cobra.Command, args []string) error {
	return nil
}

func (o *GlobalOptions) Validate(args []string) error {
	return nil
}
