package service

import (
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
)

func ValidateImageDestOciSpec(ociSpec *domain.OciRepoSpec, imageName, fieldPath string) []error {
	if ociSpec == nil {
		return nil
	}
	if ociSpec.Namespace != nil && *ociSpec.Namespace != "" {
		return []error{fmt.Errorf("%s: namespace is not valid on an ImageBuild or ImageExport destination", fieldPath)}
	}
	if ociSpec.Repository == nil || *ociSpec.Repository == "" {
		return nil
	}
	if imageName != *ociSpec.Repository {
		return []error{fmt.Errorf("%s: destination imageName %q must equal spec.repository %q", fieldPath, imageName, *ociSpec.Repository)}
	}
	return nil
}
