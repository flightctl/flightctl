package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/cli/display"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var legalOutputTypes = []string{string(display.JSONFormat), string(display.YAMLFormat), string(display.NameFormat), string(display.WideFormat)}

const maxRequestLimit = 1000 // At most the server side constraint

const (
	// Common flags
	FlagSelector      = "selector"
	FlagFieldSelector = "field-selector"
	FlagOutput        = "output"
	FlagLimit         = "limit"
	FlagContinue      = "continue"

	// Resource specific flags
	FlagFleetName   = "fleetname"    // for templateversions
	FlagCatalogName = "catalog"      // for catalogitems
	FlagRendered    = "rendered"     // for a single device
	FlagSummary     = "summary"      // for listing devices and fleets
	FlagSummaryOnly = "summary-only" // for listing devices
	FlagLastSeen    = "last-seen"    // for a single device
	FlagWithExports = "with-exports" // for imagebuilds
)

type FlagContextualRule struct {
	FlagName     string
	ResourceKind []ResourceKind
	Operations   []string // "list", "single", "any"
}

type GetOptions struct {
	GlobalOptions

	LabelSelector string
	FieldSelector string
	Output        string
	Limit         int32
	Continue      string
	FleetName     string
	CatalogName   string
	Rendered      bool
	Summary       bool
	SummaryOnly   bool
	LastSeen      bool
	WithExports   bool
}

func DefaultGetOptions() *GetOptions {
	return &GetOptions{
		GlobalOptions: DefaultGlobalOptions(),
		LabelSelector: "",
		FieldSelector: "",
		Limit:         0,
		Continue:      "",
		FleetName:     "",
		CatalogName:   "",
		Rendered:      false,
		LastSeen:      false,
		WithExports:   false,
	}
}

func NewCmdGet() *cobra.Command {
	o := DefaultGetOptions()
	cmd := &cobra.Command{
		Use:   "get (TYPE | TYPE/NAME | TYPE NAME [NAME ...])",
		Short: "Display one or many resources.",
		Args:  cobra.MinimumNArgs(1),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: true,
			AllowedKinds:       validResourceKinds,
			FleetName:          &o.FleetName,
		}.ValidArgsFunction,
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

	// Override help function to show conditional flags based on command line args
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		o.showHelpContextualFlags(cmd)
		cmd.Parent().HelpFunc()(cmd, args)
	})

	return cmd
}

func (o *GetOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVarP(&o.LabelSelector, FlagSelector, "l", o.LabelSelector, "Selector (label query) to filter on, supporting operators like '=', '!=', and 'in' (e.g., -l='key1=value1,key2!=value2,key3 in (value3, value4)').")
	fs.StringVar(&o.FieldSelector, FlagFieldSelector, o.FieldSelector, "Selector (field query) to filter on, supporting operators like '=', '!=', 'in', 'contains', '>', '<', etc. (e.g., --field-selector='metadata.name in (device1,device2)', --field-selector='metadata.owner=Fleet/test').")
	fs.StringVarP(&o.Output, FlagOutput, "o", o.Output, fmt.Sprintf("Output format. One of: (%s).", strings.Join(legalOutputTypes, ", ")))
	fs.Int32Var(&o.Limit, FlagLimit, o.Limit, "The maximum number of results returned in the list response. If the value is 0, then the result is not limited.")
	fs.StringVar(&o.Continue, FlagContinue, o.Continue, "Query more results starting from the value of the 'continue' field in the previous response.")
	fs.StringVar(&o.FleetName, FlagFleetName, o.FleetName, "Fleet name for accessing templateversions (use only when getting templateversions).")
	fs.StringVar(&o.CatalogName, FlagCatalogName, o.CatalogName, "Catalog name for accessing catalogitems (use only when getting catalogitems).")
	fs.BoolVar(&o.Rendered, FlagRendered, false, "Return the rendered device configuration that is presented to the device. Default output format is YAML.")
	fs.BoolVarP(&o.Summary, FlagSummary, "s", false, "Display summary information.")
	fs.BoolVar(&o.SummaryOnly, FlagSummaryOnly, false, "Display summary information only.")
	fs.BoolVar(&o.LastSeen, FlagLastSeen, false, "Display the last seen timestamp of the device.")
	fs.BoolVar(&o.WithExports, FlagWithExports, false, "Include related ImageExport resources when getting imagebuilds.")
	o.hideHelpContextualFlags(fs)
}

var flagContextualRules = []FlagContextualRule{
	{FlagSummaryOnly, []ResourceKind{DeviceKind}, []string{"list"}},
	{FlagSummary, []ResourceKind{DeviceKind, FleetKind}, []string{"list"}},
	{FlagRendered, []ResourceKind{DeviceKind}, []string{"single"}},
	{FlagLastSeen, []ResourceKind{DeviceKind}, []string{"single"}},
	{FlagFleetName, []ResourceKind{TemplateVersionKind}, []string{"any"}},
	{FlagCatalogName, []ResourceKind{CatalogItemKind}, []string{"any"}},
}

func (o *GetOptions) hideHelpContextualFlags(fs *pflag.FlagSet) {
	for _, rule := range flagContextualRules {
		flag := fs.Lookup(rule.FlagName)
		if flag != nil {
			flag.Hidden = true
		}
	}
}

func (o *GetOptions) showHelpContextualFlags(cmd *cobra.Command) {
	// Extract resource arguments from os.Args after "get" command
	resourceArgs := o.extractResourceArgsFromCmdLine(os.Args)
	if len(resourceArgs) == 0 {
		return
	}
	kind, names, err := parseAndValidateKindNameFromArgs(resourceArgs)
	if err != nil {
		return
	}

	operation := "list"
	if len(names) == 1 {
		operation = "single"
	}
	for _, rule := range flagContextualRules {
		if o.shouldShowHelpFlag(rule, kind, operation) {
			if flag := cmd.Flags().Lookup(rule.FlagName); flag != nil {
				flag.Hidden = false
			}
		}
	}
}

func (o *GetOptions) extractResourceArgsFromCmdLine(args []string) []string {
	for i, arg := range args {
		if arg == "get" && i+1 < len(args) {
			// Parse the arguments after "get" and get just the resource arguments
			remainingArgs := args[i+1:]
			var resourceArgs []string
			for _, a := range remainingArgs {
				if strings.HasPrefix(a, "-") {
					// Collect arguments until we hit the first flag
					break
				}
				resourceArgs = append(resourceArgs, a)
			}
			return resourceArgs
		}
	}
	return nil
}

func (o *GetOptions) shouldShowHelpFlag(rule FlagContextualRule, resourceKind ResourceKind, operation string) bool {
	if !slices.Contains(rule.ResourceKind, resourceKind) {
		return false
	}
	for _, op := range rule.Operations {
		if op == "any" || op == operation {
			return true
		}
	}
	return false
}

func (o *GetOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	// --rendered flag defaults to YAML output when no explicit output format is specified
	if o.Rendered && o.Output == "" {
		o.Output = string(display.YAMLFormat)
	}

	return nil
}

func (o *GetOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, names, err := parseAndValidateKindNameFromArgs(args)
	if err != nil {
		return err
	}

	// Validate all resource names
	for _, resName := range names {
		if errs := validation.ValidateResourceName(&resName); len(errs) > 0 {
			return fmt.Errorf("invalid resource name: %s", errors.Join(errs...).Error())
		}
	}

	validators := []func() error{
		func() error { return o.validateSelectors(names) },
		func() error { return o.validateSummary(kind, names) },
		func() error { return o.validateSummaryOnly(kind, names) },
		func() error { return o.validateTemplateVersion(kind) },
		func() error { return o.validateCatalogItem(kind) },
		func() error { return o.validateOutputFormat() },
		func() error { return o.validateRendered(kind, names) },
		func() error { return o.validateSingleResourceRestrictions(kind, names) },
		func() error { return o.validateLimit() },
		func() error { return o.validateLastSeen(kind, names) },
		func() error { return o.validateWithExports(kind) },
	}

	for _, v := range validators {
		if err := v(); err != nil {
			return err
		}
	}
	return nil
}

// validateSelectors ensures label/field selectors are not provided when requesting specific resources.
func (o *GetOptions) validateSelectors(names []string) error {
	hasSpecificResources := len(names) > 0
	if hasSpecificResources && len(o.LabelSelector) > 0 {
		return fmt.Errorf("cannot specify label selector when getting specific resources")
	}
	if hasSpecificResources && len(o.FieldSelector) > 0 {
		return fmt.Errorf("cannot specify field selector when getting specific resources")
	}
	return nil
}

// validateSummary validates the usage of the --summary flag.
func (o *GetOptions) validateSummary(kind ResourceKind, names []string) error {
	if !o.Summary {
		return nil
	}
	if kind != DeviceKind && kind != FleetKind {
		return fmt.Errorf("'--summary' can only be specified when getting a list of devices or fleets")
	}
	if kind == DeviceKind && len(names) > 0 {
		return fmt.Errorf("cannot specify '--summary' when getting specific devices")
	}
	return nil
}

// validateSummaryOnly validates the usage of the --summary-only flag.
func (o *GetOptions) validateSummaryOnly(kind ResourceKind, names []string) error {
	if !o.SummaryOnly {
		return nil
	}
	if kind != DeviceKind {
		return fmt.Errorf("'--summary-only' can only be specified when getting a list of devices")
	}
	if len(names) > 0 {
		return fmt.Errorf("cannot specify '--summary-only' when getting specific devices")
	}
	if o.Limit > 0 || len(o.Continue) > 0 {
		return fmt.Errorf("flags '--limit' and '--continue' are not supported when '--summary-only' is specified")
	}
	return nil
}

// validateTemplateVersion ensures a fleet name is provided when listing templateversions.
func (o *GetOptions) validateTemplateVersion(kind ResourceKind) error {
	if kind == TemplateVersionKind && len(o.FleetName) == 0 {
		return fmt.Errorf("a fleet name must be specified when getting a list of templateversions")
	}
	return nil
}

// validateCatalogItem ensures a catalog name is provided when listing catalogitems.
func (o *GetOptions) validateCatalogItem(kind ResourceKind) error {
	if kind == CatalogItemKind && len(o.CatalogName) == 0 {
		return fmt.Errorf("a catalog name must be specified when getting catalogitems (use --catalog)")
	}
	return nil
}

// validateOutputFormat checks that the requested output format is recognised.
func (o *GetOptions) validateOutputFormat() error {
	if len(o.Output) > 0 && !slices.Contains(legalOutputTypes, o.Output) {
		return fmt.Errorf("output format must be one of (%s)", strings.Join(legalOutputTypes, ", "))
	}
	return nil
}

// validateRendered guards the --rendered flag usage.
func (o *GetOptions) validateRendered(kind ResourceKind, names []string) error {
	if o.Rendered && (kind != DeviceKind || len(names) != 1) {
		return fmt.Errorf("'--rendered' can only be used when getting a single device")
	}
	return nil
}

// validateSingleResourceRestrictions covers kinds that cannot be fetched individually.
func (o *GetOptions) validateSingleResourceRestrictions(kind ResourceKind, names []string) error {
	if len(names) == 0 {
		return nil // list request – no restriction applies
	}
	switch kind {
	case EventKind:
		return fmt.Errorf("you cannot get individual events")
	case OrganizationKind:
		return fmt.Errorf("you cannot get individual organizations")
	default:
		return nil
	}
}

// validateLimit checks limit-related constraints.
func (o *GetOptions) validateLimit() error {
	if o.Limit < 0 {
		return fmt.Errorf("limit must be greater than 0")
	}
	if o.Limit > maxRequestLimit && len(o.Output) > 0 {
		return fmt.Errorf("limit higher than %d is only supported when using table format", maxRequestLimit)
	}
	return nil
}

// validateLastSeen checks the usage of the --last-seen flag.
func (o *GetOptions) validateLastSeen(kind ResourceKind, names []string) error {
	if o.LastSeen && (kind != DeviceKind || len(names) != 1) {
		return fmt.Errorf("'--last-seen' can only be used when getting a single device")
	}
	if o.LastSeen && (o.Rendered || o.Summary || o.SummaryOnly) {
		return fmt.Errorf("'--last-seen' cannot be combined with '--rendered', '--summary', or '--summary-only'")
	}
	// Name output requires metadata.name which DeviceLastSeen does not provide.
	if o.LastSeen && o.Output == string(display.NameFormat) {
		return fmt.Errorf("'--last-seen' does not support '-o name'")
	}
	return nil
}

// validateWithExports checks the usage of the --with-exports flag.
func (o *GetOptions) validateWithExports(kind ResourceKind) error {
	if o.WithExports && kind != ImageBuildKind {
		return fmt.Errorf("'--with-exports' can only be specified when getting imagebuilds")
	}
	return nil
}

func (o *GetOptions) Run(ctx context.Context, args []string) error {
	kind, names, err := parseAndValidateKindNameFromArgs(args)
	if err != nil {
		return err
	}

	formatter := display.NewFormatter(display.OutputFormat(o.Output))

	// Create resource fetchers based on kind
	listFetcher, singleFetcher, stopFn, err := o.createFetchers(ctx, kind)
	if err != nil {
		return err
	}
	if stopFn != nil {
		defer stopFn()
	}

	// Handle list case (no specific names)
	if len(names) == 0 {
		if err := o.handleList(ctx, formatter, kind, listFetcher); err != nil {
			return fmt.Errorf("listing %s: %w", kind.ToPlural(), err)
		}
		return nil
	}

	// Handle single or multiple names case
	if len(names) == 1 {
		return o.handleSingle(ctx, formatter, kind, names[0], singleFetcher)
	}
	return o.handleMultiple(ctx, formatter, kind, names, listFetcher)
}

// ListFetcher fetches a list of resources and returns the validated response.
type ListFetcher func() (interface{}, error)

// SingleFetcher fetches a single resource by name and returns the validated response.
type SingleFetcher func(name string) (interface{}, error)

// createFetchers returns the appropriate list and single fetchers based on the resource kind.
func (o *GetOptions) createFetchers(ctx context.Context, kind ResourceKind) (ListFetcher, SingleFetcher, func(), error) {
	if kind == ImageBuildKind || kind == ImageExportKind {
		return o.createImageBuilderFetchers(ctx, kind)
	}
	return o.createMainAPIFetchers(ctx, kind)
}

func (o *GetOptions) createMainAPIFetchers(ctx context.Context, kind ResourceKind) (ListFetcher, SingleFetcher, func(), error) {
	c, err := o.BuildClient()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating client: %w", err)
	}
	c.Start(ctx)

	listFetcher := func() (interface{}, error) {
		response, err := o.getResourceList(ctx, c, kind)
		if err != nil {
			return nil, err
		}
		if err := validateResponse(response); err != nil {
			return nil, err
		}
		return response, nil
	}

	singleFetcher := func(name string) (interface{}, error) {
		response, err := o.getSingleResource(ctx, c, kind, name)
		if err != nil {
			return nil, err
		}
		if err := validateResponse(response); err != nil {
			return nil, err
		}
		return response, nil
	}

	return listFetcher, singleFetcher, c.Stop, nil
}

func (o *GetOptions) createImageBuilderFetchers(ctx context.Context, kind ResourceKind) (ListFetcher, SingleFetcher, func(), error) {
	var c *client.ImageBuilderClient
	c, err := o.BuildImageBuilderClient()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating imagebuilder client: %w", err)
	}
	c.Start(ctx)

	var listFetcher ListFetcher
	var singleFetcher SingleFetcher

	switch kind {
	case ImageBuildKind:
		listFetcher = func() (interface{}, error) {
			params := &imagebuilderapi.ListImageBuildsParams{
				LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
				FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
				Limit:         util.ToPtrWithNilDefault(o.Limit),
				Continue:      util.ToPtrWithNilDefault(o.Continue),
				WithExports:   lo.ToPtr(o.WithExports),
			}
			response, err := c.ListImageBuildsWithResponse(ctx, params)
			if err != nil {
				return nil, err
			}
			if err := validateImageBuilderResponse(response); err != nil {
				return nil, err
			}
			return response, nil
		}
		singleFetcher = func(name string) (interface{}, error) {
			response, err := c.GetImageBuildWithResponse(ctx, name, &imagebuilderapi.GetImageBuildParams{
				WithExports: lo.ToPtr(o.WithExports),
			})
			if err != nil {
				return nil, err
			}
			if err := validateImageBuilderResponse(response); err != nil {
				return nil, err
			}
			return response, nil
		}
	case ImageExportKind:
		listFetcher = func() (interface{}, error) {
			params := &imagebuilderapi.ListImageExportsParams{
				LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
				FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
				Limit:         util.ToPtrWithNilDefault(o.Limit),
				Continue:      util.ToPtrWithNilDefault(o.Continue),
			}
			response, err := c.ListImageExportsWithResponse(ctx, params)
			if err != nil {
				return nil, err
			}
			if err := validateImageBuilderResponse(response); err != nil {
				return nil, err
			}
			return response, nil
		}
		singleFetcher = func(name string) (interface{}, error) {
			response, err := c.GetImageExportWithResponse(ctx, name)
			if err != nil {
				return nil, err
			}
			if err := validateImageBuilderResponse(response); err != nil {
				return nil, err
			}
			return response, nil
		}
	default:
		return nil, nil, nil, fmt.Errorf("unsupported image builder kind: %s", kind)
	}

	return listFetcher, singleFetcher, c.Stop, nil
}

// handleSingle fetches and displays a single resource.
func (o *GetOptions) handleSingle(ctx context.Context, formatter display.OutputFormatter, kind ResourceKind, name string, fetcher SingleFetcher) error {
	response, err := fetcher(name)
	if err != nil {
		return err
	}
	return o.displayResponse(formatter, response, kind, name)
}

// handleMultiple fetches and displays multiple resources using field selector with IN operator.
func (o *GetOptions) handleMultiple(ctx context.Context, formatter display.OutputFormatter, kind ResourceKind, names []string, listFetcher ListFetcher) error {
	// Construct field selector: metadata.name in (name1,name2,name3)
	fieldSelector := fmt.Sprintf("metadata.name in (%s)", strings.Join(names, ","))

	// Temporarily set field selector and use list functionality
	originalFieldSelector := o.FieldSelector
	o.FieldSelector = fieldSelector
	defer func() { o.FieldSelector = originalFieldSelector }()

	return o.handleList(ctx, formatter, kind, listFetcher)
}

func (o *GetOptions) getSingleResource(ctx context.Context, c *client.Client, kind ResourceKind, name string) (interface{}, error) {
	switch kind {
	case DeviceKind:
		if o.LastSeen {
			return GetLastSeenDevice(ctx, c, name)
		}
		if o.Rendered {
			return GetRenderedDevice(ctx, c, name)
		}
		return GetSingleResource(ctx, c, kind, name)
	case FleetKind:
		// FleetKind needs special handling for AddDevicesSummary parameter
		params := api.GetFleetParams{
			AddDevicesSummary: util.ToPtrWithNilDefault(o.Summary),
		}
		return c.GetFleetWithResponse(ctx, name, &params)
	case TemplateVersionKind:
		return GetTemplateVersion(ctx, c, o.FleetName, name)
	case CatalogKind:
		return c.V1Alpha1().GetCatalogWithResponse(ctx, name)
	case CatalogItemKind:
		return nil, fmt.Errorf("catalogitems cannot be retrieved individually; use 'get catalogitems --catalog <name>' to list items")
	default:
		return GetSingleResource(ctx, c, kind, name)
	}
}

func (o *GetOptions) handleList(
	ctx context.Context,
	formatter display.OutputFormatter,
	kind ResourceKind,
	fetcher ListFetcher,
) error {
	// Device summary requires special handling after batching
	var postBatchFn func() error
	if o.Summary && kind == DeviceKind {
		originalSummary := o.Summary
		o.Summary = false // Disable summary during batching
		postBatchFn = func() error {
			o.Continue = ""
			o.Limit = 0
			o.SummaryOnly = true
			o.Summary = originalSummary
			response, err := fetcher()
			if err != nil {
				return err
			}
			return o.displayResponse(formatter, response, kind, "")
		}
	}

	return o.handleListWithFetcher(formatter, kind, fetcher, postBatchFn)
}

// handleListWithFetcher handles list operations with batching support using a generic fetcher.
// The fetcher function is responsible for making the API call and validating the response.
// The optional postBatchFn is called after batching completes (used for device summary).
func (o *GetOptions) handleListWithFetcher(
	formatter display.OutputFormatter,
	kind ResourceKind,
	fetcher ListFetcher,
	postBatchFn func() error,
) error {
	// Batching is only supported for table output
	isTableOutput := o.Output == ""
	requestedLimit := o.Limit
	needsBatching := isTableOutput &&
		(requestedLimit == 0 || requestedLimit > maxRequestLimit)

	if !needsBatching {
		response, err := fetcher()
		if err != nil {
			return err
		}
		return o.displayResponse(formatter, response, kind, "")
	}

	var printedCount int32 = 0
	o.Limit = 0 // Request server-side maximum (0 == capped)

	for {
		remaining := requestedLimit - printedCount
		if requestedLimit > 0 && remaining <= maxRequestLimit {
			// Ask for exactly the remaining items - the final iteration
			o.Limit = remaining
		}

		response, err := fetcher()
		if err != nil {
			return err
		}

		if err := o.displayResponse(formatter, response, kind, ""); err != nil {
			return err
		}

		// Extract list metadata and item count from the response
		listMetadata, listItemsCount, err := getListMetadata(response)
		if err != nil {
			return fmt.Errorf("reading list metadata: %w", err)
		}

		printedCount += int32(listItemsCount) // #nosec G115

		if requestedLimit > 0 && printedCount >= requestedLimit {
			break // Reached user-requested limit
		}

		if listMetadata.Continue == nil {
			break // No more batches
		}

		o.Continue = *listMetadata.Continue
	}

	if postBatchFn != nil {
		return postBatchFn()
	}

	return nil
}

func (o *GetOptions) getResourceList(ctx context.Context, c *client.Client, kind ResourceKind) (interface{}, error) {
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
	case OrganizationKind:
		params := api.ListOrganizationsParams{
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
		}
		return c.ListOrganizationsWithResponse(ctx, &params)
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
	case AuthProviderKind:
		params := api.ListAuthProvidersParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		return c.ListAuthProvidersWithResponse(ctx, &params)
	case CatalogKind:
		params := apiv1alpha1.ListCatalogsParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			FieldSelector: util.ToPtrWithNilDefault(o.FieldSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		return c.V1Alpha1().ListCatalogsWithResponse(ctx, &params)
	case CatalogItemKind:
		params := apiv1alpha1.ListCatalogItemsParams{
			LabelSelector: util.ToPtrWithNilDefault(o.LabelSelector),
			Limit:         util.ToPtrWithNilDefault(o.Limit),
			Continue:      util.ToPtrWithNilDefault(o.Continue),
		}
		return c.V1Alpha1().ListCatalogItemsWithResponse(ctx, o.CatalogName, &params)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
}

func (o *GetOptions) displayResponse(formatter display.OutputFormatter, response interface{}, kind ResourceKind, name string) error {
	options := display.FormatOptions{
		Kind:        kind.String(),
		Name:        name,
		Summary:     o.Summary,
		SummaryOnly: o.SummaryOnly,
		Wide:        o.Output == string(display.WideFormat),
		WithExports: o.WithExports,
		Writer:      os.Stdout,
	}

	// For structured formats, use JSON200 data
	if o.Output == string(display.JSONFormat) || o.Output == string(display.YAMLFormat) || o.Output == string(display.NameFormat) {
		json200, err := ExtractJSON200(response)
		if err != nil {
			return err
		}

		// Handle 204 No Content responses with JSON/YAML outputs
		if json200 == nil {
			// 204 No Content: print nothing and succeed
			return nil
		}

		return formatter.Format(json200, options)
	}

	// For table format, pass the full response
	return formatter.Format(response, options)
}

// listMeta contains pagination metadata extracted from list responses.
// It's a generic version that works with both api.ListMeta and imagebuilderapi.ListMeta.
type listMeta struct {
	Continue *string
}

// getListMetadata extracts the ListMeta and the number of items from a list response.
// It works with any list response type that has Metadata.Continue and Items fields.
func getListMetadata(response interface{}) (*listMeta, int, error) {
	json200, err := ExtractJSON200(response)
	if err != nil {
		return nil, 0, err
	}

	v := reflect.ValueOf(json200)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, 0, fmt.Errorf("JSON200 pointer is nil")
		}
		v = v.Elem()
	}

	// Extract Continue from Metadata field using reflection
	metadataField := v.FieldByName("Metadata")
	if !metadataField.IsValid() {
		return nil, 0, fmt.Errorf("metadata field not found in response")
	}

	var continueToken *string
	continueField := metadataField.FieldByName("Continue")
	if continueField.IsValid() && !continueField.IsNil() {
		val := continueField.Elem().String()
		continueToken = &val
	}

	// Determine number of items
	itemsField := v.FieldByName("Items")
	if !itemsField.IsValid() || itemsField.Kind() != reflect.Slice {
		return nil, 0, fmt.Errorf("items field not found or not a slice")
	}

	return &listMeta{Continue: continueToken}, itemsField.Len(), nil
}
