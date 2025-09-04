package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	validCompletionArgs = []string{"bash", "zsh", "fish", "powershell", "pwsh"}
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
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell autocompletion script",
		Long: `Generate the autocompletion script for flightctl for the specified shell.

Supported shells: bash, zsh, fish, powershell.

To load completions for the current session:
  - bash:        source <(flightctl completion bash)
  - zsh:         source <(flightctl completion zsh)
  - fish:        flightctl completion fish | source
  - powershell:  flightctl completion powershell | Out-String | Invoke-Expression

To load completions persistently:
  - bash (Linux):      flightctl completion bash | sudo tee /etc/bash_completion.d/flightctl > /dev/null
  - bash (macOS/Homebrew): flightctl completion bash > $(brew --prefix)/etc/bash_completion.d/flightctl
  - zsh:               flightctl completion zsh > ${ZDOTDIR:-$HOME}/.zsh/completions/_flightctl && echo 'fpath+=(${ZDOTDIR:-$HOME}/.zsh/completions); autoload -U compinit; compinit' >> ${ZDOTDIR:-$HOME}/.zshrc
  - fish:              flightctl completion fish > ~/.config/fish/completions/flightctl.fish
  - powershell:        flightctl completion powershell > "$HOME/.config/flightctl/completion.ps1"; Add-Content -Path $PROFILE -Value ". $HOME/.config/flightctl/completion.ps1"
`,
		Example: `  # Bash (current session)
  source <(flightctl completion bash)

  # Zsh (current session)
  source <(flightctl completion zsh)

  # Fish (current session)
  flightctl completion fish | source

  # PowerShell (current session)
  flightctl completion powershell | Out-String | Invoke-Expression
`,
		SilenceUsage: true,
		ValidArgs:    validCompletionArgs,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Support `flightctl completion help` in addition to `--help`
			if len(args) > 0 && args[0] == "help" {
				return cmd.Help()
			}

			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}

			// Print a small header only when writing to a terminal
			if fi, _ := os.Stdout.Stat(); (fi.Mode() & os.ModeCharDevice) != 0 {
				switch o.Shell {
				case "bash":
					fmt.Fprint(os.Stdout, "# flightctl bash completion\n#\n# Installation: flightctl completion bash >> ~/.bashrc\n# Or:           flightctl completion bash | sudo tee /etc/bash_completion.d/flightctl > /dev/null\n# Load now:     source <(flightctl completion bash)\n")
				case "zsh":
					fmt.Fprint(os.Stdout, "# flightctl zsh completion\n#\n# Installation: flightctl completion zsh > ${ZDOTDIR:-$HOME}/.zsh/completions/_flightctl\n# Ensure:       autoload -U compinit; compinit\n# Load now:     source <(flightctl completion zsh)\n")
				case "fish":
					fmt.Fprint(os.Stdout, "# flightctl fish completion\n#\n# Installation: flightctl completion fish > ~/.config/fish/completions/flightctl.fish\n# Load now:     flightctl completion fish | source\n")
				case "powershell":
					fmt.Fprint(os.Stdout, "# flightctl PowerShell completion\n#\n# Installation: flightctl completion powershell > \"$HOME/.config/flightctl/completion.ps1\"; Add-Content -Path $PROFILE -Value \". $HOME/.config/flightctl/completion.ps1\"\n# Load now:     flightctl completion powershell | Out-String | Invoke-Expression\n")
				}
			}

			var err error
			switch o.Shell {
			case "bash":
				err = cmd.Root().GenBashCompletion(os.Stdout)
			case "zsh":
				err = cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				err = cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
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
		// Normalize shell name to lowercase and support common aliases
		candidate := strings.ToLower(args[0])
		if candidate == "pwsh" {
			candidate = "powershell"
		}
		o.Shell = candidate
	}

	return nil
}

func (o *CompletionOptions) Validate(args []string) error {
	if err := o.GlobalOptions.ValidateCmd(args); err != nil {
		return err
	}

	if len(args) != 0 {
		candidate := strings.ToLower(args[0])
		if candidate == "pwsh" {
			candidate = "powershell"
		}
		validShell := false
		for _, e := range validCompletionArgs {
			if e == candidate {
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
