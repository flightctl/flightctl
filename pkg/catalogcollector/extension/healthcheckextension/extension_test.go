package healthcheckextension

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *logrus.Entry {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log.WithField("test", true)
}

func testSettings(name string) catalogcollector.Settings {
	return catalogcollector.Settings{
		ID:     catalogcollector.ComponentID{Type: Type, Name: name},
		Logger: testLogger(),
	}
}

// freePort returns a host:port string with an ephemeral port allocated by the OS.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// startExtension creates, starts, and returns the extension. The caller is
// responsible for calling Shutdown.
func startExtension(t *testing.T, cfg *Config) *extension {
	t.Helper()
	ext, err := NewFactory().CreateExtension(
		context.Background(),
		testSettings("test"),
		cfg,
	)
	require.NoError(t, err)
	require.NoError(t, ext.Start(context.Background(), nil))
	t.Cleanup(func() {
		_ = ext.Shutdown(context.Background())
	})
	return ext.(*extension)
}

func probeResponse(t *testing.T, url string) (int, map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return resp.StatusCode, body
}

// --- Factory tests ---

func TestFactory_Type(t *testing.T) {
	f := NewFactory()
	assert.Equal(t, catalogcollector.ComponentType("healthcheck"), f.Type())
}

func TestFactory_NilLoggerRejected(t *testing.T) {
	_, err := NewFactory().CreateExtension(
		context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test"},
			Logger: nil,
		},
		&Config{
			Endpoint:  "localhost:13133",
			LivePath:  "/livez",
			ReadyPath: "/readyz",
		},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "logger must not be nil")
}

func TestFactory_InvalidConfigRejected(t *testing.T) {
	_, err := NewFactory().CreateExtension(
		context.Background(),
		testSettings("test"),
		&Config{Endpoint: "", LivePath: "/livez", ReadyPath: "/readyz"},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "endpoint")
}

func TestFactory_WrongConfigTypeRejected(t *testing.T) {
	type bogus struct{}
	_, err := NewFactory().CreateExtension(
		context.Background(),
		testSettings("test"),
		&bogus{},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "unexpected config type")
}

// --- Liveness and readiness probe tests ---

func TestLivez_ReturnsLive(t *testing.T) {
	addr := freePort(t)
	ext := startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})
	_ = ext

	code, body := probeResponse(t, "http://"+addr+"/livez")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "live", body["status"])
}

func TestReadyz_NotReadyBeforeReady(t *testing.T) {
	addr := freePort(t)
	ext := startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})
	_ = ext

	code, body := probeResponse(t, "http://"+addr+"/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "not_ready", body["status"])
}

func TestReadyz_ReadyAfterReady(t *testing.T) {
	addr := freePort(t)
	ext := startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})
	ext.Ready()

	code, body := probeResponse(t, "http://"+addr+"/readyz")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ready", body["status"])
}

func TestReadyz_NotReadyAfterNotReady(t *testing.T) {
	addr := freePort(t)
	ext := startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})
	ext.Ready()
	ext.NotReady()

	code, body := probeResponse(t, "http://"+addr+"/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "not_ready", body["status"])
}

// --- HTTP method tests ---

func TestProbe_GETReturnsBody(t *testing.T) {
	addr := freePort(t)
	startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/livez", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"status"`)
}

func TestProbe_HEADReturnsNoBody(t *testing.T) {
	addr := freePort(t)
	startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "http://"+addr+"/livez", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, raw, "HEAD response must have empty body")
}

func TestProbe_MethodNotAllowed(t *testing.T) {
	addr := freePort(t)
	startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run("When method is "+method+" it should return 405", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+"/livez", nil)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
			assert.Equal(t, "GET, HEAD", resp.Header.Get("Allow"))
		})
	}
}

// --- Bind failure test ---

func TestStart_BindFailurePropagates(t *testing.T) {
	// Occupy a port so the extension cannot bind to it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	occupiedAddr := l.Addr().String()

	ext, createErr := NewFactory().CreateExtension(
		context.Background(),
		testSettings("test"),
		&Config{
			Endpoint:  occupiedAddr,
			LivePath:  "/livez",
			ReadyPath: "/readyz",
		},
	)
	require.NoError(t, createErr)

	startErr := ext.Start(context.Background(), nil)
	require.Error(t, startErr)
	assert.ErrorContains(t, startErr, "listening on healthcheck endpoint")
}

// --- Graceful shutdown test ---

func TestShutdown_ClosesListener(t *testing.T) {
	addr := freePort(t)
	ext, err := NewFactory().CreateExtension(
		context.Background(),
		testSettings("test"),
		&Config{
			Endpoint:  addr,
			LivePath:  "/livez",
			ReadyPath: "/readyz",
		},
	)
	require.NoError(t, err)
	require.NoError(t, ext.Start(context.Background(), nil))

	// Verify server is responding.
	code, _ := probeResponse(t, "http://"+addr+"/livez")
	require.Equal(t, http.StatusOK, code)

	// Shutdown the extension.
	require.NoError(t, ext.Shutdown(context.Background()))

	// After shutdown, connecting should fail.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/livez", nil)
	require.NoError(t, reqErr)
	_, doErr := http.DefaultClient.Do(req)
	require.Error(t, doErr, "request after shutdown must fail")
}

// --- Interface compliance tests ---

func TestExtension_ImplementsReadiness(t *testing.T) {
	ext, err := NewFactory().CreateExtension(
		context.Background(),
		testSettings("test"),
		&Config{
			Endpoint:  "127.0.0.1:13133",
			LivePath:  "/livez",
			ReadyPath: "/readyz",
		},
	)
	require.NoError(t, err)

	_, ok := ext.(catalogcollector.Readiness)
	assert.True(t, ok, "extension must implement catalogcollector.Readiness")
}

// --- Concurrent Ready/NotReady/probe stress test (covered by -race flag) ---

func TestConcurrentReadyNotReadyProbes(t *testing.T) {
	addr := freePort(t)
	ext := startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})

	const goroutines = 10
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Concurrently toggle Ready/NotReady.
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				ext.Ready()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				ext.NotReady()
			}
		}()
	}

	// Concurrently probe both endpoints.
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Alternate between live and ready probes.
				var url string
				if j%2 == 0 {
					url = "http://" + addr + "/livez"
				} else {
					url = "http://" + addr + "/readyz"
				}
				resp, err := client.Get(url) //nolint:gosec
				if err != nil {
					continue // connection errors are expected during concurrent toggle
				}
				resp.Body.Close()
			}
		}()
	}

	wg.Wait()
}

// --- Readiness idempotency tests ---

func TestReady_IdempotentMultipleCalls(t *testing.T) {
	addr := freePort(t)
	ext := startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})

	// Calling Ready multiple times should not panic or change behavior.
	ext.Ready()
	ext.Ready()
	ext.Ready()

	code, body := probeResponse(t, "http://"+addr+"/readyz")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ready", body["status"])
}

func TestNotReady_IdempotentMultipleCalls(t *testing.T) {
	addr := freePort(t)
	ext := startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/livez",
		ReadyPath: "/readyz",
	})

	ext.Ready()
	ext.NotReady()
	ext.NotReady()
	ext.NotReady()

	code, body := probeResponse(t, "http://"+addr+"/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "not_ready", body["status"])
}

// --- Shutdown marks not ready ---

func TestShutdown_MarksNotReady(t *testing.T) {
	ext, err := NewFactory().CreateExtension(
		context.Background(),
		testSettings("test"),
		&Config{
			Endpoint:  freePort(t),
			LivePath:  "/livez",
			ReadyPath: "/readyz",
		},
	)
	require.NoError(t, err)
	require.NoError(t, ext.Start(context.Background(), nil))

	// Mark ready, then shutdown.
	ext.(*extension).Ready()
	require.True(t, ext.(*extension).ready.Load(), "must be ready before shutdown")

	require.NoError(t, ext.Shutdown(context.Background()))
	assert.False(t, ext.(*extension).ready.Load(), "shutdown must mark not ready")
}

// --- Custom paths test ---

func TestExtension_CustomPaths(t *testing.T) {
	addr := freePort(t)
	ext := startExtension(t, &Config{
		Endpoint:  addr,
		LivePath:  "/health/live",
		ReadyPath: "/health/ready",
	})
	ext.Ready()

	code, body := probeResponse(t, "http://"+addr+"/health/live")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "live", body["status"])

	code, body = probeResponse(t, "http://"+addr+"/health/ready")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ready", body["status"])
}
