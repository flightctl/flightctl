package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/pkg/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"
)

var (
	legalVersionOutputTypes = []string{jsonFormat, yamlFormat}
)

type VersionOptions struct {
	Output string
}

func DefaultVersionOptions() *VersionOptions {
	return &VersionOptions{
		Output: "",
	}
}

func NewCmdVersion() *cobra.Command {
	o := DefaultVersionOptions()
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print flightctl version information.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *VersionOptions) Bind(fs *pflag.FlagSet) {
	fs.StringVarP(&o.Output, "output", "o", o.Output, fmt.Sprintf("Output format. One of: (%s).", strings.Join(legalVersionOutputTypes, ", ")))
}

func (o *VersionOptions) Complete(cmd *cobra.Command, args []string) error {
	return nil
}

func (o *VersionOptions) Validate(args []string) error {
	if len(o.Output) > 0 && !slices.Contains(legalVersionOutputTypes, o.Output) {
		return fmt.Errorf("output format must be one of (%s)", strings.Join(legalVersionOutputTypes, ", "))
	}
	return nil
}

func (o *VersionOptions) Run(ctx context.Context, args []string) error {
	versionInfo := version.Get()

	switch o.Output {
	case "":
		fmt.Printf("flightctl version: %s\n", versionInfo.String())
	case "yaml":
		marshalled, err := yaml.Marshal(&versionInfo)
		if err != nil {
			return err
		}
		fmt.Print(string(marshalled))
	case "json":
		marshalled, err := json.MarshalIndent(&versionInfo, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(marshalled))
	default:
		// There is a bug in the program if we hit this case.
		// However, we follow a policy of never panicking.
		return fmt.Errorf("VersionOptions were not validated: --output=%q should have been rejected", o.Output)
	}

	return nil
}
