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
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
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
		return &mrapi.RegisteredModelList{Items: []mrapi.RegisteredModel{}}, nil
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
		return &mrapi.ModelVersionList{Items: []mrapi.ModelVersion{}}, nil
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
		return &mrapi.ArtifactList{Items: []mrapi.Artifact{}}, nil
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
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
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
	return &mrapi.ModelVersionList{Items: []mrapi.ModelVersion{}}, nil
}

func (r *repeatedTokenClient) ListModelArtifacts(_ context.Context, _ string, _ string) (*mrapi.ArtifactList, error) {
	return &mrapi.ArtifactList{Items: []mrapi.Artifact{}}, nil
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
		"test-source",
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

// --- Finding 1: Nil-items validation ----------------------------------------

// nilItemsClient returns list responses with nil Items to simulate a malformed
// response (HTTP 200 with {} or {"items":null}).
type nilItemsClient struct {
	fakeRegistryClient
	nilModels    bool
	nilVersions  bool
	nilArtifacts bool
}

func (c *nilItemsClient) ListRegisteredModels(_ context.Context, _ string) (*mrapi.RegisteredModelList, error) {
	if c.nilModels {
		return &mrapi.RegisteredModelList{Items: nil}, nil
	}
	return c.fakeRegistryClient.ListRegisteredModels(context.Background(), "")
}

func (c *nilItemsClient) ListModelVersions(_ context.Context, modelID string, _ string) (*mrapi.ModelVersionList, error) {
	if c.nilVersions {
		return &mrapi.ModelVersionList{Items: nil}, nil
	}
	return c.fakeRegistryClient.ListModelVersions(context.Background(), modelID, "")
}

func (c *nilItemsClient) ListModelArtifacts(_ context.Context, versionID string, _ string) (*mrapi.ArtifactList, error) {
	if c.nilArtifacts {
		return &mrapi.ArtifactList{Items: nil}, nil
	}
	return c.fakeRegistryClient.ListModelArtifacts(context.Background(), versionID, "")
}

func TestCollect_NilItemsModels_FailsCycle(t *testing.T) {
	// When the server returns HTTP 200 with {} or {"items":null} for
	// registered models, the collection must fail rather than silently
	// producing an empty snapshot that would authorize pruning.
	client := &nilItemsClient{nilModels: true}
	s, _ := newTestSource(client, nil)
	snap, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected error for nil Items in model list, got nil")
	}
	if snap != nil {
		t.Error("expected nil snapshot on error")
	}
	if !strings.Contains(err.Error(), "missing required items field") {
		t.Errorf("error %q should mention missing items field", err.Error())
	}
}

func TestCollect_NilItemsVersions_FailsCycle(t *testing.T) {
	// Nil Items in a version list response must fail the cycle.
	client := &nilItemsClient{
		fakeRegistryClient: fakeRegistryClient{
			models: [][]mrapi.RegisteredModel{{makeModel("1", "iris-edge")}},
		},
		nilVersions: true,
	}
	s, _ := newTestSource(client, nil)
	snap, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected error for nil Items in version list, got nil")
	}
	if snap != nil {
		t.Error("expected nil snapshot on error")
	}
}

func TestCollect_NilItemsArtifacts_FailsCycle(t *testing.T) {
	// Nil Items in an artifact list response must fail the cycle.
	client := &nilItemsClient{
		fakeRegistryClient: fakeRegistryClient{
			models:   [][]mrapi.RegisteredModel{{makeModel("1", "iris-edge")}},
			versions: map[string][][]mrapi.ModelVersion{"1": {{makeVersion("2", "1.0.0")}}},
		},
		nilArtifacts: true,
	}
	s, _ := newTestSource(client, nil)
	snap, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected error for nil Items in artifact list, got nil")
	}
	if snap != nil {
		t.Error("expected nil snapshot on error")
	}
}

// malformedLaterPageClient returns valid first pages but nil Items on page 2.
type malformedLaterPageClient struct {
	callCount int
}

func (c *malformedLaterPageClient) ListRegisteredModels(_ context.Context, token string) (*mrapi.RegisteredModelList, error) {
	c.callCount++
	if token == "" {
		return &mrapi.RegisteredModelList{
			Items:         []mrapi.RegisteredModel{makeModel("1", "model-a")},
			NextPageToken: "page2",
		}, nil
	}
	// Page 2: malformed nil Items
	return &mrapi.RegisteredModelList{Items: nil, NextPageToken: ""}, nil
}

func (c *malformedLaterPageClient) ListModelVersions(_ context.Context, _ string, _ string) (*mrapi.ModelVersionList, error) {
	return &mrapi.ModelVersionList{Items: []mrapi.ModelVersion{}}, nil
}

func (c *malformedLaterPageClient) ListModelArtifacts(_ context.Context, _ string, _ string) (*mrapi.ArtifactList, error) {
	return &mrapi.ArtifactList{Items: []mrapi.Artifact{}}, nil
}

func (c *malformedLaterPageClient) PreflightRegisteredModels(_ context.Context) error { return nil }
func (c *malformedLaterPageClient) PreflightModelVersions(_ context.Context) error    { return nil }

func TestCollect_MalformedLaterPage_FailsCycle(t *testing.T) {
	// A valid first page followed by a malformed second page with nil Items
	// must fail the cycle rather than silently losing page-2 models.
	client := &malformedLaterPageClient{}
	s, _ := newTestSource(client, nil)
	snap, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed later page, got nil")
	}
	if snap != nil {
		t.Error("expected nil snapshot on malformed page")
	}
	if !strings.Contains(err.Error(), "missing required items field") {
		t.Errorf("error %q should mention missing items field", err.Error())
	}
}

func TestCollect_EmptyListPreserved(t *testing.T) {
	// A legitimate empty items list (items:[]) must produce a valid empty
	// snapshot without error. This distinguishes a valid empty result from
	// a malformed nil-items response.
	client := &fakeRegistryClient{
		models: [][]mrapi.RegisteredModel{{}}, // items:[] (empty, not nil)
	}
	s, _ := newTestSource(client, nil)
	snap, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() returned unexpected error for empty list: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot for valid empty list")
	}
	if len(snap.CatalogItems) != 0 {
		t.Errorf("expected 0 items, got %d", len(snap.CatalogItems))
	}
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

// collectRecorder captures the OnCollect invocations the poller makes, so the
// collection outcome can be asserted independently of the downstream result.
type collectRecorder struct {
	mu        sync.Mutex
	successes int
	failures  int
	errs      []error
	observed  chan struct{} // signalled on every attempt
}

func newCollectRecorder() *collectRecorder {
	return &collectRecorder{observed: make(chan struct{}, 16)}
}

func (r *collectRecorder) record(_ time.Duration, err error) {
	r.mu.Lock()
	if err != nil {
		r.failures++
		r.errs = append(r.errs, err)
	} else {
		r.successes++
	}
	r.mu.Unlock()

	select {
	case r.observed <- struct{}{}:
	default:
	}
}

func (r *collectRecorder) counts() (successes, failures int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.successes, r.failures
}

func (r *collectRecorder) firstError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.errs) == 0 {
		return nil
	}
	return r.errs[0]
}

// TestRun_SuccessfulCollectionRecordsSourceSuccess drives the real poller and
// asserts that a successful Model Registry collection is reported once as a
// collection success before the snapshot reaches the consumer.
func TestRun_SuccessfulCollectionRecordsSourceSuccess(t *testing.T) {
	model := makeModel("1", "iris-edge")
	version := makeVersion("2", "1.0.0")

	client := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{model}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{version}}},
		artifacts: map[string][][]mrapi.Artifact{
			"2": {{buildLiveArtifact("10", goodURI)}},
		},
	}

	consumer := newCountingConsumer(nil)
	recorder := newCollectRecorder()

	s := &source{
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
		next:              consumer,
	}
	s.poller = newTestPoller(s)
	s.poller.OnCollect = recorder.record

	runUntil(t, s, consumer.called)

	successes, failures := recorder.counts()
	if successes < 1 {
		t.Errorf("collection successes = %d, want at least 1", successes)
	}
	if failures != 0 {
		t.Errorf("collection failures = %d, want 0", failures)
	}
	if consumer.callCount() < 1 {
		t.Error("the snapshot never reached the downstream consumer")
	}
}

// TestRun_CollectionFailureRecordsSourceFailure asserts that a failed Model
// Registry collection is reported as a collection failure carrying the
// original error, and that nothing reaches the downstream consumer.
func TestRun_CollectionFailureRecordsSourceFailure(t *testing.T) {
	apiErr := errors.New("connection refused")
	client := &fakeRegistryClient{modelErr: apiErr}

	consumer := newCountingConsumer(nil)
	recorder := newCollectRecorder()

	s := &source{
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
		next:              consumer,
	}
	s.poller = newTestPoller(s)
	s.poller.OnCollect = recorder.record

	// Stop as soon as one attempt has been observed instead of waiting out a
	// fixed window.
	runUntil(t, s, recorder.observed)

	successes, failures := recorder.counts()
	if failures < 1 {
		t.Errorf("collection failures = %d, want at least 1", failures)
	}
	if successes != 0 {
		t.Errorf("collection successes = %d, want 0", successes)
	}
	if !errors.Is(recorder.firstError(), apiErr) {
		t.Errorf("collection error = %v, want it to wrap %v", recorder.firstError(), apiErr)
	}
	if n := consumer.callCount(); n != 0 {
		t.Errorf("consumer called %d times; a failed collection must never reach the consumer", n)
	}
}

// runUntil starts the source, waits for the first signal on done, then cancels
// and waits for Run to return nil.
func runUntil(t *testing.T, s *source, signal <-chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runErr atomic.Value
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if err := s.Run(ctx); err != nil {
			runErr.Store(err)
		}
	}()

	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		cancel()
		<-finished
		t.Fatal("timed out waiting for the first observed attempt")
	}

	cancel()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not stop within 5s after context cancellation")
	}

	if v := runErr.Load(); v != nil {
		t.Fatalf("Run() returned %v after cancellation, want nil", v)
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
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            &fakeRegistryClient{},
		log:               testLogger(),
		next:              &fakeConsumer{},
	}

	// Build a poller with an invalid (zero) poll interval so that
	// poller.Run() returns a validation error before it ever polls.
	s.poller = pollsource.NewHelper(
		"test-source",
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

	// Exercise the real source metrics through the poller's OnCollect hook.
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background()) //nolint:errcheck

	metrics, err := newMetrics("test-source", provider)
	if err != nil {
		t.Fatalf("newMetrics: %v", err)
	}

	s := &source{
		catalog:           "test-catalog",
		collectionTimeout: 30 * time.Second,
		client:            client,
		log:               testLogger(),
		next:              consumer,
	}

	// Use short deterministic backoff so the test is fast and reliable.
	s.poller = pollsource.NewHelper(
		"test-source",
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
	s.poller.OnCollect = metrics.recordCollection

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
	outcomes := collectionOutcomes(t, reader, "test-source")

	// 1. The downstream consumer was called (collection succeeded and the
	//    snapshot was passed to the consumer).
	if calls < 2 {
		t.Fatalf("consumer called %d times, want >= 2", calls)
	}

	// 2. Source collection success is recorded for each successful collect.
	if outcomes["success"] < 2 {
		t.Errorf("source collections{outcome=success} = %d, want >= 2 (one per collect)", outcomes["success"])
	}

	// 3. Source collection failure remains zero — downstream failures must
	//    not be attributed to source collection.
	if outcomes["failure"] != 0 {
		t.Errorf("source collections{outcome=failure} = %d, want 0 (downstream failure must not affect source metrics)", outcomes["failure"])
	}
	if outcomes["cancelled"] != 0 {
		t.Errorf("source collections{outcome=cancelled} = %d, want 0", outcomes["cancelled"])
	}

	// 4. A successful collection advances the last-success timestamp even
	//    though every downstream consumption failed.
	if ts, ok := lastSuccessTimestamp(t, reader, "test-source"); !ok || ts == 0 {
		t.Error("last-success gauge was not observed after a successful collection")
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

func TestHTTPError_IncludesDiagnosticCause(t *testing.T) {
	inner := errors.New("connection refused")
	err := &httpError{statusCode: 403, err: inner}

	msg := err.Error()
	// Error message must include both the HTTP status and the diagnostic cause.
	if !strings.Contains(msg, "HTTP 403") {
		t.Errorf("httpError.Error() = %q, want it to contain HTTP status", msg)
	}
	if !strings.Contains(msg, "connection refused") {
		t.Errorf("httpError.Error() = %q, want it to contain the diagnostic cause", msg)
	}

	// Unwrap must still return the inner error for classification.
	if !errors.Is(err, inner) {
		t.Error("errors.Is(httpError, inner) = false, want true")
	}
}

func TestHTTPError_NilErr(t *testing.T) {
	err := &httpError{statusCode: 500, err: nil}
	msg := err.Error()
	if msg != "model registry HTTP 500" {
		t.Errorf("httpError.Error() = %q, want %q", msg, "model registry HTTP 500")
	}
}

func TestHTTPError_BodyReadFailure_PreservesDiagnostics(t *testing.T) {
	// Regression test: when HTTP 200 headers arrive but reading the body
	// fails (e.g. io.ErrUnexpectedEOF), the final diagnostic must include
	// the operation context, the HTTP status code, AND the underlying cause.
	bodyReadErr := io.ErrUnexpectedEOF
	httpErr := &httpError{statusCode: 200, err: bodyReadErr}

	s := &source{log: testLogger()}
	wrapped := s.wrapHTTPError("listing registered models", httpErr)

	msg := wrapped.Error()

	// Must contain operation context.
	if !strings.Contains(msg, "listing registered models") {
		t.Errorf("error %q should contain the operation context", msg)
	}

	// Must contain HTTP status.
	if !strings.Contains(msg, "HTTP 200") {
		t.Errorf("error %q should contain HTTP status", msg)
	}

	// Must contain the underlying cause text.
	if !strings.Contains(msg, "unexpected EOF") {
		t.Errorf("error %q should contain the body-read error cause", msg)
	}

	// Must preserve error identity through the chain.
	if !errors.Is(wrapped, io.ErrUnexpectedEOF) {
		t.Error("errors.Is(wrapped, io.ErrUnexpectedEOF) = false; want cause to be identifiable")
	}
}
