package client

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/go-chi/chi/v5/middleware"
)

// RPCMetricsCallback defines the signature for RPC metrics callback functions.
type RPCMetricsCallback func(operation string, durationSeconds float64, err error)

// NewFromConfig returns a new Flight Control API client from the given config.
func NewFromConfig(config *baseclient.Config, log *log.PrefixLogger, opts ...HTTPClientOption) (*client.ClientWithResponses, error) {
	options := &httpClientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	httpClient, err := baseclient.NewHTTPClientFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("NewFromConfig: creating HTTP client %w", err)
	}

	if options.retryConfig != nil {
		retryTransport := NewRetryTransport(httpClient.Transport, log, *options.retryConfig)
		httpClient.Transport = retryTransport
	}

	ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(middleware.RequestIDHeader, reqid.NextRequestID())
		for key, values := range options.httpHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		return nil
	})
	// Trim trailing slash to avoid double slash when appending /api/v1
	serverURL := baseclient.JoinServerURL(config.Service.Server, client.ServerUrlApiv1)
	return client.NewClientWithResponses(serverURL, client.WithHTTPClient(httpClient), ref)
}

// Management is the client interface for managing devices.
type Management interface {
	UpdateDeviceStatus(ctx context.Context, name string, device v1beta1.Device, rcb ...client.RequestEditorFn) error
	GetRenderedDevice(ctx context.Context, name string, params *v1beta1.GetRenderedDeviceParams, rcb ...client.RequestEditorFn) (*v1beta1.Device, int, error)
	PatchDeviceStatus(ctx context.Context, name string, patch v1beta1.PatchRequest, rcb ...client.RequestEditorFn) error
	SetRPCMetricsCallback(cb RPCMetricsCallback)
	CreateCertificateSigningRequest(ctx context.Context, csr v1beta1.CertificateSigningRequest, rcb ...client.RequestEditorFn) (*v1beta1.CertificateSigningRequest, int, error)
	GetCertificateSigningRequest(ctx context.Context, name string, rcb ...client.RequestEditorFn) (*v1beta1.CertificateSigningRequest, int, error)
}

// Enrollment is client the interface for managing device enrollment.
type Enrollment interface {
	SetRPCMetricsCallback(cb RPCMetricsCallback)
	CreateEnrollmentRequest(ctx context.Context, req v1beta1.EnrollmentRequest, cb ...client.RequestEditorFn) (*v1beta1.EnrollmentRequest, error)
	GetEnrollmentRequest(ctx context.Context, id string, cb ...client.RequestEditorFn) (*v1beta1.EnrollmentRequest, error)
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
	// Rollback swaps the bootloader ordering to boot into the previous deployment.
	Rollback(ctx context.Context) error
}

// IsCommandAvailable checks if a command is available in the PATH.
func IsCommandAvailable(cmdName string) bool {
	_, err := exec.LookPath(cmdName)
	return err == nil
}

// ClientOption is a functional option for configuring the client.
type ClientOption func(*clientOptions)

type clientOptions struct {
	pullSecretPath       string
	repositoryConfigPath string
	criConfigPath        string
	timeout              time.Duration
}

// WithPullSecret sets the path to the pull secret. If unset uses the default
// path for the runtime.
func WithPullSecret(path string) ClientOption {
	return func(opts *clientOptions) {
		opts.pullSecretPath = path
	}
}

// WithRepositoryConfig sets the path to the Helm repository configuration file.
// This is used for authenticating with HTTP-based Helm chart repositories.
func WithRepositoryConfig(path string) ClientOption {
	return func(opts *clientOptions) {
		opts.repositoryConfigPath = path
	}
}

// WithCRIConfig sets the path to the crictl configuration file.
// This is used for configuring the CRI runtime endpoint.
func WithCRIConfig(path string) ClientOption {
	return func(opts *clientOptions) {
		opts.criConfigPath = path
	}
}

// Timeout sets a custom timeout for the client operation.
// When defined, this value overrides the default client timeout.
func Timeout(timeout time.Duration) ClientOption {
	return func(opts *clientOptions) {
		opts.timeout = timeout
	}
}

// HTTPClientOption is a functional option for configuring the HTTP client
type HTTPClientOption func(*httpClientOptions)

type httpClientOptions struct {
	retryConfig *poll.Config
	httpHeaders http.Header
}

// WithHTTPRetry configures custom retry settings for the HTTP client
func WithHTTPRetry(config poll.Config) HTTPClientOption {
	return func(opts *httpClientOptions) {
		opts.retryConfig = &config
	}
}

// WithUserAgent returns an HTTPClientOption that sets the User-Agent header
// for outgoing requests using the flightctl-agent version and runtime information.
func WithUserAgent() HTTPClientOption {
	return func(opts *httpClientOptions) {
		info := version.Get()
		userAgent := fmt.Sprintf("flightctl-agent/%s (%s/%s)", info.String(), runtime.GOOS, runtime.GOARCH)
		WithHeader("User-Agent", userAgent)(opts)
	}
}

// WithHeader returns an HTTPClientOption that sets the given HTTP header for outgoing requests.
func WithHeader(key, value string) HTTPClientOption {
	return func(opts *httpClientOptions) {
		if opts.httpHeaders == nil {
			opts.httpHeaders = http.Header{}
		}
		opts.httpHeaders.Add(key, value)
	}
}
