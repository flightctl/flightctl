package helm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/chartutil"
	"github.com/flightctl/flightctl/internal/domain"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/google/uuid"
)

const repositoryPageSize int32 = 1000

// ResolveOCIRepositorySpec finds the most-specific OCI Repository resource for
// a chart reference. If none matches, a same-registry fallback (typically the
// configured delta storage repository) may supply registry credentials.
func ResolveOCIRepositorySpec(
	ctx context.Context,
	repositories repositoryservice.Service,
	orgID uuid.UUID,
	chartRef string,
	fallback *domain.OciRepoSpec,
) (*domain.OciRepoSpec, error) {
	host, chartPath, err := chartRepositoryPath(chartRef)
	if err != nil {
		return nil, err
	}
	if repositories == nil {
		return sameRegistryFallback(host, fallback), nil
	}

	var (
		continueToken *string
		best          *domain.OciRepoSpec
		bestSpecific  = -1
	)
	for {
		page, status := repositories.ListRepositories(ctx, orgID, domain.ListRepositoriesParams{
			Limit:    ptr(repositoryPageSize),
			Continue: continueToken,
		})
		if status.Code != http.StatusOK {
			return nil, fmt.Errorf("list repositories for Helm chart registry %s: %s", host, status.Message)
		}
		if page == nil {
			break
		}
		for i := range page.Items {
			spec, err := page.Items[i].Spec.AsOciRepoSpec()
			if err != nil || !strings.EqualFold(spec.Registry, host) {
				continue
			}
			specificity, matches := repositorySpecificity(&spec, chartPath)
			if !matches || specificity < bestSpecific {
				continue
			}
			if specificity == bestSpecific && best != nil && !sameRepositoryConfig(*best, spec) {
				return nil, fmt.Errorf("multiple equally specific OCI repositories match Helm chart %s", chartRef)
			}
			copySpec := spec
			best = &copySpec
			bestSpecific = specificity
		}
		continueToken = page.Metadata.Continue
		if continueToken == nil || *continueToken == "" {
			break
		}
	}
	if best != nil {
		return best, nil
	}
	return sameRegistryFallback(host, fallback), nil
}

func chartRepositoryPath(chartRef string) (host, path string, err error) {
	normalized := chartutil.NormalizeChartRef(chartRef)
	parsedURL, err := url.Parse(normalized)
	if err != nil {
		return "", "", fmt.Errorf("parse Helm chart reference %q: %w", chartRef, err)
	}
	if parsedURL.Scheme != "oci" || parsedURL.Host == "" {
		return "", "", fmt.Errorf("Helm chart reference %q is not an OCI reference", chartRef)
	}

	ref := strings.TrimPrefix(normalized, "oci://")
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", "", fmt.Errorf("parse Helm chart repository %q: %w", chartRef, err)
	}
	return parsedURL.Host, reference.Path(named), nil
}

func repositorySpecificity(spec *domain.OciRepoSpec, chartPath string) (int, bool) {
	if spec.Repository != nil && *spec.Repository != "" {
		repository := strings.Trim(*spec.Repository, "/")
		return len(repository) + 1, chartPath == repository
	}
	if spec.Namespace != nil && *spec.Namespace != "" {
		namespace := strings.Trim(*spec.Namespace, "/")
		return len(namespace), chartPath == namespace || strings.HasPrefix(chartPath, namespace+"/")
	}
	return 0, true
}

func sameRegistryFallback(host string, fallback *domain.OciRepoSpec) *domain.OciRepoSpec {
	if fallback == nil || !strings.EqualFold(fallback.Registry, host) {
		return nil
	}
	copySpec := *fallback
	return &copySpec
}

func sameRepositoryConfig(a, b domain.OciRepoSpec) bool {
	return reflect.DeepEqual(a, b)
}

func ptr[T any](value T) *T {
	return &value
}
