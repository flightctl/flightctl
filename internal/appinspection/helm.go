package appinspection

import (
	"context"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/chartutil"
	"github.com/flightctl/flightctl/internal/domain"
)

type HelmRenderer interface {
	RenderManifests(context.Context, v1beta1.HelmApplication, *domain.Device, []byte, *domain.OciRepoSpec) (string, error)
}

// InspectHelm discovers workload images from the target chart's rendered manifests.
func InspectHelm(
	ctx context.Context,
	renderer HelmRenderer,
	app v1beta1.HelmApplication,
	device *domain.Device,
	renderedConfig []byte,
	registrySpec *domain.OciRepoSpec,
) Result {
	manifests, err := renderer.RenderManifests(ctx, app, device, renderedConfig, registrySpec)
	if err != nil {
		return Result{Diagnostics: []Diagnostic{DiagnosticContentUnavailable}}
	}

	images, err := chartutil.ExtractPartialImagesFromManifests(manifests)
	var result Result
	for _, image := range images {
		result.References = append(result.References, Reference{Image: image, Role: RoleWorkload, ConsumerKind: ConsumerHelmImage})
	}
	if err != nil {
		result.Diagnostics = []Diagnostic{DiagnosticParseFailed}
	}
	return result
}
