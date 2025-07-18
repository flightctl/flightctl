package client

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

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
