package delta_worker

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/appspec"
	"github.com/flightctl/flightctl/internal/chartutil"
	preparetask "github.com/flightctl/flightctl/internal/delta_worker/tasks/prepare"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/oci"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/google/uuid"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
)

// inspectFn resolves an image reference to its content digest.
type inspectFn func(ctx context.Context, orgId uuid.UUID, image string) (string, error)
type inspectSourceFn func(
	ctx context.Context,
	orgId uuid.UUID,
	image string,
	sourceDigest string,
	fallbackPlatform *ocispec.Platform,
) (resolvedSourceDigest, targetDigest string, err error)

type appCandidatePairer func(
	ctx context.Context,
	logger logrus.FieldLogger,
	orgId uuid.UUID,
	device *domain.Device,
	imageRef string,
	digestIndex map[string][]string,
) []preparetask.DeltaCandidate

type helmImageRefsFn func(
	ctx context.Context,
	orgId uuid.UUID,
	device *domain.Device,
	app v1beta1.HelmApplication,
	renderedConfig []byte,
) ([]string, error)

// expandAppCandidates extracts application image pairs from the rendered spec
// and appends them to the existing (OS) candidates. The rendered spec carries
// fully expanded applications (inline content already resolved). For each
// application it extracts parent and nested image references, pairs them with
// the current digest from the device's status.applications[].imageDigests,
// and resolves the new digest via registry inspect.
func expandAppCandidates(
	ctx context.Context,
	logger logrus.FieldLogger,
	orgId uuid.UUID,
	device *domain.Device,
	rendered tasks.RenderedSpec,
	candidates []preparetask.DeltaCandidate,
	inspect inspectFn,
) []preparetask.DeltaCandidate {
	return expandAppCandidatesUsing(ctx, logger, orgId, device, rendered, candidates, nil, func(
		ctx context.Context,
		logger logrus.FieldLogger,
		orgId uuid.UUID,
		device *domain.Device,
		imageRef string,
		digestIndex map[string][]string,
	) []preparetask.DeltaCandidate {
		return pairCandidates(ctx, logger, orgId, applicationDeviceName(device), imageRef, digestIndex, inspect)
	})
}

func expandAppCandidatesForSourceWithHelm(
	ctx context.Context,
	logger logrus.FieldLogger,
	orgId uuid.UUID,
	device *domain.Device,
	rendered tasks.RenderedSpec,
	candidates []preparetask.DeltaCandidate,
	inspect inspectSourceFn,
	helmImageRefs helmImageRefsFn,
) []preparetask.DeltaCandidate {
	return expandAppCandidatesUsing(ctx, logger, orgId, device, rendered, candidates, helmImageRefs, func(
		ctx context.Context,
		logger logrus.FieldLogger,
		orgId uuid.UUID,
		device *domain.Device,
		imageRef string,
		digestIndex map[string][]string,
	) []preparetask.DeltaCandidate {
		return pairCandidatesForSource(
			ctx,
			logger,
			orgId,
			applicationDeviceName(device),
			imageRef,
			digestIndex,
			oci.DeviceImagePlatform(device),
			inspect,
		)
	})
}

func expandAppCandidatesUsing(
	ctx context.Context,
	logger logrus.FieldLogger,
	orgId uuid.UUID,
	device *domain.Device,
	rendered tasks.RenderedSpec,
	candidates []preparetask.DeltaCandidate,
	helmImageRefs helmImageRefsFn,
	pairer appCandidatePairer,
) []preparetask.DeltaCandidate {
	if len(rendered.Applications) == 0 {
		return candidates
	}

	var apps []domain.ApplicationProviderSpec
	if err := json.Unmarshal(rendered.Applications, &apps); err != nil {
		logger.WithError(err).WithFields(logrus.Fields{
			"orgId":      orgId,
			"deviceName": applicationDeviceName(device),
		}).Warn("failed to unmarshal rendered applications for delta expansion")
		return candidates
	}

	digestIndex := buildDigestIndex(device)

	for i := range apps {
		refs := extractNewImageRefs(&apps[i])
		if appType, err := apps[i].GetAppType(); err == nil && appType == domain.AppTypeHelm {
			if helmImageRefs == nil {
				refs = append(refs, reportedHelmImageRefs(device, &apps[i])...)
			} else {
				helmApp, err := apps[i].AsHelmApplication()
				if err != nil {
					logger.WithError(err).WithFields(logrus.Fields{
						"orgId":      orgId,
						"deviceName": applicationDeviceName(device),
					}).Warn("failed to decode Helm application for delta expansion; skipping this Helm application")
					refs = nil
				} else {
					targetRefs, renderErr := helmImageRefs(ctx, orgId, device, helmApp, rendered.Config)
					if renderErr != nil {
						logger.WithError(renderErr).WithFields(logrus.Fields{
							"orgId":      orgId,
							"deviceName": applicationDeviceName(device),
						}).Warn("failed to render target Helm chart images for delta expansion; skipping this Helm application")
						refs = nil
					} else {
						refs = append(refs, targetRefs...)
					}
				}
			}
		}
		refs = deduplicateStrings(refs)
		for _, ref := range refs {
			candidates = append(candidates, pairer(ctx, logger, orgId, device, ref, digestIndex)...)
		}
	}
	return candidates
}

// reportedHelmImageRefs returns the workload image references currently
// reported for a Helm application. The rendered server-side spec contains the
// chart reference and values, but not the images produced by Helm templates.
// These references let delta preparation cover mutable workload tags that the
// desired chart continues to use; the agent ignores hints for references that
// are absent from the rendered chart.
func reportedHelmImageRefs(device *domain.Device, app *domain.ApplicationProviderSpec) []string {
	if device == nil || device.Status == nil {
		return nil
	}
	appName, err := app.GetName()
	if err != nil {
		return nil
	}
	name := ""
	if appName != nil {
		name = *appName
	}
	if name == "" {
		helmApp, err := app.AsHelmApplication()
		if err != nil {
			return nil
		}
		imageSpec, err := helmApp.AsImageApplicationProviderSpec()
		if err != nil {
			return nil
		}
		name, err = chartutil.SanitizeReleaseName(imageSpec.Image)
		if err != nil {
			return nil
		}
	}
	for _, appStatus := range device.Status.Applications {
		if appStatus.Name != name || appStatus.ImageDigests == nil {
			continue
		}
		var refs []string
		for _, image := range *appStatus.ImageDigests {
			if image.Image != "" && image.Digest != "" {
				refs = append(refs, image.Image)
			}
		}
		return deduplicateStrings(refs)
	}
	return nil
}

// buildDigestIndex builds a lookup from image repository to current digests
// from the device's current application statuses.
func buildDigestIndex(device *domain.Device) map[string][]string {
	if device == nil || device.Status == nil {
		return nil
	}
	idx := make(map[string][]string)
	for _, app := range device.Status.Applications {
		if app.ImageDigests == nil {
			continue
		}
		for _, entry := range *app.ImageDigests {
			if entry.Image != "" && entry.Digest != "" {
				repo, err := applicationImageRepository(entry.Image)
				if err != nil {
					continue
				}
				if !slices.Contains(idx[repo], entry.Digest) {
					idx[repo] = append(idx[repo], entry.Digest)
				}
			}
		}
	}
	for repo := range idx {
		slices.Sort(idx[repo])
	}
	return idx
}

// pairCandidates pairs a new image reference with every known current digest
// for its repository. It inspects the new image once and skips identical pairs.
func pairCandidates(
	ctx context.Context,
	logger logrus.FieldLogger,
	orgId uuid.UUID,
	deviceName string,
	newImageRef string,
	digestIndex map[string][]string,
	inspect inspectFn,
) []preparetask.DeltaCandidate {
	repo, err := applicationImageRepository(newImageRef)
	if err != nil {
		logger.WithError(err).WithFields(logrus.Fields{
			"orgId":      orgId,
			"deviceName": deviceName,
			"image":      newImageRef,
		}).Warn("failed to parse rendered application image reference for delta expansion")
		return nil
	}
	currentDigests := digestIndex[repo]
	if len(currentDigests) == 0 {
		return nil
	}

	newDigest, err := inspect(ctx, orgId, newImageRef)
	if err != nil {
		logger.WithError(err).WithFields(logrus.Fields{
			"orgId":      orgId,
			"deviceName": deviceName,
			"image":      newImageRef,
		}).Warn("failed to inspect rendered application image for delta expansion")
		return nil
	}
	if newDigest == "" {
		return nil
	}

	var candidates []preparetask.DeltaCandidate
	for _, currentDigest := range currentDigests {
		if currentDigest == newDigest {
			continue
		}
		candidates = append(candidates, preparetask.DeltaCandidate{
			ImageRepository: repo,
			CurrentDigest:   currentDigest,
			NewDigest:       newDigest,
		})
	}
	return candidates
}

func pairCandidatesForSource(
	ctx context.Context,
	logger logrus.FieldLogger,
	orgId uuid.UUID,
	deviceName string,
	newImageRef string,
	digestIndex map[string][]string,
	fallbackPlatform *ocispec.Platform,
	inspect inspectSourceFn,
) []preparetask.DeltaCandidate {
	repo, err := applicationImageRepository(newImageRef)
	if err != nil {
		logger.WithError(err).WithFields(logrus.Fields{
			"orgId":      orgId,
			"deviceName": deviceName,
			"image":      newImageRef,
		}).Warn("failed to parse rendered application image reference for delta expansion")
		return nil
	}
	currentDigests := digestIndex[repo]
	if len(currentDigests) == 0 {
		return nil
	}

	var candidates []preparetask.DeltaCandidate
	for _, currentDigest := range currentDigests {
		resolvedSource, targetDigest, err := inspect(ctx, orgId, newImageRef, currentDigest, fallbackPlatform)
		if err != nil {
			logger.WithError(err).WithFields(logrus.Fields{
				"orgId":        orgId,
				"deviceName":   deviceName,
				"image":        newImageRef,
				"sourceDigest": currentDigest,
			}).Warn("failed to inspect rendered application image for source platform")
			continue
		}
		if resolvedSource == "" || targetDigest == "" || resolvedSource == targetDigest {
			continue
		}
		candidates = append(candidates, preparetask.DeltaCandidate{
			ImageRepository: repo,
			CurrentDigest:   resolvedSource,
			NewDigest:       targetDigest,
		})
	}
	return candidates
}

func applicationDeviceName(device *domain.Device) string {
	if device == nil || device.Metadata.Name == nil {
		return ""
	}
	return *device.Metadata.Name
}

func applicationImageRepository(image string) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", err
	}
	return named.Name(), nil
}

// extractNewImageRefs extracts all image references from a rendered application
// spec. These are the "new" images the device will pull after the update.
func extractNewImageRefs(app *domain.ApplicationProviderSpec) []string {
	appType, err := (*app).GetAppType()
	if err != nil {
		return nil
	}

	var refs []string
	switch appType {
	case domain.AppTypeContainer:
		refs = extractContainerRefs(app)
	case domain.AppTypeCompose:
		refs = extractComposeRefs(app)
	case domain.AppTypeQuadlet:
		refs = extractQuadletRefs(app)
	case domain.AppTypeHelm:
		refs = extractHelmRefs(app)
	case domain.AppTypeVm:
		refs = extractVmRefs(app)
	}

	refs = append(refs, extractVolumeImageRefs(app)...)
	return deduplicateStrings(refs)
}

// extractContainerRefs extracts the container image from a rendered container
// application.
func extractContainerRefs(app *domain.ApplicationProviderSpec) []string {
	container, err := (*app).AsContainerApplication()
	if err != nil {
		return nil
	}
	imageSpec, err := container.AsImageApplicationProviderSpec()
	if err != nil || imageSpec.Image == "" {
		return nil
	}
	return []string{imageSpec.Image}
}

// extractComposeRefs extracts all image references from a rendered compose
// application: the parent artifact image (when image-based) and every service
// image inside the compose YAML. Rendered compose apps always carry inline
// content — the render pipeline expands image-based artifacts into inline
// files before the spec reaches PrepareDeltas.
func extractComposeRefs(app *domain.ApplicationProviderSpec) []string {
	compose, err := (*app).AsComposeApplication()
	if err != nil {
		return nil
	}

	var refs []string

	// Parent artifact image (image-based compose).
	imageSpec, err := compose.AsImageApplicationProviderSpec()
	if err == nil && imageSpec.Image != "" {
		refs = append(refs, imageSpec.Image)
	}

	// Service images from the inline compose YAML.
	inline, err := compose.AsInlineApplicationProviderSpec()
	if err == nil {
		refs = append(refs, parseComposeServiceImages(inline.Inline)...)
	}

	return refs
}

// extractQuadletRefs extracts all image references from a rendered quadlet
// application: the parent artifact image (when image-based), references in
// inline quadlet units, and images in Pod YAML referenced by .kube units.
// Rendered quadlet apps always carry inline content.
func extractQuadletRefs(app *domain.ApplicationProviderSpec) []string {
	quadlet, err := (*app).AsQuadletApplication()
	if err != nil {
		return nil
	}

	var refs []string

	// Parent artifact image (image-based quadlet).
	imageSpec, err := quadlet.AsImageApplicationProviderSpec()
	if err == nil && imageSpec.Image != "" {
		refs = append(refs, imageSpec.Image)
	}

	// Image= refs from inline quadlet unit files.
	inline, err := quadlet.AsInlineApplicationProviderSpec()
	if err == nil {
		refs = append(refs, parseQuadletImageRefs(inline.Inline)...)
	}

	return refs
}

// extractHelmRefs extracts the chart OCI reference from a rendered Helm
// application. Workload image references are added by rendering the chart with
// its target values in the delta worker.
func extractHelmRefs(app *domain.ApplicationProviderSpec) []string {
	helm, err := (*app).AsHelmApplication()
	if err != nil {
		return nil
	}
	imageSpec, err := helm.AsImageApplicationProviderSpec()
	if err != nil || imageSpec.Image == "" {
		return nil
	}
	return []string{imageSpec.Image}
}

// extractVmRefs extracts an image-backed VM application image. Inline VM apps
// are rendered as Quadlet applications before delta candidates are prepared.
func extractVmRefs(app *domain.ApplicationProviderSpec) []string {
	vm, err := (*app).AsVmApplication()
	if err != nil {
		return nil
	}
	imageSpec, err := vm.AsImageApplicationProviderSpec()
	if err != nil || imageSpec.Image == "" {
		return nil
	}
	return []string{imageSpec.Image}
}

// extractVolumeImageRefs extracts OCI image references from application volumes.
func extractVolumeImageRefs(app *domain.ApplicationProviderSpec) []string {
	// Volumes are on the typed app specs. Try each type.
	var volumes *[]v1beta1.ApplicationVolume

	if c, err := (*app).AsContainerApplication(); err == nil {
		volumes = c.Volumes
	} else if comp, err := (*app).AsComposeApplication(); err == nil {
		volumes = comp.Volumes
	} else if q, err := (*app).AsQuadletApplication(); err == nil {
		volumes = q.Volumes
	}

	if volumes == nil {
		return nil
	}

	var refs []string
	for _, vol := range *volumes {
		volType, err := vol.Type()
		if err != nil {
			continue
		}
		switch volType {
		case v1beta1.ImageApplicationVolumeProviderType:
			imgVol, err := vol.AsImageVolumeProviderSpec()
			if err == nil && imgVol.Image.Reference != "" {
				refs = append(refs, imgVol.Image.Reference)
			}
		case v1beta1.ImageMountApplicationVolumeProviderType:
			imgVol, err := vol.AsImageMountVolumeProviderSpec()
			if err == nil && imgVol.Image.Reference != "" {
				refs = append(refs, imgVol.Image.Reference)
			}
		}
	}
	return refs
}

// parseComposeServiceImages parses compose YAML content and returns all service
// image references.
func parseComposeServiceImages(contents []v1beta1.ApplicationContent) []string {
	spec, err := appspec.ParseComposeFromSpec(contents)
	if err != nil || spec == nil {
		return nil
	}
	var images []string
	for _, svc := range spec.Services {
		if svc.Image != "" {
			images = append(images, svc.Image)
		}
	}
	return images
}

// parseQuadletImageRefs returns external images referenced by inline Quadlet
// units and any Pod YAML referenced by .kube units.
func parseQuadletImageRefs(contents []v1beta1.ApplicationContent) []string {
	images, err := appspec.ParseQuadletImageReferencesFromSpec(contents)
	if err != nil {
		return nil
	}
	return images
}

func deduplicateStrings(s []string) []string {
	if len(s) <= 1 {
		return s
	}
	seen := make(map[string]struct{}, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
