package kubeflowmodelregistrysource

import (
	"fmt"
	"strings"
	"testing"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	mrapi "github.com/kubeflow/hub/pkg/openapi"
)

// helpers -----------------------------------------------------------------

func strp(s string) *string { return &s }

func modelArtifact(uri string, state *mrapi.ArtifactState) *mrapi.ModelArtifact {
	t := "model-artifact"
	ma := &mrapi.ModelArtifact{
		Uri:          strp(uri),
		ArtifactType: &t,
		State:        state,
	}
	return ma
}

func makeVersion(id, name string) mrapi.ModelVersion {
	return mrapi.ModelVersion{Id: strp(id), Name: name}
}

func makeModel(id, name string) mrapi.RegisteredModel {
	return mrapi.RegisteredModel{Id: strp(id), Name: name}
}

const goodURI = "registry.example.com/models/modelcar-iris@sha256:bd62d86fc106e620c60f3905ebb1f8d102dc2470d049d845eada1c9b823801ba"
const goodRepo = "registry.example.com/models/modelcar-iris"
const goodDigest = "sha256:bd62d86fc106e620c60f3905ebb1f8d102dc2470d049d845eada1c9b823801ba"

// --- normalizeName tests ---------------------------------------------------

func TestNormalizeName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "already valid", input: "iris-edge", want: "iris-edge"},
		{name: "uppercase folded", input: "Iris-Edge", want: "iris-edge"},
		{name: "spaces become dashes", input: "my model", want: "my-model"},
		{name: "leading/trailing dashes stripped", input: "-iris-", want: "iris"},
		{name: "consecutive separators collapsed", input: "a___b", want: "a-b"},
		{name: "dots preserved as label boundaries", input: "my.model.1", want: "my.model.1"},
		{name: "dots with dashes", input: "my..model", want: "my.model"},
		{name: "dots collapse to single dot", input: "my...model", want: "my.model"},
		{name: "underscore becomes dash", input: "my_model", want: "my-model"},
		{name: "slash becomes dash", input: "org/model", want: "org-model"},
		{name: "empty input", input: "", wantErr: true},
		{name: "only separators", input: "---", wantErr: true},
		{name: "non-ascii becomes dash", input: "modèle", want: "mod-le"},
		{name: "label over 63 chars truncated", input: "a" + string(make([]byte, 70)), want: "a" + string(make([]byte, 62))},
		{name: "version with dot labels", input: "my-model-1.0.0", want: "my-model-1.0.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "label over 63 chars truncated" {
				// Build a 71-char 'a' string
				tc.input = repeatByte('a', 71)
				tc.want = repeatByte('a', 63)
			}
			got, err := normalizeName(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("normalizeName(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("normalizeName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func repeatByte(b byte, n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = b
	}
	return string(buf)
}

func TestNormalizeName_TotalLengthCap(t *testing.T) {
	// Build a name that results in a 300-byte normalized form.
	// 300 'a' chars → should be truncated to 253.
	long := repeatByte('a', 300)
	got, err := normalizeName(long)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) > 253 {
		t.Errorf("result length %d exceeds 253", len(got))
	}
}

// --- splitArtifactURI tests ------------------------------------------------

func TestSplitArtifactURI(t *testing.T) {
	cases := []struct {
		name       string
		uri        string
		wantRepo   string
		wantDigest string
		wantErr    bool
	}{
		{
			name:       "bare URI (no scheme)",
			uri:        goodURI,
			wantRepo:   goodRepo,
			wantDigest: goodDigest,
		},
		{
			name:       "oci:// prefix stripped",
			uri:        "oci://" + goodURI,
			wantRepo:   goodRepo,
			wantDigest: goodDigest,
		},
		{
			name:    "no @ separator",
			uri:     "registry.example.com/myimage",
			wantErr: true,
		},
		{
			name:    "truncated digest",
			uri:     "registry.example.com/myimage@sha256:abc123",
			wantErr: true,
		},
		{
			name:    "wrong hash algorithm",
			uri:     "registry.example.com/myimage@md5:bd62d86fc106e620c60f3905ebb1f8d102dc2470d049d845eada1c9b823801ba",
			wantErr: true,
		},
		{
			name:    "empty URI after scheme",
			uri:     "oci://",
			wantErr: true,
		},
		{
			name:    "empty repo",
			uri:     "@sha256:bd62d86fc106e620c60f3905ebb1f8d102dc2470d049d845eada1c9b823801ba",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, digest, err := splitArtifactURI(tc.uri)
			if (err != nil) != tc.wantErr {
				t.Fatalf("splitArtifactURI(%q) error = %v, wantErr %v", tc.uri, err, tc.wantErr)
			}
			if !tc.wantErr {
				if repo != tc.wantRepo {
					t.Errorf("repo = %q, want %q", repo, tc.wantRepo)
				}
				if digest != tc.wantDigest {
					t.Errorf("digest = %q, want %q", digest, tc.wantDigest)
				}
			}
		})
	}
}

// --- version acceptance tests ----------------------------------------------

// TestValidateVersionName verifies that the source accepts exactly the
// CatalogItem version format the Flightctl API accepts, including the
// two-component form "1.0", and rejects everything the API rejects.
func TestValidateVersionName(t *testing.T) {
	valid := []string{
		"1.0.0",
		"1.0", // two-component versions are supported by the Flightctl API
		"0.1.0",
		"0.1",
		"1.2.3-alpha.1",
		"1.2.3+build.1",
		"1.2.3-alpha.1+build",
		"1.0-rc.1",
		"10.20.30",
		// Numeric components far beyond any fixed-width integer are accepted
		// by the Flightctl API and must therefore be accepted here too.
		"99999999999999999999999999.0.0",
	}
	invalid := []string{
		"v1.0.0",  // leading v not accepted
		"1",       // missing minor
		"1.0.0.0", // extra component
		"latest",  // not a version
		"1.0.0-",  // empty pre-release
		"1.a.0",   // non-numeric core component
		"",
	}

	for _, version := range valid {
		t.Run("accepts "+version, func(t *testing.T) {
			if err := validateVersionName(version); err != nil {
				t.Errorf("validateVersionName(%q) = %v, want nil", version, err)
			}
		})
	}
	for _, version := range invalid {
		t.Run("rejects "+version, func(t *testing.T) {
			if err := validateVersionName(version); err == nil {
				t.Errorf("validateVersionName(%q) = nil, want error", version)
			}
		})
	}
}

// TestValidateVersionName_MatchesAPIValidation guards against the source and
// the Flightctl API drifting apart: every version the source accepts must
// also pass CatalogItem.Validate(), and every version it rejects must fail.
func TestValidateVersionName_MatchesAPIValidation(t *testing.T) {
	versions := []string{
		"1.0.0", "1.0", "0.1", "1.2.3-alpha.1", "1.2.3+build.1",
		"99999999999999999999999999.0.0",
		"v1.0.0", "1", "1.0.0.0", "latest", "1.0.0-", "1.a.0", "",
	}

	for _, version := range versions {
		t.Run(version, func(t *testing.T) {
			sourceAccepts := validateVersionName(version) == nil
			apiAccepts := apiAcceptsVersion(version)

			if sourceAccepts != apiAccepts {
				t.Errorf(
					"source accepts %q = %v, but Flightctl API accepts it = %v",
					version, sourceAccepts, apiAccepts,
				)
			}
		})
	}
}

// apiAcceptsVersion reports whether the Flightctl CatalogItem validator
// accepts the given version string, ignoring unrelated validation errors.
func apiAcceptsVersion(version string) bool {
	item := apiv1alpha1.CatalogItem{
		Metadata: apiv1alpha1.CatalogItemMeta{
			Name:    ptr("probe"),
			Catalog: "probe",
		},
		Spec: apiv1alpha1.CatalogItemSpec{
			Type:     apiv1alpha1.CatalogItemTypeData,
			Category: ptr(apiv1alpha1.CatalogItemCategoryApplication),
			Artifacts: []apiv1alpha1.CatalogItemArtifact{{
				Type: apiv1alpha1.CatalogItemArtifactTypeContainer,
				Uri:  goodRepo,
			}},
			Versions: []apiv1alpha1.CatalogItemVersion{{
				Version:  version,
				Channels: []string{"stable"},
				References: map[apiv1alpha1.CatalogItemArtifactType]string{
					apiv1alpha1.CatalogItemArtifactTypeContainer: goodDigest,
				},
			}},
		},
	}

	for _, err := range item.Validate() {
		if strings.Contains(err.Error(), "spec.versions[0].version") {
			return false
		}
	}
	return true
}

// --- isEligibleArtifact tests ----------------------------------------------

func TestIsEligibleArtifact(t *testing.T) {
	liveState := mrapi.ARTIFACTSTATE_LIVE
	archivedState := mrapi.ARTIFACTSTATE_ABANDONED

	cases := []struct {
		name       string
		artifact   *mrapi.ModelArtifact
		wantOK     bool
		wantErr    bool
		wantRepo   string
		wantDigest string
	}{
		{
			name:       "live artifact eligible",
			artifact:   modelArtifact(goodURI, &liveState),
			wantOK:     true,
			wantRepo:   goodRepo,
			wantDigest: goodDigest,
		},
		{
			name:       "nil state defaults to UNKNOWN (eligible)",
			artifact:   modelArtifact(goodURI, nil),
			wantOK:     true,
			wantRepo:   goodRepo,
			wantDigest: goodDigest,
		},
		{
			name:     "abandoned state ineligible",
			artifact: modelArtifact(goodURI, &archivedState),
			wantOK:   false,
		},
		{
			name: "wrong artifact type",
			artifact: func() *mrapi.ModelArtifact {
				a := modelArtifact(goodURI, &liveState)
				other := "doc-artifact"
				a.ArtifactType = &other
				return a
			}(),
			wantOK: false,
		},
		{
			name:     "nil artifact type",
			artifact: func() *mrapi.ModelArtifact { a := modelArtifact(goodURI, &liveState); a.ArtifactType = nil; return a }(),
			wantOK:   false,
		},
		{
			name:     "nil URI",
			artifact: func() *mrapi.ModelArtifact { a := modelArtifact(goodURI, &liveState); a.Uri = nil; return a }(),
			wantOK:   false,
		},
		{
			name:     "empty URI",
			artifact: func() *mrapi.ModelArtifact { a := modelArtifact("", &liveState); return a }(),
			wantOK:   false,
		},
		{
			name:     "bad URI (no digest)",
			artifact: modelArtifact("registry.example.com/myimage", &liveState),
			wantErr:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, digest, ok, err := isEligibleArtifact(tc.artifact)
			if (err != nil) != tc.wantErr {
				t.Fatalf("isEligibleArtifact() error = %v, wantErr %v", err, tc.wantErr)
			}
			if ok != tc.wantOK {
				t.Errorf("isEligibleArtifact() ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				if repo != tc.wantRepo {
					t.Errorf("repo = %q, want %q", repo, tc.wantRepo)
				}
				if digest != tc.wantDigest {
					t.Errorf("digest = %q, want %q", digest, tc.wantDigest)
				}
			}
		})
	}
}

// --- toSnapshot / toItem tests --------------------------------------------

func makeCollectedModel(modelID, modelName, versionID, versionName, uri string) collectedModel {
	repo, digest, err := splitArtifactURI(uri)
	if err != nil {
		panic(err)
	}
	return collectedModel{
		model: makeModel(modelID, modelName),
		versions: []collectedVersion{
			{version: makeVersion(versionID, versionName), repository: repo, digest: digest},
		},
	}
}

func TestToSnapshot_SingleModel(t *testing.T) {
	models := []collectedModel{
		makeCollectedModel("1", "iris-edge", "2", "1.0.0", goodURI),
	}

	catalogs, items, err := toSnapshot("my-catalog", models)
	if err != nil {
		t.Fatalf("toSnapshot() error: %v", err)
	}
	if len(catalogs) != 1 {
		t.Fatalf("expected 1 catalog, got %d", len(catalogs))
	}
	if ptrStr(catalogs[0].Metadata.Name) != "my-catalog" {
		t.Errorf("catalog name = %q, want %q", ptrStr(catalogs[0].Metadata.Name), "my-catalog")
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	item := items[0]
	if ptrStr(item.Metadata.Name) != "iris-edge" {
		t.Errorf("item name = %q, want %q", ptrStr(item.Metadata.Name), "iris-edge")
	}
	if item.Metadata.Catalog != "my-catalog" {
		t.Errorf("item catalog = %q, want %q", item.Metadata.Catalog, "my-catalog")
	}
	if item.Spec.Artifacts[0].Uri != goodRepo {
		t.Errorf("artifact uri = %q, want %q", item.Spec.Artifacts[0].Uri, goodRepo)
	}
	if len(item.Spec.Versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(item.Spec.Versions))
	}
	if item.Spec.Versions[0].Version != "1.0.0" {
		t.Errorf("version = %q, want %q", item.Spec.Versions[0].Version, "1.0.0")
	}
	if item.Spec.Versions[0].References["container"] != goodDigest {
		t.Errorf("version reference = %q, want %q", item.Spec.Versions[0].References["container"], goodDigest)
	}
	if len(item.Spec.Versions[0].Channels) != 1 || item.Spec.Versions[0].Channels[0] != "stable" {
		t.Errorf("channels = %v, want [stable]", item.Spec.Versions[0].Channels)
	}
}

func TestToSnapshot_EmptyModels(t *testing.T) {
	catalogs, items, err := toSnapshot("my-catalog", nil)
	if err != nil {
		t.Fatalf("toSnapshot() error: %v", err)
	}
	if len(catalogs) != 1 {
		t.Errorf("expected 1 catalog, got %d", len(catalogs))
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestToSnapshot_NameCollision(t *testing.T) {
	// "Iris-Edge" and "iris-edge" both normalize to "iris-edge".
	models := []collectedModel{
		makeCollectedModel("1", "iris-edge", "2", "1.0.0", goodURI),
		makeCollectedModel("3", "Iris-Edge", "4", "2.0.0", goodURI),
	}
	_, _, err := toSnapshot("my-catalog", models)
	if err == nil {
		t.Fatal("expected error for name collision, got nil")
	}
}

func TestToSnapshot_InvalidSemVer(t *testing.T) {
	models := []collectedModel{
		makeCollectedModel("1", "iris-edge", "2", "v1.0.0", goodURI), // v-prefixed is invalid
	}
	_, _, err := toSnapshot("my-catalog", models)
	if err == nil {
		t.Fatal("expected error for invalid semver, got nil")
	}
}

func TestToSnapshot_SharedRepositoryViolation(t *testing.T) {
	const uri2 = "other.registry.example.com/other-repo@sha256:bd62d86fc106e620c60f3905ebb1f8d102dc2470d049d845eada1c9b823801ba"
	repo1, digest1, _ := splitArtifactURI(goodURI)
	repo2, digest2, _ := splitArtifactURI(uri2)

	models := []collectedModel{
		{
			model: makeModel("1", "my-model"),
			versions: []collectedVersion{
				{version: makeVersion("2", "1.0.0"), repository: repo1, digest: digest1},
				{version: makeVersion("3", "2.0.0"), repository: repo2, digest: digest2},
			},
		},
	}
	_, _, err := toSnapshot("my-catalog", models)
	if err == nil {
		t.Fatal("expected shared-repository violation error, got nil")
	}
}

func TestToSnapshot_NoEligibleVersions(t *testing.T) {
	// A model with zero versions should produce an error since toItem requires
	// at least one version (sharedRepository enforces this).
	models := []collectedModel{
		{model: makeModel("1", "empty-model"), versions: nil},
	}
	_, _, err := toSnapshot("my-catalog", models)
	if err == nil {
		t.Fatal("expected error for model with no versions, got nil")
	}
}

// TestToVersions_LargeNumericComponents verifies that numeric components far
// beyond the range of any fixed-width integer are accepted, ordered
// numerically, and do not panic inside the sort comparator.
func TestToVersions_LargeNumericComponents(t *testing.T) {
	model := collectedModel{
		model: makeModel("1", "my-model"),
		versions: []collectedVersion{
			{version: makeVersion("2", "99999999999999999999.0.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("1", "1.0.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("3", "100000000000000000000.0.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("4", "9.0.0"), repository: goodRepo, digest: goodDigest},
		},
	}

	versions, err := toVersions(model)
	if err != nil {
		t.Fatalf("toVersions() error: %v", err)
	}

	want := []string{
		"1.0.0",
		"9.0.0",
		"99999999999999999999.0.0",
		"100000000000000000000.0.0",
	}
	assertVersionOrder(t, versions, want)
}

// TestToVersions_PreservesOriginalVersionString verifies that the published
// CatalogItemVersion.Version is byte-for-byte the Model Registry version name,
// even when the internal comparison key supplies a missing patch component or
// ignores build metadata.
func TestToVersions_PreservesOriginalVersionString(t *testing.T) {
	originals := []string{"1.0", "2.0.0+build.9", "3.0-rc.1", "04.5.6"}

	collectedVersions := make([]collectedVersion, 0, len(originals))
	for i, original := range originals {
		collectedVersions = append(collectedVersions, collectedVersion{
			version:    makeVersion(fmt.Sprintf("%d", i+1), original),
			repository: goodRepo,
			digest:     goodDigest,
		})
	}

	model := collectedModel{
		model:    makeModel("1", "my-model"),
		versions: collectedVersions,
	}

	versions, err := toVersions(model)
	if err != nil {
		t.Fatalf("toVersions() error: %v", err)
	}

	published := make(map[string]bool, len(versions))
	for _, version := range versions {
		published[version.Version] = true
	}
	for _, original := range originals {
		if !published[original] {
			t.Errorf("version %q was not published verbatim; got %v", original, published)
		}
	}
}

// TestToVersions_TwoAndThreeComponentOrdering verifies that a two-component
// version is ordered as if its patch component were zero while remaining a
// distinct, unmodified version name alongside its three-component spelling.
func TestToVersions_TwoAndThreeComponentOrdering(t *testing.T) {
	model := collectedModel{
		model: makeModel("1", "my-model"),
		versions: []collectedVersion{
			{version: makeVersion("4", "1.0.1"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("2", "1.0.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("1", "1.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("3", "0.9"), repository: goodRepo, digest: goodDigest},
		},
	}

	versions, err := toVersions(model)
	if err != nil {
		t.Fatalf("toVersions() error: %v", err)
	}

	// "1.0" and "1.0.0" have equivalent precedence, so the original strings
	// break the tie: "1.0" < "1.0.0".
	assertVersionOrder(t, versions, []string{"0.9", "1.0", "1.0.0", "1.0.1"})
}

// TestToVersions_EquivalentPrecedenceIsOrderIndependent verifies that versions
// with equivalent precedence produce identical output and identical snapshot
// revisions regardless of the order the Model Registry returned them in.
func TestToVersions_EquivalentPrecedenceIsOrderIndependent(t *testing.T) {
	names := []string{"1.0", "1.0.0", "1.0.0+build1", "1.0.0+build2"}
	want := []string{"1.0", "1.0.0", "1.0.0+build1", "1.0.0+build2"}

	buildModel := func(order []string) collectedModel {
		versions := make([]collectedVersion, 0, len(order))
		for i, name := range order {
			versions = append(versions, collectedVersion{
				version:    makeVersion(fmt.Sprintf("%d", i+1), name),
				repository: goodRepo,
				digest:     goodDigest,
			})
		}
		return collectedModel{model: makeModel("1", "my-model"), versions: versions}
	}

	forward := buildModel(names)
	reversed := buildModel([]string{"1.0.0+build2", "1.0.0+build1", "1.0.0", "1.0"})

	forwardVersions, err := toVersions(forward)
	if err != nil {
		t.Fatalf("toVersions(forward) error: %v", err)
	}
	reversedVersions, err := toVersions(reversed)
	if err != nil {
		t.Fatalf("toVersions(reversed) error: %v", err)
	}

	assertVersionOrder(t, forwardVersions, want)
	assertVersionOrder(t, reversedVersions, want)
}

// TestToVersions_IdenticalRevisionAcrossInputOrders verifies that the snapshot
// revision is identical for the same content supplied in different orders,
// including equivalent-precedence versions.
func TestToVersions_IdenticalRevisionAcrossInputOrders(t *testing.T) {
	names := []string{"1.0", "1.0.0", "2.0.0-rc.1", "2.0.0", "10.0.0"}

	revisionFor := func(order []string) string {
		t.Helper()
		versions := make([]collectedVersion, 0, len(order))
		for i, name := range order {
			versions = append(versions, collectedVersion{
				version:    makeVersion(fmt.Sprintf("%d", i+1), name),
				repository: goodRepo,
				digest:     goodDigest,
			})
		}
		catalogs, items, err := toSnapshot("cat", []collectedModel{{
			model:    makeModel("1", "my-model"),
			versions: versions,
		}})
		if err != nil {
			t.Fatalf("toSnapshot(%v) error: %v", order, err)
		}
		revision, err := computeRevision(catalogs, items)
		if err != nil {
			t.Fatalf("computeRevision(%v) error: %v", order, err)
		}
		return revision
	}

	forward := revisionFor(names)

	reversed := make([]string, len(names))
	for i, name := range names {
		reversed[len(names)-1-i] = name
	}
	rotated := append(append([]string{}, names[2:]...), names[:2]...)

	if got := revisionFor(reversed); got != forward {
		t.Errorf("revision for reversed order = %q, want %q", got, forward)
	}
	if got := revisionFor(rotated); got != forward {
		t.Errorf("revision for rotated order = %q, want %q", got, forward)
	}
}

// TestToVersions_DuplicateVersionNameRejected verifies that identical version
// names are rejected while equivalent-precedence but distinct names are kept.
func TestToVersions_DuplicateVersionNameRejected(t *testing.T) {
	duplicate := collectedModel{
		model: makeModel("1", "my-model"),
		versions: []collectedVersion{
			{version: makeVersion("1", "1.0.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("2", "1.0.0"), repository: goodRepo, digest: goodDigest},
		},
	}

	_, err := toVersions(duplicate)
	if err == nil {
		t.Fatal("expected an error for a duplicate version name, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate version name") {
		t.Errorf("error = %v, want it to mention the duplicate version name", err)
	}

	// "1.0" and "1.0.0" have equivalent precedence but are different names.
	distinct := collectedModel{
		model: makeModel("1", "my-model"),
		versions: []collectedVersion{
			{version: makeVersion("1", "1.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("2", "1.0.0"), repository: goodRepo, digest: goodDigest},
		},
	}

	versions, err := toVersions(distinct)
	if err != nil {
		t.Fatalf("toVersions() with equivalent-precedence names: %v", err)
	}
	assertVersionOrder(t, versions, []string{"1.0", "1.0.0"})
}

// TestToVersions_InvalidVersionErrorContext verifies that a rejected version
// name produces an actionable error naming the model, the version, and the
// reason.
func TestToVersions_InvalidVersionErrorContext(t *testing.T) {
	model := collectedModel{
		model: makeModel("17", "my-model"),
		versions: []collectedVersion{
			{version: makeVersion("42", "v1.0.0"), repository: goodRepo, digest: goodDigest},
		},
	}

	_, err := toVersions(model)
	if err == nil {
		t.Fatal("expected an error for a 'v'-prefixed version, got nil")
	}
	for _, want := range []string{"id=17", "my-model", "id=42", "v1.0.0", "'v' prefix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

// TestVersionSortKey_Compare exercises the internal comparison key directly,
// including pre-release precedence, build-metadata equivalence, supplied patch
// components, and arbitrarily large numeric identifiers.
func TestVersionSortKey_Compare(t *testing.T) {
	cases := []struct {
		name  string
		left  string
		right string
		want  int
	}{
		{"equal three-component", "1.2.3", "1.2.3", 0},
		{"two-component equals three-component", "1.0", "1.0.0", 0},
		{"two-component below patch", "1.0", "1.0.1", -1},
		{"build metadata ignored", "1.0.0+a", "1.0.0+b", 0},
		{"major precedence", "2.0.0", "1.9.9", 1},
		{"minor numeric not lexical", "1.9.0", "1.10.0", -1},
		{"patch numeric not lexical", "1.0.9", "1.0.10", -1},
		{"leading zeros ignored", "01.02.03", "1.2.3", 0},
		{"pre-release below release", "1.0.0-rc.1", "1.0.0", -1},
		{"release above pre-release", "1.0.0", "1.0.0-rc.1", 1},
		{"numeric pre-release below alphanumeric", "1.0.0-1", "1.0.0-alpha", -1},
		{"numeric pre-release compared numerically", "1.0.0-9", "1.0.0-10", -1},
		{"alphanumeric pre-release compared lexically", "1.0.0-alpha", "1.0.0-beta", -1},
		{"fewer pre-release fields first", "1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"two-component pre-release", "1.0-rc.1", "1.0.0-rc.1", 0},
		{
			"huge components do not overflow",
			"99999999999999999999999999.0.0",
			"100000000000000000000000000.0.0",
			-1,
		},
		{
			"huge equal components",
			"99999999999999999999999999.0.0",
			"99999999999999999999999999.0",
			0,
		},
	}

	sign := func(value int) int {
		switch {
		case value < 0:
			return -1
		case value > 0:
			return 1
		default:
			return 0
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			left := newVersionSortKey(tc.left)
			right := newVersionSortKey(tc.right)

			if got := sign(left.compare(right)); got != tc.want {
				t.Errorf("compare(%q, %q) = %d, want %d", tc.left, tc.right, got, tc.want)
			}
			// Comparison must be antisymmetric.
			if got := sign(right.compare(left)); got != -tc.want {
				t.Errorf("compare(%q, %q) = %d, want %d", tc.right, tc.left, got, -tc.want)
			}
		})
	}
}

// assertVersionOrder asserts the exact published version strings and order.
func assertVersionOrder(
	t *testing.T,
	versions []apiv1alpha1.CatalogItemVersion,
	want []string,
) {
	t.Helper()

	got := make([]string, 0, len(versions))
	for _, version := range versions {
		got = append(got, version.Version)
	}

	if len(got) != len(want) {
		t.Fatalf("version order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("version order = %v, want %v", got, want)
		}
	}
}

// TestToVersions_SemVerOrdering verifies that versions are sorted by semantic
// versioning rather than lexicographic ordering. Without semver-aware sorting,
// "1.10.0" would sort before "1.9.0".
func TestToVersions_SemVerOrdering(t *testing.T) {
	model := collectedModel{
		model: makeModel("1", "my-model"),
		versions: []collectedVersion{
			{version: makeVersion("5", "1.10.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("2", "1.2.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("4", "1.9.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("3", "2.0.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("1", "1.0.0"), repository: goodRepo, digest: goodDigest},
		},
	}

	versions, err := toVersions(model)
	if err != nil {
		t.Fatalf("toVersions() error: %v", err)
	}

	want := []string{"1.0.0", "1.2.0", "1.9.0", "1.10.0", "2.0.0"}
	if len(versions) != len(want) {
		t.Fatalf("expected %d versions, got %d", len(want), len(versions))
	}
	for i, v := range versions {
		if v.Version != want[i] {
			t.Errorf("version[%d] = %q, want %q", i, v.Version, want[i])
		}
	}
}

// TestToVersions_SemVerPreRelease verifies that pre-release versions sort
// before their release counterpart per the SemVer specification.
func TestToVersions_SemVerPreRelease(t *testing.T) {
	model := collectedModel{
		model: makeModel("1", "my-model"),
		versions: []collectedVersion{
			{version: makeVersion("2", "1.0.0"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("1", "1.0.0-alpha.1"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("3", "1.0.0-beta.1"), repository: goodRepo, digest: goodDigest},
		},
	}

	versions, err := toVersions(model)
	if err != nil {
		t.Fatalf("toVersions() error: %v", err)
	}

	want := []string{"1.0.0-alpha.1", "1.0.0-beta.1", "1.0.0"}
	if len(versions) != len(want) {
		t.Fatalf("expected %d versions, got %d", len(want), len(versions))
	}
	for i, v := range versions {
		if v.Version != want[i] {
			t.Errorf("version[%d] = %q, want %q", i, v.Version, want[i])
		}
	}
}

// TestToVersions_BuildMetadataDeterministic verifies that versions differing
// only in build metadata (which SemVer 2.0.0 §11 ignores for precedence)
// produce identical canonical output regardless of input order.
func TestToVersions_BuildMetadataDeterministic(t *testing.T) {
	order1 := collectedModel{
		model: makeModel("1", "my-model"),
		versions: []collectedVersion{
			{version: makeVersion("3", "1.0.0+build2"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("1", "1.0.0+build1"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("2", "1.0.0+build3"), repository: goodRepo, digest: goodDigest},
		},
	}

	order2 := collectedModel{
		model: makeModel("1", "my-model"),
		versions: []collectedVersion{
			{version: makeVersion("2", "1.0.0+build3"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("1", "1.0.0+build1"), repository: goodRepo, digest: goodDigest},
			{version: makeVersion("3", "1.0.0+build2"), repository: goodRepo, digest: goodDigest},
		},
	}

	v1, err := toVersions(order1)
	if err != nil {
		t.Fatalf("toVersions(order1) error: %v", err)
	}
	v2, err := toVersions(order2)
	if err != nil {
		t.Fatalf("toVersions(order2) error: %v", err)
	}

	if len(v1) != len(v2) {
		t.Fatalf("version counts differ: %d vs %d", len(v1), len(v2))
	}

	want := []string{"1.0.0+build1", "1.0.0+build2", "1.0.0+build3"}
	for i, v := range v1 {
		if v.Version != want[i] {
			t.Errorf("order1: version[%d] = %q, want %q", i, v.Version, want[i])
		}
	}
	for i, v := range v2 {
		if v.Version != want[i] {
			t.Errorf("order2: version[%d] = %q, want %q", i, v.Version, want[i])
		}
	}

	// Verify that revisions are identical for both input orders.
	c1, i1, _ := toSnapshot("cat", []collectedModel{order1})
	c2, i2, _ := toSnapshot("cat", []collectedModel{order2})
	r1, _ := computeRevision(c1, i1)
	r2, _ := computeRevision(c2, i2)
	if r1 != r2 {
		t.Errorf("revisions differ for same content in different order: %q vs %q", r1, r2)
	}
}

// --- computeRevision tests -------------------------------------------------

func TestComputeRevision_Deterministic(t *testing.T) {
	models := []collectedModel{
		makeCollectedModel("1", "iris-edge", "2", "1.0.0", goodURI),
	}
	catalogs, items, err := toSnapshot("my-catalog", models)
	if err != nil {
		t.Fatal(err)
	}

	r1, err := computeRevision(catalogs, items)
	if err != nil {
		t.Fatalf("computeRevision: %v", err)
	}
	r2, err := computeRevision(catalogs, items)
	if err != nil {
		t.Fatalf("computeRevision: %v", err)
	}
	if r1 != r2 {
		t.Errorf("computeRevision is not deterministic: %q vs %q", r1, r2)
	}
	if len(r1) != 16 {
		t.Errorf("revision length = %d, want 16", len(r1))
	}
}

func TestComputeRevision_ChangesWithContent(t *testing.T) {
	m1 := []collectedModel{makeCollectedModel("1", "iris-edge", "2", "1.0.0", goodURI)}
	m2 := []collectedModel{makeCollectedModel("1", "iris-edge", "2", "2.0.0", goodURI)}

	c1, i1, _ := toSnapshot("my-catalog", m1)
	c2, i2, _ := toSnapshot("my-catalog", m2)

	r1, err := computeRevision(c1, i1)
	if err != nil {
		t.Fatalf("computeRevision: %v", err)
	}
	r2, err := computeRevision(c2, i2)
	if err != nil {
		t.Fatalf("computeRevision: %v", err)
	}
	if r1 == r2 {
		t.Error("expected different revisions for different content, got equal")
	}
}

// --- Owner / Provider fallback tests ---------------------------------------

// TestModelProvider_OwnerFirstThenProvider verifies the provider resolution
// order: a non-blank Owner always wins, a non-blank Provider is the fallback,
// and an absent or blank value for both leaves the provider unset.
func TestModelProvider_OwnerFirstThenProvider(t *testing.T) {
	cases := []struct {
		name     string
		owner    *string
		provider *string
		want     string
	}{
		{
			name:  "when only owner is set it should be used",
			owner: strp("data-science-team"),
			want:  "data-science-team",
		},
		{
			name:     "when owner and provider are both set it should prefer owner",
			owner:    strp("data-science-team"),
			provider: strp("platform-team"),
			want:     "data-science-team",
		},
		{
			name:     "when owner is nil it should fall back to provider",
			owner:    nil,
			provider: strp("platform-team"),
			want:     "platform-team",
		},
		{
			name:     "when owner is empty it should fall back to provider",
			owner:    strp(""),
			provider: strp("platform-team"),
			want:     "platform-team",
		},
		{
			name:     "when owner is whitespace it should fall back to provider",
			owner:    strp("   \t "),
			provider: strp("platform-team"),
			want:     "platform-team",
		},
		{
			name:     "when owner is set it should be trimmed",
			owner:    strp("  data-science-team  "),
			provider: strp("platform-team"),
			want:     "data-science-team",
		},
		{
			name:     "when provider is used it should be trimmed",
			owner:    nil,
			provider: strp("  platform-team\n"),
			want:     "platform-team",
		},
		{
			name:     "when both are nil it should be unset",
			owner:    nil,
			provider: nil,
			want:     "",
		},
		{
			name:     "when both are blank it should be unset",
			owner:    strp(" "),
			provider: strp("\t"),
			want:     "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := makeModel("1", "my-model")
			model.Owner = tc.owner
			model.Provider = tc.provider

			if got := modelProvider(model); got != tc.want {
				t.Errorf("modelProvider() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestToItem_ProviderPropagation verifies that the resolved provider reaches
// the published CatalogItem and stays unset when no provider is available.
func TestToItem_ProviderPropagation(t *testing.T) {
	cases := []struct {
		name     string
		owner    *string
		provider *string
		want     *string
	}{
		{
			name:  "when owner is set it should populate spec.provider",
			owner: strp("data-science-team"),
			want:  strp("data-science-team"),
		},
		{
			name:     "when only provider is set it should populate spec.provider",
			provider: strp("platform-team"),
			want:     strp("platform-team"),
		},
		{
			name: "when neither is set it should leave spec.provider nil",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := makeCollectedModel("1", "iris-edge", "2", "1.0.0", goodURI)
			model.model.Owner = tc.owner
			model.model.Provider = tc.provider

			_, items, err := toSnapshot("my-catalog", []collectedModel{model})
			if err != nil {
				t.Fatalf("toSnapshot() error: %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("expected 1 item, got %d", len(items))
			}

			got := items[0].Spec.Provider
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("spec.provider = %q, want nil", *got)
			case tc.want != nil && got == nil:
				t.Errorf("spec.provider = nil, want %q", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Errorf("spec.provider = %q, want %q", *got, *tc.want)
			}
		})
	}
}

// TestToSnapshot_UsesAPIKindConstants verifies that emitted resources carry
// the Kind values defined by the Flightctl API package.
func TestToSnapshot_UsesAPIKindConstants(t *testing.T) {
	catalogs, items, err := toSnapshot("my-catalog", []collectedModel{
		makeCollectedModel("1", "iris-edge", "2", "1.0.0", goodURI),
	})
	if err != nil {
		t.Fatalf("toSnapshot() error: %v", err)
	}

	if catalogs[0].Kind != apiv1alpha1.CatalogKind {
		t.Errorf("catalog kind = %q, want %q", catalogs[0].Kind, apiv1alpha1.CatalogKind)
	}
	if items[0].Kind != apiv1alpha1.CatalogItemKind {
		t.Errorf("item kind = %q, want %q", items[0].Kind, apiv1alpha1.CatalogItemKind)
	}
}
