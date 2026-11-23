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
		if !strings.Contains(raw, "/") {
			return "", "", fmt.Errorf("missing Helm chart path")
		}
	default:
		return "", "", fmt.Errorf("unsupported consumer")
	}
	if need.Destination == "" {
		return "", "", fmt.Errorf("missing destination")
	}
	named, err := reference.ParseNormalizedNamed(raw)
	if err != nil {
		return "", "", &inputError{message: "invalid OCI reference", cause: err}
	}
	host := reference.Domain(named)
	if need.Consumer == ConsumerHelmChart {
		// Helm uses an explicit registry host, including single-label DNS names.
		host, _, _ = strings.Cut(raw, "/")
	}
	registry, err = normalizeRegistry(host)
	if err != nil {
		return "", "", fmt.Errorf("invalid OCI reference: %w", err)
	}
	if registry == "docker.io" && reference.Domain(named) != registry {
		// Canonical aliases also use Docker Hub's implicit library/ namespace.
		named, err = reference.ParseNormalizedNamed(registry + "/" + reference.Path(named))
		if err != nil {
			return "", "", &inputError{message: "invalid OCI reference", cause: err}
		}
	}
	path = reference.Path(named)
	if need.Consumer == ConsumerHelmChart && registry != reference.Domain(named) {
		// Docker normalization includes a single-label Helm host in the path.
		path = strings.TrimPrefix(path, host+"/")
	}
	return registry, path, nil
}

func normalizeRegistry(raw string) (string, error) {
	host := strings.ToLower(raw)
	if host == "" || strings.ContainsAny(host, "/@?#") {
		return "", fmt.Errorf("invalid registry")
	}
	switch host {
	case "registry-1.docker.io", "index.docker.io":
		return "docker.io", nil
	}
	// Repository endpoints are explicit, including single-label DNS names.
	if _, err := reference.Parse(host + "/flightctl-scope"); err != nil {
		return "", &inputError{message: "invalid registry", cause: err}
	}
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return host, nil
	}
	named, err := reference.ParseNormalizedNamed(host + "/flightctl-scope")
	if err != nil {
		return "", &inputError{message: "invalid registry", cause: err}
	}
	return reference.Domain(named), nil
}

func repositoryScope(spec domain.OciRepoSpec) (registry, scope string, err error) {
	registry, err = normalizeRegistry(spec.Registry)
	if err != nil {
		return "", "", fmt.Errorf("normalize registry: %w", err)
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
		return "", "", &inputError{message: "invalid scope", cause: err}
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
			return "", "", &inputError{message: "invalid scope", cause: err}
		}
		scope = reference.Path(normalized)
	}
	return registry, scope, nil
}
