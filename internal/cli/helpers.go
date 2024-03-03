package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"k8s.io/client-go/util/homedir"
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
	defaultClientConfigFile = filepath.Join(homedir.HomeDir(), ".flightctl", "client.yaml")
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
