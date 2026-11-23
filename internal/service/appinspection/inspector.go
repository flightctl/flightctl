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
	declared := appinspection.InspectSpec(app).Declared
	unavailable := appinspection.PackageResult{Result: appinspection.Result{
		References:  declared,
		Diagnostics: []appinspection.Diagnostic{appinspection.DiagnosticContentUnavailable},
	}}
	appType, err := app.GetAppType()
	if err != nil || (appType != domain.AppTypeCompose && appType != domain.AppTypeQuadlet) {
		return unavailable
	}
	var image string
	for _, ref := range declared {
		if ref.Role == appinspection.RolePackage {
			image = ref.Image
			break
		}
	}
	if image == "" {
		return unavailable
	}
	pullRef, err := oci.RewriteImageRef(image)
	if err != nil {
		return unavailable
	}
	endpoint, err := registry.ParseReference(strings.TrimPrefix(pullRef, "docker://"))
	if err != nil || endpoint.Reference == "" {
		return unavailable
	}
	access, err := i.resolveAccess(ctx, orgID, endpoint)
	if err != nil {
		return unavailable
	}
	repo, err := oci.BuildOciRepoRef(ctx, oci.SpecForRegistry(endpoint.Registry, access), endpoint.Registry+"/"+endpoint.Repository)
	if err != nil {
		return unavailable
	}
	result := appinspection.InspectPackage(ctx, appType, repo, endpoint.Reference, platform)
	result.References = append(declared, result.References...)
	return result
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
			spec, err := item.Spec.AsOciRepoSpec()
			if err != nil || !strings.EqualFold(spec.Registry, endpoint.Registry) {
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
		repo := strings.Trim(*spec.Repository, "/")
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
