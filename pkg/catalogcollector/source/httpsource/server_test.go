package httpsource

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func testLogger() *logrus.Entry {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log.WithField("test", true)
}

type fakeConsumer struct {
	mu        sync.Mutex
	snapshots []*catalogcollector.CatalogSnapshot
	err       error
}

func (f *fakeConsumer) Consume(_ context.Context, snapshot *catalogcollector.CatalogSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots = append(f.snapshots, snapshot)
	return f.err
}

func (f *fakeConsumer) lastSnapshot() *catalogcollector.CatalogSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.snapshots) == 0 {
		return nil
	}
	return f.snapshots[len(f.snapshots)-1]
}

func (f *fakeConsumer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.snapshots)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func startSource(t *testing.T, consumer *fakeConsumer) (string, context.CancelFunc) {
	t.Helper()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())

	f := NewFactory()
	src, err := f.CreateSource(ctx, catalogcollector.Settings{ID: catalogcollector.ComponentID{Type: Type, Name: "test"}, Logger: testLogger()},
		&Config{ListenAddress: addr, Path: "/v1/snapshots"}, consumer)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() { errCh <- src.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Post("http://"+addr+"/v1/snapshots", "application/json", //nolint:gosec // G107: URL is test-local loopback from freeAddr
			strings.NewReader(`{"revision":"probe","catalogs":[],"catalogItems":[]}`))
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	consumer.mu.Lock()
	consumer.snapshots = nil
	consumer.mu.Unlock()

	cleanup := func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
			t.Error("source did not stop within 10s")
		}
	}

	return "http://" + addr + "/v1/snapshots", cleanup
}

func TestFactoryType(t *testing.T) {
	f := NewFactory()
	require.Equal(t, Type, f.Type())
	require.Equal(t, catalogcollector.ComponentType("http"), f.Type())
}

func TestCreateSourceRejectsWildcardPath(t *testing.T) {
	f := NewFactory()
	cases := []struct {
		name string
		path string
	}{
		{
			name: "When path contains opening brace it should return error",
			path: "/{",
		},
		{
			name: "When path contains closing brace it should return error",
			path: "/}",
		},
		{
			name: "When path contains a ServeMux wildcard it should return error",
			path: "/items/{id}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.CreateSource(
				context.Background(),
				catalogcollector.Settings{
					ID:     catalogcollector.ComponentID{Type: Type, Name: "test"},
					Logger: testLogger(),
				},
				&Config{ListenAddress: "127.0.0.1:8080", Path: tc.path},
				&fakeConsumer{},
			)
			require.Error(t, err)
			require.Contains(t, err.Error(), "must not contain")
		})
	}
}

func TestSuccessfulPost(t *testing.T) {
	consumer := &fakeConsumer{}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	body := `{
		"revision": "rev-1",
		"catalogs": [{"apiVersion":"flightctl.io/v1alpha1","kind":"Catalog","metadata":{"name":"test-catalog"},"spec":{"displayName":"Test"}}],
		"catalogItems": []
	}`
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:gosec // G107: URL is test-local loopback from startSource
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	snap := consumer.lastSnapshot()
	require.NotNil(t, snap)
	require.Equal(t, "rev-1", snap.Revision)
	require.Len(t, snap.Catalogs, 1)
	require.Equal(t, "test-catalog", *snap.Catalogs[0].Metadata.Name)
}

func TestMissingRevision(t *testing.T) {
	consumer := &fakeConsumer{}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	resp, err := http.Post(url, "application/json", strings.NewReader(`{"catalogs":[]}`)) //nolint:gosec // G107: URL is test-local loopback from startSource
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, 0, consumer.count())
}

func TestMalformedJSON(t *testing.T) {
	consumer := &fakeConsumer{}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	resp, err := http.Post(url, "application/json", strings.NewReader(`{invalid`)) //nolint:gosec // G107: URL is test-local loopback from startSource
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, 0, consumer.count())
}

func TestUnknownRequestField(t *testing.T) {
	consumer := &fakeConsumer{}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	resp, err := http.Post(url, "application/json", //nolint:gosec // G107: URL is test-local loopback from startSource
		strings.NewReader(`{"revision":"r1","unknownField":"x"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, 0, consumer.count())
}

func TestTrailingJSON(t *testing.T) {
	consumer := &fakeConsumer{}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	resp, err := http.Post(url, "application/json", //nolint:gosec // G107: URL is test-local loopback from startSource
		strings.NewReader(`{"revision":"r1"}{"revision":"r2"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, 0, consumer.count())
}

func TestOversizedBody(t *testing.T) {
	consumer := &fakeConsumer{}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	huge := `{"revision":"r1","catalogs":[` + strings.Repeat(`{},`, maxBodySize) + `{}]}`
	resp, err := http.Post(url, "application/json", strings.NewReader(huge)) //nolint:gosec // G107: URL is test-local loopback from startSource
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, 0, consumer.count())
}

func TestWrongHTTPMethod(t *testing.T) {
	consumer := &fakeConsumer{}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	resp, err := http.Get(url) //nolint:gosec // G107: URL is test-local loopback from startSource
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	require.Equal(t, 0, consumer.count())
}

func TestDownstreamConsumerFailure(t *testing.T) {
	consumer := &fakeConsumer{err: fmt.Errorf("destination unreachable")}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	resp, err := http.Post(url, "application/json", //nolint:gosec // G107: URL is test-local loopback from startSource
		strings.NewReader(`{"revision":"r1","catalogs":[],"catalogItems":[]}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

func TestConcurrentRequests(t *testing.T) {
	consumer := &fakeConsumer{}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	var wg sync.WaitGroup
	var failures atomic.Int32
	n := 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"revision":"rev-%d","catalogs":[],"catalogItems":[]}`, idx)
			resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:gosec // G107: URL is test-local loopback from startSource
			if err != nil {
				failures.Add(1)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				failures.Add(1)
			}
		}(i)
	}
	wg.Wait()

	require.Equal(t, int32(0), failures.Load())
	require.Equal(t, n, consumer.count())
}

// TestRequiredFields covers all combinations of missing or explicit-null catalogs
// and catalogItems.  The consumer must never be called when validation fails.
func TestRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "When catalogs field is absent it should return 400",
			body: `{"revision":"r1","catalogItems":[]}`,
		},
		{
			name: "When catalogs is explicit null it should return 400",
			body: `{"revision":"r1","catalogs":null,"catalogItems":[]}`,
		},
		{
			name: "When catalogItems field is absent it should return 400",
			body: `{"revision":"r1","catalogs":[]}`,
		},
		{
			name: "When catalogItems is explicit null it should return 400",
			body: `{"revision":"r1","catalogs":[],"catalogItems":null}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			consumer := &fakeConsumer{}
			url, cleanup := startSource(t, consumer)
			defer cleanup()

			resp, err := http.Post(url, "application/json", strings.NewReader(tc.body)) //nolint:gosec // G107: URL is test-local loopback from startSource
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			require.Equal(t, 0, consumer.count(), "consumer must not be called on validation failure")
		})
	}
}

// TestEmptyArraysForwardedToConsumer verifies that explicit empty arrays are a
// valid complete snapshot (representing empty desired state) and are forwarded.
func TestEmptyArraysForwardedToConsumer(t *testing.T) {
	consumer := &fakeConsumer{}
	url, cleanup := startSource(t, consumer)
	defer cleanup()

	resp, err := http.Post(url, "application/json", //nolint:gosec // G107: URL is test-local loopback from startSource
		strings.NewReader(`{"revision":"empty","catalogs":[],"catalogItems":[]}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	snap := consumer.lastSnapshot()
	require.NotNil(t, snap)
	require.Equal(t, "empty", snap.Revision)
	require.NotNil(t, snap.Catalogs, "Catalogs slice must be non-nil")
	require.NotNil(t, snap.CatalogItems, "CatalogItems slice must be non-nil")
	require.Empty(t, snap.Catalogs)
	require.Empty(t, snap.CatalogItems)
}

// TestContentType covers Content-Type enforcement.
func TestContentType(t *testing.T) {
	validBody := `{"revision":"r1","catalogs":[],"catalogItems":[]}`

	cases := []struct {
		name         string
		contentType  string // empty means omit the header entirely
		expectStatus int
		expectCall   bool
	}{
		{
			name:         "When Content-Type is missing it should return 415",
			contentType:  "",
			expectStatus: http.StatusUnsupportedMediaType,
			expectCall:   false,
		},
		{
			name:         "When Content-Type is text/plain it should return 415",
			contentType:  "text/plain",
			expectStatus: http.StatusUnsupportedMediaType,
			expectCall:   false,
		},
		{
			name:         "When Content-Type is application/json it should return 204",
			contentType:  "application/json",
			expectStatus: http.StatusNoContent,
			expectCall:   true,
		},
		{
			name:         "When Content-Type is application/json with charset it should return 204",
			contentType:  "application/json; charset=utf-8",
			expectStatus: http.StatusNoContent,
			expectCall:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			consumer := &fakeConsumer{}
			url, cleanup := startSource(t, consumer)
			defer cleanup()

			req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(validBody))
			require.NoError(t, err)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, tc.expectStatus, resp.StatusCode)
			if tc.expectCall {
				require.Equal(t, 1, consumer.count(), "consumer must be called for valid requests")
			} else {
				require.Equal(t, 0, consumer.count(), "consumer must not be called when Content-Type is invalid")
			}
		})
	}
}

func TestGracefulContextCancellation(t *testing.T) {
	consumer := &fakeConsumer{}
	ctx, cancel := context.WithCancel(context.Background())

	f := NewFactory()
	src, err := f.CreateSource(ctx, catalogcollector.Settings{ID: catalogcollector.ComponentID{Type: Type, Name: "test"}, Logger: testLogger()},
		&Config{ListenAddress: "127.0.0.1:0", Path: "/v1/snapshots"}, consumer)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() { errCh <- src.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		require.NoError(t, err, "Run must return nil on context cancellation, not context.Canceled")
	case <-time.After(5 * time.Second):
		t.Fatal("source did not stop within 5s of context cancellation")
	}
}
