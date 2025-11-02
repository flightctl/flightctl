package pprof

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestDefaultPprofOptions(t *testing.T) {
	opts := defaultPprofOptions()
	if opts.port != pprofPortDefault {
		t.Fatalf("default port = %d, want %d", opts.port, pprofPortDefault)
	}
	if opts.cpuCap != pprofCPUCapDefault {
		t.Fatalf("default cpuCap = %v, want %v", opts.cpuCap, pprofCPUCapDefault)
	}
	if opts.traceCap != pprofTraceCapDefault {
		t.Fatalf("default traceCap = %v, want %v", opts.traceCap, pprofTraceCapDefault)
	}
}

func TestComputeWriteTimeout(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	// case 1: cpuCap > traceCap
	s := NewPprofServer(logger, WithCPUCap(3*time.Second), WithTraceCap(1*time.Second))
	got := s.computeWriteTimeout()
	want := 3*time.Second + 5*time.Second
	if got != want {
		t.Fatalf("computeWriteTimeout (cpu larger) = %v, want %v", got, want)
	}

	// case 2: traceCap > cpuCap
	s = NewPprofServer(logger, WithCPUCap(1*time.Second), WithTraceCap(4*time.Second))
	got = s.computeWriteTimeout()
	want = 4*time.Second + 5*time.Second
	if got != want {
		t.Fatalf("computeWriteTimeout (trace larger) = %v, want %v", got, want)
	}
}

func TestCapSecondsCapsAndInvalid(t *testing.T) {
	// cap at 2s
	capDur := 2 * time.Second
	var seen string

	// Dummy handler records the final "seconds" query value
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query().Get("seconds")
		w.WriteHeader(http.StatusOK)
	})

	wrapped := capSeconds(h, capDur)

	reqDo := func(u string) {
		t.Helper()
		seen = "" // reset for clarity per call

		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q) error: %v", u, err)
		}

		rr := httptestRespRec()
		wrapped.ServeHTTP(rr, req)

		if rr.code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.code, http.StatusOK)
		}
		if seen != "2" {
			t.Fatalf("capped seconds = %q, want %q", seen, "2")
		}
	}

	reqDo("http://127.0.0.1/debug/pprof/profile?seconds=10")
	reqDo("http://127.0.0.1/debug/pprof/profile?seconds=abc")
	reqDo("http://127.0.0.1/debug/pprof/profile?seconds=0")
	reqDo("http://127.0.0.1/debug/pprof/profile")
}

func TestRun_StartsServesAndShutsDown(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatalf("freeLoopbackPort error: %v", err)
	}

	// Keep caps tiny so the test is fast.
	cpuCap := 1 * time.Second
	traceCap := 1 * time.Second

	s := NewPprofServer(
		logger,
		WithPort(port),
		WithCPUCap(cpuCap),
		WithTraceCap(traceCap),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	// Wait for readiness: /debug/pprof/ should respond 200
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	if err := waitHTTP200(baseURL+"/debug/pprof/", 3*time.Second); err != nil {
		cancel()
		t.Fatalf("server not ready in time: %v", err)
	}

	// Sanity: index endpoint should contain standard pprof words like "heap" or "profile"
	resp, err := http.Get(baseURL + "/debug/pprof/")
	if err != nil {
		cancel()
		t.Fatalf("GET index error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("index status = %d, want 200", resp.StatusCode)
	}
	text := strings.ToLower(string(body))
	if !strings.Contains(text, "heap") && !strings.Contains(text, "profile") {
		cancel()
		t.Fatalf("index body doesn't look like pprof index")
	}

	// Hitting /profile with a large 'seconds' should be capped to ~1s
	start := time.Now()
	resp, err = http.Get(baseURL + "/debug/pprof/profile?seconds=9")
	if err != nil {
		cancel()
		t.Fatalf("GET profile error: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	elapsed := time.Since(start)

	// It should finish reasonably close to cpuCap (with some headroom).
	if elapsed > 3*time.Second {
		cancel()
		t.Fatalf("profile took too long: %v (cap %v)", elapsed, cpuCap)
	}

	// Now shutdown and ensure Run returns cleanly.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for server shutdown")
	}
}

/* --- helpers --- */

type respRecorder struct {
	header http.Header
	code   int
}

func httptestRespRec() *respRecorder {
	return &respRecorder{header: make(http.Header), code: 200}
}

func (r *respRecorder) Header() http.Header         { return r.header }
func (r *respRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *respRecorder) WriteHeader(statusCode int)  { r.code = statusCode }

func freeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.Port, nil
}

func waitHTTP200(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				_ = resp.Body.Close()
				return nil
			}
			_ = resp.Body.Close()
			lastErr = err
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = context.DeadlineExceeded
	}
	return lastErr
}
