package client

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1alpha1"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
)

// RPCMetricsCallback defines the signature for RPC metrics callback functions.
type RPCMetricsCallback func(operation string, durationSeconds float64, err error)

// NewFromConfig returns a new Flight Control API client from the given config.
func NewFromConfig(config *baseclient.Config) (*client.ClientWithResponses, error) {
	httpClient, err := baseclient.NewHTTPClientFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("NewFromConfig: creating HTTP client %w", err)
	}
	ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(middleware.RequestIDHeader, reqid.NextRequestID())
		return nil
	})
	return client.NewClientWithResponses(config.Service.Server, client.WithHTTPClient(httpClient), ref)
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

// ClientOption is a functional option for configuring the client.
type ClientOption func(*clientOptions)

type clientOptions struct {
	retry          bool
	pullSecretPath string
}

// WithRetry enables enables retry based on the backoff config provided.
func WithRetry() ClientOption {
	return func(opts *clientOptions) {
		opts.retry = true
	}
}

// WithPullSecret sets the path to the pull secret. If unset uses the default
// path for the runtime.
func WithPullSecret(path string) ClientOption {
	return func(opts *clientOptions) {
		opts.pullSecretPath = path
	}
}
