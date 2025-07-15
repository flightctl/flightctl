package display

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/util"
)

const NoneString = "<none>"

// TableFormatter handles table output formatting
type TableFormatter struct {
	wide      bool
	noHeaders bool
}

// Format outputs the data in table format
func (f *TableFormatter) Format(data interface{}, options FormatOptions) error {
	w := tabwriter.NewWriter(options.Writer, 0, 8, 1, '\t', 0)
	defer w.Flush()

	var err error
	if len(options.Name) == 0 {
		err = f.formatList(w, data, options)
	} else {
		err = f.formatSingle(w, data, options)
	}

	if err != nil {
		return err
	}

	// ensure that after this call headers are suppressed on subsequent calls
	// noHeaders is set to true after the first successful formatting run
	f.noHeaders = true

	return nil
}

// formatList handles formatting for list endpoints (TYPE)
func (f *TableFormatter) formatList(w *tabwriter.Writer, data interface{}, options FormatOptions) error {
	switch {
	case strings.EqualFold(options.Kind, api.DeviceKind):
		if options.SummaryOnly {
			return f.printDevicesSummaryTable(w, data.(*apiclient.ListDevicesResponse).JSON200.Summary)
		}

		if err := f.printDevicesTable(w, f.wide, data.(*apiclient.ListDevicesResponse).JSON200.Items...); err != nil {
			return err
		}
		if options.Summary {
			fmt.Fprintln(w)
			return f.printDevicesSummaryTable(w, data.(*apiclient.ListDevicesResponse).JSON200.Summary)
		}
	case strings.EqualFold(options.Kind, api.EnrollmentRequestKind):
		return f.printEnrollmentRequestsTable(w, data.(*apiclient.ListEnrollmentRequestsResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, api.FleetKind):
		return f.printFleetsTable(w, options.Summary, data.(*apiclient.ListFleetsResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, api.TemplateVersionKind):
		return f.printTemplateVersionsTable(w, data.(*apiclient.ListTemplateVersionsResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, api.RepositoryKind):
		return f.printRepositoriesTable(w, data.(*apiclient.ListRepositoriesResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, api.ResourceSyncKind):
		return f.printResourceSyncsTable(w, data.(*apiclient.ListResourceSyncsResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, api.CertificateSigningRequestKind):
		return f.printCSRTable(w, data.(*apiclient.ListCertificateSigningRequestsResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, api.EventKind):
		return f.printEventsTable(w, data.(*apiclient.ListEventsResponse).JSON200.Items...)
	default:
		return fmt.Errorf("unknown resource type %s", options.Kind)
	}
	return nil
}

// formatSingle handles formatting for single resource endpoints (TYPE/NAME)
func (f *TableFormatter) formatSingle(w *tabwriter.Writer, data interface{}, options FormatOptions) error {
	switch {
	case strings.EqualFold(options.Kind, api.DeviceKind):
		var device api.Device
		if getRenderedResponse, ok := data.(*apiclient.GetRenderedDeviceResponse); ok {
			device = *getRenderedResponse.JSON200
		} else {
			device = *data.(*apiclient.GetDeviceResponse).JSON200
		}
		return f.printDevicesTable(w, f.wide, device)
	case strings.EqualFold(options.Kind, api.EnrollmentRequestKind):
		return f.printEnrollmentRequestsTable(w, *data.(*apiclient.GetEnrollmentRequestResponse).JSON200)
	case strings.EqualFold(options.Kind, api.FleetKind):
		return f.printFleetsTable(w, options.Summary, *data.(*apiclient.GetFleetResponse).JSON200)
	case strings.EqualFold(options.Kind, api.TemplateVersionKind):
		return f.printTemplateVersionsTable(w, *data.(*apiclient.GetTemplateVersionResponse).JSON200)
	case strings.EqualFold(options.Kind, api.RepositoryKind):
		return f.printRepositoriesTable(w, *data.(*apiclient.GetRepositoryResponse).JSON200)
	case strings.EqualFold(options.Kind, api.ResourceSyncKind):
		return f.printResourceSyncsTable(w, *data.(*apiclient.GetResourceSyncResponse).JSON200)
	case strings.EqualFold(options.Kind, api.CertificateSigningRequestKind):
		return f.printCSRTable(w, *data.(*apiclient.GetCertificateSigningRequestResponse).JSON200)
	default:
		return fmt.Errorf("unknown resource type %s", options.Kind)
	}
}

// Helper function to print table rows with tab separation and newline
func (f *TableFormatter) printTableRowLn(w *tabwriter.Writer, columns ...string) {
	fmt.Fprintln(w, strings.Join(columns, "\t"))
}

// Helper function to print table rows with tab separation without newline
func (f *TableFormatter) printTableRow(w *tabwriter.Writer, columns ...string) {
	fmt.Fprint(w, strings.Join(columns, "\t"))
}

// printHeaderRowLn prints a header row iff header printing is enabled for the
// current invocation. Use this instead of printTableRowLn when outputting the
// header line(s).
func (f *TableFormatter) printHeaderRowLn(w *tabwriter.Writer, columns ...string) {
	if !f.noHeaders {
		f.printTableRowLn(w, columns...)
	}
}

func (f *TableFormatter) printDevicesSummaryTable(w *tabwriter.Writer, summary *api.DevicesSummary) error {
	fmt.Fprintln(w, "DEVICES")
	fmt.Fprintf(w, "%s\n", fmt.Sprintf("%d", summary.Total))

	fmt.Fprintln(w)
	f.printHeaderRowLn(w, "STATUS TYPE", "STATUS", "COUNT")

	for k, v := range summary.SummaryStatus {
		f.printTableRowLn(w, "SYSTEM", k, fmt.Sprintf("%d", v))
	}

	for k, v := range summary.UpdateStatus {
		f.printTableRowLn(w, "UPDATED", k, fmt.Sprintf("%d", v))
	}

	for k, v := range summary.ApplicationStatus {
		f.printTableRowLn(w, "APPLICATIONS", k, fmt.Sprintf("%d", v))
	}
	return nil
}

func (f *TableFormatter) printDevicesTable(w *tabwriter.Writer, wide bool, devices ...api.Device) error {
	if wide {
		f.printHeaderRowLn(w, "NAME", "ALIAS", "OWNER", "SYSTEM", "UPDATED", "APPLICATIONS", "LAST SEEN", "LABELS")
	} else {
		f.printHeaderRowLn(w, "NAME", "ALIAS", "OWNER", "SYSTEM", "UPDATED", "APPLICATIONS", "LAST SEEN")
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
		f.printTableRow(w,
			*d.Metadata.Name,
			alias,
			util.DefaultIfNil(d.Metadata.Owner, "<none>"),
			string(d.Status.Summary.Status),
			string(d.Status.Updated.Status),
			string(d.Status.ApplicationsSummary.Status),
			lastSeen,
		)
		if wide {
			f.printTableRowLn(w, "", strings.Join(util.LabelMapToArray(d.Metadata.Labels), ","))
		} else {
			fmt.Fprintln(w)
		}
	}
	return nil
}

func (f *TableFormatter) printEnrollmentRequestsTable(w *tabwriter.Writer, ers ...api.EnrollmentRequest) error {
	f.printHeaderRowLn(w, "NAME", "APPROVAL", "APPROVER", "APPROVED LABELS")
	for _, e := range ers {
		approval, approver, approvedLabels := "Pending", "<none>", ""
		if e.Status.Approval != nil {
			approval = util.BoolToStr(e.Status.Approval.Approved, "Approved", "Denied")
			approver = e.Status.Approval.ApprovedBy
			approvedLabels = strings.Join(util.LabelMapToArray(e.Status.Approval.Labels), ",")
		}
		f.printTableRowLn(w,
			*e.Metadata.Name,
			approval,
			approver,
			approvedLabels,
		)
	}
	return nil
}

func (f *TableFormatter) printFleetsTable(w *tabwriter.Writer, showSummary bool, fleets ...api.Fleet) error {
	if showSummary {
		f.printHeaderRowLn(w, "NAME", "OWNER", "SELECTOR", "VALID", "DEVICES")
	} else {
		f.printHeaderRowLn(w, "NAME", "OWNER", "SELECTOR", "VALID")
	}
	for i := range fleets {
		fleet := fleets[i]
		selector := "<none>"
		if fleet.Spec.Selector != nil {
			selector = strings.Join(util.LabelMapToArray(fleet.Spec.Selector.MatchLabels), ",")
		}
		valid := "Unknown"
		if fleet.Status != nil {
			condition := api.FindStatusCondition(fleet.Status.Conditions, api.ConditionTypeFleetValid)
			if condition != nil {
				valid = string(condition.Status)
			}
		}

		numDevices := "Unknown"
		if showSummary && fleet.Status.DevicesSummary != nil {
			numDevices = fmt.Sprintf("%d", fleet.Status.DevicesSummary.Total)
		}

		f.printTableRow(w,
			*fleet.Metadata.Name,
			util.DefaultIfNil(fleet.Metadata.Owner, "<none>"),
			selector,
			valid,
		)

		if showSummary {
			f.printTableRow(w, "", numDevices)
		}
		fmt.Fprintln(w)
	}
	return nil
}

func (f *TableFormatter) printTemplateVersionsTable(w *tabwriter.Writer, tvs ...api.TemplateVersion) error {
	f.printHeaderRowLn(w, "FLEET", "NAME")
	for _, tv := range tvs {
		f.printTableRowLn(w, tv.Spec.Fleet, *tv.Metadata.Name)
	}
	return nil
}

func (f *TableFormatter) printRepositoriesTable(w *tabwriter.Writer, repos ...api.Repository) error {
	f.printHeaderRowLn(w, "NAME", "TYPE", "REPOSITORY URL", "ACCESSIBLE")
	for _, r := range repos {
		accessible := "Unknown"
		if r.Status != nil {
			condition := api.FindStatusCondition(r.Status.Conditions, api.ConditionTypeRepositoryAccessible)
			if condition != nil {
				accessible = string(condition.Status)
			}
		}

		repoSpec, _ := r.Spec.GetGenericRepoSpec()
		repoType := repoSpec.Type

		f.printTableRowLn(w,
			*r.Metadata.Name,
			fmt.Sprintf("%v", repoType),
			util.DefaultIfError(r.Spec.GetRepoURL, ""),
			accessible,
		)
	}
	return nil
}

func (f *TableFormatter) printResourceSyncsTable(w *tabwriter.Writer, resourcesyncs ...api.ResourceSync) error {
	f.printHeaderRowLn(w, "NAME", "REPOSITORY", "PATH", "REVISION", "ACCESSIBLE", "SYNCED", "LAST SYNC")

	for _, rs := range resourcesyncs {
		accessible, synced, lastSynced := "Unknown", "Unknown", "Unknown"
		if rs.Status != nil {
			condition := api.FindStatusCondition(rs.Status.Conditions, api.ConditionTypeResourceSyncAccessible)
			if condition != nil {
				accessible = string(condition.Status)
			}
			condition = api.FindStatusCondition(rs.Status.Conditions, api.ConditionTypeResourceSyncSynced)
			if condition != nil {
				synced = string(condition.Status)
				lastSynced = humanize.Time(condition.LastTransitionTime)
			}
		}
		f.printTableRowLn(w,
			*rs.Metadata.Name,
			rs.Spec.Repository,
			rs.Spec.Path,
			rs.Spec.TargetRevision,
			accessible,
			synced,
			lastSynced,
		)
	}
	return nil
}

func (f *TableFormatter) printCSRTable(w *tabwriter.Writer, csrs ...api.CertificateSigningRequest) error {
	f.printHeaderRowLn(w, "NAME", "AGE", "SIGNERNAME", "REQUESTOR", "REQUESTEDDURATION", "CONDITION")

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
		if api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
			condition = "Approved"
		} else if api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied) {
			condition = "Denied"
		} else if api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestFailed) {
			condition = "Failed"
		}
		if csr.Status != nil && csr.Status.Certificate != nil {
			condition += ",Issued"
		}

		f.printTableRowLn(w,
			*csr.Metadata.Name,
			age,
			csr.Spec.SignerName,
			util.DefaultIfNil(csr.Spec.Username, NoneString),
			duration,
			condition,
		)
	}
	return nil
}

func (f *TableFormatter) printEventsTable(w *tabwriter.Writer, events ...api.Event) error {
	f.printHeaderRowLn(w, "AGE", "INVOLVEDOBJECT.KIND", "INVOLVEDOBJECT.NAME", "TYPE", "MESSAGE")
	for _, e := range events {
		f.printTableRowLn(w,
			humanize.Time(*e.Metadata.CreationTimestamp),
			e.InvolvedObject.Kind,
			e.InvolvedObject.Name,
			string(e.Type),
			e.Message,
		)
	}
	return nil
}
