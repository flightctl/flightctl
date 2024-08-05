package client

import (
	"context"
	"fmt"
	"net/http"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1alpha1"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/middleware"
)

type ClientFactory[T any] interface {
	CreateClient(config *baseclient.Config) (T, error)
}

// NewFromConfig returns a new FlightCtl API client from the given config.
func NewFromConfig(config *baseclient.Config) (*client.ClientWithResponses, error) {

	httpClient, err := baseclient.NewHTTPClientFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("NewFromConfig: creating HTTP client %w", err)
	}
	ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(middleware.RequestIDHeader, reqid.GetReqID())
		return nil
	})
	return client.NewClientWithResponses(config.Service.Server, client.WithHTTPClient(httpClient), ref)
}

func NewGRPCClientFromConfig(config *baseclient.Config, endpoint string) (grpc_v1.RouterServiceClient, error) {
	return baseclient.NewGRPCClientFromConfig(config, endpoint)
}

type Config = baseclient.Config
type AuthInfo = baseclient.AuthInfo
type Service = baseclient.Service

func NewDefault() *Config {
	return baseclient.NewDefault()
}

// Management is the client interface for managing devices.
type Management interface {
	UpdateDeviceStatus(ctx context.Context, name string, device v1alpha1.Device, rcb ...client.RequestEditorFn) error
	GetRenderedDeviceSpec(ctx context.Context, name string, params *v1alpha1.GetRenderedDeviceSpecParams, rcb ...client.RequestEditorFn) (*v1alpha1.RenderedDeviceSpec, int, error)
}

// Enrollment is client the interface for managing device enrollment.
type Enrollment interface {
	SetRPCMetricsCallback(cb func(operation string, durationSeconds float64, err error))
	CreateEnrollmentRequest(ctx context.Context, req v1alpha1.EnrollmentRequest, cb ...client.RequestEditorFn) (*v1alpha1.EnrollmentRequest, error)
	GetEnrollmentRequest(ctx context.Context, id string, cb ...client.RequestEditorFn) (*v1alpha1.EnrollmentRequest, error)
}
