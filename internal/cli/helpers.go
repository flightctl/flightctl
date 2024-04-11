package cli

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/client"
)

const (
	DeviceKind            = "device"
	EnrollmentRequestKind = "enrollmentrequest"
	FleetKind             = "fleet"
	RepositoryKind        = "repository"
	ResourceSyncKind      = "resourcesync"
	TemplateVersionKind   = "templateversion"
)

var (
	defaultClientConfigFile string
	resourceKinds           = map[string]string{
		DeviceKind:            "devices",
		EnrollmentRequestKind: "enrollmentrequests",
		FleetKind:             "fleets",
		RepositoryKind:        "repositories",
		ResourceSyncKind:      "resourcesyncs",
		TemplateVersionKind:   "templateversions",
	}
)

func init() {
	defaultClientConfigFile = client.DefaultFlightctlClientConfigPath()
}

func parseAndValidateKindName(arg string) (string, string, error) {
	kind, name, _ := strings.Cut(arg, "/")
	kind = singular(kind)
	if _, ok := resourceKinds[kind]; !ok {
		return "", "", fmt.Errorf("invalid resource kind: %s", kind)
	}
	return kind, name, nil
}

func singular(kind string) string {
	for singular, plural := range resourceKinds {
		if kind == plural {
			return singular
		}
	}
	return kind
}

func plural(kind string) string {
	return resourceKinds[kind]
}
