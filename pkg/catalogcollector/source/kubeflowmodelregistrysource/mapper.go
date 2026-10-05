package kubeflowmodelregistrysource

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	gosemver "github.com/coreos/go-semver/semver"
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

// semverRe implements the SemVer 2.0.0 grammar. A leading "v" is deliberately
// not accepted because registry version names are not silently rewritten.
var semverRe = regexp.MustCompile(
	`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?` +
		`(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`,
)

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
		Kind:       "Catalog",
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
		Kind:       "CatalogItem",
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
		if !isValidSemVer(versionName) {
			return nil, fmt.Errorf(
				"registered model id=%s name=%q, version id=%s name=%q: "+
					"version name is not valid SemVer 2.0.0; rename it in "+
					"the Model Registry rather than relying on normalization",
				safeID(model.model.Id),
				model.model.Name,
				safeID(collected.version.Id),
				versionName,
			)
		}

		// Verify that the version string can be fully parsed by the
		// semver library. The regex above is a fast-path filter, but it
		// cannot catch all invalid inputs (e.g. integer overflow in
		// numeric components). gosemver.New() used in the sort below
		// panics when NewVersion returns an error, so we must validate
		// here first.
		if _, err := gosemver.NewVersion(versionName); err != nil {
			return nil, fmt.Errorf(
				"registered model id=%s name=%q, version id=%s name=%q: "+
					"version name passes SemVer regex but cannot be parsed: %w",
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
	// pagination or insertion order. Parse as semver (already validated above)
	// so that "1.9.0" sorts before "1.10.0".
	//
	// SemVer 2.0.0 §11 ignores build metadata when comparing precedence, so
	// versions like "1.0.0+build1" and "1.0.0+build2" are considered equal.
	// When semantic precedence is equal, the original version string is used
	// as a lexical tie-breaker so that sort.Slice produces identical output
	// regardless of the input order.
	sort.Slice(versions, func(i, j int) bool {
		vi := gosemver.New(versions[i].Version)
		vj := gosemver.New(versions[j].Version)
		if vi.LessThan(*vj) {
			return true
		}
		if vj.LessThan(*vi) {
			return false
		}
		// Semantic precedence is equal; break tie lexically.
		return versions[i].Version < versions[j].Version
	})

	return versions, nil
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

func isValidSemVer(version string) bool {
	return semverRe.MatchString(version)
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
