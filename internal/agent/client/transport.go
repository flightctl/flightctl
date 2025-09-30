package client

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
)

type contextKey string

const responseHookKey contextKey = "responseHook"

// ResponseHook is called before retry decision is made
// Return true to continue with retry logic, false to stop retrying
type ResponseHook func(resp *http.Response, attempt int) bool

type RetryTransport struct {
	transport  http.RoundTripper
	pollConfig poll.Config
	log        *log.PrefixLogger
}

func NewRetryTransport(transport http.RoundTripper, log *log.PrefixLogger, pollConfig poll.Config) *RetryTransport {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &RetryTransport{
		transport:  transport,
		log:        log,
		pollConfig: pollConfig,
	}
}

func (r *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body.Close()
	}

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= r.pollConfig.MaxSteps; attempt++ {
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}

		if attempt > 0 {
			r.log.Debugf("Retry attempt %d/%d for %s %s", attempt, r.pollConfig.MaxSteps, req.Method, req.URL.Path)
		}

		resp, err = r.transport.RoundTrip(req)
		if err != nil {
			r.log.Debugf("Request failed with error: %v", err)
			return nil, err
		}

		// fire hook if defined
		if hook, ok := req.Context().Value(responseHookKey).(ResponseHook); ok {
			if !hook(resp, attempt) {
				return resp, nil
			}
		}

		if !shouldRetry(resp.StatusCode) || attempt >= r.pollConfig.MaxSteps {
			if shouldRetry(resp.StatusCode) && attempt >= r.pollConfig.MaxSteps {
				r.log.Debugf("Max retry attempts reached (%d), returning response with status %d", r.pollConfig.MaxSteps, resp.StatusCode)
			}
			return resp, nil
		}

		wait := poll.CalculateBackoffDelay(&r.pollConfig, attempt+1)

		// check Retry-After header
		if retryAfter := r.parseRetryAfter(resp); retryAfter > 0 {
			wait = retryAfter
			r.log.Debugf("Using Retry-After header value: %v", wait)
		}

		r.log.Debugf("Request failed with status %d, retrying in %v", resp.StatusCode, wait)

		resp.Body.Close()

		select {
		case <-time.After(wait):
			// retry
		case <-req.Context().Done():
			// honor context
			return nil, req.Context().Err()
		}
	}

	return resp, err
}

func shouldRetry(statusCode int) bool {
	// Retry on:
	// - 429: Too Many Requests (rate limiting)
	// - 5xx: All server errors (500-599)
	if statusCode == http.StatusTooManyRequests || (statusCode >= 500 && statusCode < 600) {
		return true
	}
	return false
}

// WithResponseHook creates a RequestEditorFn that adds a response hook to the request context
// The hook will be called before retry decisions are made
func WithResponseHook(hook ResponseHook) func(ctx context.Context, req *http.Request) error {
	return func(ctx context.Context, req *http.Request) error {
		*req = *req.WithContext(context.WithValue(req.Context(), responseHookKey, hook))
		return nil
	}
}

func (r *RetryTransport) parseRetryAfter(resp *http.Response) time.Duration {
	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(retryAfter); err == nil {
		duration := time.Duration(seconds) * time.Second
		// cap the retry-after to MaxDelay
		if r.pollConfig.MaxDelay > 0 && duration > r.pollConfig.MaxDelay {
			return r.pollConfig.MaxDelay
		}
		return duration
	}

	if t, err := http.ParseTime(retryAfter); err == nil {
		duration := time.Until(t)
		if duration < 0 {
			return 0
		}
		// cap the retry-after to MaxDelay
		if r.pollConfig.MaxDelay > 0 && duration > r.pollConfig.MaxDelay {
			return r.pollConfig.MaxDelay
		}
		return duration
	}

	return 0
}
