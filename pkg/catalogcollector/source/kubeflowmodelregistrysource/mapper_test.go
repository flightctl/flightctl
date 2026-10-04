package kubeflowmodelregistrysource

import (
	"testing"

	mrapi "github.com/kubeflow/hub/pkg/openapi"
)

// helpers -----------------------------------------------------------------

func strp(s string) *string { return &s }

func artifactStatep(s mrapi.ArtifactState) *mrapi.ArtifactState { return &s }

func modelArtifact(uri string, state *mrapi.ArtifactState) *mrapi.ModelArtifact {
	t := "model-artifact"
	ma := &mrapi.ModelArtifact{
		Uri:          strp(uri),
		ArtifactType: &t,
		State:        state,
	}
	return ma
}

func wrapArtifact(ma *mrapi.ModelArtifact) mrapi.Artifact {
	return mrapi.Artifact{ModelArtifact: ma}
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

// --- isValidSemVer tests ---------------------------------------------------

func TestIsValidSemVer(t *testing.T) {
	valid := []string{
		"1.0.0",
		"0.1.0",
		"1.2.3-alpha.1",
		"1.2.3+build.1",
		"1.2.3-alpha.1+build",
		"10.20.30",
	}
	invalid := []string{
		"v1.0.0",  // leading v not accepted
		"1.0",     // missing patch
		"1",       // missing minor + patch
		"1.0.0.0", // extra component
		"latest",  // not semver
		"",
	}

	for _, s := range valid {
		if !isValidSemVer(s) {
			t.Errorf("isValidSemVer(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if isValidSemVer(s) {
			t.Errorf("isValidSemVer(%q) = true, want false", s)
		}
	}
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
	if string(item.Spec.Versions[0].Version) != "1.0.0" {
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
		if string(v.Version) != want[i] {
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
		if string(v.Version) != want[i] {
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
		if string(v.Version) != want[i] {
			t.Errorf("order1: version[%d] = %q, want %q", i, v.Version, want[i])
		}
	}
	for i, v := range v2 {
		if string(v.Version) != want[i] {
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
