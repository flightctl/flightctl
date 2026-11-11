package common

import (
	"context"
	"slices"

	"github.com/flightctl/flightctl/internal/domain"
)

// RepositoryRefsOutcome is the outcome of a guarded replacement of a
// resource's repository references. It is meaningful only with an OK status.
type RepositoryRefsOutcome int

const (
	// RepositoryRefsReplaced means the references now equal the complete set
	// computed from the given spec.
	RepositoryRefsReplaced RepositoryRefsOutcome = iota + 1
	// RepositoryRefsStale means the resource changed since the caller read it,
	// or no longer accepts references from this writer; nothing was written.
	RepositoryRefsStale
	// RepositoryRefsIncomplete means the firmware references cannot be known
	// yet, so the stored references were left unchanged.
	RepositoryRefsIncomplete
)

func (o RepositoryRefsOutcome) String() string {
	switch o {
	case RepositoryRefsReplaced:
		return "replaced"
	case RepositoryRefsStale:
		return "stale"
	case RepositoryRefsIncomplete:
		return "incomplete"
	default:
		return "not written"
	}
}

// CollectConfigRepositoryRefs returns the repository of every git and http
// configuration provider, in configuration order. Providers whose type cannot
// be determined, and other provider types, contribute nothing.
func CollectConfigRepositoryRefs(config *[]domain.ConfigProviderSpec) []string {
	if config == nil {
		return nil
	}
	var names []string
	for _, provider := range *config {
		providerType, err := provider.Type()
		if err != nil {
			continue
		}
		switch providerType {
		case domain.GitConfigProviderType:
			if git, err := provider.AsGitConfigProviderSpec(); err == nil {
				names = append(names, git.GitRef.Repository)
			}
		case domain.HttpConfigProviderType:
			if httpProvider, err := provider.AsHttpConfigProviderSpec(); err == nil {
				names = append(names, httpProvider.HttpRef.Repository)
			}
		}
	}
	return names
}

// RepositoryDependencySet returns the sorted union of configuration and
// firmware repository names, listing a repository used by both once.
func RepositoryDependencySet(configRefs, firmwareRefs []string) []string {
	names := slices.Concat(configRefs, firmwareRefs)
	slices.Sort(names)
	return append([]string{}, slices.Compact(names)...)
}

// CollectRepositoryDependencies returns the complete set of repositories that
// a device spec depends on: its configuration repositories and the literal
// artifact and signature repositories of its firmware. It is recomputed from
// the spec on every call, so a repository disappears once no source uses it.
//
// complete is false when the spec has firmware whose references cannot be
// known yet, because the descriptor of its updater release is unknown or
// unavailable. Such a partial set must not replace stored references.
func CollectRepositoryDependencies(ctx context.Context, provider ApprovedDescriptorProvider, spec *domain.DeviceSpec,
	mode FirmwareValidationMode) (names []string, complete bool, err error) {
	if spec == nil {
		return []string{}, true, nil
	}
	firmwareRefs, complete, err := ExtractFirmwareRepositoryRefs(ctx, provider, spec.Firmware, mode)
	if err != nil || !complete {
		return nil, false, err
	}
	return RepositoryDependencySet(CollectConfigRepositoryRefs(spec.Config), firmwareRefs), true, nil
}
