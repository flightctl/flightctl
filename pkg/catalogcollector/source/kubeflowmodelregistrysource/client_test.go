package kubeflowmodelregistrysource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	mrapi "github.com/kubeflow/hub/pkg/openapi"
)

// recordedRequest captures the query parameters of a single HTTP request.
type recordedRequest struct {
	path  string
	query url.Values
}

// filterServer builds a test HTTP server that validates filterQuery params and
// returns minimal JSON responses. It records every request to reqs.
//
// responder is called with the path to build the response body.
func filterServer(t *testing.T, reqs *[]recordedRequest, responder func(path string) any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reqs = append(*reqs, recordedRequest{path: r.URL.Path, query: r.URL.Query()})
		body := responder(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
}

func newOpenapiClient(baseURL, pageSize string) (*openapiClient, error) {
	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: baseURL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	return &openapiClient{
		api:           api,
		pageSize:      pageSize,
		modelFilter:   defaultModelFilter,
		versionFilter: defaultVersionFilter,
	}, nil
}

// TestClient_RegisteredModels_FilterQuery verifies that every request to list
// registered models carries filterQuery=state='LIVE'.
func TestClient_RegisteredModels_FilterQuery(t *testing.T) {
	var reqs []recordedRequest
	page2Token := "page2"

	srv := filterServer(t, &reqs, func(path string) any {
		// First call returns a page with a nextPageToken; second returns empty.
		if len(reqs) == 1 {
			return map[string]any{
				"items": []map[string]any{
					{"id": "1", "name": "m1", "state": "LIVE"},
				},
				"nextPageToken": page2Token,
			}
		}
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	c, err := newOpenapiClient(srv.URL, "10")
	if err != nil {
		t.Fatalf("newOpenapiClient: %v", err)
	}

	// First page.
	list1, err := c.ListRegisteredModels(context.Background(), "")
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(list1.Items) != 1 {
		t.Errorf("page 1 items = %d, want 1", len(list1.Items))
	}
	if list1.NextPageToken != page2Token {
		t.Errorf("page 1 nextPageToken = %q, want %q", list1.NextPageToken, page2Token)
	}

	// Second page.
	_, err = c.ListRegisteredModels(context.Background(), page2Token)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}

	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	for i, req := range reqs {
		fq := req.query.Get("filterQuery")
		if fq != "state='LIVE'" {
			t.Errorf("request %d: filterQuery = %q, want \"state='LIVE'\"", i+1, fq)
		}
		if req.query.Get("orderBy") != "ID" {
			t.Errorf("request %d: orderBy = %q, want \"ID\"", i+1, req.query.Get("orderBy"))
		}
		if req.query.Get("sortOrder") != "ASC" {
			t.Errorf("request %d: sortOrder = %q, want \"ASC\"", i+1, req.query.Get("sortOrder"))
		}
	}
	// Second page must carry the token.
	if reqs[1].query.Get("nextPageToken") != page2Token {
		t.Errorf("page 2 request missing nextPageToken=%q", page2Token)
	}
}

// TestClient_ModelVersions_FilterQuery verifies that every request to list
// model versions carries filterQuery=state='LIVE'.
func TestClient_ModelVersions_FilterQuery(t *testing.T) {
	var reqs []recordedRequest
	page2Token := "vtok"

	srv := filterServer(t, &reqs, func(path string) any {
		if len(reqs) == 1 {
			return map[string]any{
				"items": []map[string]any{
					{"id": "10", "name": "1.0.0", "state": "LIVE", "registeredModelId": "5"},
				},
				"nextPageToken": page2Token,
			}
		}
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "10")

	_, err := c.ListModelVersions(context.Background(), "5", "")
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	_, err = c.ListModelVersions(context.Background(), "5", page2Token)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}

	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	for i, req := range reqs {
		fq := req.query.Get("filterQuery")
		if fq != "state='LIVE'" {
			t.Errorf("request %d: filterQuery = %q, want \"state='LIVE'\"", i+1, fq)
		}
	}
}

// TestClient_Artifacts_NoStateFilter verifies that artifact list requests use
// the model-artifact type filter but do NOT impose a state filter.
// Artifacts from real RHOAI may carry state=UNKNOWN; requiring LIVE would
// incorrectly exclude them.
func TestClient_Artifacts_NoStateFilter(t *testing.T) {
	var reqs []recordedRequest

	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{
			"items": []map[string]any{
				{"id": "3", "artifactType": "model-artifact",
					"uri": goodURI},
			},
			"nextPageToken": "",
		}
	})
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "10")
	_, err := c.ListModelArtifacts(context.Background(), "10", "")
	if err != nil {
		t.Fatalf("ListModelArtifacts: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	req := reqs[0]

	// Must carry the artifact-type filter.
	if req.query.Get("artifactType") != "model-artifact" {
		t.Errorf("artifactType = %q, want \"model-artifact\"", req.query.Get("artifactType"))
	}
	// Must NOT carry a state filter.
	if fq := req.query.Get("filterQuery"); fq != "" {
		t.Errorf("artifact request has unexpected filterQuery=%q; artifacts must not be state-filtered", fq)
	}
}

// TestClient_Artifacts_PaginationRetainsTypeFilter verifies that pagination
// requests for artifacts retain the artifactType filter and remain state-free.
func TestClient_Artifacts_PaginationRetainsTypeFilter(t *testing.T) {
	var reqs []recordedRequest
	page2Token := "atok"

	srv := filterServer(t, &reqs, func(_ string) any {
		if len(reqs) == 1 {
			return map[string]any{
				"items":         []map[string]any{{"id": "1", "artifactType": "model-artifact", "uri": goodURI}},
				"nextPageToken": page2Token,
			}
		}
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "1")
	_, _ = c.ListModelArtifacts(context.Background(), "10", "")
	_, _ = c.ListModelArtifacts(context.Background(), "10", page2Token)

	for i, req := range reqs {
		if req.query.Get("artifactType") != "model-artifact" {
			t.Errorf("page %d: missing artifactType filter", i+1)
		}
		if fq := req.query.Get("filterQuery"); fq != "" {
			t.Errorf("page %d: unexpected filterQuery=%q on artifact request", i+1, fq)
		}
	}
}

// TestClient_ServerError_FailsCycle verifies that a non-200 response from the
// model-registry API propagates as an error (fail-closed).
func TestClient_ServerError_FailsCycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "10")
	_, err := c.ListRegisteredModels(context.Background(), "")
	if err == nil {
		t.Fatal("expected error from 500 response, got nil")
	}
}

// TestClient_HTTPStatusCode_Preserved verifies that when the Model Registry
// returns a non-2xx response, the HTTP status code is preserved via httpError
// for each list method.
func TestClient_HTTPStatusCode_Preserved(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		listFunc   func(c *openapiClient) error
	}{
		{
			name:       "When ListRegisteredModels gets 403 it should preserve status code",
			statusCode: http.StatusForbidden,
			listFunc: func(c *openapiClient) error {
				_, err := c.ListRegisteredModels(context.Background(), "")
				return err
			},
		},
		{
			name:       "When ListModelVersions gets 404 it should preserve status code",
			statusCode: http.StatusNotFound,
			listFunc: func(c *openapiClient) error {
				_, err := c.ListModelVersions(context.Background(), "1", "")
				return err
			},
		},
		{
			name:       "When ListModelArtifacts gets 429 it should preserve status code",
			statusCode: http.StatusTooManyRequests,
			listFunc: func(c *openapiClient) error {
				_, err := c.ListModelArtifacts(context.Background(), "10", "")
				return err
			},
		},
		{
			name:       "When ListRegisteredModels gets 500 it should preserve status code",
			statusCode: http.StatusInternalServerError,
			listFunc: func(c *openapiClient) error {
				_, err := c.ListRegisteredModels(context.Background(), "")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "error", tc.statusCode)
			}))
			defer srv.Close()

			c, err := newOpenapiClient(srv.URL, "10")
			if err != nil {
				t.Fatalf("newOpenapiClient: %v", err)
			}

			err = tc.listFunc(c)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var httpErr *httpError
			if !errors.As(err, &httpErr) {
				t.Fatalf("expected *httpError, got %T: %v", err, err)
			}
			if httpErr.statusCode != tc.statusCode {
				t.Errorf("status code = %d, want %d", httpErr.statusCode, tc.statusCode)
			}
		})
	}
}

// TestClient_PageSize_SentInEveryRequest verifies that the pageSize parameter
// is transmitted on every request.
func TestClient_PageSize_SentInEveryRequest(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "7")
	_, _ = c.ListRegisteredModels(context.Background(), "")
	_, _ = c.ListModelVersions(context.Background(), "1", "")
	_, _ = c.ListModelArtifacts(context.Background(), "10", "")

	for i, req := range reqs {
		if ps := req.query.Get("pageSize"); ps != "7" {
			t.Errorf("request %d (%s): pageSize=%q, want \"7\"", i+1, req.path, ps)
		}
	}
}

// TestClient_CustomModelFilter verifies that a custom model filter is sent
// as filterQuery on every registered-models request.
func TestClient_CustomModelFilter(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: srv.URL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	c := &openapiClient{
		api:           api,
		pageSize:      "10",
		modelFilter:   "external_id='abc'",
		versionFilter: defaultVersionFilter,
	}

	_, err := c.ListRegisteredModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListRegisteredModels: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	fq := reqs[0].query.Get("filterQuery")
	if fq != "external_id='abc'" {
		t.Errorf("filterQuery = %q, want \"external_id='abc'\"", fq)
	}
}

// TestClient_EmptyModelFilter_OmitsFilterQuery verifies that an empty model
// filter causes filterQuery to be omitted from the request.
func TestClient_EmptyModelFilter_OmitsFilterQuery(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: srv.URL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	c := &openapiClient{
		api:           api,
		pageSize:      "10",
		modelFilter:   "",
		versionFilter: defaultVersionFilter,
	}

	_, err := c.ListRegisteredModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListRegisteredModels: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if fq := reqs[0].query.Get("filterQuery"); fq != "" {
		t.Errorf("expected filterQuery to be omitted, got %q", fq)
	}
}

// TestClient_EmptyVersionFilter_OmitsFilterQuery verifies that an empty version
// filter causes filterQuery to be omitted from the versions request.
func TestClient_EmptyVersionFilter_OmitsFilterQuery(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: srv.URL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	c := &openapiClient{
		api:           api,
		pageSize:      "10",
		modelFilter:   defaultModelFilter,
		versionFilter: "",
	}

	_, err := c.ListModelVersions(context.Background(), "1", "")
	if err != nil {
		t.Fatalf("ListModelVersions: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if fq := reqs[0].query.Get("filterQuery"); fq != "" {
		t.Errorf("expected filterQuery to be omitted, got %q", fq)
	}
}

// TestClient_Preflight_RegisteredModels verifies that the preflight check for
// registered models hits the exact expected path with pageSize=1, configured
// model filter, no nextPageToken, and makes exactly one request.
func TestClient_Preflight_RegisteredModels(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "100")
	err := c.PreflightRegisteredModels(context.Background())
	if err != nil {
		t.Fatalf("PreflightRegisteredModels: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(reqs))
	}
	req := reqs[0]

	// Assert the exact API path for registered models.
	const wantPath = "/api/model_registry/v1alpha3/registered_models"
	if req.path != wantPath {
		t.Errorf("path = %q, want %q", req.path, wantPath)
	}
	if ps := req.query.Get("pageSize"); ps != "1" {
		t.Errorf("pageSize = %q, want \"1\"", ps)
	}
	if fq := req.query.Get("filterQuery"); fq != defaultModelFilter {
		t.Errorf("filterQuery = %q, want %q", fq, defaultModelFilter)
	}
	// Preflight must not carry a nextPageToken.
	if req.query.Has("nextPageToken") {
		t.Errorf("preflight must not send nextPageToken, got %q", req.query.Get("nextPageToken"))
	}
}

// TestClient_Preflight_ModelVersions verifies that the preflight check for
// model versions hits the exact expected path with pageSize=1, configured
// version filter, no nextPageToken, and makes exactly one request.
func TestClient_Preflight_ModelVersions(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "100")
	err := c.PreflightModelVersions(context.Background())
	if err != nil {
		t.Fatalf("PreflightModelVersions: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(reqs))
	}
	req := reqs[0]

	// Assert the exact API path for model versions.
	const wantPath = "/api/model_registry/v1alpha3/model_versions"
	if req.path != wantPath {
		t.Errorf("path = %q, want %q", req.path, wantPath)
	}
	if ps := req.query.Get("pageSize"); ps != "1" {
		t.Errorf("pageSize = %q, want \"1\"", ps)
	}
	if fq := req.query.Get("filterQuery"); fq != defaultVersionFilter {
		t.Errorf("filterQuery = %q, want %q", fq, defaultVersionFilter)
	}
	// Preflight must not carry a nextPageToken.
	if req.query.Has("nextPageToken") {
		t.Errorf("preflight must not send nextPageToken, got %q", req.query.Get("nextPageToken"))
	}
}

// TestClient_CombinedFilterExpression verifies that a combined filter
// expression is sent exactly as filterQuery.
func TestClient_CombinedFilterExpression(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: srv.URL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	c := &openapiClient{
		api:           api,
		pageSize:      "10",
		modelFilter:   "state = 'LIVE' AND to_flightctl.bool_value = true",
		versionFilter: defaultVersionFilter,
	}

	_, err := c.ListRegisteredModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListRegisteredModels: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	fq := reqs[0].query.Get("filterQuery")
	if fq != "state = 'LIVE' AND to_flightctl.bool_value = true" {
		t.Errorf("filterQuery = %q, want %q", fq, "state = 'LIVE' AND to_flightctl.bool_value = true")
	}
}

// TestClient_CustomFilterRetainedOnPagination verifies custom model filter
// is retained on page 2.
func TestClient_CustomFilterRetainedOnPagination(t *testing.T) {
	var reqs []recordedRequest
	page2Token := "page2"

	srv := filterServer(t, &reqs, func(_ string) any {
		if len(reqs) == 1 {
			return map[string]any{
				"items":         []map[string]any{{"id": "1", "name": "m1"}},
				"nextPageToken": page2Token,
			}
		}
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: srv.URL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	c := &openapiClient{
		api:           api,
		pageSize:      "10",
		modelFilter:   "external_id='abc'",
		versionFilter: defaultVersionFilter,
	}

	_, err := c.ListRegisteredModels(context.Background(), "")
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	_, err = c.ListRegisteredModels(context.Background(), page2Token)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}

	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	for i, req := range reqs {
		fq := req.query.Get("filterQuery")
		if fq != "external_id='abc'" {
			t.Errorf("request %d: filterQuery = %q, want \"external_id='abc'\"", i+1, fq)
		}
	}
}

// TestClient_EmptyModelFilter_OmitsFilterQuery_HasCheck verifies that an empty
// model filter causes filterQuery to be truly absent, not just empty-valued.
func TestClient_EmptyModelFilter_OmitsFilterQuery_HasCheck(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: srv.URL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	c := &openapiClient{
		api:           api,
		pageSize:      "10",
		modelFilter:   "",
		versionFilter: defaultVersionFilter,
	}

	_, err := c.ListRegisteredModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListRegisteredModels: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].query.Has("filterQuery") {
		t.Errorf("filterQuery key must be absent from request, but was present with value %q", reqs[0].query.Get("filterQuery"))
	}
}

// TestClient_EmptyVersionFilter_OmitsFilterQuery_HasCheck verifies that an
// empty version filter causes filterQuery to be truly absent.
func TestClient_EmptyVersionFilter_OmitsFilterQuery_HasCheck(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: srv.URL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	c := &openapiClient{
		api:           api,
		pageSize:      "10",
		modelFilter:   defaultModelFilter,
		versionFilter: "",
	}

	_, err := c.ListModelVersions(context.Background(), "1", "")
	if err != nil {
		t.Fatalf("ListModelVersions: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].query.Has("filterQuery") {
		t.Errorf("filterQuery key must be absent from request, but was present with value %q", reqs[0].query.Get("filterQuery"))
	}
}

// TestClient_Preflight_EmptyModelFilter_OmitsFilterQuery verifies preflight
// with empty model filter omits filterQuery.
func TestClient_Preflight_EmptyModelFilter_OmitsFilterQuery(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: srv.URL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	c := &openapiClient{
		api:           api,
		pageSize:      "10",
		modelFilter:   "",
		versionFilter: defaultVersionFilter,
	}

	err := c.PreflightRegisteredModels(context.Background())
	if err != nil {
		t.Fatalf("PreflightRegisteredModels: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].query.Has("filterQuery") {
		t.Errorf("filterQuery key must be absent from preflight request, but was present")
	}
}

// TestClient_Preflight_EmptyVersionFilter_OmitsFilterQuery verifies preflight
// with empty version filter omits filterQuery.
func TestClient_Preflight_EmptyVersionFilter_OmitsFilterQuery(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	cfg := mrapi.NewConfiguration()
	cfg.Servers = mrapi.ServerConfigurations{{URL: srv.URL}}
	api := mrapi.NewAPIClient(cfg).ModelRegistryServiceAPI
	c := &openapiClient{
		api:           api,
		pageSize:      "10",
		modelFilter:   defaultModelFilter,
		versionFilter: "",
	}

	err := c.PreflightModelVersions(context.Background())
	if err != nil {
		t.Fatalf("PreflightModelVersions: %v", err)
	}

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].query.Has("filterQuery") {
		t.Errorf("filterQuery key must be absent from preflight request, but was present")
	}
}

// TestClient_Preflight_NoPagination verifies preflight makes exactly one
// request (no pagination).
func TestClient_Preflight_NoPagination(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{
			"items":         []map[string]any{{"id": "1", "name": "m1"}},
			"nextPageToken": "should-not-be-followed",
		}
	})
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "100")
	err := c.PreflightRegisteredModels(context.Background())
	if err != nil {
		t.Fatalf("PreflightRegisteredModels: %v", err)
	}

	if len(reqs) != 1 {
		t.Errorf("preflight must make exactly 1 request, got %d", len(reqs))
	}
}

// TestClient_Preflight_NoDownstream verifies preflight doesn't touch
// downstream/artifacts endpoints.
func TestClient_Preflight_NoDownstream(t *testing.T) {
	var reqs []recordedRequest
	srv := filterServer(t, &reqs, func(_ string) any {
		return map[string]any{"items": []any{}, "nextPageToken": ""}
	})
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "100")

	_ = c.PreflightRegisteredModels(context.Background())
	_ = c.PreflightModelVersions(context.Background())

	// Exactly 2 requests: one for models, one for versions.
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}

	for _, req := range reqs {
		if req.query.Has("artifactType") {
			t.Errorf("preflight request %s must not include artifactType filter", req.path)
		}
	}
}

// TestClient_Preflight_ServerError verifies that a server error during
// preflight propagates as an httpError.
func TestClient_Preflight_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "10")

	err := c.PreflightRegisteredModels(context.Background())
	if err == nil {
		t.Fatal("expected error from 500 response, got nil")
	}
	var httpErr *httpError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *httpError, got %T: %v", err, err)
	}
	if httpErr.statusCode != http.StatusInternalServerError {
		t.Errorf("status code = %d, want %d", httpErr.statusCode, http.StatusInternalServerError)
	}
}

// TestClient_Preflight_NilItems_FailsPreflight verifies that preflight
// detects nil Items in the response and fails rather than silently passing.
func TestClient_Preflight_NilItems_FailsPreflight(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return HTTP 200 with {} — no items field at all.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c, _ := newOpenapiClient(srv.URL, "1")

	err := c.PreflightRegisteredModels(context.Background())
	if err == nil {
		t.Fatal("PreflightRegisteredModels() = nil, want error for nil Items")
	}
	if !strings.Contains(err.Error(), "missing required items field") {
		t.Errorf("error %q should mention missing items field", err.Error())
	}

	err = c.PreflightModelVersions(context.Background())
	if err == nil {
		t.Fatal("PreflightModelVersions() = nil, want error for nil Items")
	}
	if !strings.Contains(err.Error(), "missing required items field") {
		t.Errorf("error %q should mention missing items field", err.Error())
	}
}

// TestSource_RepeatedPageToken_FailsCycle verifies that the source detects
// a server returning the same nextPageToken twice and fails the cycle.
func TestSource_RepeatedPageToken_RegisteredModels(t *testing.T) {
	callCount := 0
	c := &repeatedTokenClient{
		onCall: func(token string) (*mrapi.RegisteredModelList, error) {
			callCount++
			if callCount > 3 {
				return nil, fmt.Errorf("should have stopped by now")
			}
			return &mrapi.RegisteredModelList{
				Items:         []mrapi.RegisteredModel{makeModel("1", "m1")},
				NextPageToken: "same-token",
			}, nil
		},
	}
	s, _ := newTestSource(c, nil)
	_, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected repeated-token error, got nil")
	}
}

// TestSource_RepeatedPageToken_ModelVersions verifies repeated-token detection
// at the version pagination level.
func TestSource_RepeatedPageToken_ModelVersions(t *testing.T) {
	c := &repeatedVersionTokenClient{}
	s, _ := newTestSource(c, nil)
	_, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected repeated-version-token error, got nil")
	}
}

// repeatedVersionTokenClient simulates infinite pagination at the versions level.
type repeatedVersionTokenClient struct {
	modelCall int
}

func (r *repeatedVersionTokenClient) ListRegisteredModels(_ context.Context, _ string) (*mrapi.RegisteredModelList, error) {
	r.modelCall++
	if r.modelCall > 1 {
		return &mrapi.RegisteredModelList{Items: []mrapi.RegisteredModel{}}, nil
	}
	return &mrapi.RegisteredModelList{
		Items: []mrapi.RegisteredModel{makeModel("1", "model-a")},
	}, nil
}

func (r *repeatedVersionTokenClient) ListModelVersions(_ context.Context, _ string, _ string) (*mrapi.ModelVersionList, error) {
	// Always return the same token → pagination loop.
	return &mrapi.ModelVersionList{
		Items:         []mrapi.ModelVersion{makeVersion("10", "1.0.0")},
		NextPageToken: "same-version-token",
	}, nil
}

func (r *repeatedVersionTokenClient) ListModelArtifacts(_ context.Context, _ string, _ string) (*mrapi.ArtifactList, error) {
	return &mrapi.ArtifactList{Items: []mrapi.Artifact{}}, nil
}

func (r *repeatedVersionTokenClient) PreflightRegisteredModels(_ context.Context) error { return nil }
func (r *repeatedVersionTokenClient) PreflightModelVersions(_ context.Context) error    { return nil }

// TestSource_LiveVersionWithNoArtifact_FailsCycle verifies fail-closed: a LIVE
// version with zero eligible artifacts aborts the collection.
func TestSource_LiveVersionWithNoArtifact_FailsCycle(t *testing.T) {
	c := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{makeModel("1", "iris-edge")}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{makeVersion("2", "1.0.0")}}},
		// No artifacts at all → case 0 → must fail.
		artifacts: map[string][][]mrapi.Artifact{},
	}
	s, _ := newTestSource(c, nil)
	_, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected error for LIVE version with no artifact, got nil")
	}
}

// TestSource_LiveVersionMultipleArtifacts_FailsCycle verifies fail-closed:
// a LIVE version with two eligible artifacts is ambiguous and aborts collection.
func TestSource_LiveVersionMultipleArtifacts_FailsCycle(t *testing.T) {
	const uri2 = "other.registry.example.com/other@sha256:bd62d86fc106e620c60f3905ebb1f8d102dc2470d049d845eada1c9b823801ba"
	c := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{makeModel("1", "iris-edge")}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{makeVersion("2", "1.0.0")}}},
		artifacts: map[string][][]mrapi.Artifact{
			"2": {{
				buildLiveArtifact("10", goodURI),
				buildLiveArtifact("11", uri2),
			}},
		},
	}
	s, _ := newTestSource(c, nil)
	_, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected error for LIVE version with multiple artifacts, got nil")
	}
}

// TestSource_UnknownStateArtifact_Eligible verifies that an artifact with
// nil/absent state is treated as UNKNOWN and remains eligible.
// This reflects the real RHOAI Compose environment where the state field is
// absent from the artifact JSON (validated against RHOAI 3.5.1, API v1alpha3).
func TestSource_UnknownStateArtifact_Eligible(t *testing.T) {
	nilState := mrapi.Artifact{
		ModelArtifact: &mrapi.ModelArtifact{
			Id:           strp("3"),
			ArtifactType: strp("model-artifact"),
			Uri:          strp(goodURI),
			State:        nil, // absent state → treated as UNKNOWN → eligible
		},
	}
	c := &fakeRegistryClient{
		models:    [][]mrapi.RegisteredModel{{makeModel("1", "iris-edge")}},
		versions:  map[string][][]mrapi.ModelVersion{"1": {{makeVersion("2", "1.0.0")}}},
		artifacts: map[string][][]mrapi.Artifact{"2": {{nilState}}},
	}
	s, _ := newTestSource(c, nil)
	snap, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}
	if len(snap.CatalogItems) != 1 {
		t.Errorf("expected 1 item (UNKNOWN-state artifact eligible), got %d", len(snap.CatalogItems))
	}
}

// TestSource_AbandonedArtifact_FailsCycle verifies that a LIVE version whose
// only artifact has an ABANDONED state aborts the collection.
func TestSource_AbandonedArtifact_FailsCycle(t *testing.T) {
	abandoned := mrapi.ARTIFACTSTATE_ABANDONED
	c := &fakeRegistryClient{
		models:   [][]mrapi.RegisteredModel{{makeModel("1", "iris-edge")}},
		versions: map[string][][]mrapi.ModelVersion{"1": {{makeVersion("2", "1.0.0")}}},
		artifacts: map[string][][]mrapi.Artifact{
			"2": {{mrapi.Artifact{ModelArtifact: &mrapi.ModelArtifact{
				Id:           strp("10"),
				ArtifactType: strp("model-artifact"),
				Uri:          strp(goodURI),
				State:        &abandoned,
			}}}},
		},
	}
	s, _ := newTestSource(c, nil)
	_, err := s.collect(context.Background())
	if err == nil {
		t.Fatal("expected error for LIVE version with only ABANDONED artifact, got nil")
	}
}

// TestSource_SkippedModelsAggregate verifies that multiple models with no LIVE
// versions are all silently skipped and do not fail the cycle.
func TestSource_SkippedModelsAggregate(t *testing.T) {
	// Models without versions → silently skipped.
	c := &fakeRegistryClient{
		models: [][]mrapi.RegisteredModel{{
			makeModel("1", "model-no-versions"),
			makeModel("2", "another-empty"),
		}},
		versions:  map[string][][]mrapi.ModelVersion{},
		artifacts: map[string][][]mrapi.Artifact{},
	}
	s, _ := newTestSource(c, nil)
	snap, err := s.collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.CatalogItems) != 0 {
		t.Errorf("expected 0 items (all models skipped), got %d", len(snap.CatalogItems))
	}
}
