package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/org"
)

const (
	NoneString = "<none>"
)

const (
	CertificateSigningRequestKind = "certificatesigningrequest"
	DeviceKind                    = "device"
	EnrollmentRequestKind         = "enrollmentrequest"
	EventKind                     = "event"
	FleetKind                     = "fleet"
	OrganizationKind              = "organization"
	RepositoryKind                = "repository"
	ResourceSyncKind              = "resourcesync"
	TemplateVersionKind           = "templateversion"
)

var (
	pluralKinds = map[string]string{
		CertificateSigningRequestKind: "certificatesigningrequests",
		DeviceKind:                    "devices",
		EnrollmentRequestKind:         "enrollmentrequests",
		EventKind:                     "events",
		FleetKind:                     "fleets",
		OrganizationKind:              "organizations",
		RepositoryKind:                "repositories",
		ResourceSyncKind:              "resourcesyncs",
		TemplateVersionKind:           "templateversions",
	}

	shortnameKinds = map[string]string{
		CertificateSigningRequestKind: "csr",
		DeviceKind:                    "dev",
		EnrollmentRequestKind:         "er",
		EventKind:                     "ev",
		FleetKind:                     "flt",
		OrganizationKind:              "org",
		RepositoryKind:                "repo",
		ResourceSyncKind:              "rs",
		TemplateVersionKind:           "tv",
	}
)

func getValidResourceKinds() []string {
	resourceKinds := make([]string, len(pluralKinds))
	i := 0
	for _, v := range pluralKinds {
		resourceKinds[i] = v
		i++
	}
	return resourceKinds
}

func parseAndValidateKindName(arg string) (string, string, error) {
	kind, name, _ := strings.Cut(arg, "/")
	kind = singular(kind)
	kind = fullname(kind)
	if _, ok := pluralKinds[kind]; !ok {
		return "", "", fmt.Errorf("invalid resource kind: %s", kind)
	}
	return kind, name, nil
}

func singular(kind string) string {
	for singular, plural := range pluralKinds {
		if kind == plural {
			return singular
		}
	}
	return kind
}

func plural(kind string) string {
	return pluralKinds[kind]
}

func fullname(kind string) string {
	for fullname, shortname := range shortnameKinds {
		if kind == shortname {
			return fullname
		}
	}
	return kind
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
// This function centralizes the resource fetching logic used by both get and edit commands.
func GetSingleResource(ctx context.Context, c *apiclient.ClientWithResponses, kind, name string) (interface{}, error) {
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
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
}

// GetRenderedDevice fetches a rendered device configuration.
func GetRenderedDevice(ctx context.Context, c *apiclient.ClientWithResponses, name string) (interface{}, error) {
	return c.GetRenderedDeviceWithResponse(ctx, name, &api.GetRenderedDeviceParams{})
}

// GetLastSeenDevice fetches the last seen timestamp for a device.
func GetLastSeenDevice(ctx context.Context, c *apiclient.ClientWithResponses, name string) (interface{}, error) {
	return c.GetDeviceLastSeenWithResponse(ctx, name)
}

// GetTemplateVersion fetches a template version with the specified fleet name.
func GetTemplateVersion(ctx context.Context, c *apiclient.ClientWithResponses, fleetName, name string) (interface{}, error) {
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
