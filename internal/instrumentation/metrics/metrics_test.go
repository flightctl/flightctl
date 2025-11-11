package metrics

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

func TestMetricsServer_ServeAndScrape_OK(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// simple counter collector
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_counter_total",
		Help: "test counter",
	})
	counter.Inc() // ensure it appears

	s := NewMetricsServer(newSilentLogger(), counter)

	// run server
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx, WithListenAddr(addr))
	}()

	// wait until it accepts connections
	waitForReady(t, "http://"+addr+"/metrics")

	// scrape once
	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status: %d body=%s", resp.StatusCode, string(body))
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "test_counter_total") {
		t.Fatalf("expected metric name in body; got:\n%s", string(b)[:min(400, len(b))])
	}

	// shutdown
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server shutdown")
	}
}

func TestMetricsServer_HandlerWrapper_Applied(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "wrapper_probe",
		Help: "ensures wrapper executed",
	})
	g.Set(1)

	s := NewMetricsServer(newSilentLogger(), g)

	const hdrKey = "X-Test-Wrapper"
	wrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(hdrKey, "1")
			next.ServeHTTP(w, r)
		})
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx, WithListenAddr(addr), WithHandlerWrapper(wrap))
	}()

	waitForReady(t, "http://"+addr+"/metrics")

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/metrics", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if want := "1"; resp.Header.Get(hdrKey) != want {
		t.Fatalf("wrapper header missing or wrong: got %q want %q", resp.Header.Get(hdrKey), want)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server shutdown")
	}
}

func waitForReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec
		if err == nil {
			// any HTTP response means the server is up; drain body
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server did not become ready: %s", url)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp :0: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func newSilentLogger() *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.PanicLevel)
	l.Out = io.Discard
	return l
}
