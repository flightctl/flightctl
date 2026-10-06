package kubeflowmodelregistrysource

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/pollsource"
	mrapi "github.com/kubeflow/hub/pkg/openapi"
)

// These tests exercise artifact decoding through the real generated SDK type
// (mrapi.ArtifactList) rather than hand-constructed Go values, because the
// discriminator handling that decides whether an artifact is visible to the
// mapper lives entirely in the generated Artifact.UnmarshalJSON.
//
// Artifact is a polymorphic oneOf keyed on the "artifactType" discriminator.
// When that field is missing or carries an unknown value, the generated
// UnmarshalJSON matches none of the variants, returns a nil error, and leaves
// every variant pointer nil. The resulting Artifact value is indistinguishable
// from an empty one, so extractModelArtifact reports "not a model artifact"
// and the record disappears before isEligibleArtifact is ever consulted.

// decodeArtifactList decodes a raw Model Registry artifact-list payload with
// the generated SDK types.
func decodeArtifactList(t *testing.T, payload string) *mrapi.ArtifactList {
	t.Helper()

	var list mrapi.ArtifactList
	if err := json.Unmarshal([]byte(payload), &list); err != nil {
		t.Fatalf("decoding artifact list with the generated SDK: %v", err)
	}
	return &list
}

const validArtifactJSON = `{
  "id": "10",
  "name": "iris-modelcar",
  "artifactType": "model-artifact",
  "state": "LIVE",
  "uri": "` + goodURI + `"
}`

// malformedArtifactJSON is byte-identical to validArtifactJSON except that the
// required "artifactType" discriminator is absent.
const malformedArtifactJSON = `{
  "id": "11",
  "name": "iris-modelcar-malformed",
  "state": "LIVE",
  "uri": "` + goodURI + `"
}`

func artifactListJSON(items ...string) string {
	return `{"items":[` + strings.Join(items, ",") + `],"nextPageToken":"","pageSize":100,"size":1}`
}

// TestArtifactDecoding_MissingArtifactTypeYieldsEmptyVariant proves, at the
// SDK level, that a record missing the discriminator decodes without error
// into an Artifact whose variant pointers are all nil.
func TestArtifactDecoding_MissingArtifactTypeYieldsEmptyVariant(t *testing.T) {
	list := decodeArtifactList(t, artifactListJSON(malformedArtifactJSON))

	if len(list.Items) != 1 {
		t.Fatalf("decoded %d items, want 1", len(list.Items))
	}

	artifact := list.Items[0]
	if artifact.ModelArtifact != nil {
		t.Error("ModelArtifact was populated without an artifactType discriminator")
	}
	if artifact.DocArtifact != nil || artifact.DataSet != nil ||
		artifact.Metric != nil || artifact.Parameter != nil {
		t.Error("an unexpected oneOf variant was populated")
	}

	if _, ok := extractModelArtifact(artifact); ok {
		t.Error("extractModelArtifact accepted an artifact with no decoded variant")
	}
}

// TestArtifactDecoding_UnknownArtifactTypeYieldsEmptyVariant covers an
// artifactType the SDK does not know about. It behaves the same as an absent
// discriminator.
func TestArtifactDecoding_UnknownArtifactTypeYieldsEmptyVariant(t *testing.T) {
	unknown := `{"id":"12","artifactType":"future-artifact","state":"LIVE","uri":"` + goodURI + `"}`
	list := decodeArtifactList(t, artifactListJSON(unknown))

	if len(list.Items) != 1 {
		t.Fatalf("decoded %d items, want 1", len(list.Items))
	}
	if _, ok := extractModelArtifact(list.Items[0]); ok {
		t.Error("extractModelArtifact accepted an unknown artifactType")
	}
}

// newArtifactSource builds a source whose single model version returns the
// supplied decoded artifact page.
func newArtifactSource(
	t *testing.T,
	list *mrapi.ArtifactList,
) (*source, *countingConsumer) {
	t.Helper()

	client := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{makeModel("1", "iris-edge")}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{makeVersion("2", "1.0.0")}}},
		artifacts: map[string][][]mrapi.Artifact{
			"2": {list.Items},
		},
	}

	consumer := newCountingConsumer(nil)

	return &source{
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
		next:              consumer,
	}, consumer
}

// runOneCycle drives the real poller for a single cycle and reports whether a
// snapshot reached the downstream consumer.
func runOneCycle(t *testing.T, s *source) (delivered int) {
	t.Helper()

	consumer, ok := s.next.(*countingConsumer)
	if !ok {
		t.Fatalf("source consumer is %T, want *countingConsumer", s.next)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.poller = pollsource.NewHelper(
		"artifact-decode",
		10*time.Millisecond,
		pollsource.BackoffConfig{
			InitialInterval:     util.Duration(5 * time.Millisecond),
			MaxInterval:         util.Duration(10 * time.Millisecond),
			Multiplier:          2,
			RandomizationFactor: 0,
		},
		s.log,
		nil,
		func(time.Duration) time.Duration { return 0 },
	)

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	select {
	case <-consumer.called:
		cancel()
	case <-time.After(250 * time.Millisecond):
		// No snapshot was delivered within the window; stop the loop and
		// report zero deliveries.
		cancel()
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}

	return consumer.callCount()
}

// TestCollect_EntirelyMalformedArtifactPage_FailsClosed verifies that a page
// whose every artifact is missing its discriminator fails the complete
// collection cycle and delivers no snapshot downstream.
func TestCollect_EntirelyMalformedArtifactPage_FailsClosed(t *testing.T) {
	list := decodeArtifactList(t, artifactListJSON(
		malformedArtifactJSON,
		`{"id":"12","state":"LIVE","uri":"`+goodURI+`"}`,
	))

	s, _ := newArtifactSource(t, list)

	snapshot, err := s.collect(context.Background())
	if err == nil {
		t.Fatalf("collect() succeeded with %d items, want the cycle to fail closed",
			len(snapshot.CatalogItems))
	}
	if snapshot != nil {
		t.Error("collect() returned a partial snapshot alongside an error")
	}
	if !strings.Contains(err.Error(), "no eligible model-artifact") {
		t.Errorf("error %q does not explain the missing artifact", err.Error())
	}
	for _, want := range []string{"id=1", "iris-edge", "id=2", "1.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}

	if delivered := runOneCycle(t, s); delivered != 0 {
		t.Errorf("consumer received %d snapshots, want 0", delivered)
	}
}

// TestCollect_MixedValidAndMalformedArtifacts_CurrentBehavior documents what
// happens when one page mixes a well-formed model artifact with a record whose
// artifactType discriminator is missing.
//
// FINDING: the malformed record is dropped silently. Because exactly one
// eligible candidate remains, the cycle succeeds, a snapshot is published, and
// the only trace of the dropped record is its absence. Nothing is logged and
// no metric distinguishes this from a page that genuinely contained a single
// artifact. An operator whose registry starts returning records the SDK cannot
// decode would see a silently shrinking catalog rather than an error.
//
// Changing this is deliberately not part of this change: the stated scope
// forbids altering how malformed selected records are handled before the
// behaviour and a proposed fix have been reported. The test therefore pins
// today's behaviour so the gap is visible and a later change has a concrete
// assertion to flip.
func TestCollect_MixedValidAndMalformedArtifacts_CurrentBehavior(t *testing.T) {
	list := decodeArtifactList(t, artifactListJSON(
		validArtifactJSON,
		malformedArtifactJSON,
	))

	if len(list.Items) != 2 {
		t.Fatalf("decoded %d items, want 2", len(list.Items))
	}
	if list.Items[0].ModelArtifact == nil {
		t.Fatal("the well-formed artifact did not decode")
	}
	if list.Items[1].ModelArtifact != nil {
		t.Fatal("the malformed artifact unexpectedly decoded")
	}

	s, _ := newArtifactSource(t, list)

	snapshot, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf(
			"collect() now rejects a mixed page (%v); the silent-drop gap "+
				"appears to have been closed, so update this test",
			err,
		)
	}
	if len(snapshot.CatalogItems) != 1 {
		t.Fatalf("collect() produced %d items, want 1", len(snapshot.CatalogItems))
	}

	versions := snapshot.CatalogItems[0].Spec.Versions
	if len(versions) != 1 {
		t.Fatalf("item has %d versions, want 1", len(versions))
	}
	if got := versions[0].References["container"]; got != goodDigest {
		t.Errorf("published digest = %q, want %q", got, goodDigest)
	}

	// The snapshot does reach the downstream consumer.
	if delivered := runOneCycle(t, s); delivered < 1 {
		t.Errorf("consumer received %d snapshots, want at least 1", delivered)
	}
}

// TestCollect_MalformedArtifactAlongsideSecondValid_FailsOnAmbiguity verifies
// that dropping an undecodable record does not mask the ambiguity rule: two
// decodable eligible artifacts still fail the cycle.
func TestCollect_MalformedArtifactAlongsideSecondValid_FailsOnAmbiguity(t *testing.T) {
	secondValid := `{
	  "id": "13",
	  "artifactType": "model-artifact",
	  "state": "LIVE",
	  "uri": "` + goodURI + `"
	}`

	list := decodeArtifactList(t, artifactListJSON(
		validArtifactJSON,
		malformedArtifactJSON,
		secondValid,
	))

	s, _ := newArtifactSource(t, list)

	if _, err := s.collect(context.Background()); err == nil {
		t.Fatal("collect() succeeded with two eligible artifacts, want an ambiguity error")
	} else if !strings.Contains(err.Error(), "exactly one is required") {
		t.Errorf("error %q does not report the ambiguity", err.Error())
	}
}

// TestCollect_MalformedArtifactWithInvalidURI_StaysUndecodable confirms that
// the missing discriminator short-circuits before URI validation, so an
// otherwise actionable data error is not reported either.
func TestCollect_MalformedArtifactWithInvalidURI_StaysUndecodable(t *testing.T) {
	malformedWithBadURI := `{"id":"14","state":"LIVE","uri":"registry.example.com/no-digest"}`

	list := decodeArtifactList(t, artifactListJSON(
		validArtifactJSON,
		malformedWithBadURI,
	))

	s, _ := newArtifactSource(t, list)

	snapshot, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf(
			"collect() now surfaces the undecodable record (%v); update this test",
			err,
		)
	}
	if len(snapshot.CatalogItems) != 1 {
		t.Fatalf("collect() produced %d items, want 1", len(snapshot.CatalogItems))
	}
}

// TestCollect_MalformedArtifactDoesNotBreakSnapshotValidation verifies that
// the published snapshot still satisfies the pipeline's validation contract.
func TestCollect_MalformedArtifactDoesNotBreakSnapshotValidation(t *testing.T) {
	list := decodeArtifactList(t, artifactListJSON(
		validArtifactJSON,
		malformedArtifactJSON,
	))

	s, _ := newArtifactSource(t, list)

	snapshot, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}
	if err := catalogcollector.ValidateSnapshot(snapshot); err != nil {
		t.Errorf("ValidateSnapshot() = %v, want nil", err)
	}
}
