package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"
)

const (
	jsonFormat = "json"
	yamlFormat = "yaml"
	wideFormat = "wide"
)

var (
	legalOutputTypes = []string{jsonFormat, yamlFormat, wideFormat}
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
			return o.Run(cmd.Context(), args)
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
	fs.BoolVar(&o.Rendered, "rendered", false, "Return the rendered device configuration that is presented to the device (use only when getting devices).")
	fs.BoolVarP(&o.Summary, "summary", "s", false, "Display summary information.")
	fs.BoolVar(&o.SummaryOnly, "summary-only", false, "Display summary information only.")
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
	if len(name) > 0 && len(o.FieldSelector) > 0 {
		return fmt.Errorf("cannot specify field selector when fetching a single resource")
	}
	if o.Summary {
		if kind != DeviceKind && kind != FleetKind {
			return fmt.Errorf("summary can only be specified when fetching devices or fleets")
		}
		if kind == DeviceKind && len(name) > 0 {
			return fmt.Errorf("cannot specify summary when fetching a single device")
		}
	}
	if o.SummaryOnly {
		if kind != DeviceKind {
			return fmt.Errorf("summary-only can only be specified when fetching devices")
		}
		if len(name) > 0 {
			return fmt.Errorf("cannot specify summary-only when fetching a single device")
		}
		if o.Limit > 0 || len(o.Continue) > 0 {
			return fmt.Errorf("flags such as 'limit' and 'continue' are not supported when 'summary-only' is specified")
		}
	}
	if kind == TemplateVersionKind && len(o.FleetName) == 0 {
		return fmt.Errorf("fleetname must be specified when fetching templateversions")
	}
	if len(o.Output) > 0 && !slices.Contains(legalOutputTypes, o.Output) {
		return fmt.Errorf("output format must be one of (%s)", strings.Join(legalOutputTypes, ", "))
	}
	if o.Rendered {
		if kind != DeviceKind || len(name) == 0 {
			return fmt.Errorf("rendered must only be specified when fetching a specific device")
		}
		if o.Output != jsonFormat && o.Output != yamlFormat {
			return fmt.Errorf("rendered output must be one of (json, yaml)")
		}
	}
	if o.Limit < 0 {
		return fmt.Errorf("limit must be greater than 0")
	}
	return nil
}

func (o *GetOptions) Run(ctx context.Context, args []string) error { //nolint:gocyclo
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
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
			SummaryOnly:   lo.ToPtr(o.SummaryOnly),
		}
		response, err = c.ListDevicesWithResponse(ctx, &params)
	case kind == EnrollmentRequestKind && len(name) > 0:
		response, err = c.ReadEnrollmentRequestWithResponse(ctx, name)
	case kind == EnrollmentRequestKind && len(name) == 0:
		params := api.ListEnrollmentRequestsParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListEnrollmentRequestsWithResponse(ctx, &params)
	case kind == FleetKind && len(name) > 0:
		params := api.ReadFleetParams{
			AddDevicesSummary: util.ToPtrWithNilDefault(o.Summary),
		}
		response, err = c.ReadFleetWithResponse(ctx, name, &params)
	case kind == FleetKind && len(name) == 0:
		params := api.ListFleetsParams{
			LabelSelector:     util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector:     util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:             util.ToPtrWithNilDefault(o.Limit),
			Continue:          util.ToPtrWithNilDefault(o.Continue),
			AddDevicesSummary: util.ToPtrWithNilDefault(o.Summary),
		}
		response, err = c.ListFleetsWithResponse(ctx, &params)
	case kind == TemplateVersionKind && len(name) > 0:
		response, err = c.ReadTemplateVersionWithResponse(ctx, o.FleetName, name)
	case kind == TemplateVersionKind && len(name) == 0:
		params := api.ListTemplateVersionsParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListTemplateVersionsWithResponse(ctx, o.FleetName, &params)
	case kind == RepositoryKind && len(name) > 0:
		response, err = c.ReadRepositoryWithResponse(ctx, name)
	case kind == RepositoryKind && len(name) == 0:
		params := api.ListRepositoriesParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListRepositoriesWithResponse(ctx, &params)
	case kind == ResourceSyncKind && len(name) > 0:
		response, err = c.ReadResourceSyncWithResponse(ctx, name)
	case kind == ResourceSyncKind && len(name) == 0:
		params := api.ListResourceSyncParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListResourceSyncWithResponse(ctx, &params)
	case kind == CertificateSigningRequestKind && len(name) > 0:
		response, err = c.ReadCertificateSigningRequestWithResponse(ctx, name)
	case kind == CertificateSigningRequestKind && len(name) == 0:
		params := api.ListCertificateSigningRequestsParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		response, err = c.ListCertificateSigningRequestsWithResponse(ctx, &params)
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}
	return o.processReponse(response, err, kind, name)
}

func (o *GetOptions) processReponse(response interface{}, err error, kind string, name string) error {
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
			var dest api.Error
			if err := json.Unmarshal(responseBody, &dest); err != nil {
				return fmt.Errorf("unmarshalling error: %w", err)
			}
			return fmt.Errorf(errorPrefix+": %d, message: %s", httpResponse.StatusCode, dest.Message)
		}
		return fmt.Errorf(errorPrefix+": %d", httpResponse.StatusCode)
	}

	json200, err := responseField[interface{}](response, "JSON200")
	if err != nil {
		return err
	}

	switch o.Output {
	case jsonFormat:
		marshalled, err := json.Marshal(json200)
		if err != nil {
			return fmt.Errorf("marshalling resource: %w", err)
		}
		fmt.Printf("%s\n", string(marshalled))
		return nil
	case yamlFormat:
		marshalled, err := yaml.Marshal(json200)
		if err != nil {
			return fmt.Errorf("marshalling resource: %w", err)
		}
		fmt.Printf("%s\n", string(marshalled))
		return nil
	default:
		return o.printTable(response, kind, name)
	}
}

//nolint:gocyclo
func (o *GetOptions) printTable(response interface{}, kind string, name string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', 0)
	switch {
	case kind == DeviceKind && len(name) == 0:
		if o.SummaryOnly {
			o.printDevicesSummaryTable(w, response.(*apiclient.ListDevicesResponse).JSON200.Summary)
			break
		}

		o.printDevicesTable(w, response.(*apiclient.ListDevicesResponse).JSON200.Items...)
		if o.Summary {
			o.printNewLine(w)
			o.printDevicesSummaryTable(w, response.(*apiclient.ListDevicesResponse).JSON200.Summary)
		}
	case kind == DeviceKind && len(name) > 0:
		o.printDevicesTable(w, *(response.(*apiclient.ReadDeviceResponse).JSON200))
	case kind == EnrollmentRequestKind && len(name) == 0:
		o.printEnrollmentRequestsTable(w, response.(*apiclient.ListEnrollmentRequestsResponse).JSON200.Items...)
	case kind == EnrollmentRequestKind && len(name) > 0:
		o.printEnrollmentRequestsTable(w, *(response.(*apiclient.ReadEnrollmentRequestResponse).JSON200))
	case kind == FleetKind && len(name) == 0:
		o.printFleetsTable(w, response.(*apiclient.ListFleetsResponse).JSON200.Items...)
	case kind == FleetKind && len(name) > 0:
		o.printFleetsTable(w, *(response.(*apiclient.ReadFleetResponse).JSON200))
	case kind == TemplateVersionKind && len(name) == 0:
		o.printTemplateVersionsTable(w, response.(*apiclient.ListTemplateVersionsResponse).JSON200.Items...)
	case kind == TemplateVersionKind && len(name) > 0:
		o.printTemplateVersionsTable(w, *(response.(*apiclient.ReadTemplateVersionResponse).JSON200))
	case kind == RepositoryKind && len(name) == 0:
		o.printRepositoriesTable(w, response.(*apiclient.ListRepositoriesResponse).JSON200.Items...)
	case kind == RepositoryKind && len(name) > 0:
		o.printRepositoriesTable(w, *(response.(*apiclient.ReadRepositoryResponse).JSON200))
	case kind == ResourceSyncKind && len(name) == 0:
		o.printResourceSyncsTable(w, response.(*apiclient.ListResourceSyncResponse).JSON200.Items...)
	case kind == ResourceSyncKind && len(name) > 0:
		o.printResourceSyncsTable(w, *(response.(*apiclient.ReadResourceSyncResponse).JSON200))
	case kind == CertificateSigningRequestKind && len(name) == 0:
		o.printCSRTable(w, response.(*apiclient.ListCertificateSigningRequestsResponse).JSON200.Items...)
	case kind == CertificateSigningRequestKind && len(name) > 0:
		o.printCSRTable(w, *(response.(*apiclient.ReadCertificateSigningRequestResponse).JSON200))
	default:
		return fmt.Errorf("unknown resource type %s", kind)
	}
	w.Flush()
	return nil
}

// Helper function to print a new line
func (o *GetOptions) printNewLine(w *tabwriter.Writer) {
	fmt.Fprintln(w)
}

func (o *GetOptions) printDevicesSummaryTable(w *tabwriter.Writer, summary *api.DevicesSummary) {
	fmt.Fprintln(w, "DEVICES")
	fmt.Fprintf(w, "%s\n", fmt.Sprintf("%d", summary.Total))

	fmt.Fprintln(w, "\nSTATUS TYPE\tSTATUS\tCOUNT")

	for k, v := range summary.SummaryStatus {
		fmt.Fprintf(w, "%s\t%s\t%s\n", "SYSTEM", k, fmt.Sprintf("%d", v))
	}

	for k, v := range summary.UpdateStatus {
		fmt.Fprintf(w, "%s\t%s\t%s\n", "UPDATED", k, fmt.Sprintf("%d", v))
	}

	for k, v := range summary.ApplicationStatus {
		fmt.Fprintf(w, "%s\t%s\t%s\n", "APPLICATIONS", k, fmt.Sprintf("%d", v))
	}
}

func (o *GetOptions) printDevicesTable(w *tabwriter.Writer, devices ...api.Device) {
	if o.Output == wideFormat {
		fmt.Fprintln(w, "NAME\tALIAS\tOWNER\tSYSTEM\tUPDATED\tAPPLICATIONS\tLAST SEEN\tLABELS")
	} else {
		fmt.Fprintln(w, "NAME\tALIAS\tOWNER\tSYSTEM\tUPDATED\tAPPLICATIONS\tLAST SEEN")
	}
	for _, d := range devices {
		lastSeen := "<never>"
		if !d.Status.LastSeen.IsZero() {
			lastSeen = humanize.Time(d.Status.LastSeen)
		}
		alias := ""
		if d.Metadata.Labels != nil {
			alias = (*d.Metadata.Labels)["alias"]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s",
			*d.Metadata.Name,
			alias,
			util.DefaultIfNil(d.Metadata.Owner, "<none>"),
			d.Status.Summary.Status,
			d.Status.Updated.Status,
			d.Status.ApplicationsSummary.Status,
			lastSeen,
		)
		if o.Output == wideFormat {
			fmt.Fprintf(w, "\t%s\n", strings.Join(util.LabelMapToArray(d.Metadata.Labels), ","))
		} else {
			fmt.Fprintln(w)
		}
	}
}

func (o *GetOptions) printEnrollmentRequestsTable(w *tabwriter.Writer, ers ...api.EnrollmentRequest) {
	fmt.Fprintln(w, "NAME\tAPPROVAL\tAPPROVER\tAPPROVED LABELS")
	for _, e := range ers {
		approval, approver, approvedLabels := "Pending", "<none>", ""
		if e.Status.Approval != nil {
			approval = util.BoolToStr(e.Status.Approval.Approved, "Approved", "Denied")
			approver = e.Status.Approval.ApprovedBy
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

func (o *GetOptions) printFleetsTable(w *tabwriter.Writer, fleets ...api.Fleet) {
	fmt.Fprintln(w, "NAME\tOWNER\tSELECTOR\tVALID\tDEVICES")
	for i := range fleets {
		f := fleets[i]
		selector := "<none>"
		if f.Spec.Selector != nil {
			selector = strings.Join(util.LabelMapToArray(f.Spec.Selector.MatchLabels), ",")
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

func (o *GetOptions) printTemplateVersionsTable(w *tabwriter.Writer, tvs ...api.TemplateVersion) {
	fmt.Fprintln(w, "FLEET\tNAME")
	for _, tv := range tvs {
		fmt.Fprintf(w, "%s\t%s\n", tv.Spec.Fleet, *tv.Metadata.Name)
	}
}

func (o *GetOptions) printRepositoriesTable(w *tabwriter.Writer, repos ...api.Repository) {
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

func (o *GetOptions) printResourceSyncsTable(w *tabwriter.Writer, resourcesyncs ...api.ResourceSync) {
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

func (o *GetOptions) printCSRTable(w *tabwriter.Writer, csrs ...api.CertificateSigningRequest) {
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
