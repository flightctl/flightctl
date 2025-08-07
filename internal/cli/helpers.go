package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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
