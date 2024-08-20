package cli

import (
	"fmt"
	"strings"
)

const (
	NoneString = "<none>"
)

const (
	DeviceKind                    = "device"
	EnrollmentRequestKind         = "enrollmentrequest"
	FleetKind                     = "fleet"
	RepositoryKind                = "repository"
	ResourceSyncKind              = "resourcesync"
	TemplateVersionKind           = "templateversion"
	CertificateSigningRequestKind = "certificatesigningrequest"
)

var (
	pluralKinds = map[string]string{
		DeviceKind:                    "devices",
		EnrollmentRequestKind:         "enrollmentrequests",
		FleetKind:                     "fleets",
		RepositoryKind:                "repositories",
		ResourceSyncKind:              "resourcesyncs",
		TemplateVersionKind:           "templateversions",
		CertificateSigningRequestKind: "certificatesigningrequests",
	}

	shortnameKinds = map[string]string{
		DeviceKind:                    "dev",
		EnrollmentRequestKind:         "er",
		FleetKind:                     "flt",
		RepositoryKind:                "repo",
		ResourceSyncKind:              "rs",
		TemplateVersionKind:           "tv",
		CertificateSigningRequestKind: "csr",
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
