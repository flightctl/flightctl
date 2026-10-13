package kubeflowmodelregistrysource

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	internalvalidation "github.com/flightctl/flightctl/internal/util/validation"
	mrapi "github.com/kubeflow/hub/pkg/openapi"
)

// eligibleArtifactStates contains the ModelArtifact lifecycle states accepted
// for synchronization.
//
// Validation against RHOAI 3.5.1 using the Model Registry v1alpha3 API showed
// that RHOAI-created ModelCar artifacts omit the state field. An absent state
// is therefore treated as UNKNOWN. LIVE and UNKNOWN artifacts are eligible;
// lifecycle states such as ABANDONED are not.
var eligibleArtifactStates = map[mrapi.ArtifactState]bool{
	mrapi.ARTIFACTSTATE_LIVE:    true,
	mrapi.ARTIFACTSTATE_UNKNOWN: true,
}

// ociDigestRe matches the immutable digest format currently supported by the
// Flightctl model deployment path.
var ociDigestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// collectedModel is the normalized intermediate representation of a
// RegisteredModel and its eligible versions.
type collectedModel struct {
	model    mrapi.RegisteredModel
	versions []collectedVersion
}

// collectedVersion is the normalized intermediate representation of a
// ModelVersion and its single eligible ModelArtifact.
type collectedVersion struct {
	version    mrapi.ModelVersion
	repository string
	digest     string
}

// toSnapshot converts collected models into a complete desired-state Catalog
// snapshot.
//
// The caller omits registered models that have no LIVE versions. Any model
// passed to this function must therefore contain at least one eligible version.
// Violating a mapping invariant fails the complete snapshot.
func toSnapshot(
	catalogName string,
	models []collectedModel,
) ([]apiv1alpha1.Catalog, []apiv1alpha1.CatalogItem, error) {
	catalog := buildCatalog(catalogName)
	seenNames := make(map[string]string, len(models))
	items := make([]apiv1alpha1.CatalogItem, 0, len(models))

	for _, model := range models {
		item, err := toItem(catalogName, model, seenNames)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}

	// Canonicalize output independently of the order returned by the upstream
	// API. This keeps snapshot hashing stable across otherwise equivalent
	// collection cycles.
	sort.Slice(items, func(i, j int) bool {
		return ptrStr(items[i].Metadata.Name) <
			ptrStr(items[j].Metadata.Name)
	})

	return []apiv1alpha1.Catalog{catalog}, items, nil
}

func buildCatalog(name string) apiv1alpha1.Catalog {
	return apiv1alpha1.Catalog{
		ApiVersion: "flightctl.io/v1alpha1",
		Kind:       apiv1alpha1.CatalogKind,
		Metadata: apiv1beta1.ObjectMeta{
			Name: ptr(name),
		},
		Spec: apiv1alpha1.CatalogSpec{
			DisplayName: ptr(name),
		},
	}
}

func toItem(
	catalogName string,
	model collectedModel,
	seenNames map[string]string,
) (apiv1alpha1.CatalogItem, error) {
	normalizedName, err := normalizeName(model.model.Name)
	if err != nil {
		return apiv1alpha1.CatalogItem{}, fmt.Errorf(
			"registered model id=%s name=%q: name normalization failed: %w",
			safeID(model.model.Id),
			model.model.Name,
			err,
		)
	}

	if originalName, found := seenNames[normalizedName]; found {
		return apiv1alpha1.CatalogItem{}, fmt.Errorf(
			"name collision: registered models %q and %q both normalize to %q; "+
				"rename one of the models to disambiguate them",
			originalName,
			model.model.Name,
			normalizedName,
		)
	}
	seenNames[normalizedName] = model.model.Name

	repository, err := sharedRepository(model)
	if err != nil {
		return apiv1alpha1.CatalogItem{}, err
	}

	versions, err := toVersions(model)
	if err != nil {
		return apiv1alpha1.CatalogItem{}, err
	}

	spec := apiv1alpha1.CatalogItemSpec{
		Type:     apiv1alpha1.CatalogItemTypeData,
		Category: ptr(apiv1alpha1.CatalogItemCategoryApplication),
		Artifacts: []apiv1alpha1.CatalogItemArtifact{
			{
				Type: apiv1alpha1.CatalogItemArtifactTypeContainer,
				Uri:  repository,
			},
		},
		Versions:    versions,
		DisplayName: ptr(model.model.Name),
	}

	if model.model.Description != nil &&
		strings.TrimSpace(*model.model.Description) != "" {
		spec.ShortDescription = ptr(*model.model.Description)
	}

	if provider := modelProvider(model.model); provider != "" {
		spec.Provider = ptr(provider)
	}

	return apiv1alpha1.CatalogItem{
		ApiVersion: "flightctl.io/v1alpha1",
		Kind:       apiv1alpha1.CatalogItemKind,
		Metadata: apiv1alpha1.CatalogItemMeta{
			Name:    ptr(normalizedName),
			Catalog: catalogName,
		},
		Spec: spec,
	}, nil
}

// sharedRepository verifies that all eligible versions of one model use the
// same version-less OCI repository. CatalogItem stores the repository once and
// stores each version's digest in its references map, so different repositories
// cannot be represented safely.
func sharedRepository(model collectedModel) (string, error) {
	if len(model.versions) == 0 {
		return "", fmt.Errorf(
			"registered model id=%s name=%q has no eligible versions",
			safeID(model.model.Id),
			model.model.Name,
		)
	}

	repository := model.versions[0].repository
	for _, version := range model.versions[1:] {
		if version.repository != repository {
			return "", fmt.Errorf(
				"registered model id=%s name=%q: versions %q and %q use "+
					"different OCI repositories; all versions of one model "+
					"must share a repository",
				safeID(model.model.Id),
				model.model.Name,
				model.versions[0].version.Name,
				version.version.Name,
			)
		}
	}

	return repository, nil
}

// toVersions converts eligible ModelVersions into CatalogItemVersion entries.
// Duplicate version names are rejected because they cannot be represented
// unambiguously in one CatalogItem.
func toVersions(
	model collectedModel,
) ([]apiv1alpha1.CatalogItemVersion, error) {
	versions := make([]apiv1alpha1.CatalogItemVersion, 0, len(model.versions))
	seenVersions := make(map[string]string, len(model.versions))

	for _, collected := range model.versions {
		versionName := collected.version.Name
		if err := validateVersionName(versionName); err != nil {
			return nil, fmt.Errorf(
				"registered model id=%s name=%q, version id=%s name=%q: "+
					"version name is not a supported CatalogItem version: %w; "+
					"rename it in the Model Registry rather than relying on "+
					"normalization",
				safeID(model.model.Id),
				model.model.Name,
				safeID(collected.version.Id),
				versionName,
				err,
			)
		}

		if previousID, found := seenVersions[versionName]; found {
			return nil, fmt.Errorf(
				"registered model id=%s name=%q has duplicate version name %q "+
					"on version ids %s and %s",
				safeID(model.model.Id),
				model.model.Name,
				versionName,
				previousID,
				safeID(collected.version.Id),
			)
		}
		seenVersions[versionName] = safeID(collected.version.Id)

		versions = append(versions, apiv1alpha1.CatalogItemVersion{
			Version:  versionName,
			Channels: []string{"stable"},
			References: map[apiv1alpha1.CatalogItemArtifactType]string{
				apiv1alpha1.CatalogItemArtifactTypeContainer: collected.digest,
			},
		})
	}

	// Canonicalize version order so snapshot revisions do not depend on API
	// pagination or insertion order. Ordering uses an internal comparison key
	// derived from the validated version string so that "1.9.0" sorts before
	// "1.10.0". The published CatalogItemVersion.Version value is never
	// rewritten by sorting.
	//
	// SemVer 2.0.0 §11 ignores build metadata when comparing precedence, so
	// versions like "1.0.0+build1" and "1.0.0+build2" are considered equal.
	// "1.0" and "1.0.0" also compare equal because the comparison key supplies
	// the missing patch component. When precedence is equal, the original
	// version string is used as a lexical tie-breaker so that sorting produces
	// identical output regardless of the input order.
	sortVersions(versions)

	return versions, nil
}

// sortVersions orders CatalogItemVersion entries by version precedence using
// an internal comparison key. The entries themselves are never modified.
func sortVersions(versions []apiv1alpha1.CatalogItemVersion) {
	keys := make(map[string]versionSortKey, len(versions))
	for _, version := range versions {
		if _, found := keys[version.Version]; !found {
			keys[version.Version] = newVersionSortKey(version.Version)
		}
	}

	sort.SliceStable(versions, func(i, j int) bool {
		left := versions[i].Version
		right := versions[j].Version

		if comparison := keys[left].compare(keys[right]); comparison != 0 {
			return comparison < 0
		}
		// Precedence is equal; break the tie on the original strings so the
		// result is deterministic and "1.0" stays distinct from "1.0.0".
		return left < right
	})
}

// validateVersionName checks a Model Registry version name against the
// CatalogItem version format accepted by the Flightctl API.
//
// The grammar is deliberately not re-implemented here. Sharing the API
// validator keeps the source from rejecting versions the API would accept
// (notably two-component versions such as "1.0") and from accepting versions
// the API would later reject.
func validateVersionName(version string) error {
	return internalvalidation.ValidateCatalogItemVersion(version)
}

// versionSortKey is the internal, overflow-safe comparison key derived from a
// validated CatalogItem version string.
//
// The key is used for ordering only. It never replaces the published version
// string: a two-component version such as "1.0" keeps its original spelling in
// the CatalogItem while comparing as if its patch component were zero.
type versionSortKey struct {
	// core holds the three numeric core identifiers as digit strings. Numbers
	// are compared as digit strings rather than integers so that components
	// larger than any fixed-width integer compare correctly and cannot
	// overflow or panic.
	core [3]string

	// prerelease holds the dot-separated pre-release identifiers. It is empty
	// when the version has no pre-release suffix.
	prerelease []string

	// hasPrerelease distinguishes "1.0.0" from "1.0.0-0", because a release
	// has higher precedence than any pre-release of the same core version.
	hasPrerelease bool
}

// newVersionSortKey derives a comparison key from a version string that has
// already been accepted by validateVersionName.
//
// Build metadata is ignored, matching SemVer 2.0.0 §11. A missing patch
// component is supplied as "0" for comparison purposes only. Input that was
// not validated still produces a usable key rather than panicking.
func newVersionSortKey(version string) versionSortKey {
	key := versionSortKey{}

	value := version
	if plus := strings.IndexByte(value, '+'); plus >= 0 {
		value = value[:plus]
	}
	if hyphen := strings.IndexByte(value, '-'); hyphen >= 0 {
		key.hasPrerelease = true
		key.prerelease = strings.Split(value[hyphen+1:], ".")
		value = value[:hyphen]
	}

	components := strings.Split(value, ".")
	for i := range key.core {
		if i < len(components) {
			key.core[i] = components[i]
			continue
		}
		// Flightctl accepts a two-component version such as "1.0". Supply the
		// absent patch component so that "1.0" and "1.0.0" have equivalent
		// precedence.
		key.core[i] = "0"
	}

	return key
}

// compare returns a negative number when k has lower precedence than other,
// zero when precedence is equivalent, and a positive number otherwise.
func (k versionSortKey) compare(other versionSortKey) int {
	for i := range k.core {
		if comparison := compareNumericIdentifier(
			k.core[i],
			other.core[i],
		); comparison != 0 {
			return comparison
		}
	}

	switch {
	case !k.hasPrerelease && !other.hasPrerelease:
		return 0
	case !k.hasPrerelease:
		// A release has higher precedence than a pre-release.
		return 1
	case !other.hasPrerelease:
		return -1
	}

	return comparePrerelease(k.prerelease, other.prerelease)
}

// comparePrerelease compares two pre-release identifier lists per
// SemVer 2.0.0 §11.
func comparePrerelease(left, right []string) int {
	shortest := min(len(left), len(right))

	for i := 0; i < shortest; i++ {
		if comparison := comparePrereleaseIdentifier(
			left[i],
			right[i],
		); comparison != 0 {
			return comparison
		}
	}

	// A larger set of pre-release fields has higher precedence when all
	// preceding identifiers are equal.
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	default:
		return 0
	}
}

// comparePrereleaseIdentifier compares one pre-release identifier. Numeric
// identifiers always have lower precedence than alphanumeric identifiers.
func comparePrereleaseIdentifier(left, right string) int {
	leftNumeric := isNumericIdentifier(left)
	rightNumeric := isNumericIdentifier(right)

	switch {
	case leftNumeric && rightNumeric:
		return compareNumericIdentifier(left, right)
	case leftNumeric:
		return -1
	case rightNumeric:
		return 1
	default:
		return strings.Compare(left, right)
	}
}

// compareNumericIdentifier compares two decimal digit strings numerically
// without converting them to a fixed-width integer, so arbitrarily large
// components are handled without overflow.
func compareNumericIdentifier(left, right string) int {
	left = trimLeadingZeros(left)
	right = trimLeadingZeros(right)

	if len(left) != len(right) {
		if len(left) < len(right) {
			return -1
		}
		return 1
	}

	return strings.Compare(left, right)
}

// trimLeadingZeros removes insignificant leading zeros while keeping at least
// one digit.
func trimLeadingZeros(value string) string {
	index := 0
	for index < len(value)-1 && value[index] == '0' {
		index++
	}
	return value[index:]
}

// isNumericIdentifier reports whether a pre-release identifier consists only
// of decimal digits.
func isNumericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// splitArtifactURI splits a digest-pinned OCI reference into the version-less
// repository stored in CatalogItem.spec.artifacts and the digest stored in the
// version's references map.
//
// An optional "oci://" prefix is accepted and removed. Tags are allowed only
// when the same reference is also pinned by digest; the tag is discarded.
// Error messages deliberately avoid including the supplied URI because an
// invalid URI could contain secret material.
func splitArtifactURI(uri string) (repository, digest string, err error) {
	if uri == "" {
		return "", "", fmt.Errorf("artifact URI is empty")
	}
	if strings.TrimSpace(uri) != uri {
		return "", "", fmt.Errorf(
			"artifact URI must not contain leading or trailing whitespace",
		)
	}

	rawURI := uri
	if strings.HasPrefix(rawURI, "oci://") {
		rawURI = strings.TrimPrefix(rawURI, "oci://")
	} else if strings.Contains(rawURI, "://") {
		return "", "", fmt.Errorf(
			"artifact URI uses an unsupported scheme; only an optional oci:// prefix is supported",
		)
	}

	if rawURI == "" {
		return "", "", fmt.Errorf(
			"artifact URI contains no OCI image reference",
		)
	}

	if strings.Count(rawURI, "@") != 1 {
		return "", "", fmt.Errorf(
			"artifact URI must contain exactly one immutable digest separator",
		)
	}

	at := strings.LastIndexByte(rawURI, '@')
	if at <= 0 || at == len(rawURI)-1 {
		return "", "", fmt.Errorf(
			"artifact URI must include both an OCI repository and digest",
		)
	}

	digest = rawURI[at+1:]
	if !ociDigestRe.MatchString(digest) {
		return "", "", fmt.Errorf(
			"artifact URI digest must be sha256 followed by exactly 64 lowercase hexadecimal characters",
		)
	}

	matches := internalvalidation.StrictOciImageReferenceRegexp.FindStringSubmatch(
		rawURI,
	)
	if len(matches) != 4 {
		return "", "", fmt.Errorf(
			"artifact URI is not a valid fully qualified OCI image reference",
		)
	}

	repository = matches[1]
	parsedDigest := matches[3]
	if repository == "" || parsedDigest != digest {
		return "", "", fmt.Errorf(
			"artifact URI could not be separated into a repository and digest",
		)
	}

	return repository, digest, nil
}

// isEligibleArtifact evaluates the artifact type and lifecycle state, then
// validates its immutable OCI reference.
//
// Artifacts of another type or lifecycle state are ignored. A model-artifact
// in an eligible state with an invalid URI is an actionable data error and
// therefore fails the complete collection cycle.
func isEligibleArtifact(
	artifact *mrapi.ModelArtifact,
) (repository, digest string, eligible bool, err error) {
	if artifact == nil {
		return "", "", false, nil
	}

	if artifact.ArtifactType == nil ||
		*artifact.ArtifactType != "model-artifact" {
		return "", "", false, nil
	}

	state := mrapi.ARTIFACTSTATE_UNKNOWN
	if artifact.State != nil {
		state = *artifact.State
	}
	if !eligibleArtifactStates[state] {
		return "", "", false, nil
	}

	if artifact.Uri == nil || strings.TrimSpace(*artifact.Uri) == "" {
		return "", "", false, nil
	}

	repository, digest, err = splitArtifactURI(*artifact.Uri)
	if err != nil {
		return "", "", false, fmt.Errorf(
			"model artifact id=%s has an invalid immutable OCI reference: %w",
			safeID(artifact.Id),
			err,
		)
	}

	return repository, digest, true, nil
}

func modelProvider(model mrapi.RegisteredModel) string {
	if model.Owner != nil {
		if owner := strings.TrimSpace(*model.Owner); owner != "" {
			return owner
		}
	}
	if model.Provider != nil {
		if provider := strings.TrimSpace(*model.Provider); provider != "" {
			return provider
		}
	}
	return ""
}

func safeID(id *string) string {
	if id == nil || strings.TrimSpace(*id) == "" {
		return "<unknown>"
	}
	return *id
}

func ptr[T any](value T) *T {
	return &value
}

func ptrStr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
