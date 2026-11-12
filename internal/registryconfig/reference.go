package registryconfig

import (
	"fmt"
	"strings"

	"github.com/distribution/reference"
	"github.com/flightctl/flightctl/internal/domain"
)

func parseNeed(need PullNeed) (registry, path string, err error) {
	raw := need.Reference
	switch need.Consumer {
	case ConsumerContainer, ConsumerHelmImage, ConsumerOS:
		raw = strings.TrimPrefix(raw, "docker://")
	case ConsumerHelmChart:
		raw = strings.TrimPrefix(raw, "oci://")
	default:
		return "", "", fmt.Errorf("unsupported consumer")
	}
	if need.Destination == "" {
		return "", "", fmt.Errorf("missing destination")
	}
	named, err := reference.ParseNormalizedNamed(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid OCI reference")
	}
	registry, err = normalizeRegistry(reference.Domain(named))
	if err != nil {
		return "", "", fmt.Errorf("invalid OCI reference")
	}
	return registry, reference.Path(named), nil
}

func normalizeRegistry(raw string) (string, error) {
	host := strings.ToLower(raw)
	if host == "" || strings.ContainsAny(host, "/@?#") {
		return "", fmt.Errorf("invalid registry")
	}
	// Repository endpoints are explicit, including single-label DNS names.
	if _, err := reference.Parse(host + "/flightctl-scope"); err != nil {
		return "", fmt.Errorf("invalid registry")
	}
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return host, nil
	}
	named, err := reference.ParseNormalizedNamed(host + "/flightctl-scope")
	if err != nil {
		return "", fmt.Errorf("invalid registry")
	}
	return reference.Domain(named), nil
}

func repositoryScope(spec domain.OciRepoSpec) (registry, scope string, err error) {
	registry, err = normalizeRegistry(spec.Registry)
	if err != nil {
		return "", "", err
	}
	namespace, repository := "", ""
	if spec.Namespace != nil {
		namespace = *spec.Namespace
	}
	if spec.Repository != nil {
		repository = *spec.Repository
	}
	if namespace != "" && repository != "" {
		return "", "", fmt.Errorf("conflicting scope fields")
	}
	path := repository
	if namespace != "" {
		path = namespace
	}
	if path == "" {
		return registry, "", nil
	}
	if strings.Trim(path, "/") != path || strings.ContainsAny(path, ":@") {
		return "", "", fmt.Errorf("invalid scope")
	}
	// The suffix validates a namespace as a prefix, without adding Docker Hub's library/.
	raw := registry + "/" + path
	if namespace != "" {
		raw += "/flightctl-scope"
	}
	parsed, err := reference.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid scope")
	}
	named, ok := parsed.(reference.Named)
	if !ok {
		return "", "", fmt.Errorf("invalid scope")
	}
	scope = reference.Path(named)
	if namespace != "" {
		scope = strings.TrimSuffix(scope, "/flightctl-scope")
	}
	if registry == "docker.io" && namespace == "" {
		normalized, err := reference.ParseNormalizedNamed(raw)
		if err != nil {
			return "", "", fmt.Errorf("invalid scope")
		}
		scope = reference.Path(normalized)
	}
	return registry, scope, nil
}
