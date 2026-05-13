package display

import (
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	apiclientv1alpha1 "github.com/flightctl/flightctl/internal/api/client/v1alpha1"
	imagebuilderclient "github.com/flightctl/flightctl/internal/api/imagebuilder/client"
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
	case strings.EqualFold(options.Kind, api.OrganizationKind):
		return f.printOrganizationsTable(w, data.(*apiclient.ListOrganizationsResponse).JSON200.Items...)
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
	case strings.EqualFold(options.Kind, api.AuthProviderKind):
		return f.printAuthProvidersTable(w, data.(*apiclient.ListAuthProvidersResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, string(imagebuilderapi.ResourceKindImageBuild)):
		return f.printImageBuildsTable(w, options.WithExports, data.(*imagebuilderclient.ListImageBuildsResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, string(imagebuilderapi.ResourceKindImageExport)):
		return f.printImageExportsTable(w, data.(*imagebuilderclient.ListImageExportsResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, api.AuthConfigKind):
		// Special case for AuthConfig which contains providers
		authConfig := data.(*api.AuthConfig)
		if authConfig.Providers != nil && len(*authConfig.Providers) > 0 {
			return f.printAuthConfigProvidersTable(w, authConfig)
		}
		return nil
	case strings.EqualFold(options.Kind, apiv1alpha1.CatalogKind):
		return f.printCatalogsTable(w, data.(*apiclientv1alpha1.ListCatalogsResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, apiv1alpha1.CatalogItemKind):
		return f.printCatalogItemsTable(w, options.CatalogName == "", data.(*apiclientv1alpha1.ListAllCatalogItemsResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, apiv1alpha1.VulnerabilityGroupKind):
		if resp, ok := data.(*apiclientv1alpha1.ListVulnerabilitiesResponse); ok {
			return f.printVulnerabilityGroupsTable(w, false, resp.JSON200.Items...)
		}
		return fmt.Errorf("unexpected data type for %s", apiv1alpha1.VulnerabilityGroupKind)
	case strings.EqualFold(options.Kind, apiv1alpha1.FleetVulnerabilityGroupKind):
		if resp, ok := data.(*apiclientv1alpha1.GetFleetVulnerabilitiesResponse); ok {
			return f.printVulnerabilityGroupsTable(w, true, resp.JSON200.Items...)
		}
		return fmt.Errorf("unexpected data type for %s", apiv1alpha1.FleetVulnerabilityGroupKind)
	case strings.EqualFold(options.Kind, apiv1alpha1.VulnerabilityKind):
		return f.printVulnerabilitiesTable(w, data.(*apiclientv1alpha1.GetDeviceVulnerabilitiesResponse).JSON200.Items...)
	case strings.EqualFold(options.Kind, apiv1alpha1.VulnerabilityImpactKind):
		return f.printVulnerabilityImpactTable(w, data.(*apiclientv1alpha1.GetVulnerabilityImpactResponse).JSON200)
	case strings.EqualFold(options.Kind, apiv1alpha1.VulnerabilitySummaryKind):
		return f.printVulnerabilitySummaryTable(w, data.(*apiclientv1alpha1.GetVulnerabilitySummaryResponse).JSON200)
	case strings.EqualFold(options.Kind, apiv1alpha1.DeviceVulnerabilitySummaryKind):
		return f.printDeviceVulnerabilitySummaryTable(w, data.(*apiclientv1alpha1.GetDeviceVulnerabilitySummaryResponse).JSON200)
	case strings.EqualFold(options.Kind, apiv1alpha1.FleetVulnerabilitySummaryKind):
		return f.printFleetVulnerabilitySummaryTable(w, data.(*apiclientv1alpha1.GetFleetVulnerabilitySummaryResponse).JSON200)
	default:
		return fmt.Errorf("unknown resource type %s", options.Kind)
	}
	return nil
}

// formatSingle handles formatting for single resource endpoints (TYPE/NAME)
func (f *TableFormatter) formatSingle(w *tabwriter.Writer, data interface{}, options FormatOptions) error {
	switch {
	case strings.EqualFold(options.Kind, api.DeviceKind):
		if getLastSeenResponse, ok := data.(*apiclient.GetDeviceLastSeenResponse); ok {
			// Check HTTP status code explicitly to distinguish between 204 (no content) and error responses
			switch statusCode := getLastSeenResponse.StatusCode(); statusCode {
			case 204:
				// 204 No Content - device has no last-seen timestamp
				return f.printDevicesLastSeenTable(w, nil)
			case 200:
				// 200 OK - device has last-seen timestamp
				return f.printDevicesLastSeenTable(w, getLastSeenResponse.JSON200)
			default:
				// Error response (401/403/404/429/503) - surface the error
				return fmt.Errorf("failed to get device last seen: HTTP %d", statusCode)
			}
		}
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
	case strings.EqualFold(options.Kind, api.AuthProviderKind):
		return f.printAuthProvidersTable(w, *data.(*apiclient.GetAuthProviderResponse).JSON200)
	case strings.EqualFold(options.Kind, string(imagebuilderapi.ResourceKindImageBuild)):
		return f.printImageBuildsTable(w, options.WithExports, *data.(*imagebuilderclient.GetImageBuildResponse).JSON200)
	case strings.EqualFold(options.Kind, string(imagebuilderapi.ResourceKindImageExport)):
		return f.printImageExportsTable(w, *data.(*imagebuilderclient.GetImageExportResponse).JSON200)
	case strings.EqualFold(options.Kind, apiv1alpha1.CatalogKind):
		return f.printCatalogsTable(w, *data.(*apiclientv1alpha1.GetCatalogResponse).JSON200)
	case strings.EqualFold(options.Kind, apiv1alpha1.CatalogItemKind):
		return f.printCatalogItemsTable(w, options.CatalogName == "", *data.(*apiclientv1alpha1.GetCatalogItemResponse).JSON200)
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

func (f *TableFormatter) printDevicesLastSeenTable(w *tabwriter.Writer, lastSeen *api.DeviceLastSeen) error {
	f.printHeaderRowLn(w, "LAST SEEN", "TIME AGO")
	if lastSeen == nil {
		f.printTableRowLn(w, "<never>", "<never>")
	} else {
		f.printTableRowLn(w, lastSeen.LastSeen.Format(time.RFC3339), humanize.Time(lastSeen.LastSeen))
	}
	return nil
}

func (f *TableFormatter) printDevicesTable(w *tabwriter.Writer, wide bool, devices ...api.Device) error {
	if wide {
		f.printHeaderRowLn(w, "NAME", "ALIAS", "OWNER", "SYSTEM", "UPDATED", "APPLICATIONS", "LABELS")
	} else {
		f.printHeaderRowLn(w, "NAME", "ALIAS", "OWNER", "SYSTEM", "UPDATED", "APPLICATIONS")
	}
	for _, d := range devices {
		alias := ""
		if d.Metadata.Labels != nil {
			alias = (*d.Metadata.Labels)["alias"]
		}

		// Handle nil status gracefully
		summaryStatus := "Unknown"
		updatedStatus := "Unknown"
		applicationsStatus := "Unknown"
		if d.Status != nil {
			summaryStatus = string(d.Status.Summary.Status)
			updatedStatus = string(d.Status.Updated.Status)
			applicationsStatus = string(d.Status.ApplicationsSummary.Status)
		}

		f.printTableRow(w,
			*d.Metadata.Name,
			alias,
			util.DefaultIfNil(d.Metadata.Owner, NoneString),
			summaryStatus,
			updatedStatus,
			applicationsStatus,
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
		approval, approver, approvedLabels := "Pending", NoneString, ""
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
		selector := NoneString
		if fleet.Spec.Selector != nil {
			selector = strings.Join(util.LabelMapToArray(fleet.Spec.Selector.MatchLabels), ",")
		}
		valid := "Unknown"
		numDevices := "Unknown"
		if fleet.Status != nil {
			condition := api.FindStatusCondition(fleet.Status.Conditions, api.ConditionTypeFleetValid)
			if condition != nil {
				valid = string(condition.Status)
			}
			if showSummary && fleet.Status.DevicesSummary != nil {
				numDevices = fmt.Sprintf("%d", fleet.Status.DevicesSummary.Total)
			}
		}

		f.printTableRow(w,
			*fleet.Metadata.Name,
			util.DefaultIfNil(fleet.Metadata.Owner, NoneString),
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

func (f *TableFormatter) printOrganizationsTable(w *tabwriter.Writer, orgs ...api.Organization) error {
	f.printHeaderRowLn(w, "NAME", "DISPLAY NAME", "EXTERNAL ID")
	for _, org := range orgs {
		displayName := util.DefaultIfNil(org.Spec.DisplayName, NoneString)
		externalID := util.DefaultIfNil(org.Spec.ExternalId, NoneString)
		f.printTableRowLn(w,
			*org.Metadata.Name,
			displayName,
			externalID,
		)
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

		repoType, err := r.Spec.Discriminator()
		if err != nil {
			repoType = "unknown"
		}

		f.printTableRowLn(w,
			*r.Metadata.Name,
			repoType,
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

func (f *TableFormatter) printAuthConfigProvidersTable(w *tabwriter.Writer, authConfig *api.AuthConfig) error {
	if authConfig == nil {
		return fmt.Errorf("auth config is nil")
	}

	f.printHeaderRowLn(w, "NAME", "TYPE", "ISSUER", "ENABLED", "DEFAULT")

	defaultProvider := ""
	if authConfig.DefaultProvider != nil {
		defaultProvider = *authConfig.DefaultProvider
	}

	for _, ap := range *authConfig.Providers {
		issuer := NoneString
		enabled := NoneString
		name := NoneString
		isDefault := ""
		if ap.Metadata.Name != nil {
			name = *ap.Metadata.Name
		}

		if name == defaultProvider {
			isDefault = "*"
		}

		// Extract type from the discriminator
		providerType, err := ap.Spec.Discriminator()
		if err != nil {
			return fmt.Errorf("failed to get discriminator for provider %s: %w", name, err)
		}

		// Extract issuer and enabled based on type
		switch providerType {
		case string(api.Oidc):
			oidcSpec, err := ap.Spec.AsOIDCProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse OIDC provider spec for %s: %w", name, err)
			}
			issuer = oidcSpec.Issuer
			if oidcSpec.Enabled != nil {
				enabled = util.BoolToStr(*oidcSpec.Enabled, "true", "false")
			}
		case string(api.Oauth2):
			oauth2Spec, err := ap.Spec.AsOAuth2ProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse OAuth2 provider spec for %s: %w", name, err)
			}
			if oauth2Spec.Issuer != nil {
				issuer = *oauth2Spec.Issuer
			}
			if oauth2Spec.Enabled != nil {
				enabled = util.BoolToStr(*oauth2Spec.Enabled, "true", "false")
			}
		case string(api.Openshift):
			openshiftSpec, err := ap.Spec.AsOpenShiftProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse OpenShift provider spec for %s: %w", name, err)
			}
			if openshiftSpec.Issuer != nil {
				issuer = *openshiftSpec.Issuer
			}
			if openshiftSpec.Enabled != nil {
				enabled = util.BoolToStr(*openshiftSpec.Enabled, "true", "false")
			}
		case string(api.Aap):
			aapSpec, err := ap.Spec.AsAapProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse AAP provider spec for %s: %w", name, err)
			}
			if aapSpec.Enabled != nil {
				enabled = util.BoolToStr(*aapSpec.Enabled, "true", "false")
			}
			if aapSpec.ApiUrl != "" {
				issuer = aapSpec.ApiUrl
			}
		case string(api.K8s):
			k8sSpec, err := ap.Spec.AsK8sProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse K8s provider spec for %s: %w", name, err)
			}
			if k8sSpec.Enabled != nil {
				enabled = util.BoolToStr(*k8sSpec.Enabled, "true", "false")
			}
			if k8sSpec.ApiUrl != "" {
				issuer = k8sSpec.ApiUrl
			}
		default:
			issuer = NoneString
			enabled = NoneString
		}

		f.printTableRowLn(w, name, providerType, issuer, enabled, isDefault)
	}
	return nil
}

func (f *TableFormatter) printAuthProvidersTable(w *tabwriter.Writer, authProviders ...api.AuthProvider) error {
	f.printHeaderRowLn(w, "NAME", "TYPE", "ISSUER", "CLIENT ID", "ENABLED")
	for _, ap := range authProviders {
		name := NoneString
		if ap.Metadata.Name != nil {
			name = *ap.Metadata.Name
		}

		issuer := NoneString
		clientId := NoneString
		enabled := NoneString

		// Extract type from the discriminator
		providerType, err := ap.Spec.Discriminator()
		if err != nil {
			return fmt.Errorf("failed to get discriminator for provider %s: %w", name, err)
		}

		// Extract issuer, clientId, and enabled based on type
		switch providerType {
		case string(api.Oidc):
			oidcSpec, err := ap.Spec.AsOIDCProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse OIDC provider spec for %s: %w", name, err)
			}
			issuer = oidcSpec.Issuer
			clientId = oidcSpec.ClientId
			if oidcSpec.Enabled != nil {
				enabled = util.BoolToStr(*oidcSpec.Enabled, "true", "false")
			}
		case string(api.Oauth2):
			oauth2Spec, err := ap.Spec.AsOAuth2ProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse OAuth2 provider spec for %s: %w", name, err)
			}
			if oauth2Spec.Issuer != nil {
				issuer = *oauth2Spec.Issuer
			}
			clientId = oauth2Spec.ClientId
			if oauth2Spec.Enabled != nil {
				enabled = util.BoolToStr(*oauth2Spec.Enabled, "true", "false")
			}
		case string(api.Openshift):
			openshiftSpec, err := ap.Spec.AsOpenShiftProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse OpenShift provider spec for %s: %w", name, err)
			}
			if openshiftSpec.Issuer != nil {
				issuer = *openshiftSpec.Issuer
			}
			if openshiftSpec.ClientId != nil {
				clientId = *openshiftSpec.ClientId
			}
			if openshiftSpec.Enabled != nil {
				enabled = util.BoolToStr(*openshiftSpec.Enabled, "true", "false")
			}
		case string(api.Aap):
			aapSpec, err := ap.Spec.AsAapProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse AAP provider spec for %s: %w", name, err)
			}
			if aapSpec.Enabled != nil {
				enabled = util.BoolToStr(*aapSpec.Enabled, "true", "false")
			}
		case string(api.K8s):
			k8sSpec, err := ap.Spec.AsK8sProviderSpec()
			if err != nil {
				return fmt.Errorf("failed to parse K8s provider spec for %s: %w", name, err)
			}
			if k8sSpec.Enabled != nil {
				enabled = util.BoolToStr(*k8sSpec.Enabled, "true", "false")
			}
		}

		f.printTableRowLn(w, name, providerType, issuer, clientId, enabled)
	}
	return nil
}

func (f *TableFormatter) printImageBuildsTable(w *tabwriter.Writer, withExports bool, imageBuilds ...imagebuilderapi.ImageBuild) error {
	if withExports {
		f.printHeaderRowLn(w, "NAME", "PHASE", "INPUT", "OUTPUT", "EXPORTS", "AGE")
	} else {
		f.printHeaderRowLn(w, "NAME", "PHASE", "INPUT", "OUTPUT", "AGE")
	}
	for _, ib := range imageBuilds {
		name := NoneString
		if ib.Metadata.Name != nil {
			name = *ib.Metadata.Name
		}

		phase := NoneString
		if ib.Status != nil && ib.Status.Conditions != nil {
			for _, cond := range *ib.Status.Conditions {
				if cond.Type == imagebuilderapi.ImageBuildConditionTypeReady {
					phase = cond.Reason
					break
				}
			}
		}

		source := fmt.Sprintf("%s/%s:%s", ib.Spec.Source.Repository, ib.Spec.Source.ImageName, ib.Spec.Source.ImageTag)
		destination := fmt.Sprintf("%s/%s:%s", ib.Spec.Destination.Repository, ib.Spec.Destination.ImageName, ib.Spec.Destination.ImageTag)

		age := NoneString
		if ib.Metadata.CreationTimestamp != nil {
			age = humanize.Time(*ib.Metadata.CreationTimestamp)
		}

		if withExports {
			exports := NoneString
			if ib.Imageexports != nil && len(*ib.Imageexports) > 0 {
				formatMap := make(map[string]bool)
				for _, export := range *ib.Imageexports {
					if export.Spec.Format != "" {
						formatMap[string(export.Spec.Format)] = true
					}
				}
				if len(formatMap) > 0 {
					var formats []string
					for format := range formatMap {
						formats = append(formats, format)
					}
					// Sort formats for consistent output
					slices.Sort(formats)
					exports = strings.Join(formats, ",")
				}
			}
			f.printTableRowLn(w, name, phase, source, destination, exports, age)
		} else {
			f.printTableRowLn(w, name, phase, source, destination, age)
		}
	}
	return nil
}

func (f *TableFormatter) printImageExportsTable(w *tabwriter.Writer, imageExports ...imagebuilderapi.ImageExport) error {
	f.printHeaderRowLn(w, "NAME", "PHASE", "SOURCE", "OUTPUT", "FORMAT", "AGE")
	for _, ie := range imageExports {
		name := NoneString
		if ie.Metadata.Name != nil {
			name = *ie.Metadata.Name
		}

		phase := NoneString
		if ie.Status != nil && ie.Status.Conditions != nil {
			for _, cond := range *ie.Status.Conditions {
				if cond.Type == imagebuilderapi.ImageExportConditionTypeReady {
					phase = cond.Reason
					break
				}
			}
		}

		source := NoneString
		output := NoneString
		discriminator, err := ie.Spec.Source.Discriminator()
		if err == nil {
			switch discriminator {
			case string(imagebuilderapi.ImageExportSourceTypeImageBuild):
				if buildSource, err := ie.Spec.Source.AsImageBuildRefSource(); err == nil {
					source = fmt.Sprintf("imagebuild/%s", buildSource.ImageBuildRef)
					// Output uses ImageBuild destination
					output = fmt.Sprintf("imagebuild/%s", buildSource.ImageBuildRef)
				}
			}
		}

		format := NoneString
		if ie.Spec.Format != "" {
			format = string(ie.Spec.Format)
		}

		age := NoneString
		if ie.Metadata.CreationTimestamp != nil {
			age = humanize.Time(*ie.Metadata.CreationTimestamp)
		}

		f.printTableRowLn(w, name, phase, source, output, format, age)
	}
	return nil
}

func (f *TableFormatter) printCatalogsTable(w *tabwriter.Writer, catalogs ...apiv1alpha1.Catalog) error {
	f.printHeaderRowLn(w, "NAME", "DISPLAY NAME", "AGE")

	for _, cat := range catalogs {
		name := NoneString
		if cat.Metadata.Name != nil {
			name = *cat.Metadata.Name
		}

		displayName := NoneString
		if cat.Spec.DisplayName != nil {
			displayName = *cat.Spec.DisplayName
		}

		age := NoneString
		if cat.Metadata.CreationTimestamp != nil {
			age = humanize.Time(*cat.Metadata.CreationTimestamp)
		}

		f.printTableRowLn(w, name, displayName, age)
	}
	return nil
}

func (f *TableFormatter) printCatalogItemsTable(w *tabwriter.Writer, showCatalog bool, items ...apiv1alpha1.CatalogItem) error {
	if showCatalog {
		if f.wide {
			f.printHeaderRowLn(w, "CATALOG", "NAME", "CATEGORY", "TYPE", "DISPLAY NAME")
		} else {
			f.printHeaderRowLn(w, "CATALOG", "NAME", "TYPE", "DISPLAY NAME")
		}
	} else {
		if f.wide {
			f.printHeaderRowLn(w, "NAME", "CATEGORY", "TYPE", "DISPLAY NAME")
		} else {
			f.printHeaderRowLn(w, "NAME", "TYPE", "DISPLAY NAME")
		}
	}

	for _, item := range items {
		name := NoneString
		if item.Metadata.Name != nil {
			name = *item.Metadata.Name
		}

		itemType := NoneString
		if item.Spec.Type != "" {
			itemType = string(item.Spec.Type)
		}

		displayName := NoneString
		if item.Spec.DisplayName != nil {
			displayName = *item.Spec.DisplayName
		}

		category := NoneString
		if f.wide && item.Spec.Category != nil {
			category = string(*item.Spec.Category)
		}

		if showCatalog {
			if f.wide {
				f.printTableRowLn(w, item.Metadata.Catalog, name, category, itemType, displayName)
			} else {
				f.printTableRowLn(w, item.Metadata.Catalog, name, itemType, displayName)
			}
		} else {
			if f.wide {
				f.printTableRowLn(w, name, category, itemType, displayName)
			} else {
				f.printTableRowLn(w, name, itemType, displayName)
			}
		}
	}
	return nil
}

func (f *TableFormatter) printVulnerabilitiesTable(w *tabwriter.Writer, vulns ...apiv1alpha1.Vulnerability) error {
	if f.wide {
		f.printHeaderRowLn(w, "CVE ID", "SEVERITY", "CVSS", "ADVISORY", "PUBLISHED", "DESCRIPTION")
	} else {
		f.printHeaderRowLn(w, "CVE ID", "SEVERITY", "CVSS", "ADVISORY", "PUBLISHED")
	}
	if len(vulns) == 0 {
		return nil
	}
	for _, v := range vulns {
		cvss := NoneString
		if v.CvssScore != nil {
			cvss = fmt.Sprintf("%.1f", *v.CvssScore)
		}
		published := NoneString
		if v.PublishedAt != nil {
			published = humanize.Time(*v.PublishedAt)
		}
		advisory := NoneString
		if v.AdvisoryId != nil {
			advisory = *v.AdvisoryId
		}

		if f.wide {
			description := NoneString
			if v.Description != nil {
				desc := *v.Description
				if len(desc) > 60 {
					desc = desc[:57] + "..."
				}
				description = desc
			}
			f.printTableRowLn(w, v.CveId, string(v.Severity), cvss, advisory, published, description)
		} else {
			f.printTableRowLn(w, v.CveId, string(v.Severity), cvss, advisory, published)
		}
	}
	return nil
}

func (f *TableFormatter) printVulnerabilityGroupsTable(w *tabwriter.Writer, showImages bool, groups ...apiv1alpha1.VulnerabilityGroup) error {
	if showImages {
		if f.wide {
			f.printHeaderRowLn(w, "CVE ID", "SEVERITY", "CVSS", "ADVISORY", "IMAGES", "PUBLISHED", "DESCRIPTION")
		} else {
			f.printHeaderRowLn(w, "CVE ID", "SEVERITY", "CVSS", "ADVISORY", "IMAGES", "PUBLISHED")
		}
	} else {
		if f.wide {
			f.printHeaderRowLn(w, "CVE ID", "SEVERITY", "CVSS", "ADVISORY", "DEVICES", "PUBLISHED", "DESCRIPTION")
		} else {
			f.printHeaderRowLn(w, "CVE ID", "SEVERITY", "CVSS", "ADVISORY", "DEVICES", "PUBLISHED")
		}
	}
	if len(groups) == 0 {
		return nil
	}

	for _, g := range groups {
		cvss := NoneString
		if g.MaxCvssScore != nil {
			cvss = fmt.Sprintf("%.1f", *g.MaxCvssScore)
		}
		advisory := NoneString
		if len(g.Findings) > 0 && g.Findings[0].AdvisoryId != nil {
			advisory = *g.Findings[0].AdvisoryId
		}
		published := NoneString
		if g.MaxPublishedAt != nil {
			published = humanize.Time(*g.MaxPublishedAt)
		}

		countCol := "0"
		if showImages {
			countCol = fmt.Sprintf("%d", len(g.Findings))
		} else if g.AffectedDevices != nil {
			countCol = fmt.Sprintf("%d", *g.AffectedDevices)
		}

		if f.wide {
			description := NoneString
			if len(g.Findings) > 0 && g.Findings[0].Description != nil {
				desc := *g.Findings[0].Description
				if len(desc) > 60 {
					desc = desc[:57] + "..."
				}
				description = desc
			}
			f.printTableRowLn(w, g.CveId, string(g.Severity), cvss, advisory, countCol, published, description)
		} else {
			f.printTableRowLn(w, g.CveId, string(g.Severity), cvss, advisory, countCol, published)
		}
	}
	return nil
}

func (f *TableFormatter) printVulnerabilityImpactTable(w *tabwriter.Writer, impact *apiv1alpha1.VulnerabilityImpact) error {
	fmt.Fprintf(w, "CVE ID\t%s\n", impact.CveId)

	severity := string(impact.Severity)
	if impact.MaxCvssScore != nil {
		severity = fmt.Sprintf("%s (CVSS %.1f)", impact.Severity, *impact.MaxCvssScore)
	}
	fmt.Fprintf(w, "SEVERITY\t%s\n", severity)

	advisory := NoneString
	if len(impact.Items) > 0 && len(impact.Items[0].Findings) > 0 && impact.Items[0].Findings[0].AdvisoryId != nil {
		advisory = *impact.Items[0].Findings[0].AdvisoryId
	}
	fmt.Fprintf(w, "ADVISORY\t%s\n", advisory)

	issuer := NoneString
	if impact.Issuer != nil {
		issuer = *impact.Issuer
	}
	fmt.Fprintf(w, "ISSUER\t%s\n", issuer)

	link := NoneString
	if impact.Link != nil {
		link = *impact.Link
	}
	fmt.Fprintf(w, "LINK\t%s\n", link)

	description := findImpactDescription(impact)
	if description == "" {
		fmt.Fprintf(w, "DESCRIPTION\t%s\n", NoneString)
	} else {
		fmt.Fprintln(w, "DESCRIPTION")
		for _, line := range wrapText(description, 80) {
			fmt.Fprintln(w, line)
		}
	}

	fmt.Fprintln(w)

	if len(impact.Items) == 0 {
		fmt.Fprintln(w, "No affected fleets or devices found.")
		return nil
	}

	f.printHeaderRowLn(w, "FLEET", "AFFECTED DEVICES", "IMAGES")
	for _, fleet := range impact.Items {
		fleetName := fleet.FleetName
		if fleet.Fleetless {
			fleetName = "(fleetless)"
		}
		images := formatImpactImages(fleet.Findings, f.wide)
		f.printTableRowLn(w, fleetName, fmt.Sprintf("%d", fleet.AffectedDevices), images)
	}
	return nil
}

func findImpactDescription(impact *apiv1alpha1.VulnerabilityImpact) string {
	for _, fleet := range impact.Items {
		for _, finding := range fleet.Findings {
			if finding.Description != nil && *finding.Description != "" {
				return *finding.Description
			}
		}
	}
	return ""
}

func wrapText(text string, width int) []string {
	if width <= 0 || len(text) <= width {
		return []string{text}
	}

	var lines []string
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}

	currentLine := words[0]
	for _, word := range words[1:] {
		if len(currentLine)+1+len(word) <= width {
			currentLine += " " + word
		} else {
			lines = append(lines, currentLine)
			currentLine = word
		}
	}
	lines = append(lines, currentLine)
	return lines
}

func formatImpactImages(findings []apiv1alpha1.VulnerabilityGroupItem, wide bool) string {
	if len(findings) == 0 {
		return NoneString
	}

	var parts []string
	for _, finding := range findings {
		imageRef := finding.ImageDigest
		if len(finding.ImageRefs) > 0 {
			imageRef = finding.ImageRefs[0]
		}
		count := int64(0)
		if finding.AffectedDevices != nil {
			count = *finding.AffectedDevices
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", imageRef, count))
	}

	result := strings.Join(parts, ", ")
	if !wide && len(result) > 60 {
		result = result[:57] + "..."
	}
	return result
}

func (f *TableFormatter) printVulnerabilitySummaryTable(w *tabwriter.Writer, summary *apiv1alpha1.VulnerabilitySummaryResponse) error {
	f.printHeaderRowLn(w, "", "CRITICAL", "HIGH", "MEDIUM", "LOW", "TOTAL")
	f.printTableRowLn(w, "CVEs",
		fmt.Sprintf("%d", summary.CvesBySeverity.Critical),
		fmt.Sprintf("%d", summary.CvesBySeverity.High),
		fmt.Sprintf("%d", summary.CvesBySeverity.Medium),
		fmt.Sprintf("%d", summary.CvesBySeverity.Low),
		fmt.Sprintf("%d", summary.CvesBySeverity.Total))
	return nil
}

func (f *TableFormatter) printDeviceVulnerabilitySummaryTable(w *tabwriter.Writer, summary *apiv1alpha1.DeviceVulnerabilitySummaryResponse) error {
	if summary.Image != nil {
		imageStr := *summary.Image
		if summary.ImageDigest != nil {
			digest := *summary.ImageDigest
			if len(digest) > 20 {
				digest = digest[:17] + "..."
			}
			imageStr = fmt.Sprintf("%s (%s)", imageStr, digest)
		}
		fmt.Fprintf(w, "IMAGE\t%s\n", imageStr)
	}
	fmt.Fprintf(w, "SUMMARY\tCritical: %d  High: %d  Medium: %d  Low: %d  Total: %d\n",
		summary.Summary.Critical,
		summary.Summary.High,
		summary.Summary.Medium,
		summary.Summary.Low,
		summary.Summary.Total)
	return nil
}

func (f *TableFormatter) printFleetVulnerabilitySummaryTable(w *tabwriter.Writer, summary *apiv1alpha1.FleetVulnerabilitySummaryResponse) error {
	fmt.Fprintf(w, "SUMMARY\tCritical: %d  High: %d  Medium: %d  Low: %d  Total: %d  Images: %d\n",
		summary.Summary.Critical,
		summary.Summary.High,
		summary.Summary.Medium,
		summary.Summary.Low,
		summary.Summary.Total,
		summary.Summary.UniqueDigests)
	return nil
}
