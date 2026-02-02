package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/org"
)

const (
	NoneString = "<none>"
)

type ResourceKind string

const (
	InvalidKind                   ResourceKind = ""
	CatalogKind                   ResourceKind = "catalog"
	CatalogItemKind               ResourceKind = "catalogitem"
	CertificateSigningRequestKind ResourceKind = "certificatesigningrequest"
	DeviceKind                    ResourceKind = "device"
	EnrollmentRequestKind         ResourceKind = "enrollmentrequest"
	EventKind                     ResourceKind = "event"
	AuthProviderKind              ResourceKind = "authprovider"
	FleetKind                     ResourceKind = "fleet"
	ImageBuildKind                ResourceKind = "imagebuild"
	ImageExportKind               ResourceKind = "imageexport"
	OrganizationKind              ResourceKind = "organization"
	RepositoryKind                ResourceKind = "repository"
	ResourceSyncKind              ResourceKind = "resourcesync"
	TemplateVersionKind           ResourceKind = "templateversion"
)

func (r ResourceKind) String() string {
	return string(r)
}

func (r ResourceKind) ToPlural() string {
	return kindToPlural[r]
}

func ResourceKindFromString(kindLike string) (ResourceKind, error) {
	kindLike = strings.ToLower(kindLike)
	if _, ok := resourceKindSet[ResourceKind(kindLike)]; ok {
		return ResourceKind(kindLike), nil
	}
	if kind, ok := pluralToKind[kindLike]; ok {
		return kind, nil
	}
	if kind, ok := shortnameToKind[kindLike]; ok {
		return kind, nil
	}
	return InvalidKind, fmt.Errorf("invalid resource kind: %s", kindLike)
}

var (
	resourceKindSet = map[ResourceKind]struct{}{
		CatalogKind:                   {},
		CatalogItemKind:               {},
		CertificateSigningRequestKind: {},
		DeviceKind:                    {},
		EnrollmentRequestKind:         {},
		EventKind:                     {},
		AuthProviderKind:              {},
		FleetKind:                     {},
		ImageBuildKind:                {},
		ImageExportKind:               {},
		OrganizationKind:              {},
		RepositoryKind:                {},
		ResourceSyncKind:              {},
		TemplateVersionKind:           {},
	}

	validResourceKinds = slices.Collect(maps.Keys(resourceKindSet))

	pluralToKind = map[string]ResourceKind{
		"catalogs":                   CatalogKind,
		"catalogitems":               CatalogItemKind,
		"certificatesigningrequests": CertificateSigningRequestKind,
		"devices":                    DeviceKind,
		"enrollmentrequests":         EnrollmentRequestKind,
		"events":                     EventKind,
		"authproviders":              AuthProviderKind,
		"fleets":                     FleetKind,
		"imagebuilds":                ImageBuildKind,
		"imageexports":               ImageExportKind,
		"organizations":              OrganizationKind,
		"repositories":               RepositoryKind,
		"resourcesyncs":              ResourceSyncKind,
		"templateversions":           TemplateVersionKind,
	}

	kindToPlural = map[ResourceKind]string{
		CatalogKind:                   "catalogs",
		CatalogItemKind:               "catalogitems",
		CertificateSigningRequestKind: "certificatesigningrequests",
		DeviceKind:                    "devices",
		EnrollmentRequestKind:         "enrollmentrequests",
		EventKind:                     "events",
		AuthProviderKind:              "authproviders",
		FleetKind:                     "fleets",
		ImageBuildKind:                "imagebuilds",
		ImageExportKind:               "imageexports",
		OrganizationKind:              "organizations",
		RepositoryKind:                "repositories",
		ResourceSyncKind:              "resourcesyncs",
		TemplateVersionKind:           "templateversions",
	}

	shortnameToKind = map[string]ResourceKind{
		"cat":  CatalogKind,
		"ci":   CatalogItemKind,
		"csr":  CertificateSigningRequestKind,
		"dev":  DeviceKind,
		"er":   EnrollmentRequestKind,
		"ev":   EventKind,
		"ap":   AuthProviderKind,
		"flt":  FleetKind,
		"ib":   ImageBuildKind,
		"ie":   ImageExportKind,
		"org":  OrganizationKind,
		"repo": RepositoryKind,
		"rs":   ResourceSyncKind,
		"tv":   TemplateVersionKind,
	}
)

func getValidPluralResourceKinds() []string {
	resourceKinds := make([]string, len(pluralToKind))
	i := 0
	for v := range pluralToKind {
		resourceKinds[i] = v
		i++
	}
	return resourceKinds
}

func parseAndValidateKindName(arg string) (ResourceKind, string, error) {
	kindLike, name, _ := strings.Cut(arg, "/")
	kind, err := ResourceKindFromString(kindLike)
	if err != nil {
		return "", "", err
	}
	return kind, name, nil
}

// parseAndValidateKindNameFromArgs handles both "kind/name" and "kind name [name ...]" formats
func parseAndValidateKindNameFromArgs(args []string) (ResourceKind, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("no arguments provided")
	}

	// Check if first argument contains a slash (TYPE/NAME format)
	if strings.Contains(args[0], "/") {
		if len(args) > 1 {
			return "", nil, fmt.Errorf("cannot mix TYPE/NAME syntax with additional arguments. Use either 'get TYPE/NAME' or 'get TYPE NAME [NAME ...]'")
		}
		kind, name, err := parseAndValidateKindName(args[0])
		if err != nil {
			return "", nil, err
		}
		// Validate that name is not empty when using slash format
		if name == "" {
			return "", nil, fmt.Errorf("resource name cannot be empty when using TYPE/NAME format")
		}
		var names []string
		if name != "" {
			names = []string{name}
		}
		return kind, names, nil
	}

	// Handle TYPE NAME [NAME ...] format
	kindLike := args[0]
	kind, err := ResourceKindFromString(kindLike)
	if err != nil {
		return "", nil, err
	}

	var names []string
	if len(args) > 1 {
		names = args[1:]
	}

	return kind, names, nil
}

// parseAndValidateKindNameFromArgsOptionalSingle handles "kind", "kind/name" and "kind name" formats
// but only allows at most a single resource name (not multiple)
func parseAndValidateKindNameFromArgsOptionalSingle(args []string) (ResourceKind, string, error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("no arguments provided")
	}
	if len(args) > 2 {
		return "", "", errors.New("resource must be specified in TYPE/NAME, or TYPE NAME format")
	}

	// Check if first argument contains a slash (TYPE/NAME format)
	if strings.Contains(args[0], "/") {
		if len(args) > 1 {
			return "", "", fmt.Errorf("cannot mix TYPE/NAME syntax with additional arguments. Use either 'TYPE/NAME' or 'TYPE NAME'")
		}
		kind, name, err := parseAndValidateKindName(args[0])
		if err != nil {
			return "", "", err
		}
		// Validate that name is not empty when using slash format
		if name == "" {
			return "", "", fmt.Errorf("resource name cannot be empty when using TYPE/NAME format")
		}
		return kind, name, nil
	}

	kindLike := args[0]
	kind, err := ResourceKindFromString(kindLike)
	if err != nil {
		return "", "", err
	}

	name := ""
	// Handle TYPE NAME format (single name only)
	if len(args) == 2 {
		name = args[1]
	}

	return kind, name, nil
}

// parseAndValidateKindNameFromArgsSingle handles both "kind/name" and "kind name" formats
// but only allows a single resource name (not multiple)
func parseAndValidateKindNameFromArgsSingle(args []string) (ResourceKind, string, error) {
	if len(args) > 2 {
		return "", "", fmt.Errorf("exactly one resource name must be specified")
	}
	kind, name, err := parseAndValidateKindNameFromArgsOptionalSingle(args)
	if err != nil {
		return "", "", err
	}
	if len(name) == 0 {
		return "", "", fmt.Errorf("exactly one resource name must be specified. Use 'TYPE NAME' format")
	}
	return kind, name, nil
}

func validateHttpResponse(responseBody []byte, statusCode int, expectedStatusCode int) error {
	if statusCode != expectedStatusCode {
		var responseError api.Status
		err := json.Unmarshal(responseBody, &responseError)
		if err != nil {
			return fmt.Errorf("%d %s", statusCode, string(responseBody))
		}
		return fmt.Errorf("%d %s", statusCode, responseError.Message)
	}
	return nil
}

func validateOrganizationID(orgID string) error {
	if _, err := org.Parse(orgID); err != nil {
		return err
	}
	return nil
}

// responseField extracts a field from a response struct by name and returns it as the specified type T.
// The function performs a series of checks to ensure the validity and type-safety of the operation.
// If any of these checks fail, it returns an appropriate error message.
func responseField[T any](response interface{}, name string) (T, error) {
	var zero T

	v := reflect.ValueOf(response)

	if !v.IsValid() {
		return zero, fmt.Errorf("response is invalid")
	}

	if v.Kind() != reflect.Ptr {
		return zero, fmt.Errorf("response must be a pointer to a struct, got: %T", response)
	}

	if v.IsNil() {
		return zero, fmt.Errorf("response pointer is nil")
	}

	v = v.Elem()

	if v.Kind() != reflect.Struct {
		return zero, fmt.Errorf("expected a struct, got: %T", v.Interface())
	}

	field := v.FieldByName(name)
	if !field.IsValid() {
		return zero, fmt.Errorf("field %q does not exist in struct: %T", name, response)
	}

	if !field.CanInterface() {
		return zero, fmt.Errorf("field %q cannot be interfaced", name)
	}

	fieldValue, ok := field.Interface().(T)
	if !ok {
		return zero, fmt.Errorf("field %q is not of type %T, got: %T", name, zero, field.Interface())
	}

	return fieldValue, nil
}

// GetSingleResource fetches a single resource by kind and name.
func GetSingleResource(ctx context.Context, c *client.Client, kind ResourceKind, name string) (interface{}, error) {
	switch kind {
	case DeviceKind:
		return c.GetDeviceWithResponse(ctx, name)
	case EnrollmentRequestKind:
		return c.GetEnrollmentRequestWithResponse(ctx, name)
	case FleetKind:
		params := api.GetFleetParams{}
		return c.GetFleetWithResponse(ctx, name, &params)
	case RepositoryKind:
		return c.GetRepositoryWithResponse(ctx, name)
	case ResourceSyncKind:
		return c.GetResourceSyncWithResponse(ctx, name)
	case CertificateSigningRequestKind:
		return c.GetCertificateSigningRequestWithResponse(ctx, name)
	case AuthProviderKind:
		return c.GetAuthProviderWithResponse(ctx, name)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
}

// GetRenderedDevice fetches a rendered device configuration.
func GetRenderedDevice(ctx context.Context, c *client.Client, name string) (interface{}, error) {
	return c.GetRenderedDeviceWithResponse(ctx, name, &api.GetRenderedDeviceParams{})
}

// GetLastSeenDevice fetches the last seen timestamp for a device.
func GetLastSeenDevice(ctx context.Context, c *client.Client, name string) (interface{}, error) {
	return c.GetDeviceLastSeenWithResponse(ctx, name)
}

// GetTemplateVersion fetches a template version with the specified fleet name.
func GetTemplateVersion(ctx context.Context, c *client.Client, fleetName, name string) (interface{}, error) {
	return c.GetTemplateVersionWithResponse(ctx, fleetName, name)
}

// ExtractJSON200 extracts the JSON200 data from a response after validating it.
// This function centralizes the common pattern of validating and extracting JSON200 data.
func ExtractJSON200(response interface{}) (interface{}, error) {
	// Validate the response
	if err := validateResponse(response); err != nil {
		return nil, err
	}

	// Check if this is a 204 response (no content)
	httpResponse, err := responseField[*http.Response](response, "HTTPResponse")
	if err != nil {
		return nil, err
	}

	if httpResponse.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	// Extract JSON200 data
	json200, err := responseField[interface{}](response, "JSON200")
	if err != nil {
		return nil, err
	}

	return json200, nil
}

// validateResponse validates an HTTP response and returns an error if the status is not OK.
func validateResponse(response interface{}) error {
	httpResponse, err := responseField[*http.Response](response, "HTTPResponse")
	if err != nil {
		return err
	}

	responseBody, err := responseField[[]byte](response, "Body")
	if err != nil {
		return err
	}

	if httpResponse.StatusCode != http.StatusOK && httpResponse.StatusCode != http.StatusNoContent {
		if strings.Contains(httpResponse.Header.Get("Content-Type"), "json") {
			var dest api.Status
			if err := json.Unmarshal(responseBody, &dest); err != nil {
				return fmt.Errorf("unmarshalling error: %w", err)
			}
			return fmt.Errorf("response status: %d, message: %s", httpResponse.StatusCode, dest.Message)
		}
		return fmt.Errorf("response status: %d", httpResponse.StatusCode)
	}
	return nil
}

// validateImageBuilderResponse validates an imagebuilder HTTP response and returns an error if the status is not OK.
func validateImageBuilderResponse(response interface{}) error {
	httpResponse, err := responseField[*http.Response](response, "HTTPResponse")
	if err != nil {
		return err
	}

	responseBody, err := responseField[[]byte](response, "Body")
	if err != nil {
		return err
	}

	if httpResponse.StatusCode != http.StatusOK && httpResponse.StatusCode != http.StatusCreated && httpResponse.StatusCode != http.StatusNoContent {
		if strings.Contains(httpResponse.Header.Get("Content-Type"), "json") {
			var dest api.Status
			if err := json.Unmarshal(responseBody, &dest); err != nil {
				return fmt.Errorf("unmarshalling error: %w", err)
			}
			return fmt.Errorf("response status: %d, message: %s", httpResponse.StatusCode, dest.Message)
		}
		return fmt.Errorf("response status: %d", httpResponse.StatusCode)
	}
	return nil
}
