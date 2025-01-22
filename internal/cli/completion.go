package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	validCompletionArgs = []string{"bash", "zsh", "fish", "PowerShell"}
)

type CompletionOptions struct {
	GlobalOptions

	Shell string
}

func DefaultCompletionOptions() *CompletionOptions {
	return &CompletionOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Shell:         "bash",
	}
}

func NewCmdCompletion() *cobra.Command {
	o := DefaultCompletionOptions()
	cmd := &cobra.Command{
		Use:          "completion",
		Short:        "Generate autocompletion script",
		SilenceUsage: true,
		ValidArgs:    validCompletionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}

			var err error
			switch o.Shell {
			case "bash":
				err = cmd.Root().GenBashCompletion(os.Stdout)
			case "zsh":
				err = cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				err = cmd.Root().GenFishCompletion(os.Stdout, true)
			case "PowerShell":
				err = cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			}

			return err
		},
	}

	return cmd
}

func (o *CompletionOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	if len(args) != 0 {
		o.Shell = args[0]
	}

	return nil
}

func (o *CompletionOptions) Validate(args []string) error {
	if err := o.GlobalOptions.ValidateCmd(args); err != nil {
		return err
	}

	if len(args) != 0 {
		validShell := false
		for _, e := range validCompletionArgs {
			if e == args[0] {
				validShell = true
				break
			}
		}

		if !validShell {
			return fmt.Errorf("autocompletion for shell %v not supported by Cobra", args[0])
		}
	}

	return nil
}
