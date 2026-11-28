package appinspection

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/flightctl/flightctl/internal/appinspection"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/oci"
	"github.com/flightctl/flightctl/internal/service/repository"
	"github.com/google/uuid"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

type Inspector struct {
	repositories repository.Service
}

func NewInspector(repositories repository.Service) (*Inspector, error) {
	if repositories == nil {
		return nil, errors.New("repository service is required for package inspection")
	}
	return &Inspector{repositories: repositories}, nil
}

// InspectPackage takes the effective spec after catalog resolution. Service
// access is used only for pulling from the resolved package endpoint.
func (i *Inspector) InspectPackage(ctx context.Context, orgID uuid.UUID, app *domain.ApplicationProviderSpec, platform *ocispec.Platform) appinspection.PackageResult {
	declared := appinspection.DeclaredReferences(app)
	unavailable := appinspection.PackageResult{Result: appinspection.Result{
		References:  declared,
		Diagnostics: []appinspection.Diagnostic{appinspection.DiagnosticContentUnavailable},
	}}
	appType, err := app.GetAppType()
	if err != nil || (appType != domain.AppTypeCompose && appType != domain.AppTypeQuadlet) {
		return unavailable
	}
	endpoint, err := packageEndpoint(declared)
	if err != nil {
		return unavailable
	}
	repo, err := i.packageRepository(ctx, orgID, endpoint)
	if err != nil {
		return unavailable
	}
	result := appinspection.InspectPackage(ctx, appType, repo, endpoint.Reference, platform)
	result.References = append(declared, result.References...)
	return result
}

func parsePackageEndpoint(pullRef string) (registry.Reference, error) {
	endpoint, err := registry.ParseReference(strings.TrimPrefix(pullRef, "docker://"))
	if err != nil {
		return registry.Reference{}, err
	}
	endpoint.Repository = packageRepositoryPath(endpoint.Registry, endpoint.Repository)
	return endpoint, nil
}

func packageRepositoryPath(host, path string) string {
	// Docker Hub serves official images under the library namespace.
	switch strings.ToLower(host) {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		if path != "" && !strings.Contains(path, "/") {
			return "library/" + path
		}
	}
	return path
}

func packageEndpoint(declared []appinspection.Reference) (registry.Reference, error) {
	for _, ref := range declared {
		if ref.Role != appinspection.RolePackage {
			continue
		}
		pullRef, err := oci.RewriteImageRef(ref.Image)
		if err != nil {
			return registry.Reference{}, err
		}
		endpoint, err := parsePackageEndpoint(pullRef)
		if err != nil || endpoint.Reference == "" {
			return registry.Reference{}, errors.New("invalid inspection package reference")
		}
		return endpoint, nil
	}
	return registry.Reference{}, errors.New("inspection package reference is required")
}

func (i *Inspector) packageRepository(ctx context.Context, orgID uuid.UUID, endpoint registry.Reference) (*remote.Repository, error) {
	access, err := i.resolveAccess(ctx, orgID, endpoint)
	if err != nil {
		return nil, err
	}
	return oci.BuildOciRepoRef(ctx, oci.SpecForRegistry(endpoint.Registry, access), endpoint.Registry+"/"+endpoint.Repository)
}

func (i *Inspector) resolveAccess(ctx context.Context, orgID uuid.UUID, endpoint registry.Reference) (*domain.OciRepoSpec, error) {
	limit := int32(1000)
	var token *string
	var best *domain.OciRepoSpec
	bestScore := -1
	ambiguous := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, status := i.repositories.ListRepositories(ctx, orgID, domain.ListRepositoriesParams{Limit: &limit, Continue: token})
		if status.Code != http.StatusOK || page == nil {
			return nil, errors.New("unable to resolve package repository access")
		}
		for _, item := range page.Items {
			specType, err := item.Spec.Discriminator()
			if err != nil {
				return nil, errors.New("unable to determine package repository type")
			}
			if specType != string(domain.RepoSpecTypeOci) {
				continue
			}
			spec, err := item.Spec.AsOciRepoSpec()
			if err != nil {
				return nil, errors.New("unable to decode OCI package repository access")
			}
			if !strings.EqualFold(spec.Registry, endpoint.Registry) {
				continue
			}
			score := accessSpecificity(spec, endpoint.Repository)
			if score < 0 || score < bestScore {
				continue
			}
			spec.Registry = endpoint.Registry
			if score > bestScore {
				best, bestScore, ambiguous = &spec, score, false
			} else if !sameServiceAccess(best, &spec) {
				ambiguous = true
			}
		}
		token = page.Metadata.Continue
		if token == nil || *token == "" {
			break
		}
	}
	if ambiguous {
		return nil, errors.New("conflicting service access for package repository")
	}
	return best, nil
}

func sameServiceAccess(a, b *domain.OciRepoSpec) bool {
	return reflect.DeepEqual(a.OciAuth, b.OciAuth) && reflect.DeepEqual(a.Scheme, b.Scheme) &&
		reflect.DeepEqual(a.SkipServerVerification, b.SkipServerVerification) && reflect.DeepEqual(a.CaCrt, b.CaCrt)
}

// Give exact repositories precedence over namespaces and require a path boundary.
func accessSpecificity(spec domain.OciRepoSpec, path string) int {
	if spec.Repository != nil && *spec.Repository != "" {
		repo := packageRepositoryPath(spec.Registry, strings.Trim(*spec.Repository, "/"))
		if path == repo {
			return len(repo) + 1
		}
		return -1
	}
	if spec.Namespace != nil && *spec.Namespace != "" {
		namespace := strings.Trim(*spec.Namespace, "/")
		if path == namespace || strings.HasPrefix(path, namespace+"/") {
			return len(namespace)
		}
		return -1
	}
	return 0
}
