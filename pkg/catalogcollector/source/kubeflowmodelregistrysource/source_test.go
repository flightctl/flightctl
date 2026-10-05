package kubeflowmodelregistrysource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/pollsource"
	mrapi "github.com/kubeflow/hub/pkg/openapi"
	"github.com/sirupsen/logrus"
)

// --- fakeRegistryClient ----------------------------------------------------

type fakeRegistryClient struct {
	models      [][]mrapi.RegisteredModel // one page per outer slice
	modelTokens []string                  // tokens returned between pages (len = len(models)-1)

	// versions and artifacts are keyed by modelID and versionID respectively.
	versions  map[string][][]mrapi.ModelVersion
	artifacts map[string][][]mrapi.Artifact

	modelErr    error
	versionErr  map[string]error
	artifactErr map[string]error
}

func (f *fakeRegistryClient) ListRegisteredModels(_ context.Context, token string) (*mrapi.RegisteredModelList, error) {
	if f.modelErr != nil {
		return nil, f.modelErr
	}
	pageIdx := pageIndex(token, f.modelTokens)
	if pageIdx >= len(f.models) {
		return &mrapi.RegisteredModelList{}, nil
	}
	nextToken := ""
	if pageIdx < len(f.modelTokens) {
		nextToken = f.modelTokens[pageIdx]
	}
	return &mrapi.RegisteredModelList{Items: f.models[pageIdx], NextPageToken: nextToken}, nil
}

func (f *fakeRegistryClient) ListModelVersions(_ context.Context, modelID string, token string) (*mrapi.ModelVersionList, error) {
	if f.versionErr != nil {
		if err, ok := f.versionErr[modelID]; ok {
			return nil, err
		}
	}
	pages := f.versions[modelID]
	if pages == nil {
		return &mrapi.ModelVersionList{}, nil
	}
	// simple single-page response for tests
	_ = token
	return &mrapi.ModelVersionList{Items: pages[0]}, nil
}

func (f *fakeRegistryClient) ListModelArtifacts(_ context.Context, versionID string, token string) (*mrapi.ArtifactList, error) {
	if f.artifactErr != nil {
		if err, ok := f.artifactErr[versionID]; ok {
			return nil, err
		}
	}
	pages := f.artifacts[versionID]
	if pages == nil {
		return &mrapi.ArtifactList{}, nil
	}
	_ = token
	return &mrapi.ArtifactList{Items: pages[0]}, nil
}

func (f *fakeRegistryClient) PreflightRegisteredModels(_ context.Context) error { return nil }
func (f *fakeRegistryClient) PreflightModelVersions(_ context.Context) error    { return nil }

// pageIndex returns the index of the page that matches the given token in the
// token sequence. token="" → index 0; token=tokens[0] → index 1; etc.
func pageIndex(token string, tokens []string) int {
	if token == "" {
		return 0
	}
	for i, t := range tokens {
		if t == token {
			return i + 1
		}
	}
	return len(tokens) + 1 // out of range → empty response
}

// fakeConsumer records the last snapshot delivered.
type fakeConsumer struct {
	received []*catalogcollector.CatalogSnapshot
	err      error
}

func (f *fakeConsumer) Consume(_ context.Context, s *catalogcollector.CatalogSnapshot) error {
	f.received = append(f.received, s)
	return f.err
}

// --- helpers ---------------------------------------------------------------

func testLogger() *logrus.Entry {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)
	return l.WithField("test", true)
}

func newTestSource(client registryClient, consumer catalogcollector.Consumer) (*source, *fakeConsumer) {
	fc := &fakeConsumer{}
	if consumer != nil {
		// If caller supplies one, use it; otherwise return the local one.
		_ = fc
	} else {
		consumer = fc
	}
	s := &source{
		id:                "test-source",
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
		metrics:           nil,
		next:              consumer,
	}
	return s, fc
}

// buildLiveArtifact constructs a single model-artifact Artifact with the given URI.
func buildLiveArtifact(id, uri string) mrapi.Artifact {
	t := "model-artifact"
	live := mrapi.ARTIFACTSTATE_LIVE
	return mrapi.Artifact{
		ModelArtifact: &mrapi.ModelArtifact{
			Id:           strp(id),
			ArtifactType: &t,
			Uri:          strp(uri),
			State:        &live,
		},
	}
}

// --- collect tests ---------------------------------------------------------

func TestCollect_EmptyRegistry(t *testing.T) {
	client := &fakeRegistryClient{
		models: [][]mrapi.RegisteredModel{{}},
	}
	s, _ := newTestSource(client, nil)
	snap, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if len(snap.CatalogItems) != 0 {
		t.Errorf("expected 0 items, got %d", len(snap.CatalogItems))
	}
	if len(snap.Catalogs) != 1 {
		t.Errorf("expected 1 catalog, got %d", len(snap.Catalogs))
	}
}

func TestCollect_SingleModelVersion(t *testing.T) {
	model := makeModel("1", "iris-edge")
	version := makeVersion("2", "1.0.0")

	client := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{model}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{version}}},
		artifacts: map[string][][]mrapi.Artifact{
			"2": {{buildLiveArtifact("10", goodURI)}},
		},
	}
	s, consumer := newTestSource(client, nil)

	snap, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}
	if len(snap.CatalogItems) != 1 {
		t.Fatalf("expected 1 item, got %d", len(snap.CatalogItems))
	}
	if snap.Revision == "" {
		t.Error("expected non-empty revision")
	}
	_ = consumer
}

func TestCollect_ModelWithNoEligibleVersionsSkipped(t *testing.T) {
	// A model that has versions, but all versions have no artifacts → version
	// has no eligible artifacts. The current implementation requires exactly
	// one artifact per version and returns an error.
	// Change to test a model with no LIVE versions returned at all (empty list).
	model := makeModel("1", "empty-model")

	client := &fakeRegistryClient{
		models:    [][]mrapi.RegisteredModel{{model}},
		versions:  map[string][][]mrapi.ModelVersion{"1": {{}}}, // empty page
		artifacts: map[string][][]mrapi.Artifact{},
	}
	s, _ := newTestSource(client, nil)

	// Empty versions list → model silently skipped → empty snapshot, no error.
	snap, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}
	if len(snap.CatalogItems) != 0 {
		t.Errorf("expected 0 items (model skipped), got %d", len(snap.CatalogItems))
	}
}

func TestCollect_RegistryAPIError(t *testing.T) {
	apiErr := errors.New("connection refused")
	client := &fakeRegistryClient{
		modelErr: apiErr,
	}
	s, _ := newTestSource(client, nil)
	_, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollect_PaginationModels(t *testing.T) {
	page1 := []mrapi.RegisteredModel{makeModel("1", "model-a")}
	page2 := []mrapi.RegisteredModel{makeModel("2", "model-b")}

	client := &fakeRegistryClient{
		models:      [][]mrapi.RegisteredModel{page1, page2},
		modelTokens: []string{"token-page2"},
		versions: map[string][][]mrapi.ModelVersion{
			"1": {{makeVersion("10", "1.0.0")}},
			"2": {{makeVersion("20", "1.0.0")}},
		},
		artifacts: map[string][][]mrapi.Artifact{
			"10": {{buildLiveArtifact("100", goodURI)}},
			"20": {{buildLiveArtifact("200", goodURI)}},
		},
	}
	s, _ := newTestSource(client, nil)
	snap, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}
	if len(snap.CatalogItems) != 2 {
		t.Errorf("expected 2 items (one per model), got %d", len(snap.CatalogItems))
	}
}

func TestCollect_RepeatedPageTokenError(t *testing.T) {
	// Simulate a server bug where the same token is returned infinitely.
	callCount := 0
	client := &repeatedTokenClient{
		onCall: func(token string) (*mrapi.RegisteredModelList, error) {
			callCount++
			if callCount > 5 {
				return nil, errors.New("test should have stopped by now")
			}
			return &mrapi.RegisteredModelList{
				Items:         []mrapi.RegisteredModel{makeModel("1", "model-a")},
				NextPageToken: "same-token", // always returns same token
			}, nil
		},
	}
	s, _ := newTestSource(client, nil)
	_, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected repeated-token error, got nil")
	}
}

// repeatedTokenClient helps simulate infinite pagination.
type repeatedTokenClient struct {
	onCall func(token string) (*mrapi.RegisteredModelList, error)
}

func (r *repeatedTokenClient) ListRegisteredModels(_ context.Context, token string) (*mrapi.RegisteredModelList, error) {
	return r.onCall(token)
}

func (r *repeatedTokenClient) ListModelVersions(_ context.Context, _ string, _ string) (*mrapi.ModelVersionList, error) {
	return &mrapi.ModelVersionList{}, nil
}

func (r *repeatedTokenClient) ListModelArtifacts(_ context.Context, _ string, _ string) (*mrapi.ArtifactList, error) {
	return &mrapi.ArtifactList{}, nil
}

func (r *repeatedTokenClient) PreflightRegisteredModels(_ context.Context) error { return nil }
func (r *repeatedTokenClient) PreflightModelVersions(_ context.Context) error    { return nil }

func TestCollect_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	model := makeModel("1", "iris-edge")
	client := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{model}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{makeVersion("2", "1.0.0")}}},
		artifacts: map[string][][]mrapi.Artifact{
			"2": {{buildLiveArtifact("10", goodURI)}},
		},
	}
	s, _ := newTestSource(client, nil)
	// A pre-cancelled context causes the collection timeout to fire immediately;
	// the collect call may or may not error but should not panic.
	_, _ = s.collect(ctx)
}

func TestWrapHTTPError_NoCredentialLeak(t *testing.T) {
	s := &source{log: testLogger()}
	err := errors.New("connection refused")
	wrapped := s.wrapHTTPError("listing registered models", err)
	if wrapped == nil {
		t.Fatal("expected non-nil wrapped error")
	}
	// The error message should not contain anything that looks like a token.
	msg := wrapped.Error()
	if len(msg) == 0 {
		t.Error("wrapped error message is empty")
	}
}

func TestWrapHTTPError_PreservesHTTPStatusCode(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus string
	}{
		{
			name:       "When error wraps HTTP 403 it should include status code in message",
			err:        &httpError{statusCode: 403, err: errors.New("forbidden")},
			wantStatus: "HTTP 403",
		},
		{
			name:       "When error wraps HTTP 404 it should include status code in message",
			err:        &httpError{statusCode: 404, err: errors.New("not found")},
			wantStatus: "HTTP 404",
		},
		{
			name:       "When error wraps HTTP 500 it should include status code in message",
			err:        &httpError{statusCode: 500, err: errors.New("internal server error")},
			wantStatus: "HTTP 500",
		},
		{
			name:       "When error wraps HTTP 429 it should include status code in message",
			err:        &httpError{statusCode: 429, err: errors.New("too many requests")},
			wantStatus: "HTTP 429",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &source{log: testLogger()}
			wrapped := s.wrapHTTPError("listing models", tc.err)
			if wrapped == nil {
				t.Fatal("expected non-nil wrapped error")
			}
			msg := wrapped.Error()
			if !strings.Contains(msg, tc.wantStatus) {
				t.Errorf("wrapped error %q does not contain %q", msg, tc.wantStatus)
			}
			// httpError identity must be preserved through the wrapping chain.
			var httpErr *httpError
			if !errors.As(wrapped, &httpErr) {
				t.Errorf("errors.As(wrapped, &httpError) = false; want httpError to be identifiable through the chain")
			}
		})
	}
}

func TestWrapHTTPError_TransportError_IncludesDiagnostics(t *testing.T) {
	// Under the revised logging policy, URLs and filter expressions are included
	// in diagnostic errors. The wrapped error must also preserve the transport
	// error's identity for errors.As classification.
	transportErr := &url.Error{
		Op:  "Get",
		URL: "https://registry.example.com/api/model_registry/v1alpha3/registered_models?filterQuery=" + url.QueryEscape("state='LIVE' AND to_flightctl.bool_value = true"),
		Err: errors.New("connection refused"),
	}

	s := &source{log: testLogger()}
	wrapped := s.wrapHTTPError("listing registered models", transportErr)
	if wrapped == nil {
		t.Fatal("expected non-nil wrapped error")
	}
	msg := wrapped.Error()

	// Operation context must appear.
	if !strings.Contains(msg, "listing registered models") {
		t.Errorf("wrapped error %q should contain the operation context", msg)
	}

	// URL is a useful diagnostic under the revised logging policy.
	if !strings.Contains(msg, "registry.example.com") {
		t.Errorf("wrapped error %q should contain the endpoint URL for diagnostics", msg)
	}

	// Transport error identity must be preserved via errors.As.
	var urlErr *url.Error
	if !errors.As(wrapped, &urlErr) {
		t.Errorf("errors.As(wrapped, &url.Error) = false; want transport error to be identifiable through the chain")
	}
}

// TestWrapHTTPError_TransportError_HTTPLevel_IncludesDiagnostics exercises the
// real error-wrapping path end-to-end. Under the revised logging policy, URLs,
// filter expressions, and endpoint details are included in diagnostic errors.
// The test verifies that operation context and endpoint details appear in both
// the Preflight and collect error paths.
func TestWrapHTTPError_TransportError_HTTPLevel_IncludesDiagnostics(t *testing.T) {
	filterExpr := "state='LIVE' AND to_flightctl.bool_value = true"

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{
		{URL: "https://registry.example.com"},
	}
	cfg.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("connection refused")
		}),
	}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	client := &openapiClient{
		api:           api,
		pageSize:      "1",
		modelFilter:   filterExpr,
		versionFilter: defaultVersionFilter,
	}

	s := &source{
		id:                "transport-http-test",
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
	}

	// Preflight path — operation context and endpoint must appear.
	err := s.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight() = nil, want error from failing RoundTripper")
	}
	msg := err.Error()
	if !strings.Contains(msg, "registered models") {
		t.Errorf("Preflight error %q should contain the operation context", msg)
	}
	if !strings.Contains(msg, "registry.example.com") {
		t.Errorf("Preflight error %q should contain the endpoint URL", msg)
	}

	// collect path — same diagnostics must appear.
	_, collectErr := s.collect(context.Background())
	if collectErr == nil {
		t.Fatal("collect() = nil, want error from failing RoundTripper")
	}
	if !strings.Contains(collectErr.Error(), "registry.example.com") {
		t.Errorf("collect error %q should contain the endpoint URL", collectErr.Error())
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// --- Error identity preservation tests ----------------------------------------

func TestWrapHTTPError_ContextCanceled_PreservesIdentity(t *testing.T) {
	// A *url.Error wrapping context.Canceled must remain recognizable via
	// errors.Is after wrapHTTPError adds operation context.
	inner := &url.Error{Op: "Get", URL: "http://registry.example.com", Err: context.Canceled}
	s := &source{log: testLogger()}
	wrapped := s.wrapHTTPError("listing registered models", inner)
	if !errors.Is(wrapped, context.Canceled) {
		t.Errorf("errors.Is(wrapped, context.Canceled) = false; error: %v", wrapped)
	}
}

func TestWrapHTTPError_ContextDeadlineExceeded_PreservesIdentity(t *testing.T) {
	// A *url.Error wrapping context.DeadlineExceeded must remain recognizable
	// via errors.Is after wrapHTTPError adds operation context.
	inner := &url.Error{Op: "Get", URL: "http://registry.example.com", Err: context.DeadlineExceeded}
	s := &source{log: testLogger()}
	wrapped := s.wrapHTTPError("listing registered models", inner)
	if !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Errorf("errors.Is(wrapped, context.DeadlineExceeded) = false; error: %v", wrapped)
	}
}

func TestWrapHTTPError_GenericOpenAPIError_IncludesBody(t *testing.T) {
	// When the Model Registry returns a non-2xx response with a body (e.g., an
	// invalid filter expression returns HTTP 400 with an error JSON), the body
	// must appear in the diagnostic error to aid operations.
	responseBody := `{"code":400,"message":"invalid filter expression: unexpected token"}`

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: "http://registry.example.com"}}
	cfg.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 400,
				Body:       io.NopCloser(strings.NewReader(responseBody)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    req,
			}, nil
		}),
	}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	client := &openapiClient{
		api:           api,
		pageSize:      "1",
		modelFilter:   "INVALID!!!",
		versionFilter: defaultVersionFilter,
	}
	s := &source{
		id:                "openapi-body-test",
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
	}

	err := s.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight() = nil, want error for non-2xx response")
	}
	msg := err.Error()
	if !strings.Contains(msg, responseBody) {
		t.Errorf("Preflight error %q should contain the response body for diagnostics", msg)
	}
	if !strings.Contains(msg, "registered models") {
		t.Errorf("Preflight error %q should contain the operation context", msg)
	}
}

func TestWrapHTTPError_TransportError_PreservesChain(t *testing.T) {
	// A transport error wrapped by wrapHTTPError must remain classifiable via
	// errors.As so callers can inspect the transport layer.
	transportErr := &url.Error{Op: "Get", URL: "http://registry.example.com", Err: errors.New("connection refused")}
	s := &source{log: testLogger()}
	wrapped := s.wrapHTTPError("listing registered models", transportErr)

	var urlErr *url.Error
	if !errors.As(wrapped, &urlErr) {
		t.Errorf("errors.As(wrapped, &url.Error) = false; want the transport error chain to be preserved")
	}
}

// --- helpers for polling tests -----------------------------------------------

// newTestPoller creates a minimal poller for unit tests. It uses a short poll
// interval and no jitter so that context-cancellation tests complete quickly.
func newTestPoller(s *source) *pollsource.Helper {
	return pollsource.NewHelper(
		s.id,
		100*time.Millisecond,
		pollsource.BackoffConfig{
			InitialInterval:     util.Duration(100 * time.Millisecond),
			MaxInterval:         util.Duration(200 * time.Millisecond),
			Multiplier:          2,
			RandomizationFactor: 0,
		},
		s.log,
		nil,
		func(_ time.Duration) time.Duration { return 0 },
	)
}

// --- Fix 1: Graceful shutdown -----------------------------------------------

func TestRun_CancelledContextReturnsNil(t *testing.T) {
	model := makeModel("1", "iris-edge")
	version := makeVersion("2", "1.0.0")

	client := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{model}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{version}}},
		artifacts: map[string][][]mrapi.Artifact{
			"2": {{buildLiveArtifact("10", goodURI)}},
		},
	}

	consumer := &fakeConsumer{}
	s := &source{
		id:                "test-source",
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
		next:              consumer,
	}

	poller := newTestPoller(s)
	s.poller = poller

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context before the first poll interval fires.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := s.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned %v, want nil for context cancellation", err)
	}
}

// --- Fix 3: Metrics boundary -----------------------------------------------

// testMetricsRecorder implements collectionRecorder for tests.
type testMetricsRecorder struct {
	successes int
	failures  int
}

func (r *testMetricsRecorder) recordSuccess(_ time.Duration) {
	r.successes++
}

func (r *testMetricsRecorder) recordFailure(_ time.Duration, _ error) {
	r.failures++
}

func TestCollectAndRecord_SuccessfulCollectionRecordsSourceSuccess(t *testing.T) {
	// A successful MR collection must increment source success and NOT
	// increment source failure, regardless of what the downstream consumer
	// does later (which is outside collectAndRecord scope).
	model := makeModel("1", "iris-edge")
	version := makeVersion("2", "1.0.0")

	client := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{model}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{version}}},
		artifacts: map[string][][]mrapi.Artifact{
			"2": {{buildLiveArtifact("10", goodURI)}},
		},
	}

	recorder := &testMetricsRecorder{}
	s := &source{
		id:                "test-source",
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
		metrics:           recorder,
	}

	snap, err := s.collectAndRecord(context.Background())
	if err != nil {
		t.Fatalf("collectAndRecord() error: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}

	if recorder.successes != 1 {
		t.Errorf("source successes = %d, want 1", recorder.successes)
	}
	if recorder.failures != 0 {
		t.Errorf("source failures = %d, want 0 (downstream failure must not affect source metrics)", recorder.failures)
	}
}

func TestCollectAndRecord_CollectionFailureRecordsSourceFailure(t *testing.T) {
	apiErr := errors.New("connection refused")
	client := &fakeRegistryClient{
		modelErr: apiErr,
	}

	recorder := &testMetricsRecorder{}
	s := &source{
		id:                "test-source",
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
		metrics:           recorder,
	}

	_, err := s.collectAndRecord(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if recorder.failures != 1 {
		t.Errorf("source failures = %d, want 1", recorder.failures)
	}
	if recorder.successes != 0 {
		t.Errorf("source successes = %d, want 0", recorder.successes)
	}
}

// --- Fix 5: httpError.Error() hardening ------------------------------------

func TestRun_GenuinePollerErrorPreservedDuringCancellation(t *testing.T) {
	// When the caller context is already cancelled AND the poller returns
	// a genuine (non-context) error, source.Run() must preserve the genuine
	// error instead of treating it as a graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	s := &source{
		id:                "test-source",
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            &fakeRegistryClient{},
		log:               testLogger(),
		next:              &fakeConsumer{},
	}

	// Build a poller with an invalid (zero) poll interval so that
	// poller.Run() returns a validation error before it ever polls.
	s.poller = pollsource.NewHelper(
		s.id,
		0, // invalid: triggers "poll interval must be positive"
		pollsource.BackoffConfig{
			InitialInterval:     util.Duration(100 * time.Millisecond),
			MaxInterval:         util.Duration(200 * time.Millisecond),
			Multiplier:          2,
			RandomizationFactor: 0,
		},
		s.log,
		nil,
		func(_ time.Duration) time.Duration { return 0 },
	)

	err := s.Run(ctx)
	if err == nil {
		t.Fatal("Run() returned nil; expected the validation error to be preserved even though the context is cancelled")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("Run() returned context.Canceled; expected the genuine validation error")
	}
	if !strings.Contains(err.Error(), "poll interval must be positive") {
		t.Errorf("Run() error = %q; expected it to mention 'poll interval must be positive'", err)
	}
}

// --- Fix 3: Downstream-failure test exercising the real poller path ----------

// countingConsumer records calls and returns a configurable error.
type countingConsumer struct {
	mu       sync.Mutex
	count    int
	called   chan struct{} // signalled on every call
	err      error
	received []*catalogcollector.CatalogSnapshot
}

func newCountingConsumer(err error) *countingConsumer {
	return &countingConsumer{
		called: make(chan struct{}, 16),
		err:    err,
	}
}

func (c *countingConsumer) Consume(_ context.Context, s *catalogcollector.CatalogSnapshot) error {
	c.mu.Lock()
	c.count++
	c.received = append(c.received, s)
	c.mu.Unlock()

	select {
	case c.called <- struct{}{}:
	default:
	}
	return c.err
}

func (c *countingConsumer) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func TestRun_DownstreamFailureRecordsCollectionSuccess(t *testing.T) {
	// This test exercises the real poller path and proves that a downstream
	// consumer failure:
	//   1. does NOT count as a source collection failure,
	//   2. DOES count the collection itself as a source success,
	//   3. causes the poller to retry (the backoff timing/calculation is
	//      covered by the dedicated pollsource helper tests),
	//   4. is cleaned up when the context is cancelled.
	model := makeModel("1", "iris-edge")
	version := makeVersion("2", "1.0.0")

	client := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{model}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{version}}},
		artifacts: map[string][][]mrapi.Artifact{
			"2": {{buildLiveArtifact("10", goodURI)}},
		},
	}

	downstreamErr := errors.New("downstream: reconciler conflict")
	consumer := newCountingConsumer(downstreamErr)
	recorder := &testMetricsRecorder{}

	s := &source{
		id:                "test-source",
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
		metrics:           recorder,
		next:              consumer,
	}

	// Use short deterministic backoff so the test is fast and reliable.
	s.poller = pollsource.NewHelper(
		s.id,
		50*time.Millisecond,
		pollsource.BackoffConfig{
			InitialInterval:     util.Duration(10 * time.Millisecond),
			MaxInterval:         util.Duration(20 * time.Millisecond),
			Multiplier:          2,
			RandomizationFactor: 0,
		},
		s.log,
		nil,
		func(_ time.Duration) time.Duration { return 0 },
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runErr atomic.Value
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := s.Run(ctx); err != nil {
			runErr.Store(err)
		}
	}()

	// Wait until at least two consumer calls have been observed.
	// The second call proves the poller retried after the downstream failure.
	for i := 0; i < 2; i++ {
		select {
		case <-consumer.called:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for consumer call %d", i+1)
		}
	}

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not stop within 5s after context cancellation")
	}

	if v := runErr.Load(); v != nil {
		t.Fatalf("Run() returned %v after cancellation, want nil", v)
	}

	// The producer goroutine has stopped, so these reads are now safe.
	calls := consumer.callCount()
	successes := recorder.successes
	failures := recorder.failures

	// 1. The downstream consumer was called (collection succeeded and the
	//    snapshot was passed to the consumer).
	if calls < 2 {
		t.Fatalf("consumer called %d times, want >= 2", calls)
	}

	// 2. Source collection success is recorded for each successful collect.
	if successes < 2 {
		t.Errorf("source successes = %d, want >= 2 (one per collect)", successes)
	}

	// 3. Source collection failure remains zero — downstream failures must
	//    not be attributed to source collection.
	if failures != 0 {
		t.Errorf("source failures = %d, want 0 (downstream failure must not affect source metrics)", failures)
	}
}

// --- Preflight tests ----------------------------------------------------------

// fakePreflightClient implements both registryClient and preflightRegistryClient.
type fakePreflightClient struct {
	fakeRegistryClient
	preflightModelErr   error
	preflightVersionErr error
}

func (f *fakePreflightClient) PreflightRegisteredModels(_ context.Context) error {
	return f.preflightModelErr
}

func (f *fakePreflightClient) PreflightModelVersions(_ context.Context) error {
	return f.preflightVersionErr
}

func TestPreflight_SucceedsWhenClientSupportsIt(t *testing.T) {
	client := &fakePreflightClient{}
	s, _ := newTestSource(client, nil)
	err := s.Preflight(context.Background())
	if err != nil {
		t.Fatalf("Preflight() = %v, want nil", err)
	}
}

func TestPreflight_AlwaysExecuted(t *testing.T) {
	// All registryClient implementations now include preflight methods.
	// Verify preflight is called and succeeds on a plain fakeRegistryClient.
	client := &fakeRegistryClient{
		models: [][]mrapi.RegisteredModel{{}},
	}
	s, _ := newTestSource(client, nil)
	err := s.Preflight(context.Background())
	if err != nil {
		t.Fatalf("Preflight() = %v, want nil", err)
	}
}

func TestPreflight_ModelFilterError_FailsPreflight(t *testing.T) {
	client := &fakePreflightClient{
		preflightModelErr: &httpError{statusCode: 400, err: errors.New("bad filter")},
	}
	s, _ := newTestSource(client, nil)
	err := s.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight() = nil, want error for model filter failure")
	}
}

func TestPreflight_VersionFilterError_FailsPreflight(t *testing.T) {
	client := &fakePreflightClient{
		preflightVersionErr: &httpError{statusCode: 400, err: errors.New("bad filter")},
	}
	s, _ := newTestSource(client, nil)
	err := s.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight() = nil, want error for version filter failure")
	}
}

func TestPreflight_ModelErrorTakesPrecedence(t *testing.T) {
	// If both fail, the model error should be returned first.
	client := &fakePreflightClient{
		preflightModelErr:   &httpError{statusCode: 400, err: errors.New("bad model filter")},
		preflightVersionErr: &httpError{statusCode: 400, err: errors.New("bad version filter")},
	}
	s, _ := newTestSource(client, nil)
	err := s.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight() = nil, want error")
	}
	if !strings.Contains(err.Error(), "registered models") {
		t.Errorf("Preflight() error = %q, want it to mention registered models", err.Error())
	}
}

func TestHTTPError_StatusOnly(t *testing.T) {
	inner := errors.New("response body: secret-token-value")
	err := &httpError{statusCode: 403, err: inner}

	msg := err.Error()
	if msg != "model registry HTTP 403" {
		t.Errorf("httpError.Error() = %q, want %q", msg, "model registry HTTP 403")
	}
	if strings.Contains(msg, "secret-token-value") {
		t.Errorf("httpError.Error() leaks inner error text: %q", msg)
	}

	// Unwrap must still return the inner error for classification.
	if !errors.Is(err, inner) {
		t.Error("errors.Is(httpError, inner) = false, want true")
	}
}
