package cli

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"k8s.io/client-go/util/homedir"
)

var (
	defaultClientConfigFile string
	resourceKinds           = map[string]string{
		"device":            "",
		"enrollmentrequest": "",
		"fleet":             "",
		"repository":        "",
		"resourcesync":      "",
	}
	resourceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9\-]+$`)
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
	if len(name) > 0 && !resourceNameRegex.MatchString(name) {
		return "", "", fmt.Errorf("invalid resource name: %s", name)
	}
	return kind, name, nil
}

func singular(kind string) string {
	if kind == "repositories" {
		return "repository"
	} else if strings.HasSuffix(kind, "s") {
		return kind[:len(kind)-1]
	}
	return kind
}
