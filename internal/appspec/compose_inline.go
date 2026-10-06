package appspec

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/api/common"
)

var (
	BaseComposeFiles = []string{
		"docker-compose.yaml",
		"docker-compose.yml",
		"podman-compose.yaml",
		"podman-compose.yml",
	}

	OverrideComposeFiles = []string{
		"docker-compose.override.yaml",
		"docker-compose.override.yml",
		"podman-compose.override.yaml",
		"podman-compose.override.yml",
	}
)

// ParseComposeFromSpec parses a Compose specification from a slice of inline application content
// for use by server-side code and inline application providers.
func ParseComposeFromSpec(contents []v1beta1.ApplicationContent) (*common.ComposeSpec, error) {
	spec := &common.ComposeSpec{
		Services: make(map[string]common.ComposeService),
		Volumes:  make(map[string]common.ComposeVolume),
	}

	var baseFound bool
	for _, c := range contents {
		filename := c.Path
		if filename == "" {
			continue
		}

		contentBytes, err := c.ContentsDecoded()
		if err != nil {
			return nil, fmt.Errorf("decoding content %q: %w", filename, err)
		}

		isBase := slices.Contains(BaseComposeFiles, filename)
		isOverride := slices.Contains(OverrideComposeFiles, filename)

		if !isBase && !isOverride {
			continue
		}

		partial, err := common.ParseComposeSpec(contentBytes)
		if err != nil {
			return nil, fmt.Errorf("parsing compose spec from %q: %w", filename, err)
		}

		// First match from BaseComposeFiles takes precedence
		if isBase && !baseFound {
			maps.Copy(spec.Services, partial.Services)
			maps.Copy(spec.Volumes, partial.Volumes)
			baseFound = true
			continue
		}

		if isOverride && baseFound {
			maps.Copy(spec.Services, partial.Services)
			maps.Copy(spec.Volumes, partial.Volumes)
		}
	}

	if !baseFound {
		return nil, fmt.Errorf("%w: no base compose file found in inline spec (expected one of: %s)", common.ErrNoComposeFile, strings.Join(BaseComposeFiles, ", "))
	}

	if len(spec.Services) == 0 {
		return nil, common.ErrNoComposeServices
	}

	return spec, nil
}
