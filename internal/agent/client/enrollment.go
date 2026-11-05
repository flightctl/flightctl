package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
)

var _ Enrollment = (*enrollment)(nil)

func NewEnrollment(
	client *client.ClientWithResponses, cb RPCMetricsCallback,
) Enrollment {
	return &enrollment{
		client:                 client,
		rpcMetricsCallbackFunc: cb,
	}
}

type enrollment struct {
	client                 *client.ClientWithResponses
	rpcMetricsCallbackFunc RPCMetricsCallback
}

func (e *enrollment) SetRPCMetricsCallback(cb RPCMetricsCallback) {
	e.rpcMetricsCallbackFunc = cb
}

func (e *enrollment) CreateEnrollmentRequest(ctx context.Context, req v1beta1.EnrollmentRequest, cb ...client.RequestEditorFn) (*v1beta1.EnrollmentRequest, error) {
	start := time.Now()
	resp, err := e.client.CreateEnrollmentRequestWithResponse(ctx, req, cb...)

	if e.rpcMetricsCallbackFunc != nil {
		e.rpcMetricsCallbackFunc("create_enrollmentrequest_duration", time.Since(start).Seconds(), err)
	}

	if err != nil {
		return nil, err
	}
	if resp.HTTPResponse != nil {
		defer func() { _ = resp.HTTPResponse.Body.Close() }()
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
	return nil, fmt.Errorf("create enrollmentrequest failed: %s", responseStatus(resp.Status(), resp.Body))
}

// responseStatus describes a non-success API response without discarding the
// server-provided message, which is the only clue for enrollment rejections.
func responseStatus(status string, body []byte) string {
	message := strings.TrimSpace(string(body))
	var apiStatus v1beta1.Status
	if err := json.Unmarshal(body, &apiStatus); err == nil && apiStatus.Message != "" {
		message = apiStatus.Message
	}
	if message == "" {
		message = ErrEmptyResponse.Error()
	}
	return fmt.Sprintf("%s: %s", status, message)
}

func (e *enrollment) GetEnrollmentRequest(ctx context.Context, id string, cb ...client.RequestEditorFn) (*v1beta1.EnrollmentRequest, error) {
	start := time.Now()
	resp, err := e.client.GetEnrollmentRequestWithResponse(ctx, id, cb...)

	if e.rpcMetricsCallbackFunc != nil {
		e.rpcMetricsCallbackFunc("get_enrollmentrequest_duration", time.Since(start).Seconds(), err)
	}

	if err != nil {
		return nil, err
	}
	if resp.HTTPResponse != nil {
		defer func() { _ = resp.HTTPResponse.Body.Close() }()
	}

	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("get enrollmentrequest failed: %s", responseStatus(resp.Status(), resp.Body))
	}

	return resp.JSON200, nil
}
