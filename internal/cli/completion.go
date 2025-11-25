package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	api "github.com/flightctl/flightctl/api/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
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
		Use:   "completion [bash|zsh|fish|powershell|pwsh]",
		Short: "Generate shell autocompletion script",
		Long: `Generate the autocompletion script for flightctl for the specified shell.

Supported shells: bash, zsh, fish, powershell.

To load completions for the current session:
  - bash:        source <(flightctl completion bash)
  - zsh:         source <(flightctl completion zsh)
  - fish:        flightctl completion fish | source
  - powershell:  flightctl completion powershell | Out-String | Invoke-Expression

To load completions persistently:
  - bash (Linux):          flightctl completion bash | sudo tee /etc/bash_completion.d/flightctl > /dev/null
  - bash (macOS/Homebrew): flightctl completion bash > $(brew --prefix)/etc/bash_completion.d/flightctl
  - zsh:                   flightctl completion zsh > ${ZDOTDIR:-$HOME}/.zsh/completions/_flightctl && echo 'fpath+=(${ZDOTDIR:-$HOME}/.zsh/completions); autoload -U compinit; compinit' >> ${ZDOTDIR:-$HOME}/.zshrc
  - fish:                  flightctl completion fish > ~/.config/fish/completions/flightctl.fish
  - powershell:            flightctl completion powershell > "$env:USERPROFILE\Documents\PowerShell\completion.ps1"; Add-Content -Path $PROFILE -Value ". $env:USERPROFILE\Documents\PowerShell\completion.ps1"
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
			if fi, err := os.Stdout.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
				switch o.Shell {
				case "bash":
					fmt.Fprint(os.Stdout, "# flightctl bash completion\n#\n# Installation: flightctl completion bash >> ~/.bashrc\n# Or:           flightctl completion bash | sudo tee /etc/bash_completion.d/flightctl > /dev/null\n# Load now:     source <(flightctl completion bash)\n")
				case "zsh":
					fmt.Fprint(os.Stdout, "# flightctl zsh completion\n#\n# Installation: flightctl completion zsh > ${ZDOTDIR:-$HOME}/.zsh/completions/_flightctl\n# Ensure:       autoload -U compinit; compinit\n# Load now:     source <(flightctl completion zsh)\n")
				case "fish":
					fmt.Fprint(os.Stdout, "# flightctl fish completion\n#\n# Installation: flightctl completion fish > ~/.config/fish/completions/flightctl.fish\n# Load now:     flightctl completion fish | source\n")
				case "powershell":
					fmt.Fprint(os.Stdout, "# flightctl PowerShell completion\n#\n# Installation: flightctl completion powershell > \"$env:USERPROFILE\\Documents\\PowerShell\\completion.ps1\"; Add-Content -Path $PROFILE -Value \". $env:USERPROFILE\\Documents\\PowerShell\\completion.ps1\"\n# Load now:     flightctl completion powershell | Out-String | Invoke-Expression\n")
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
			return fmt.Errorf("unsupported shell %q. Supported shells: bash, zsh, fish, powershell", args[0])
		}
	}

	return nil
}

type ClientBuilderOptions interface {
	Complete(cmd *cobra.Command, args []string) error
	BuildClient() (*apiclient.ClientWithResponses, error)
}

type KindNameAutocomplete struct {
	Options            ClientBuilderOptions
	AllowMultipleNames bool
	AllowedKinds       []ResourceKind
	FleetName          *string
}

func (kna KindNameAutocomplete) ValidArgsFunction(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) >= 2 && !kna.AllowMultipleNames {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	if len(args) == 0 {
		kindLike, _, _ := strings.Cut(toComplete, "/")
		if kind, err := ResourceKindFromString(kindLike); err == nil && kna.isKindAllowed(kind) {
			names := kna.getAutocompleteNames(cmd, kna.Options, kind)
			if len(names) > 0 {
				var out []string
				for _, n := range names {
					out = append(out, kindLike+"/"+n)
				}
				return out, cobra.ShellCompDirectiveNoFileComp
			}
		}

		var kindStrs []string
		for _, k := range kna.AllowedKinds {
			if kna.AllowMultipleNames {
				kindStrs = append(kindStrs, k.ToPlural())
			} else {
				kindStrs = append(kindStrs, k.String())
			}
		}
		return kindStrs, cobra.ShellCompDirectiveNoFileComp
	}

	existingNames := args[1:]

	kind, err := ResourceKindFromString(args[0])
	if err != nil || !kna.isKindAllowed(kind) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := kna.getAutocompleteNames(cmd, kna.Options, kind)
	uniqueNames := slices.DeleteFunc(names, func(n string) bool {
		return slices.Contains(existingNames, n)
	})

	uniqueNames = slices.DeleteFunc(uniqueNames, func(n string) bool {
		return !strings.HasPrefix(n, toComplete)
	})

	return uniqueNames, cobra.ShellCompDirectiveNoFileComp
}

//nolint:gocyclo
func (kna *KindNameAutocomplete) getAutocompleteNames(cmd *cobra.Command, o ClientBuilderOptions, kind ResourceKind) []string {
	var names []string
	if err := o.Complete(cmd, nil); err != nil {
		return nil
	}
	c, err := o.BuildClient()
	if err == nil {
		switch kind {
		case DeviceKind:
			resp, err := c.ListDevicesWithResponse(context.Background(), &api.ListDevicesParams{})
			if err == nil && resp.JSON200 != nil {
				for _, er := range resp.JSON200.Items {
					if er.Metadata.Name != nil {
						names = append(names, *er.Metadata.Name)
					}
				}
			}
		case EnrollmentRequestKind:
			resp, err := c.ListEnrollmentRequestsWithResponse(context.Background(), &api.ListEnrollmentRequestsParams{})
			if err == nil && resp.JSON200 != nil {
				for _, er := range resp.JSON200.Items {
					if er.Metadata.Name != nil {
						names = append(names, *er.Metadata.Name)
					}
				}
			}
		case CertificateSigningRequestKind:
			resp, err := c.ListCertificateSigningRequestsWithResponse(context.Background(), &api.ListCertificateSigningRequestsParams{})
			if err == nil && resp.JSON200 != nil {
				for _, er := range resp.JSON200.Items {
					if er.Metadata.Name != nil {
						names = append(names, *er.Metadata.Name)
					}
				}
			}
		case EventKind:
			resp, err := c.ListEventsWithResponse(context.Background(), &api.ListEventsParams{})
			if err == nil && resp.JSON200 != nil {
				for _, er := range resp.JSON200.Items {
					if er.Metadata.Name != nil {
						names = append(names, *er.Metadata.Name)
					}
				}
			}
		case FleetKind:
			resp, err := c.ListFleetsWithResponse(context.Background(), &api.ListFleetsParams{})
			if err == nil && resp.JSON200 != nil {
				for _, er := range resp.JSON200.Items {
					if er.Metadata.Name != nil {
						names = append(names, *er.Metadata.Name)
					}
				}
			}
		case OrganizationKind:
			resp, err := c.ListOrganizationsWithResponse(context.Background(), &api.ListOrganizationsParams{})
			if err == nil && resp.JSON200 != nil {
				for _, er := range resp.JSON200.Items {
					if er.Metadata.Name != nil {
						names = append(names, *er.Metadata.Name)
					}
				}
			}
		case RepositoryKind:
			resp, err := c.ListRepositoriesWithResponse(context.Background(), &api.ListRepositoriesParams{})
			if err == nil && resp.JSON200 != nil {
				for _, er := range resp.JSON200.Items {
					if er.Metadata.Name != nil {
						names = append(names, *er.Metadata.Name)
					}
				}
			}
		case ResourceSyncKind:
			resp, err := c.ListResourceSyncsWithResponse(context.Background(), &api.ListResourceSyncsParams{})
			if err == nil && resp.JSON200 != nil {
				for _, er := range resp.JSON200.Items {
					if er.Metadata.Name != nil {
						names = append(names, *er.Metadata.Name)
					}
				}
			}
		case TemplateVersionKind:
			if kna.FleetName != nil {
				resp, err := c.ListTemplateVersionsWithResponse(context.Background(), *kna.FleetName, &api.ListTemplateVersionsParams{})
				if err == nil && resp.JSON200 != nil {
					for _, er := range resp.JSON200.Items {
						if er.Metadata.Name != nil {
							names = append(names, *er.Metadata.Name)
						}
					}
				}
			}
		}

	}
	return names
}

func (kna *KindNameAutocomplete) isKindAllowed(kind ResourceKind) bool {
	return slices.Contains(kna.AllowedKinds, kind)
}
