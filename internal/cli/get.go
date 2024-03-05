package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"text/tabwriter"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/spf13/cobra"
	"github.com/thoas/go-funk"
	"sigs.k8s.io/yaml"
)

const (
	yamlFormat = "yaml"
)

var (
	legalOutputTypes = []string{yamlFormat}
)

type GetOptions struct {
	Owner         string
	LabelSelector string
	Output        string
	Limit         int32
	Continue      string
	FleetName     string
}

func NewCmdGet() *cobra.Command {
	o := &GetOptions{LabelSelector: "", Limit: 0, Continue: ""}

	cmd := &cobra.Command{
		Use:   "get",
		Short: "get resources",
		Args:  cobra.ExactArgs(1),
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

	cmd.Flags().StringVar(&o.Owner, "owner", o.Owner, "filter by owner")
	cmd.Flags().StringVarP(&o.LabelSelector, "labelselector", "l", o.LabelSelector, "label selector as a comma-separated list of key=value")
	cmd.Flags().StringVarP(&o.Output, "output", "o", o.Output, "output format (yaml)")
	cmd.Flags().Int32Var(&o.Limit, "limit", o.Limit, "the maximum number of results returned in the list response")
	cmd.Flags().StringVar(&o.Continue, "continue", o.Continue, "query more results starting from the value of the 'continue' field in the previous response")
	cmd.Flags().StringVarP(&o.FleetName, "fleetname", "f", o.FleetName, "fleet name for accessing individual templateversions")
	return cmd
}

func (o *GetOptions) Complete(cmd *cobra.Command, args []string) error {
	return nil
}

func (o *GetOptions) Validate(args []string) error {
	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}
	if len(name) > 0 && len(o.LabelSelector) > 0 {
		return fmt.Errorf("cannot specify label selector when fetching a single resource")
	}
	if len(o.Owner) > 0 {
		if kind != DeviceKind && kind != FleetKind && kind != TemplateVersionKind {
			return fmt.Errorf("owner can only be specified when fetching devices, fleets, and templateversions")
		}
		if (kind == DeviceKind || kind == FleetKind) && len(name) > 0 {
			return fmt.Errorf("cannot specify owner together with a device or fleet name")
		}
	}
	if kind == TemplateVersionKind && len(name) > 0 {
		if len(o.FleetName) == 0 {
			return fmt.Errorf("fleetname must be specified when fetching a specific templatevesion")
		}
	} else {
		if len(o.FleetName) > 0 {
			return fmt.Errorf("fleetname must only be specified when fetching a specific templatevesion")
		}
	}
	if len(o.Output) > 0 && !funk.Contains(legalOutputTypes, o.Output) {
		return fmt.Errorf("output format must be one of %s", strings.Join(legalOutputTypes, ", "))
	}
	if o.Limit < 0 {
		return fmt.Errorf("limit must be greater than 0")
	}
	return nil
}

func (o *GetOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(defaultClientConfigFile)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	var response interface{}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}
	switch {
	case kind == DeviceKind && len(name) > 0:
		response, err = c.ReadDeviceWithResponse(ctx, name)
	case kind == DeviceKind && len(name) == 0:
		params := api.ListDevicesParams{
			Owner:         util.StrToPtrWithNilDefault(o.Owner),
			LabelSelector: util.StrToPtrWithNilDefault(o.LabelSelector),
			Limit:         util.Int32ToPtrWithNilDefault(o.Limit),
			Continue:      util.StrToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListDevicesWithResponse(ctx, &params)
	case kind == EnrollmentRequestKind && len(name) > 0:
		response, err = c.ReadEnrollmentRequestWithResponse(ctx, name)
	case kind == EnrollmentRequestKind && len(name) == 0:
		params := api.ListEnrollmentRequestsParams{
			LabelSelector: util.StrToPtrWithNilDefault(o.LabelSelector),
			Limit:         util.Int32ToPtrWithNilDefault(o.Limit),
			Continue:      util.StrToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListEnrollmentRequestsWithResponse(ctx, &params)
	case kind == FleetKind && len(name) > 0:
		response, err = c.ReadFleetWithResponse(ctx, name)
	case kind == FleetKind && len(name) == 0:
		params := api.ListFleetsParams{
			Owner:         util.StrToPtrWithNilDefault(o.Owner),
			LabelSelector: util.StrToPtrWithNilDefault(o.LabelSelector),
			Limit:         util.Int32ToPtrWithNilDefault(o.Limit),
			Continue:      util.StrToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListFleetsWithResponse(ctx, &params)
	case kind == TemplateVersionKind && len(name) > 0:
		response, err = c.ReadTemplateVersionWithResponse(ctx, o.FleetName, name)
	case kind == TemplateVersionKind && len(name) == 0:
		params := api.ListTemplateVersionsParams{
			Owner:         util.StrToPtrWithNilDefault(o.Owner),
			LabelSelector: util.StrToPtrWithNilDefault(o.LabelSelector),
			Limit:         util.Int32ToPtrWithNilDefault(o.Limit),
			Continue:      util.StrToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListTemplateVersionsWithResponse(ctx, &params)
	case kind == RepositoryKind && len(name) > 0:
		response, err = c.ReadRepositoryWithResponse(ctx, name)
	case kind == RepositoryKind && len(name) == 0:
		params := api.ListRepositoriesParams{
			LabelSelector: util.StrToPtrWithNilDefault(o.LabelSelector),
			Limit:         util.Int32ToPtrWithNilDefault(o.Limit),
			Continue:      util.StrToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListRepositoriesWithResponse(ctx, &params)
	case kind == ResourceSyncKind && len(name) > 0:
		response, err = c.ReadResourceSyncWithResponse(ctx, name)
	case kind == ResourceSyncKind && len(name) == 0:
		params := api.ListResourceSyncParams{
			LabelSelector: util.StrToPtrWithNilDefault(o.LabelSelector),
			Limit:         util.Int32ToPtrWithNilDefault(o.Limit),
			Continue:      util.StrToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListResourceSyncWithResponse(ctx, &params)
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}
	return processReponse(response, err, kind, name, o.Output)
}

func processReponse(response interface{}, err error, kind string, name string, output string) error {
	if len(name) > 0 {
		if err != nil {
			return fmt.Errorf("reading %s/%s: %w", kind, name, err)
		}
		out, err := serializeResponse(response, fmt.Sprintf("%s/%s", kind, name))
		if err != nil {
			return fmt.Errorf("serializing response for %s/%s: %w", kind, name, err)
		}
		fmt.Printf("%s\n", string(out))
		return nil
	}

	if err != nil {
		return fmt.Errorf("listing %s: %w", plural(kind), err)
	}
	return printListResourceResponse(response, err, plural(kind), output)
}

func serializeResponse(response interface{}, name string) ([]byte, error) {
	v := reflect.ValueOf(response).Elem()
	if v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int() != http.StatusOK {
		return nil, fmt.Errorf("reading %s: %d", name, v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int())
	}

	return yaml.Marshal(v.FieldByName("JSON200").Interface())
}

func printListResourceResponse(response interface{}, err error, resourceType string, output string) error {
	if err != nil {
		return fmt.Errorf("listing %s: %w", resourceType, err)
	}
	v := reflect.ValueOf(response).Elem()
	if v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int() != http.StatusOK {
		return fmt.Errorf("listing %s: %d", resourceType, v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int())
	}

	if output == yamlFormat {
		marshalled, err := yaml.Marshal(v.FieldByName("JSON200").Interface())
		if err != nil {
			return fmt.Errorf("marshalling resource: %w", err)
		}

		fmt.Printf("%s\n", string(marshalled))
		return nil
	}

	// Tabular
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', 0)
	switch resourceType {
	case plural(DeviceKind):
		printDevicesTable(w, response.(*apiclient.ListDevicesResponse))
	case plural(EnrollmentRequestKind):
		printEnrollmentRequestsTable(w, response.(*apiclient.ListEnrollmentRequestsResponse))
	case plural(FleetKind):
		printFleetsTable(w, response.(*apiclient.ListFleetsResponse))
	case plural(TemplateVersionKind):
		printTemplateVersionsTable(w, response.(*apiclient.ListTemplateVersionsResponse))
	case plural(RepositoryKind):
		printRepositoriesTable(w, response.(*apiclient.ListRepositoriesResponse))
	case plural(ResourceSyncKind):
		printResourceSyncsTable(w, response.(*apiclient.ListResourceSyncResponse))
	default:
		return fmt.Errorf("unknown resource type %s", resourceType)
	}
	w.Flush()
	return nil
}

func printDevicesTable(w *tabwriter.Writer, response *apiclient.ListDevicesResponse) {
	fmt.Fprintln(w, "NAME\tOWNER")
	for _, d := range response.JSON200.Items {
		fmt.Fprintf(w, "%s\t%s\n", *d.Metadata.Name, util.DefaultIfNil(d.Metadata.Owner, "<None>"))
	}
}

func printEnrollmentRequestsTable(w *tabwriter.Writer, response *apiclient.ListEnrollmentRequestsResponse) {
	fmt.Fprintln(w, "NAME\tAPPROVED\tREGION")
	for _, e := range response.JSON200.Items {
		approved := ""
		region := ""
		if e.Status.Approval != nil {
			approved = fmt.Sprintf("%t", e.Status.Approval.Approved)
			region = *e.Status.Approval.Region
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", *e.Metadata.Name, approved, region)
	}
}

func printFleetsTable(w *tabwriter.Writer, response *apiclient.ListFleetsResponse) {
	fmt.Fprintln(w, "NAME\tOWNER")
	for _, f := range response.JSON200.Items {
		fmt.Fprintf(w, "%s\t%s\n", *f.Metadata.Name, util.DefaultIfNil(f.Metadata.Owner, "<None>"))
	}
}

func printTemplateVersionsTable(w *tabwriter.Writer, response *apiclient.ListTemplateVersionsResponse) {
	fmt.Fprintln(w, "FLEET\tNAME")
	for _, f := range response.JSON200.Items {
		fmt.Fprintf(w, "%s\t%s\n", f.Spec.Fleet, *f.Metadata.Name)
	}
}

func printRepositoriesTable(w *tabwriter.Writer, response *apiclient.ListRepositoriesResponse) {
	fmt.Fprintln(w, "NAME\tACCESSIBLE\tREASON\tMESSAGE")

	for _, f := range response.JSON200.Items {
		accessible := "-"
		reason := ""
		message := ""
		if f.Status != nil && f.Status.Conditions != nil && len(*f.Status.Conditions) > 0 {
			accessible = string((*f.Status.Conditions)[0].Status)
			if (*f.Status.Conditions)[0].Reason != nil {
				reason = *(*f.Status.Conditions)[0].Reason
			}
			if (*f.Status.Conditions)[0].Message != nil {
				message = *(*f.Status.Conditions)[0].Message
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", *f.Metadata.Name, accessible, reason, message)
	}
}

func printResourceSyncsTable(w *tabwriter.Writer, response *apiclient.ListResourceSyncResponse) {
	fmt.Fprintln(w, "NAME\tREPOSITORY\tPATH")

	for _, f := range response.JSON200.Items {
		reponame := *f.Spec.Repository
		path := *f.Spec.Path
		fmt.Fprintf(w, "%s\t%s\t%s\n", *f.Metadata.Name, reponame, path)
	}
}
