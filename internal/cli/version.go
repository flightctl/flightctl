package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"
)

var (
	legalVersionOutputTypes = []string{jsonFormat, yamlFormat}
)

type VersionOptions struct {
	GlobalOptions

	Output string
}

const (
	cliVersionTitle     = "Client Version"
	serviceVersionTitle = "Server Version"

	errReadingVersion = "Could not read server version"
	errUnmarshalling  = "Could not unmarshal server response"
)

func DefaultVersionOptions() *VersionOptions {
	return &VersionOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Output:        "",
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
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			return o.Run(ctx, args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *VersionOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVarP(&o.Output, "output", "o", o.Output, fmt.Sprintf("Output format. One of: (%s).", strings.Join(legalVersionOutputTypes, ", ")))
}

func (o *VersionOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}
	return nil
}

func (o *VersionOptions) Validate(args []string) error {
	if len(o.Output) > 0 && !slices.Contains(legalVersionOutputTypes, o.Output) {
		return fmt.Errorf("output format must be one of (%s)", strings.Join(legalVersionOutputTypes, ", "))
	}
	return nil
}

func (o *VersionOptions) processResponse(response interface{}, err error) (*api.Version, error) {
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errReadingVersion, err)
	}

	httpResponse, err := responseField[*http.Response](response, "HTTPResponse")
	if err != nil {
		return nil, err
	}

	responseBody, err := responseField[[]byte](response, "Body")
	if err != nil {
		return nil, err
	}

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s (%s)", errReadingVersion, httpResponse.Status)
	}

	var serverVersion api.Version
	if err := json.Unmarshal(responseBody, &serverVersion); err != nil {
		return nil, fmt.Errorf("%s: %w", errUnmarshalling, err)
	}
	return &serverVersion, nil
}

func (o *VersionOptions) Run(ctx context.Context, args []string) error {
	clientVersion := version.Get()

	var serverVersion *api.Version
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err == nil {
		var response *apiclient.GetVersionResponse
		response, err = c.GetVersionWithResponse(ctx)
		serverVersion, err = o.processResponse(response, err)
	}

	versions := struct {
		ClientVersion *version.Info `json:"clientVersion,omitempty"`
		ServerVersion *api.Version  `json:"serverVersion,omitempty"`
	}{
		ClientVersion: &clientVersion,
		ServerVersion: serverVersion,
	}

	switch o.Output {
	case "":
		fmt.Printf("%s: %s\n", cliVersionTitle, versions.ClientVersion.String())
		if versions.ServerVersion != nil {
			fmt.Printf("%s: %s\n", serviceVersionTitle, versions.ServerVersion.Version)
		}
	case "yaml":
		marshalled, err := yaml.Marshal(&versions)
		if err != nil {
			return err
		}
		fmt.Print(string(marshalled))
	case "json":
		marshalled, err := json.MarshalIndent(&versions, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(marshalled))
	default:
		// There is a bug in the program if we hit this case.
		// However, we follow a policy of never panicking.
		return fmt.Errorf("VersionOptions were not validated: --output=%q should have been rejected", o.Output)
	}

	// Don't treat it as error if the server cannot be reached, just print the message.
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err.Error())
	}
	return nil
}
