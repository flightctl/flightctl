package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

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
	cliVersionTitle     = "flightctl CLI version"
	serviceVersionTitle = "flightctl service version"

	errReadingVersion = "reading service version"
	errUnmarshalling  = "unmarshalling error"
)

type serviceVersion struct {
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

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

func (o *VersionOptions) processResponse(response interface{}, err error) serviceVersion {
	var serviceVer serviceVersion

	if err != nil {
		return serviceVersion{Error: fmt.Errorf("%s: %w", errReadingVersion, err).Error()}
	}

	httpResponse, err := responseField[*http.Response](response, "HTTPResponse")
	if err != nil {
		return serviceVersion{Error: err.Error()}
	}

	responseBody, err := responseField[[]byte](response, "Body")
	if err != nil {
		return serviceVersion{Error: err.Error()}
	}

	if httpResponse.StatusCode != http.StatusOK {
		return serviceVersion{Error: fmt.Errorf("%s: %d", errReadingVersion, httpResponse.StatusCode).Error()}
	}
	if err := json.Unmarshal(responseBody, &serviceVer); err != nil {
		return serviceVersion{Error: fmt.Errorf("%s: %w", errUnmarshalling, err).Error()}
	}

	return serviceVer
}

func (o *VersionOptions) Run(ctx context.Context, args []string) error {
	versions := make(map[string]interface{}, 2)
	cliVersion := version.Get()
	var serviceVersion serviceVersion
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		serviceVersion = o.processResponse(nil, err)
	} else {
		// Call a function with a context timeout
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		response, err := c.GetVersionWithResponse(ctx)
		serviceVersion = o.processResponse(response, err)
	}
	versions[cliVersionTitle] = &cliVersion
	versions[serviceVersionTitle] = &serviceVersion

	switch o.Output {
	case "":
		fmt.Printf("%s: %s\n", cliVersionTitle, cliVersion.String())
		fmt.Printf("%s: ", serviceVersionTitle)
		if serviceVersion.Error != "" {
			fmt.Print(serviceVersion.Error)
		} else {
			fmt.Print(serviceVersion.Version)
		}
		fmt.Println()

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

	return nil
}
