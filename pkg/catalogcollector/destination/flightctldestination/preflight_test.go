package flightctldestination

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	"github.com/flightctl/flightctl/api/versioning"
	"github.com/flightctl/flightctl/internal/util"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/stretchr/testify/require"
)

// preflightOf asserts that a constructed destination advertises the
// destination preflight capability and returns it.
func preflightOf(
	t *testing.T,
	destination catalogcollector.Destination,
) catalogcollector.DestinationPreflight {
	t.Helper()
	pf, ok := destination.(catalogcollector.DestinationPreflight)
	require.True(
		t,
		ok,
		"flightctl destination must implement catalogcollector.DestinationPreflight",
	)
	return pf
}

// recordingRequest captures what a preflight request looked like on the wire.
type recordingRequest struct {
	method string
	path   string
	query  string
	auth   string
	accept string
}

// recordingServer answers every request with an empty, well-formed list and
// records what it received.
type recordingServer struct {
	mu       sync.Mutex
	requests []recordingRequest
}

func (s *recordingServer) all() []recordingRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordingRequest(nil), s.requests...)
}

func (s *recordingServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, recordingRequest{
			method: r.Method,
			path:   strings.TrimPrefix(r.URL.Path, "/api/v1"),
			query:  r.URL.RawQuery,
			auth:   r.Header.Get("Authorization"),
			accept: r.Header.Get(versioning.HeaderAPIVersion),
		})
		s.mu.Unlock()

		switch strings.TrimPrefix(r.URL.Path, "/api/v1") {
		case "/catalogs":
			writeJSON(w, http.StatusOK, apiv1alpha1.CatalogList{
				ApiVersion: "v1alpha1",
				Kind:       "CatalogList",
				Items:      []apiv1alpha1.Catalog{},
			})
		case "/catalogitems":
			writeJSON(w, http.StatusOK, apiv1alpha1.CatalogItemList{
				ApiVersion: "v1alpha1",
				Kind:       "CatalogItemList",
				Items:      []apiv1alpha1.CatalogItem{},
			})
		default:
			writeStatus(w, http.StatusNotFound, "not found")
		}
	})
}

// When both collections answer with an empty list it should succeed and
// should have issued exactly one bounded read of each collection.
func TestPreflight_EmptyListsSucceed(t *testing.T) {
	rs := &recordingServer{}
	server := httptest.NewTLSServer(rs.handler())
	defer server.Close()

	err := preflightOf(t, newTestDestination(t, server)).
		Preflight(context.Background())
	require.NoError(t, err)

	requests := rs.all()
	require.Len(t, requests, 2, "preflight must issue exactly two requests")

	require.Equal(t, http.MethodGet, requests[0].method)
	require.Equal(t, "/catalogs", requests[0].path)
	require.Equal(t, http.MethodGet, requests[1].method)
	require.Equal(t, "/catalogitems", requests[1].path)

	for _, request := range requests {
		require.Equal(
			t,
			"1",
			queryValue(t, request.query, "limit"),
			"preflight %s must be bounded to a single item", request.path,
		)
		require.Empty(
			t,
			queryValue(t, request.query, "continue"),
			"preflight %s must not paginate", request.path,
		)
		require.Empty(
			t,
			queryValue(t, request.query, "labelSelector"),
			"preflight %s must not assume a pipeline ownership selector",
			request.path,
		)
		require.Equal(
			t,
			versioning.V1Alpha1,
			request.accept,
			"preflight must reuse the configured API version",
		)
	}
}

// When the collections already hold resources it should still succeed, and
// it must not read or write anything beyond the two list calls.
func TestPreflight_NonEmptyListsSucceedWithoutWrites(t *testing.T) {
	fs := newFakeServer()
	catalog := testCatalog("existing", "Existing")
	catalog.Metadata.Labels = managementLabels("other-pipeline")
	fs.catalogs["existing"] = &catalog
	item := testCatalogItem("existing", "item", "Item")
	item.Metadata.Labels = managementLabels("other-pipeline")
	fs.items["existing/item"] = &item

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	err := preflightOf(t, newTestDestination(t, server)).
		Preflight(context.Background())
	require.NoError(t, err)

	require.Equal(
		t,
		[]string{"LIST catalogs", "LIST catalogitems"},
		fs.calls,
		"preflight must be read-only and must not touch any resource",
	)
	require.Len(t, fs.catalogs, 1, "preflight must not delete existing catalogs")
	require.Len(t, fs.items, 1, "preflight must not delete existing catalog items")
}

// When the server rejects the credentials or the permissions it should fail
// with the status visible to the operator.
func TestPreflight_RejectedCredentialsAndPermissions(t *testing.T) {
	tests := []struct {
		name   string
		status int
		detail string
	}{
		{
			name:   "rejected credentials",
			status: http.StatusUnauthorized,
			detail: "token is invalid",
		},
		{
			name:   "rejected permissions",
			status: http.StatusForbidden,
			detail: "forbidden: catalogs.list is not allowed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeStatus(w, test.status, test.detail)
				}),
			)
			defer server.Close()

			err := preflightOf(t, newTestDestination(t, server)).
				Preflight(context.Background())
			require.Error(t, err)
			require.ErrorContains(t, err, "preflight")
			require.ErrorContains(t, err, "listing catalogs")
			require.ErrorContains(t, err, test.detail)
		})
	}
}

// When catalogs can be read but catalog items cannot it should fail and name
// the collection that was refused.
func TestPreflight_CatalogItemsRejected(t *testing.T) {
	server := httptest.NewTLSServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/catalogitems") {
				writeStatus(w, http.StatusForbidden, "catalogitems.list is not allowed")
				return
			}
			writeJSON(w, http.StatusOK, apiv1alpha1.CatalogList{
				ApiVersion: "v1alpha1",
				Kind:       "CatalogList",
				Items:      []apiv1alpha1.Catalog{},
			})
		}),
	)
	defer server.Close()

	err := preflightOf(t, newTestDestination(t, server)).
		Preflight(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "listing catalog items")
	require.ErrorContains(t, err, "catalogitems.list is not allowed")
}

// When the server answers 200 with something that is not a resource list it
// should fail rather than treat the response as an empty collection.
func TestPreflight_InvalidResponses(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantError   string
	}{
		{
			name:        "non-JSON body",
			contentType: "text/html",
			body:        "<html>login required</html>",
			wantError:   "login required",
		},
		{
			name:        "malformed JSON body",
			contentType: "application/json",
			body:        "{\"items\": ",
			wantError:   "preflight",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", test.contentType)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(test.body))
				}),
			)
			defer server.Close()

			err := preflightOf(t, newTestDestination(t, server)).
				Preflight(context.Background())
			require.Error(t, err)
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

// When the caller cancels the context it should return promptly and the
// cancellation must stay in the error chain so the service can recognize a
// graceful shutdown.
func TestPreflight_ContextCancelled(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			writeJSON(w, http.StatusOK, apiv1alpha1.CatalogList{
				ApiVersion: "v1alpha1",
				Kind:       "CatalogList",
				Items:      []apiv1alpha1.Catalog{},
			})
		}),
	)
	defer server.Close()
	defer close(release)

	pf := preflightOf(t, newTestDestination(t, server))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pf.Preflight(ctx) }()

	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("preflight did not return after context cancellation")
	}
}

// When the configured client timeout elapses it should fail instead of
// blocking startup forever.
func TestPreflight_ClientTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			writeJSON(w, http.StatusOK, apiv1alpha1.CatalogList{
				ApiVersion: "v1alpha1",
				Kind:       "CatalogList",
				Items:      []apiv1alpha1.Catalog{},
			})
		}),
	)
	defer server.Close()
	defer close(release)

	timeout := util.Duration(100 * time.Millisecond)
	destination, err := NewFactory().CreateDestination(
		context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test-dest"},
			Logger: testLogger(),
		},
		&Config{Server: server.URL, InsecureSkipVerify: true, Timeout: &timeout},
	)
	require.NoError(t, err)

	start := time.Now()
	err = preflightOf(t, destination).Preflight(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "preflight")
	require.Less(t, time.Since(start), 5*time.Second,
		"preflight must honor the configured client timeout")
}

// When an authenticator extension is configured it should reuse it, so the
// preflight request carries the same credentials as reconciliation.
func TestPreflight_ReusesConfiguredAuthentication(t *testing.T) {
	rs := &recordingServer{}
	server := httptest.NewTLSServer(rs.handler())
	defer server.Close()

	destination := newTestDestinationWithToken(t, server, "preflight-token")

	err := preflightOf(t, destination).Preflight(context.Background())
	require.NoError(t, err)

	requests := rs.all()
	require.Len(t, requests, 2)
	for _, request := range requests {
		require.Equal(t, "Bearer preflight-token", request.auth,
			"preflight must reuse the configured authenticator")
	}
}

// When an organization is configured it should be present on every preflight
// request.
func TestPreflight_ReusesConfiguredOrganization(t *testing.T) {
	rs := &recordingServer{}
	server := httptest.NewTLSServer(rs.handler())
	defer server.Close()

	const orgID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	destination, err := NewFactory().CreateDestination(
		context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test-dest"},
			Logger: testLogger(),
		},
		&Config{Server: server.URL, InsecureSkipVerify: true, OrgID: orgID},
	)
	require.NoError(t, err)

	require.NoError(t, preflightOf(t, destination).Preflight(context.Background()))

	requests := rs.all()
	require.Len(t, requests, 2)
	for _, request := range requests {
		require.Equal(t, orgID, queryValue(t, request.query, "org_id"),
			"preflight must reuse the configured organization")
	}
}

// When TLS verification is enabled and the server certificate is untrusted it
// should fail instead of silently downgrading.
func TestPreflight_TLSVerificationFailure(t *testing.T) {
	rs := &recordingServer{}
	server := httptest.NewTLSServer(rs.handler())
	defer server.Close()

	destination, err := NewFactory().CreateDestination(
		context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test-dest"},
			Logger: testLogger(),
		},
		&Config{Server: server.URL},
	)
	require.NoError(t, err)

	err = preflightOf(t, destination).Preflight(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "preflight")
	require.Empty(t, rs.all(), "an untrusted server must not receive the request")
}

// When preflight fails it should not leak the configured credentials.
func TestPreflight_CredentialsNeverInErrors(t *testing.T) {
	const secret = "super-secret-token"
	server := httptest.NewTLSServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeStatus(w, http.StatusUnauthorized, "unauthorized")
		}),
	)
	defer server.Close()

	destination := newTestDestinationWithToken(t, server, secret)

	err := preflightOf(t, destination).Preflight(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
}

func queryValue(t *testing.T, rawQuery string, key string) string {
	t.Helper()
	values, err := url.ParseQuery(rawQuery)
	require.NoError(t, err)
	return values.Get(key)
}

// A 200 response with a JSON content type is decoded into the list type by the
// generated client no matter what the body actually contains, so a body that
// carries no items field yields a non-nil JSON200 whose Items is nil. Such a
// response proves nothing about read access and must be rejected on either
// collection.
func TestPreflight_MissingItemsFieldRejected(t *testing.T) {
	const (
		catalogsPath = "/catalogs"
		itemsPath    = "/catalogitems"
	)

	bodies := []struct {
		name string
		body string
	}{
		{name: "empty object", body: `{}`},
		{name: "null body", body: `null`},
		{name: "explicit null items", body: `{"items":null}`},
		{name: "unrelated object", body: `{"status":"login required"}`},
	}

	collections := []struct {
		name      string
		badPath   string
		wantError string
	}{
		{
			name:      "catalogs",
			badPath:   catalogsPath,
			wantError: "listing catalogs: response missing required items field",
		},
		{
			name:      "catalog items",
			badPath:   itemsPath,
			wantError: "listing catalog items: response missing required items field",
		},
	}

	for _, collection := range collections {
		for _, body := range bodies {
			t.Run(collection.name+"/"+body.name, func(t *testing.T) {
				server := httptest.NewTLSServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						path := strings.TrimPrefix(r.URL.Path, "/api/v1")
						if path == collection.badPath {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusOK)
							_, _ = w.Write([]byte(body.body))
							return
						}
						// The other collection answers with a valid empty
						// list so the rejection is attributable to badPath.
						switch path {
						case catalogsPath:
							writeJSON(w, http.StatusOK, apiv1alpha1.CatalogList{
								ApiVersion: "v1alpha1",
								Kind:       "CatalogList",
								Items:      []apiv1alpha1.Catalog{},
							})
						case itemsPath:
							writeJSON(w, http.StatusOK, apiv1alpha1.CatalogItemList{
								ApiVersion: "v1alpha1",
								Kind:       "CatalogItemList",
								Items:      []apiv1alpha1.CatalogItem{},
							})
						default:
							writeStatus(w, http.StatusNotFound, "not found")
						}
					}),
				)
				defer server.Close()

				err := preflightOf(t, newTestDestination(t, server)).
					Preflight(context.Background())
				require.Error(t, err)
				require.ErrorContains(t, err, collection.wantError)
				require.ErrorContains(t, err, "test-dest")
			})
		}
	}
}

// An empty collection sends "items": [], which decodes to a non-nil empty
// slice. The missing-items guard must not reject it.
func TestPreflight_EmptyItemsArrayAccepted(t *testing.T) {
	server := httptest.NewTLSServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"apiVersion":"v1alpha1","items":[]}`))
		}),
	)
	defer server.Close()

	require.NoError(t, preflightOf(t, newTestDestination(t, server)).
		Preflight(context.Background()))
}
