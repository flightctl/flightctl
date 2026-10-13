package kubeflowmodelregistrysource

import (
	"context"
	"encoding/json"
	"fmt"
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
// from an empty one, so it cannot be told apart from a recognized non-model
// artifact by content alone.
//
// Such a record used to be dropped the same way a DocArtifact or a DataSet is
// dropped. That silently lost registry data: the version still published,
// carrying only the artifacts the SDK happened to understand, and nothing in
// the snapshot, the logs, or the metrics recorded the loss. The collection
// cycle now fails closed instead, so an undecodable artifact surfaces as an
// error rather than as missing data. Recognized non-model artifacts are still
// excluded normally.

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

// unknownDiscriminatorArtifactJSON carries an "artifactType" value that the
// SDK's oneOf mapping does not know, which decodes to the same empty value as
// a missing discriminator.
const unknownDiscriminatorArtifactJSON = `{
  "id": "12",
  "name": "iris-modelcar-future",
  "artifactType": "future-artifact",
  "state": "LIVE",
  "uri": "` + goodURI + `"
}`

// Recognized non-model artifacts. These decode into a named oneOf variant, so
// the source can tell they are not model artifacts and exclude them.
const (
	docArtifactJSON = `{
	  "id": "20",
	  "name": "iris-model-card",
	  "artifactType": "doc-artifact",
	  "state": "LIVE",
	  "uri": "https://docs.example.com/iris/model-card.md"
	}`

	dataSetArtifactJSON = `{
	  "id": "21",
	  "name": "iris-training-set",
	  "artifactType": "dataset-artifact",
	  "state": "LIVE",
	  "uri": "s3://datasets.example.com/iris/train"
	}`

	metricArtifactJSON = `{
	  "id": "22",
	  "name": "iris-accuracy",
	  "artifactType": "metric",
	  "state": "LIVE",
	  "value": 0.97
	}`

	parameterArtifactJSON = `{
	  "id": "23",
	  "name": "iris-learning-rate",
	  "artifactType": "parameter",
	  "state": "LIVE",
	  "value": "0.01"
	}`
)

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
	if hasDecodedVariant(artifact) {
		t.Error("hasDecodedVariant reported a variant for an undecodable artifact")
	}
}

// TestArtifactDecoding_UnknownArtifactTypeYieldsEmptyVariant covers an
// artifactType the SDK does not know about. It behaves the same as an absent
// discriminator.
func TestArtifactDecoding_UnknownArtifactTypeYieldsEmptyVariant(t *testing.T) {
	list := decodeArtifactList(t, artifactListJSON(unknownDiscriminatorArtifactJSON))

	if len(list.Items) != 1 {
		t.Fatalf("decoded %d items, want 1", len(list.Items))
	}
	if _, ok := extractModelArtifact(list.Items[0]); ok {
		t.Error("extractModelArtifact accepted an unknown artifactType")
	}
	if hasDecodedVariant(list.Items[0]) {
		t.Error("hasDecodedVariant reported a variant for an unknown artifactType")
	}
}

// TestArtifactDecoding_RecognizedNonModelVariants verifies that the non-model
// artifact types the registry can legitimately return do decode into a named
// variant, so they are distinguishable from undecodable records.
func TestArtifactDecoding_RecognizedNonModelVariants(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		check   func(mrapi.Artifact) bool
	}{
		{"doc-artifact", docArtifactJSON, func(a mrapi.Artifact) bool { return a.DocArtifact != nil }},
		{"dataset-artifact", dataSetArtifactJSON, func(a mrapi.Artifact) bool { return a.DataSet != nil }},
		{"metric", metricArtifactJSON, func(a mrapi.Artifact) bool { return a.Metric != nil }},
		{"parameter", parameterArtifactJSON, func(a mrapi.Artifact) bool { return a.Parameter != nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := decodeArtifactList(t, artifactListJSON(tc.payload))
			if len(list.Items) != 1 {
				t.Fatalf("decoded %d items, want 1", len(list.Items))
			}

			artifact := list.Items[0]
			if !tc.check(artifact) {
				t.Fatalf("%s did not decode into its own variant", tc.name)
			}
			if !hasDecodedVariant(artifact) {
				t.Errorf("hasDecodedVariant() = false for a recognized %s", tc.name)
			}
			if _, ok := extractModelArtifact(artifact); ok {
				t.Errorf("extractModelArtifact accepted a %s", tc.name)
			}
		})
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

// pagedArtifactClient serves one model, one version, and a multi-page artifact
// list. The package-level fakeRegistryClient only ever returns the first
// artifact page, so paginated artifact coverage needs its own client.
type pagedArtifactClient struct {
	pages [][]mrapi.Artifact
}

var _ registryClient = (*pagedArtifactClient)(nil)

func (c *pagedArtifactClient) ListRegisteredModels(
	_ context.Context,
	_ string,
) (*mrapi.RegisteredModelList, error) {
	return &mrapi.RegisteredModelList{
		Items: []mrapi.RegisteredModel{makeModel("1", "iris-edge")},
	}, nil
}

func (c *pagedArtifactClient) ListModelVersions(
	_ context.Context,
	_ string,
	_ string,
) (*mrapi.ModelVersionList, error) {
	return &mrapi.ModelVersionList{
		Items: []mrapi.ModelVersion{makeVersion("2", "1.0.0")},
	}, nil
}

func (c *pagedArtifactClient) ListModelArtifacts(
	_ context.Context,
	_ string,
	token string,
) (*mrapi.ArtifactList, error) {
	index := 0
	if token != "" {
		if _, err := fmt.Sscanf(token, "artifact-page-%d", &index); err != nil {
			return nil, fmt.Errorf("unexpected nextPageToken %q: %w", token, err)
		}
	}
	if index < 0 || index >= len(c.pages) {
		return nil, fmt.Errorf("nextPageToken %q is out of range", token)
	}

	next := ""
	if index+1 < len(c.pages) {
		next = fmt.Sprintf("artifact-page-%d", index+1)
	}

	return &mrapi.ArtifactList{
		Items:         c.pages[index],
		NextPageToken: next,
	}, nil
}

func (c *pagedArtifactClient) PreflightRegisteredModels(_ context.Context) error { return nil }
func (c *pagedArtifactClient) PreflightModelVersions(_ context.Context) error    { return nil }

// newPagedArtifactSource builds a source whose single model version returns
// the supplied artifact pages in order.
func newPagedArtifactSource(
	t *testing.T,
	pages ...*mrapi.ArtifactList,
) (*source, *countingConsumer) {
	t.Helper()

	items := make([][]mrapi.Artifact, 0, len(pages))
	for _, page := range pages {
		items = append(items, page.Items)
	}

	consumer := newCountingConsumer(nil)

	return &source{
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            &pagedArtifactClient{pages: items},
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

	// Observing the collection attempt directly removes the need to wait out a
	// fixed window before concluding that nothing was delivered.
	failedAttempt := make(chan struct{}, 1)
	s.poller.OnCollect = func(_ time.Duration, err error) {
		if err == nil {
			return
		}
		select {
		case failedAttempt <- struct{}{}:
		default:
		}
	}

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	select {
	case <-consumer.called:
		// A snapshot was delivered downstream.
		cancel()
	case <-failedAttempt:
		// The collection failed, so nothing can reach the consumer.
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("no collection attempt completed within 5s")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}

	return consumer.callCount()
}

// assertFailsClosed asserts the complete fail-closed contract for an
// undecodable artifact: collect() reports an error that identifies the model
// and the version, returns no snapshot, and nothing reaches the downstream
// consumer when the real poller drives a cycle.
func assertFailsClosed(t *testing.T, s *source) {
	t.Helper()

	snapshot, err := s.collect(context.Background())
	if err == nil {
		t.Fatalf(
			"collect() succeeded with %d items, want the cycle to fail closed",
			len(snapshot.CatalogItems),
		)
	}
	if snapshot != nil {
		t.Error("collect() returned a partial snapshot alongside an error")
	}

	if !strings.Contains(err.Error(), "decoded into no known artifact type") {
		t.Errorf("error %q does not explain the undecodable artifact", err.Error())
	}
	// The error must name the model and the version so an operator can find
	// the offending record in the registry.
	for _, want := range []string{"id=1", "iris-edge", "id=2", "1.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}

	if delivered := runOneCycle(t, s); delivered != 0 {
		t.Errorf("consumer received %d snapshots, want 0", delivered)
	}
}

// TestCollect_ValidArtifactPlusMissingArtifactType_FailsClosed verifies that a
// well-formed model artifact does not make a page containing a record with no
// artifactType acceptable.
func TestCollect_ValidArtifactPlusMissingArtifactType_FailsClosed(t *testing.T) {
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
	if hasDecodedVariant(list.Items[1]) {
		t.Fatal("the malformed artifact unexpectedly decoded into a variant")
	}

	s, _ := newArtifactSource(t, list)
	assertFailsClosed(t, s)
}

// TestCollect_ValidArtifactPlusUnknownDiscriminator_FailsClosed verifies that
// an artifactType the SDK does not recognize is rejected rather than silently
// excluded alongside a valid model artifact.
func TestCollect_ValidArtifactPlusUnknownDiscriminator_FailsClosed(t *testing.T) {
	list := decodeArtifactList(t, artifactListJSON(
		validArtifactJSON,
		unknownDiscriminatorArtifactJSON,
	))

	s, _ := newArtifactSource(t, list)
	assertFailsClosed(t, s)
}

// TestCollect_UndecodableArtifactOnLaterPage_FailsClosed verifies that the
// check covers every page, not just the first one. The first page contains
// only a valid model artifact, so a per-page decision would publish.
func TestCollect_UndecodableArtifactOnLaterPage_FailsClosed(t *testing.T) {
	firstPage := decodeArtifactList(t, artifactListJSON(validArtifactJSON))
	secondPage := decodeArtifactList(t, artifactListJSON(docArtifactJSON))
	thirdPage := decodeArtifactList(t, artifactListJSON(malformedArtifactJSON))

	s, _ := newPagedArtifactSource(t, firstPage, secondPage, thirdPage)
	assertFailsClosed(t, s)
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
	assertFailsClosed(t, s)
}

// TestCollect_UndecodableArtifactWithInvalidURI_FailsClosed confirms that a
// record which is both undecodable and carries an unusable URI is reported
// rather than dropped. The missing discriminator short-circuits before URI
// validation, so the decode failure is the error that surfaces.
func TestCollect_UndecodableArtifactWithInvalidURI_FailsClosed(t *testing.T) {
	malformedWithBadURI := `{"id":"14","state":"LIVE","uri":"registry.example.com/no-digest"}`

	list := decodeArtifactList(t, artifactListJSON(
		validArtifactJSON,
		malformedWithBadURI,
	))

	s, _ := newArtifactSource(t, list)
	assertFailsClosed(t, s)
}

// TestCollect_UndecodableArtifactWithTwoEligible_FailsClosed verifies that an
// undecodable record is reported even when the page would otherwise fail the
// exactly-one-eligible-artifact rule: the decode failure is detected while
// iterating and takes precedence over the ambiguity verdict, which is only
// reached once every artifact has been classified.
func TestCollect_UndecodableArtifactWithTwoEligible_FailsClosed(t *testing.T) {
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
	assertFailsClosed(t, s)
}

// TestCollect_ValidArtifactWithRecognizedNonModelArtifacts_Succeeds verifies
// that the stricter check does not over-reject: artifacts that decode into a
// known non-model variant are still excluded, leaving exactly one eligible
// model artifact, and the resulting snapshot is published.
func TestCollect_ValidArtifactWithRecognizedNonModelArtifacts_Succeeds(t *testing.T) {
	list := decodeArtifactList(t, artifactListJSON(
		docArtifactJSON,
		validArtifactJSON,
		dataSetArtifactJSON,
		metricArtifactJSON,
		parameterArtifactJSON,
	))

	if len(list.Items) != 5 {
		t.Fatalf("decoded %d items, want 5", len(list.Items))
	}
	for i, artifact := range list.Items {
		if !hasDecodedVariant(artifact) {
			t.Fatalf("item %d did not decode into any variant", i)
		}
	}

	s, _ := newArtifactSource(t, list)

	snapshot, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() error: %v", err)
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

	if err := catalogcollector.ValidateSnapshot(snapshot); err != nil {
		t.Errorf("ValidateSnapshot() = %v, want nil", err)
	}

	if delivered := runOneCycle(t, s); delivered < 1 {
		t.Errorf("consumer received %d snapshots, want at least 1", delivered)
	}
}

// TestCollect_RecognizedNonModelArtifactsOnly_FailsOnMissingModelArtifact
// verifies that the existing no-eligible-artifact rule still applies when a
// version carries only recognized non-model artifacts. That case must keep
// its own error rather than being reported as a decode failure.
func TestCollect_RecognizedNonModelArtifactsOnly_FailsOnMissingModelArtifact(t *testing.T) {
	list := decodeArtifactList(t, artifactListJSON(
		docArtifactJSON,
		dataSetArtifactJSON,
	))

	s, _ := newArtifactSource(t, list)

	snapshot, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("collect() succeeded without a model artifact, want an error")
	}
	if snapshot != nil {
		t.Error("collect() returned a partial snapshot alongside an error")
	}
	if !strings.Contains(err.Error(), "no eligible model-artifact") {
		t.Errorf("error %q does not report the missing model artifact", err.Error())
	}
	if strings.Contains(err.Error(), "decoded into no known artifact type") {
		t.Errorf("error %q misreports recognized artifacts as undecodable", err.Error())
	}
}
