package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/thoas/go-funk"
	"sigs.k8s.io/yaml"
)

const (
	jsonFormat = "json"
	yamlFormat = "yaml"
)

var (
	legalOutputTypes = []string{jsonFormat, yamlFormat}
)

type GetOptions struct {
	GlobalOptions

	Owner         string
	LabelSelector string
	StatusFilter  []string
	Output        string
	Limit         int32
	Continue      string
	FleetName     string
	Rendered      bool
}

func DefaultGetOptions() *GetOptions {
	return &GetOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Owner:         "",
		LabelSelector: "",
		StatusFilter:  []string{},
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
			return o.Run(cmd.Context(), args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *GetOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVar(&o.Owner, "owner", o.Owner, "filter by owner")
	fs.StringVarP(&o.LabelSelector, "selector", "l", o.LabelSelector, "Selector (label query) to filter on, as a comma-separated list of key=value.")
	fs.StringSliceVar(&o.StatusFilter, "status-filter", o.StatusFilter, "Filter the results by status field path using key-value pairs. Example: --status-filter=updated.status=UpToDate")
	fs.StringVarP(&o.Output, "output", "o", o.Output, fmt.Sprintf("Output format. One of: (%s).", strings.Join(legalOutputTypes, ", ")))
	fs.Int32Var(&o.Limit, "limit", o.Limit, "The maximum number of results returned in the list response.")
	fs.StringVar(&o.Continue, "continue", o.Continue, "Query more results starting from the value of the 'continue' field in the previous response.")
	fs.StringVar(&o.FleetName, "fleetname", o.FleetName, "Fleet name for accessing templateversions (use only when getting templateversions).")
	fs.BoolVar(&o.Rendered, "rendered", false, "Return the rendered device configuration that is presented to the device (use only when getting devices).")
}

func (o *GetOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	// The RenderedDeviceSpec can only be printed as JSON or YAML, so default to JSON if not set
	if o.Rendered && len(o.Output) == 0 {
		o.Output = jsonFormat
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
		return fmt.Errorf("cannot specify label selector when fetching a single resource")
	}
	if len(o.Owner) > 0 {
		if kind != DeviceKind && kind != FleetKind {
			return fmt.Errorf("owner can only be specified when fetching devices and fleets")
		}
		if (kind == DeviceKind || kind == FleetKind) && len(name) > 0 {
			return fmt.Errorf("cannot specify owner together with a device or fleet name")
		}
	}
	if kind == TemplateVersionKind && len(o.FleetName) == 0 {
		return fmt.Errorf("fleetname must be specified when fetching templateversions")
	}
	if o.Rendered && (kind != DeviceKind || len(name) == 0) {
		return fmt.Errorf("rendered must only be specified when fetching a specific device")
	}
	if len(o.Output) > 0 && !funk.Contains(legalOutputTypes, o.Output) {
		return fmt.Errorf("output format must be one of %s", strings.Join(legalOutputTypes, ", "))
	}
	if o.Limit < 0 {
		return fmt.Errorf("limit must be greater than 0")
	}
	return nil
}

func (o *GetOptions) Run(ctx context.Context, args []string) error { // nolint: gocyclo
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	var response interface{}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}
	switch {
	case kind == DeviceKind && len(name) > 0 && !o.Rendered:
		response, err = c.ReadDeviceWithResponse(ctx, name)
	case kind == DeviceKind && len(name) > 0 && o.Rendered:
		response, err = c.GetRenderedDeviceSpecWithResponse(ctx, name, &api.GetRenderedDeviceSpecParams{})
	case kind == DeviceKind && len(name) == 0:
		params := api.ListDevicesParams{
			Owner:         util.StrToPtrWithNilDefault(o.Owner),
			LabelSelector: util.StrToPtrWithNilDefault(o.LabelSelector),
			StatusFilter:  util.SliceToPtrWithNilDefault(o.StatusFilter),
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
		response, err = c.ReadFleetWithResponse(ctx, name, nil)
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
			LabelSelector: util.StrToPtrWithNilDefault(o.LabelSelector),
			Limit:         util.Int32ToPtrWithNilDefault(o.Limit),
			Continue:      util.StrToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListTemplateVersionsWithResponse(ctx, o.FleetName, &params)
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
	case kind == CertificateSigningRequestKind && len(name) > 0:
		response, err = c.ReadCertificateSigningRequestWithResponse(ctx, name)
	case kind == CertificateSigningRequestKind && len(name) == 0:
		params := api.ListCertificateSigningRequestsParams{
			LabelSelector: util.StrToPtrWithNilDefault(o.LabelSelector),
			Limit:         util.Int32ToPtrWithNilDefault(o.Limit),
			Continue:      util.StrToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListCertificateSigningRequestsWithResponse(ctx, &params)
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}
	return processReponse(response, err, kind, name, o.Output)
}

func processReponse(response interface{}, err error, kind string, name string, output string) error {
	errorPrefix := fmt.Sprintf("reading %s/%s", kind, name)
	if len(name) == 0 {
		errorPrefix = fmt.Sprintf("listing %s", plural(kind))
	}

	if err != nil {
		return fmt.Errorf(errorPrefix+": %w", err)
	}

	v := reflect.ValueOf(response).Elem()
	if v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int() != http.StatusOK {
		return fmt.Errorf(errorPrefix+": %d", v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int())
	}

	switch output {
	case jsonFormat:
		marshalled, err := json.Marshal(v.FieldByName("JSON200").Interface())
		if err != nil {
			return fmt.Errorf("marshalling resource: %w", err)
		}
		fmt.Printf("%s\n", string(marshalled))
		return nil
	case yamlFormat:
		marshalled, err := yaml.Marshal(v.FieldByName("JSON200").Interface())
		if err != nil {
			return fmt.Errorf("marshalling resource: %w", err)
		}
		fmt.Printf("%s\n", string(marshalled))
		return nil
	default:
		return printTable(response, kind, name)
	}
}

func printTable(response interface{}, kind string, name string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', 0)
	switch {
	case kind == DeviceKind && len(name) == 0:
		printDevicesTable(w, response.(*apiclient.ListDevicesResponse).JSON200.Items...)
	case kind == DeviceKind && len(name) > 0:
		printDevicesTable(w, *(response.(*apiclient.ReadDeviceResponse).JSON200))
	case kind == EnrollmentRequestKind && len(name) == 0:
		printEnrollmentRequestsTable(w, response.(*apiclient.ListEnrollmentRequestsResponse).JSON200.Items...)
	case kind == EnrollmentRequestKind && len(name) > 0:
		printEnrollmentRequestsTable(w, *(response.(*apiclient.ReadEnrollmentRequestResponse).JSON200))
	case kind == FleetKind && len(name) == 0:
		printFleetsTable(w, response.(*apiclient.ListFleetsResponse).JSON200.Items...)
	case kind == FleetKind && len(name) > 0:
		printFleetsTable(w, *(response.(*apiclient.ReadFleetResponse).JSON200))
	case kind == TemplateVersionKind && len(name) == 0:
		printTemplateVersionsTable(w, response.(*apiclient.ListTemplateVersionsResponse).JSON200.Items...)
	case kind == TemplateVersionKind && len(name) > 0:
		printTemplateVersionsTable(w, *(response.(*apiclient.ReadTemplateVersionResponse).JSON200))
	case kind == RepositoryKind && len(name) == 0:
		printRepositoriesTable(w, response.(*apiclient.ListRepositoriesResponse).JSON200.Items...)
	case kind == RepositoryKind && len(name) > 0:
		printRepositoriesTable(w, *(response.(*apiclient.ReadRepositoryResponse).JSON200))
	case kind == ResourceSyncKind && len(name) == 0:
		printResourceSyncsTable(w, response.(*apiclient.ListResourceSyncResponse).JSON200.Items...)
	case kind == ResourceSyncKind && len(name) > 0:
		printResourceSyncsTable(w, *(response.(*apiclient.ReadResourceSyncResponse).JSON200))
	case kind == CertificateSigningRequestKind && len(name) == 0:
		printCSRTable(w, response.(*apiclient.ListCertificateSigningRequestsResponse).JSON200.Items...)
	case kind == CertificateSigningRequestKind && len(name) > 0:
		printCSRTable(w, *(response.(*apiclient.ReadCertificateSigningRequestResponse).JSON200))
	default:
		return fmt.Errorf("unknown resource type %s", kind)
	}
	w.Flush()
	return nil
}

func printDevicesTable(w *tabwriter.Writer, devices ...api.Device) {
	fmt.Fprintln(w, "NAME\tALIAS\tOWNER\tSYSTEM\tUPDATED\tAPPLICATIONS\tLAST SEEN")
	for _, d := range devices {
		lastSeen := "<never>"
		if !d.Status.LastSeen.IsZero() {
			lastSeen = humanize.Time(d.Status.LastSeen)
		}
		alias := ""
		if d.Metadata.Labels != nil {
			alias = (*d.Metadata.Labels)["alias"]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			*d.Metadata.Name,
			alias,
			util.DefaultIfNil(d.Metadata.Owner, "<none>"),
			d.Status.Summary.Status,
			d.Status.Updated.Status,
			d.Status.Applications.Summary.Status,
			lastSeen,
		)
	}
}

func printEnrollmentRequestsTable(w *tabwriter.Writer, ers ...api.EnrollmentRequest) {
	fmt.Fprintln(w, "NAME\tAPPROVAL\tAPPROVER\tAPPROVED LABELS")
	for _, e := range ers {
		approval, approver, approvedLabels := "Pending", "<none>", ""
		if e.Status.Approval != nil {
			approval = util.BoolToStr(e.Status.Approval.Approved, "Approved", "Denied")
			if e.Status.Approval.ApprovedBy != nil {
				approver = *e.Status.Approval.ApprovedBy
			}
			approvedLabels = strings.Join(util.LabelMapToArray(e.Status.Approval.Labels), ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			*e.Metadata.Name,
			approval,
			approver,
			approvedLabels,
		)
	}
}

func printFleetsTable(w *tabwriter.Writer, fleets ...api.Fleet) {
	fmt.Fprintln(w, "NAME\tOWNER\tSELECTOR\tVALID\tDEVICES")
	for i := range fleets {
		f := fleets[i]
		selector := "<none>"
		if f.Spec.Selector != nil {
			selector = strings.Join(util.LabelMapToArray(&f.Spec.Selector.MatchLabels), ",")
		}
		valid := "Unknown"
		if f.Status != nil {

			condition := api.FindStatusCondition(f.Status.Conditions, api.FleetValid)
			if condition != nil {
				valid = string(condition.Status)
			}
			condition = api.FindStatusCondition(f.Status.Conditions, api.FleetOverlappingSelectors)
			if condition != nil && condition.Status == api.ConditionStatusTrue {
				valid = string(api.ConditionStatusFalse)
			}
		}
		numDevices := "Unknown"
		if f.Status.DevicesSummary != nil {
			numDevices = fmt.Sprintf("%d", f.Status.DevicesSummary.Total)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			*f.Metadata.Name,
			util.DefaultIfNil(f.Metadata.Owner, "<none>"),
			selector,
			valid,
			numDevices,
		)
	}
}

func printTemplateVersionsTable(w *tabwriter.Writer, tvs ...api.TemplateVersion) {
	fmt.Fprintln(w, "FLEET\tNAME")
	for _, tv := range tvs {
		fmt.Fprintf(w, "%s\t%s\n", tv.Spec.Fleet, *tv.Metadata.Name)
	}
}

func printRepositoriesTable(w *tabwriter.Writer, repos ...api.Repository) {
	fmt.Fprintln(w, "NAME\tTYPE\tREPOSITORY URL\tACCESSIBLE")
	for _, r := range repos {
		accessible := "Unknown"
		if r.Status != nil {
			condition := api.FindStatusCondition(r.Status.Conditions, api.RepositoryAccessible)
			if condition != nil {
				accessible = string(condition.Status)
			}
		}

		repoSpec, _ := r.Spec.GetGenericRepoSpec()
		repoType := repoSpec.Type

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			*r.Metadata.Name,
			fmt.Sprintf("%v", repoType),
			util.DefaultIfError(r.Spec.GetRepoURL, ""),
			accessible,
		)
	}
}

func printResourceSyncsTable(w *tabwriter.Writer, resourcesyncs ...api.ResourceSync) {
	fmt.Fprintln(w, "NAME\tREPOSITORY\tPATH\tREVISION\tACCESSIBLE\tSYNCED\tLAST SYNC")

	for _, rs := range resourcesyncs {
		accessible, synced, lastSynced := "Unknown", "Unknown", "Unknown"
		if rs.Status != nil {
			condition := api.FindStatusCondition(rs.Status.Conditions, api.ResourceSyncAccessible)
			if condition != nil {
				accessible = string(condition.Status)
			}
			condition = api.FindStatusCondition(rs.Status.Conditions, api.ResourceSyncSynced)
			if condition != nil {
				synced = string(condition.Status)
				lastSynced = humanize.Time(condition.LastTransitionTime)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			*rs.Metadata.Name,
			rs.Spec.Repository,
			rs.Spec.Path,
			rs.Spec.TargetRevision,
			accessible,
			synced,
			lastSynced,
		)
	}
}

func printCSRTable(w *tabwriter.Writer, csrs ...api.CertificateSigningRequest) {
	fmt.Fprintln(w, "NAME\tAGE\tSIGNERNAME\tUSERNAME\tREQUESTEDDURATION\tCONDITION")

	for _, csr := range csrs {
		age := NoneString
		if csr.Metadata.CreationTimestamp != nil {
			age = time.Since(*csr.Metadata.CreationTimestamp).Round(time.Second).String()
		}

		duration := NoneString
		if csr.Spec.ExpirationSeconds != nil {
			duration = time.Duration(int64(*csr.Spec.ExpirationSeconds) * int64(time.Second)).String()
		}

		condition := "Pending"
		if api.IsStatusConditionTrue(csr.Status.Conditions, api.CertificateSigningRequestApproved) {
			condition = "Approved"
		} else if api.IsStatusConditionTrue(csr.Status.Conditions, api.CertificateSigningRequestDenied) {
			condition = "Denied"
		} else if api.IsStatusConditionTrue(csr.Status.Conditions, api.CertificateSigningRequestFailed) {
			condition = "Failed"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			*csr.Metadata.Name,
			age,
			csr.Spec.SignerName,
			util.DefaultIfNil(csr.Spec.Username, NoneString),
			duration,
			condition,
		)
	}
}
