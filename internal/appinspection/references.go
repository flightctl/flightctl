package appinspection

import (
	"slices"

	"github.com/flightctl/flightctl/api/core/v1beta1"
)

type ArtifactRole string

const (
	RolePackage  ArtifactRole = "package"
	RoleWorkload ArtifactRole = "workload"
	RoleVolume   ArtifactRole = "volume"
)

type ConsumerKind string

const (
	ConsumerContainer ConsumerKind = "container"
	ConsumerHelmChart ConsumerKind = "helm-chart"
	ConsumerHelmImage ConsumerKind = "helm-image"
)

type Diagnostic string

const (
	DiagnosticParseFailed        Diagnostic = "parse_failed"
	DiagnosticContentUnavailable Diagnostic = "content_unavailable"
)

type Reference struct {
	Image        string
	Role         ArtifactRole
	ConsumerKind ConsumerKind
}

type Result struct {
	References  []Reference
	Diagnostics []Diagnostic
}

func (r Result) Complete() bool {
	return len(r.Diagnostics) == 0
}

type SpecReferences struct {
	Declared []Reference
	Inline   *Result // Nil when the app has no supplied inline content.
}

// InspectSpec finds references declared by an effective app and in its supplied inline files.
func InspectSpec(app *v1beta1.ApplicationProviderSpec) SpecReferences {
	appType, err := app.GetAppType()
	if err != nil {
		return SpecReferences{}
	}

	var result SpecReferences
	var image string
	var volumes *[]v1beta1.ApplicationVolume
	role, consumer := RolePackage, ConsumerContainer
	switch appType {
	case v1beta1.AppTypeContainer:
		spec, err := app.AsContainerApplication()
		if err != nil {
			break
		}
		role = RoleWorkload
		image = providerImage(spec.AsImageApplicationProviderSpec())
		volumes = spec.Volumes
	case v1beta1.AppTypeCompose:
		spec, err := app.AsComposeApplication()
		if err != nil {
			break
		}
		image = providerImage(spec.AsImageApplicationProviderSpec())
		volumes = spec.Volumes
		if inline, err := spec.AsInlineApplicationProviderSpec(); err != nil {
			result.Inline = &Result{Diagnostics: []Diagnostic{DiagnosticParseFailed}}
		} else if inline.Inline != nil {
			inspected := InspectCompose(inline.Inline)
			result.Inline = &inspected
		}
	case v1beta1.AppTypeQuadlet:
		spec, err := app.AsQuadletApplication()
		if err != nil {
			break
		}
		image = providerImage(spec.AsImageApplicationProviderSpec())
		volumes = spec.Volumes
		if inline, err := spec.AsInlineApplicationProviderSpec(); err != nil {
			result.Inline = &Result{Diagnostics: []Diagnostic{DiagnosticParseFailed}}
		} else if inline.Inline != nil {
			inspected := InspectQuadlet(inline.Inline)
			result.Inline = &inspected
		}
	case v1beta1.AppTypeHelm:
		spec, err := app.AsHelmApplication()
		if err != nil {
			break
		}
		consumer = ConsumerHelmChart
		image = providerImage(spec.AsImageApplicationProviderSpec())
	case v1beta1.AppTypeVm:
		spec, err := app.AsVmApplication()
		if err != nil {
			break
		}
		image = providerImage(spec.AsImageApplicationProviderSpec())
	}
	result.Declared = addReference(result.Declared, image, role, consumer)
	if volumes != nil {
		for _, vol := range *volumes {
			result.Declared = addReference(result.Declared, volumeImage(vol), RoleVolume, ConsumerContainer)
		}
	}
	result.Declared = uniqueReferences(result.Declared)
	return result
}

func providerImage(spec v1beta1.ImageApplicationProviderSpec, err error) string {
	if err != nil {
		return ""
	}
	return spec.Image
}

func volumeImage(volume v1beta1.ApplicationVolume) string {
	kind, err := volume.Type()
	if err != nil {
		return ""
	}
	switch kind {
	case v1beta1.ImageApplicationVolumeProviderType:
		if spec, err := volume.AsImageVolumeProviderSpec(); err == nil {
			return spec.Image.Reference
		}
	case v1beta1.ImageMountApplicationVolumeProviderType:
		if spec, err := volume.AsImageMountVolumeProviderSpec(); err == nil {
			return spec.Image.Reference
		}
	}
	return ""
}

func addReference(refs []Reference, image string, role ArtifactRole, consumer ConsumerKind) []Reference {
	if image == "" {
		return refs
	}
	return append(refs, Reference{Image: image, Role: role, ConsumerKind: consumer})
}

func uniqueReferences(refs []Reference) []Reference {
	if len(refs) < 2 {
		return refs
	}
	seen := make(map[Reference]struct{}, len(refs))
	return slices.DeleteFunc(refs, func(ref Reference) bool {
		_, duplicate := seen[ref]
		seen[ref] = struct{}{}
		return duplicate
	})
}
