package client

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
)

var _ Enrollment = (*enrollment)(nil)

func NewEnrollment(
	client *client.ClientWithResponses,
) Enrollment {
	return &enrollment{
		client: client,
	}
}

type enrollment struct {
	client                 *client.ClientWithResponses
	rpcMetricsCallbackFunc func(operation string, durationSeconds float64, err error)
}

func (e *enrollment) SetRPCMetricsCallback(cb func(operation string, durationSeconds float64, err error)) {
	e.rpcMetricsCallbackFunc = cb
}

func (e *enrollment) CreateEnrollmentRequest(ctx context.Context, req v1alpha1.EnrollmentRequest, cb ...client.RequestEditorFn) (*v1alpha1.EnrollmentRequest, error) {
	start := time.Now()
	resp, err := e.client.CreateEnrollmentRequestWithResponse(ctx, req, cb...)
	if err != nil {
		return nil, err
	}
	if resp.HTTPResponse != nil {
		defer func() { _ = resp.HTTPResponse.Body.Close() }()
	}

	if e.rpcMetricsCallbackFunc != nil {
		e.rpcMetricsCallbackFunc("create_enrollmentrequest_duration", time.Since(start).Seconds(), err)
	}

	switch resp.StatusCode() {
	case http.StatusCreated:
		if resp.JSON201 != nil {
			return resp.JSON201, nil
		}
	case http.StatusConflict:
		// An enrollment request already exists, so get and return it
		return e.GetEnrollmentRequest(ctx, *req.Metadata.Name)
	}
	return nil, fmt.Errorf("create enrollmentrequest failed: %s", ErrEmptyResponse)
}

func (e *enrollment) GetEnrollmentRequest(ctx context.Context, id string, cb ...client.RequestEditorFn) (*v1alpha1.EnrollmentRequest, error) {
	start := time.Now()
	resp, err := e.client.GetEnrollmentRequestWithResponse(ctx, id, cb...)
	if err != nil {
		return nil, err
	}
	if resp.HTTPResponse != nil {
		defer func() { _ = resp.HTTPResponse.Body.Close() }()
	}

	if e.rpcMetricsCallbackFunc != nil {
		e.rpcMetricsCallbackFunc("get_enrollmentrequest_duration", time.Since(start).Seconds(), err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("get enrollmentrequest failed: %s", resp.Status())
	}

	if resp.JSON200 == nil {
		return nil, fmt.Errorf("get enrollmentrequest failed: %s", ErrEmptyResponse)
	}

	return resp.JSON200, nil
}
