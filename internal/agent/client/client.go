package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
)

// RPCMetricsCallback defines the signature for RPC metrics callback functions.
type RPCMetricsCallback func(operation string, durationSeconds float64, err error)

// DeviceNotFoundCallback defines the signature for device not found callback functions.
type DeviceNotFoundCallback func(ctx context.Context) error

// NewFromConfig returns a new Flight Control API client with global 5xx retry and device 404 handling.
func NewFromConfig(config *baseclient.Config, deviceNotFoundCallback DeviceNotFoundCallback) (*client.ClientWithResponses, error) {
	httpClient, err := baseclient.NewHTTPClientFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("NewFromConfig: creating HTTP client %w", err)
	}

	return NewFromConfigWithHTTPClient(config, httpClient, deviceNotFoundCallback)
}

// NewFromConfigWithHTTPClient returns a new Flight Control API client using a pre-configured HTTP client
// with global 5xx retry and device 404 handling.
func NewFromConfigWithHTTPClient(config *baseclient.Config, httpClient *http.Client, deviceNotFoundCallback DeviceNotFoundCallback) (*client.ClientWithResponses, error) {
	// Wrap the HTTP client with device 404 detection and 5xx retry logic
	wrappedClient := &DeviceNotFoundHTTPClient{
		Client:   httpClient,
		Callback: deviceNotFoundCallback,
	}

	ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(middleware.RequestIDHeader, reqid.NextRequestID())
		return nil
	})
	return client.NewClientWithResponses(config.Service.Server, client.WithHTTPClient(wrappedClient), ref)
}

// DeviceNotFoundHTTPClient wraps an HTTP client to intercept device 404 responses and retry 5xx errors
type DeviceNotFoundHTTPClient struct {
	Client                 *http.Client
	Callback               DeviceNotFoundCallback
	reEnrollmentInProgress atomic.Bool // Prevents multiple simultaneous re-enrollment attempts
}

// Do implements the http.Client interface with 5xx retry logic and device 404 detection
func (c *DeviceNotFoundHTTPClient) Do(req *http.Request) (*http.Response, error) {
	// Create exponential backoff for 5xx errors
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 1 * time.Second
	b.MaxInterval = 5 * time.Minute
	b.MaxElapsedTime = 0 // 0 means never stop retrying
	b.Multiplier = 2.0
	b.RandomizationFactor = 0.1 // 10% jitter

	// Wrap with context from request
	contextBackoff := backoff.WithContext(b, req.Context())

	var finalResp *http.Response
	var finalErr error

	err := backoff.Retry(func() error {
		// Clone the request for retry attempts (important for request body)
		reqClone := c.cloneRequest(req)

		resp, err := c.Client.Do(reqClone)
		if err != nil {
			// Network errors should be retried
			return err
		}

		// Store response for final return
		finalResp = resp
		finalErr = nil

		// Check for 5xx errors to retry
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			// Close the response body before retrying
			resp.Body.Close()
			return fmt.Errorf("server error: status %d", resp.StatusCode)
		}

		// Check for device-specific 404 errors (don't retry, but trigger re-enrollment)
		if resp.StatusCode == http.StatusNotFound && c.isDeviceNotFoundResponse(resp) {
			// Only trigger re-enrollment if not already in progress
			if c.reEnrollmentInProgress.CompareAndSwap(false, true) {
				// Trigger re-enrollment callback in a separate goroutine to avoid blocking the request
				go func() {
					defer c.reEnrollmentInProgress.Store(false)

					if c.Callback != nil {
						// Create a new context with timeout for re-enrollment
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
						defer cancel()

						if err := c.Callback(ctx); err != nil {
							// Log error but don't fail the original request
							// The callback should handle its own logging
							_ = err
						}
					}
				}()
			}
		}

		// Success or non-retryable error
		return nil
	}, contextBackoff)

	if err != nil {
		return nil, err
	}

	return finalResp, finalErr
}

// cloneRequest creates a clone of the HTTP request for retry attempts
func (c *DeviceNotFoundHTTPClient) cloneRequest(req *http.Request) *http.Request {
	// Clone the request
	reqClone := req.Clone(req.Context())

	// If the request has a body, we need to handle it carefully for retries
	if req.Body != nil {
		// First, try to use GetBody if it's available (proper retry support)
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err == nil {
				reqClone.Body = body
				reqClone.GetBody = req.GetBody
				return reqClone
			}
		}

		// Fallback: Check if the body is seekable (like bytes.Reader from OpenAPI client)
		if seeker, ok := req.Body.(io.ReadSeeker); ok {
			// Try to seek back to the beginning
			if _, err := seeker.Seek(0, io.SeekStart); err == nil {
				// Successfully rewound, can reuse the same body
				reqClone.Body = req.Body
				reqClone.GetBody = req.GetBody
				return reqClone
			}
		}

		// Last resort: if we can't rewind or recreate the body,
		// we'll have to use the original body (this may fail on retry)
		reqClone.Body = req.Body
		reqClone.GetBody = req.GetBody
	}

	return reqClone
}

// isDeviceNotFoundResponse checks if the response is a device-specific 404 error
func (c *DeviceNotFoundHTTPClient) isDeviceNotFoundResponse(resp *http.Response) bool {
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		return false
	}

	// Read the response body to check for device-specific error message
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}

	// Reset the response body so it can be read again by the caller
	resp.Body = io.NopCloser(bytes.NewReader(body))

	// Try to parse as Status object
	var status v1alpha1.Status
	if err := json.Unmarshal(body, &status); err != nil {
		// If not JSON, check raw body for device not found message
		bodyStr := string(body)
		return strings.Contains(bodyStr, "Device of name") && strings.Contains(bodyStr, "not found")
	}

	// Check if it's a device-specific not found error
	return status.Code == http.StatusNotFound &&
		strings.Contains(status.Message, "Device of name") &&
		strings.Contains(status.Message, "not found")
}

func NewGRPCClientFromConfig(config *baseclient.Config) (grpc_v1.RouterServiceClient, error) {
	return baseclient.NewGRPCClientFromConfig(config, "")
}

// Management is the client interface for managing devices.
type Management interface {
	UpdateDeviceStatus(ctx context.Context, name string, device v1alpha1.Device, rcb ...client.RequestEditorFn) error
	GetRenderedDevice(ctx context.Context, name string, params *v1alpha1.GetRenderedDeviceParams, rcb ...client.RequestEditorFn) (*v1alpha1.Device, int, error)
	PatchDeviceStatus(ctx context.Context, name string, patch v1alpha1.PatchRequest, rcb ...client.RequestEditorFn) error
	SetRPCMetricsCallback(cb RPCMetricsCallback)
	CreateCertificateSigningRequest(ctx context.Context, csr v1alpha1.CertificateSigningRequest, rcb ...client.RequestEditorFn) (*v1alpha1.CertificateSigningRequest, int, error)
	GetCertificateSigningRequest(ctx context.Context, name string, rcb ...client.RequestEditorFn) (*v1alpha1.CertificateSigningRequest, int, error)
}

// Enrollment is client the interface for managing device enrollment.
type Enrollment interface {
	SetRPCMetricsCallback(cb RPCMetricsCallback)
	CreateEnrollmentRequest(ctx context.Context, req v1alpha1.EnrollmentRequest, cb ...client.RequestEditorFn) (*v1alpha1.EnrollmentRequest, error)
	GetEnrollmentRequest(ctx context.Context, id string, cb ...client.RequestEditorFn) (*v1alpha1.EnrollmentRequest, error)
}

type Bootc interface {
	// Status returns the current bootc status.
	Status(ctx context.Context) (*container.BootcHost, error)
	// Switch targets a new container image reference to boot.
	Switch(ctx context.Context, image string) error
	// UsrOverlay adds a transient writable overlayfs on `/usr` that will be discarded on reboot.
	UsrOverlay(ctx context.Context) error
	// Apply restart or reboot into the new target image.
	Apply(ctx context.Context) error
}

// IsCommandAvailable checks if a command is available in the PATH.
func IsCommandAvailable(cmdName string) bool {
	_, err := exec.LookPath(cmdName)
	return err == nil
}

func IsComposeAvailable() bool {
	for _, cmdName := range []string{"podman-compose", "docker-compose"} {
		if IsCommandAvailable(cmdName) {
			return true
		}
	}
	return false
}

type PullSecret struct {
	// Absolute path to the pull secret
	Path string
	// Cleanup function for temporary files, or no-op
	Cleanup func()
}

// ResolvePullSecret returns the image pull secret path, preferring inline spec
// auth then falling back to on disk.  Cleanup removes tmp files generated from
// inline spec if found and is otherwise a no-op.
func ResolvePullSecret(
	log *log.PrefixLogger,
	rw fileio.ReadWriter,
	desired *v1alpha1.DeviceSpec,
	authPath string,
) (*PullSecret, bool, error) {
	specContent, found, err := authFromSpec(log, desired, authPath)
	if err != nil {
		return nil, false, err
	}
	if found {
		exists, err := rw.PathExists(authPath)
		if err != nil {
			return nil, false, err
		}
		if exists {
			diskContent, err := rw.ReadFile(authPath)
			if err != nil {
				return nil, false, fmt.Errorf("reading existing auth file: %w", err)
			}

			if bytes.Equal(diskContent, specContent) {
				log.Debugf("Using on-disk pull secret (identical to spec): %s", authPath)
				return &PullSecret{Path: authPath, Cleanup: func() {}}, true, nil
			}
		}
		path, cleanup, err := fileio.WriteTmpFile(rw, "os_auth_", "auth.json", specContent, 0600)
		if err != nil {
			return nil, false, fmt.Errorf("writing inline auth file: %w", err)
		}
		log.Debugf("Using inline auth from device spec")
		return &PullSecret{Path: path, Cleanup: cleanup}, true, nil
	}

	exists, err := rw.PathExists(authPath)
	if err != nil {
		return nil, false, err
	}
	if exists {
		log.Debugf("Using on-disk pull secret: %s", authPath)
		return &PullSecret{Path: authPath, Cleanup: func() {}}, true, nil
	}

	return nil, false, nil
}

func authFromSpec(log *log.PrefixLogger, device *v1alpha1.DeviceSpec, authPath string) ([]byte, bool, error) {
	if device.Config == nil {
		return nil, false, nil
	}

	for _, provider := range *device.Config {
		pType, err := provider.Type()
		if err != nil {
			return nil, false, fmt.Errorf("provider type: %v", err)
		}
		if pType != v1alpha1.InlineConfigProviderType {
			// agent should only ever see inline config
			log.Errorf("Invalid config provider type: %s", pType)
			continue
		}
		spec, err := provider.AsInlineConfigProviderSpec()
		if err != nil {
			return nil, false, fmt.Errorf("convert inline config provider: %v", err)
		}

		for _, file := range spec.Inline {
			if strings.TrimSpace(file.Path) == authPath {
				// ensure content is properly decoded
				contents, err := fileio.DecodeContent(file.Content, file.ContentEncoding)
				if err != nil {
					log.Errorf("decode content: %v", err)
					continue
				}
				return contents, true, nil
			}
		}
	}

	return nil, false, nil
}

// ClientOption is a functional option for configuring the client.
type ClientOption func(*clientOptions)

type clientOptions struct {
	pullSecretPath string
	timeout        time.Duration
}

// WithPullSecret sets the path to the pull secret. If unset uses the default
// path for the runtime.
func WithPullSecret(path string) ClientOption {
	return func(opts *clientOptions) {
		opts.pullSecretPath = path
	}
}

// Timeout sets a custom timeout for the client operation.
// When defined, this value overrides the default client timeout.
func Timeout(timeout time.Duration) ClientOption {
	return func(opts *clientOptions) {
		opts.timeout = timeout
	}
}
