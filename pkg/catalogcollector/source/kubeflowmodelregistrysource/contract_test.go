package kubeflowmodelregistrysource

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mrapi "github.com/kubeflow/hub/pkg/openapi"
)

// fixtureDir points to sanitized JSON fixtures that represent the subset of
// the RHOAI 3.5.1 Model Registry v1alpha3 API response shape used by the
// contract tests. The fixtures contain no tokens, credentials, cookies,
// internal headers, or environment-specific data.
const fixtureDir = "testdata"

func readFixture[T any](t *testing.T, name string) T {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parsing fixture %s: %v", name, err)
	}
	return v
}

// fixtureClient serves sanitized API responses from the fixture directory.
// It represents a one-model, one-version, one-artifact registry.
type fixtureClient struct {
	models    *mrapi.RegisteredModelList
	version   *mrapi.ModelVersion
	artifacts *mrapi.ArtifactList
}

func newFixtureClient(t *testing.T) *fixtureClient {
	t.Helper()
	return &fixtureClient{
		models:    readFixture[*mrapi.RegisteredModelList](t, "registered-models-list.json"),
		version:   readFixture[*mrapi.ModelVersion](t, "model-version.json"),
		artifacts: readFixture[*mrapi.ArtifactList](t, "model-version-artifacts.json"),
	}
}

func (f *fixtureClient) ListRegisteredModels(_ context.Context, _ string) (*mrapi.RegisteredModelList, error) {
	return f.models, nil
}

func (f *fixtureClient) ListModelVersions(_ context.Context, _ string, _ string) (*mrapi.ModelVersionList, error) {
	// Wrap the single version object into a list.
	return &mrapi.ModelVersionList{Items: []mrapi.ModelVersion{*f.version}}, nil
}

func (f *fixtureClient) ListModelArtifacts(_ context.Context, _ string, _ string) (*mrapi.ArtifactList, error) {
	return f.artifacts, nil
}

func (f *fixtureClient) PreflightRegisteredModels(_ context.Context) error { return nil }
func (f *fixtureClient) PreflightModelVersions(_ context.Context) error    { return nil }

// TestContractFixtures verifies the exact mapping output for the sanitized
// API response fixtures. It validates catalog identity, item naming, artifact
// URI splitting, version and channel mapping, revision format, and description
// propagation.
func TestContractFixtures(t *testing.T) {
	client := newFixtureClient(t)
	s := &source{
		catalog:           "rhoai-models",
		collectionTimeout: 30e9, // 30 seconds in nanoseconds
		client:            client,
		log:               testLogger(),
	}

	snap, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() with real fixtures: %v", err)
	}

	// --- Catalog ---
	if len(snap.Catalogs) != 1 {
		t.Fatalf("expected 1 catalog, got %d", len(snap.Catalogs))
	}
	catalogName := ptrStr(snap.Catalogs[0].Metadata.Name)
	if catalogName != "rhoai-models" {
		t.Errorf("catalog name = %q, want %q", catalogName, "rhoai-models")
	}

	// --- CatalogItems ---
	// The fixture has one model: iris-edge (id=1, LIVE).
	if len(snap.CatalogItems) != 1 {
		t.Fatalf("expected 1 item, got %d", len(snap.CatalogItems))
	}
	item := snap.CatalogItems[0]

	// The model name "iris-edge" already satisfies DNS normalization.
	if ptrStr(item.Metadata.Name) != "iris-edge" {
		t.Errorf("item.metadata.name = %q, want %q", ptrStr(item.Metadata.Name), "iris-edge")
	}
	if item.Metadata.Catalog != "rhoai-models" {
		t.Errorf("item.metadata.catalog = %q, want %q", item.Metadata.Catalog, "rhoai-models")
	}

	// --- Spec: displayName preserved verbatim ---
	if ptrStr(item.Spec.DisplayName) != "iris-edge" {
		t.Errorf("displayName = %q, want %q", ptrStr(item.Spec.DisplayName), "iris-edge")
	}

	// --- Artifact: versionless OCI repository ---
	const wantRepo = "registry.example.com/models/modelcar-iris"
	if len(item.Spec.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(item.Spec.Artifacts))
	}
	if item.Spec.Artifacts[0].Uri != wantRepo {
		t.Errorf("artifact.uri = %q, want %q", item.Spec.Artifacts[0].Uri, wantRepo)
	}

	// --- Version ---
	if len(item.Spec.Versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(item.Spec.Versions))
	}
	v := item.Spec.Versions[0]
	if v.Version != "1.0.0" {
		t.Errorf("version = %q, want %q", v.Version, "1.0.0")
	}
	const wantDigest = "sha256:bd62d86fc106e620c60f3905ebb1f8d102dc2470d049d845eada1c9b823801ba"
	if v.References["container"] != wantDigest {
		t.Errorf("references[container] = %q, want %q", v.References["container"], wantDigest)
	}
	if len(v.Channels) != 1 || v.Channels[0] != "stable" {
		t.Errorf("channels = %v, want [stable]", v.Channels)
	}

	// --- Revision: non-empty 16 hex chars ---
	if len(snap.Revision) != 16 {
		t.Errorf("revision length = %d, want 16", len(snap.Revision))
	}

	// --- Description propagated ---
	if ptrStr(item.Spec.ShortDescription) != "Iris classifier model for edge inference" {
		t.Errorf("shortDescription = %q", ptrStr(item.Spec.ShortDescription))
	}
}

// TestContractFixtures_EmptyList verifies that an empty registered-models
// response produces a valid snapshot with zero CatalogItems.
func TestContractFixtures_EmptyList(t *testing.T) {
	emptyList := readFixture[*mrapi.RegisteredModelList](t, "empty-list.json")
	client := &fixtureClient{models: emptyList}

	s := &source{
		catalog:           "rhoai-models",
		collectionTimeout: 30e9,
		client:            client,
		log:               testLogger(),
	}

	snap, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() with empty fixture: %v", err)
	}
	if len(snap.CatalogItems) != 0 {
		t.Errorf("expected 0 items, got %d", len(snap.CatalogItems))
	}
	if snap.Revision == "" {
		t.Error("expected non-empty revision even for empty snapshot")
	}
}
