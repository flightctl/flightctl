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
	"github.com/flightctl/flightctl/internal/cli/display"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	legalOutputTypes = []string{string(display.JSONFormat), string(display.YAMLFormat), string(display.NameFormat), string(display.WideFormat)}
)

type GetOptions struct {
	GlobalOptions

	LabelSelector string
	FieldSelector string
	Output        string
	Limit         int32
	Continue      string
	FleetName     string
	Rendered      bool
	Summary       bool
	SummaryOnly   bool
}

func DefaultGetOptions() *GetOptions {
	return &GetOptions{
		GlobalOptions: DefaultGlobalOptions(),
		LabelSelector: "",
		FieldSelector: "",
		Limit:         0,
		Continue:      "",
		FleetName:     "",
		Rendered:      false,
	}
}

func NewCmdGet() *cobra.Command {
	o := DefaultGetOptions()
	cmd := &cobra.Command{
		Use:       "get (TYPE | TYPE/NAME)",
		Short:     "Display one or many resources.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: getValidResourceKinds(),
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

func (o *GetOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVarP(&o.LabelSelector, "selector", "l", o.LabelSelector, "Selector (label query) to filter on, supporting operators like '=', '!=', and 'in' (e.g., -l='key1=value1,key2!=value2,key3 in (value3, value4)').")
	fs.StringVar(&o.FieldSelector, "field-selector", o.FieldSelector, "Selector (field query) to filter on, supporting operators like '=', '==', and '!=' (e.g., --field-selector='key1=value1,key2!=value2').")
	fs.StringVarP(&o.Output, "output", "o", o.Output, fmt.Sprintf("Output format. One of: (%s).", strings.Join(legalOutputTypes, ", ")))
	fs.Int32Var(&o.Limit, "limit", o.Limit, "The maximum number of results returned in the list response.")
	fs.StringVar(&o.Continue, "continue", o.Continue, "Query more results starting from the value of the 'continue' field in the previous response.")
	fs.StringVar(&o.FleetName, "fleetname", o.FleetName, "Fleet name for accessing templateversions (use only when getting templateversions).")
	fs.BoolVar(&o.Rendered, "rendered", false, "Return the rendered device configuration that is presented to the device (use only when getting a single device).")
	fs.BoolVarP(&o.Summary, "summary", "s", false, "Display summary information.")
	fs.BoolVar(&o.SummaryOnly, "summary-only", false, "Display summary information only.")
}

func (o *GetOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}
	return nil
}

func (o *GetOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}
	if len(name) > 0 && len(o.LabelSelector) > 0 {
		return fmt.Errorf("cannot specify label selector when getting a single resource")
	}
	if len(name) > 0 && len(o.FieldSelector) > 0 {
		return fmt.Errorf("cannot specify field selector when getting a single resource")
	}
	if o.Summary {
		if kind != DeviceKind && kind != FleetKind {
			return fmt.Errorf("'--summary' can only be specified when getting a list of devices or fleets")
		}
		if kind == DeviceKind && len(name) > 0 {
			return fmt.Errorf("cannot specify '--summary' when getting a single device")
		}
	}
	if o.SummaryOnly {
		if kind != DeviceKind {
			return fmt.Errorf("'--summary-only' can only be specified when getting a list of devices")
		}
		if len(name) > 0 {
			return fmt.Errorf("cannot specify '--summary-only' when getting a single device")
		}
		if o.Limit > 0 || len(o.Continue) > 0 {
			return fmt.Errorf("flags '--limit' and '--continue' are not supported when '--summary-only' is specified")
		}
	}
	if kind == TemplateVersionKind && len(o.FleetName) == 0 {
		return fmt.Errorf("a fleet name must be specified when getting a list of templateversions")
	}
	if len(o.Output) > 0 && !slices.Contains(legalOutputTypes, o.Output) {
		return fmt.Errorf("output format must be one of (%s)", strings.Join(legalOutputTypes, ", "))
	}
	if o.Rendered && (kind != DeviceKind || len(name) == 0) {
		return fmt.Errorf("'--rendered' can only be used when getting a single device")
	}
	if kind == EventKind && len(name) > 0 {
		return fmt.Errorf("you cannot get a single event")
	}
	if o.Limit < 0 {
		return fmt.Errorf("limit must be greater than 0")
	}
	return nil
}

func (o *GetOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	var response interface{}
	if len(name) == 0 {
		// List resources (no name given)
		response, err = o.getResourceList(ctx, c, kind)
	} else {
		// Get a single resource by name
		response, err = o.getSingleResource(ctx, c, kind, name)
	}

	return o.processResponse(response, err, kind, name)
}

func (o *GetOptions) getSingleResource(ctx context.Context, c *apiclient.ClientWithResponses, kind, name string) (interface{}, error) {
	switch kind {
	case DeviceKind:
		if o.Rendered {
			return c.GetRenderedDeviceWithResponse(ctx, name, &api.GetRenderedDeviceParams{})
		}
		return c.GetDeviceWithResponse(ctx, name)
	case EnrollmentRequestKind:
		return c.GetEnrollmentRequestWithResponse(ctx, name)
	case FleetKind:
		params := api.GetFleetParams{
			AddDevicesSummary: util.ToPtrWithNilDefault(o.Summary),
		}
		return c.GetFleetWithResponse(ctx, name, &params)
	case TemplateVersionKind:
		return c.GetTemplateVersionWithResponse(ctx, o.FleetName, name)
	case RepositoryKind:
		return c.GetRepositoryWithResponse(ctx, name)
	case ResourceSyncKind:
		return c.GetResourceSyncWithResponse(ctx, name)
	case CertificateSigningRequestKind:
		return c.GetCertificateSigningRequestWithResponse(ctx, name)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
}

func (o *GetOptions) getResourceList(ctx context.Context, c *apiclient.ClientWithResponses, kind string) (interface{}, error) {
	switch kind {
	case DeviceKind:
		params := api.ListDevicesParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
			SummaryOnly:   lo.ToPtr(o.SummaryOnly),
		}
		return c.ListDevicesWithResponse(ctx, &params)
	case EnrollmentRequestKind:
		params := api.ListEnrollmentRequestsParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		return c.ListEnrollmentRequestsWithResponse(ctx, &params)
	case FleetKind:
		params := api.ListFleetsParams{
			LabelSelector:     util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector:     util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:             util.ToPtrWithNilDefault(o.Limit),
			Continue:          util.ToPtrWithNilDefault(o.Continue),
			AddDevicesSummary: util.ToPtrWithNilDefault(o.Summary),
		}
		return c.ListFleetsWithResponse(ctx, &params)
	case TemplateVersionKind:
		params := api.ListTemplateVersionsParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		return c.ListTemplateVersionsWithResponse(ctx, o.FleetName, &params)
	case RepositoryKind:
		params := api.ListRepositoriesParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		return c.ListRepositoriesWithResponse(ctx, &params)
	case ResourceSyncKind:
		params := api.ListResourceSyncsParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		return c.ListResourceSyncsWithResponse(ctx, &params)
	case CertificateSigningRequestKind:
		params := api.ListCertificateSigningRequestsParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		return c.ListCertificateSigningRequestsWithResponse(ctx, &params)
	case EventKind:
		params := api.ListEventsParams{
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		return c.ListEventsWithResponse(ctx, &params)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
}

func (o *GetOptions) processResponse(response interface{}, err error, kind string, name string) error {
	errorPrefix := fmt.Sprintf("reading %s/%s", kind, name)
	if len(name) == 0 {
		errorPrefix = fmt.Sprintf("listing %s", plural(kind))
	}

	if err != nil {
		return fmt.Errorf(errorPrefix+": %w", err)
	}

	httpResponse, err := responseField[*http.Response](response, "HTTPResponse")
	if err != nil {
		return err
	}

	responseBody, err := responseField[[]byte](response, "Body")
	if err != nil {
		return err
	}

	if httpResponse.StatusCode != http.StatusOK {
		if strings.Contains(httpResponse.Header.Get("Content-Type"), "json") {
			var dest api.Status
			if err := json.Unmarshal(responseBody, &dest); err != nil {
				return fmt.Errorf("unmarshalling error: %w", err)
			}
			return fmt.Errorf(errorPrefix+": %d, message: %s", httpResponse.StatusCode, dest.Message)
		}
		return fmt.Errorf(errorPrefix+": %d", httpResponse.StatusCode)
	}

	formatter := display.NewFormatter(display.OutputFormat(o.Output))
	options := display.FormatOptions{
		Kind:        kind,
		Name:        name,
		Summary:     o.Summary,
		SummaryOnly: o.SummaryOnly,
		Wide:        o.Output == string(display.WideFormat),
		Writer:      os.Stdout,
	}

	// For structured formats, use JSON200 data
	if o.Output == string(display.JSONFormat) || o.Output == string(display.YAMLFormat) || o.Output == string(display.NameFormat) {
		json200, err := responseField[interface{}](response, "JSON200")
		if err != nil {
			return err
		}
		return formatter.Format(json200, options)
	}

	// For table format, pass the full response
	return formatter.Format(response, options)
}
