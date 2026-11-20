//go:build linux

package packagemodemigration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	e2e "github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/stretchr/testify/require"
)

func TestWaitForEnrollmentRequestRespectsDeadline(t *testing.T) {
	// A server that delays longer than the client deadline. Using a finite
	// sleep instead of blocking on r.Context().Done() lets httptest.Close
	// drain cleanly without hanging.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer srv.Close()

	client, err := apiclient.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = e2e.WaitForEnrollmentRequestWithContext(ctx, client, "test-id", 50*time.Millisecond)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, elapsed, 2*time.Second, "must unblock near the deadline, not hang")
}

func TestWaitForEnrollmentRequestSucceeds(t *testing.T) {
	// A server that returns a valid enrollment request on GET.
	er := v1beta1.EnrollmentRequest{Metadata: v1beta1.ObjectMeta{Name: strPtr("test-id")}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(er) //nolint:errcheck
	}))
	defer srv.Close()

	client, err := apiclient.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	result, err := e2e.WaitForEnrollmentRequestWithContext(context.Background(), client, "test-id", 50*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestApproveEnrollmentRespectsDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer srv.Close()

	client, err := apiclient.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = e2e.ApproveEnrollmentWithContext(ctx, client, "test-id", map[string]string{"key": "val"})
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Less(t, elapsed, 2*time.Second)
}

func TestApproveEnrollmentPreservesLabels(t *testing.T) {
	var receivedLabels map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var approval v1beta1.EnrollmentRequestApproval
		json.NewDecoder(r.Body).Decode(&approval) //nolint:errcheck
		if approval.Labels != nil {
			receivedLabels = *approval.Labels
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := apiclient.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	err = e2e.ApproveEnrollmentWithContext(context.Background(), client, "test-id",
		map[string]string{"fleet": "my-fleet", "test-id": "abc"})
	require.NoError(t, err)
	require.Equal(t, "my-fleet", receivedLabels["fleet"])
	require.Equal(t, "abc", receivedLabels["test-id"])
}

func TestCheckDeviceOnlineRespectsDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer srv.Close()

	client, err := apiclient.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = e2e.CheckDeviceOnlineWithContext(ctx, client, "test-device")
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Less(t, elapsed, 2*time.Second)
}

func TestCheckDeviceOnlineReturnsNilWhenNotReady(t *testing.T) {
	// Return a device that is NOT online.
	device := v1beta1.Device{
		Metadata: v1beta1.ObjectMeta{Name: strPtr("test-device")},
		Status: &v1beta1.DeviceStatus{
			Summary: v1beta1.DeviceSummaryStatus{Status: v1beta1.DeviceSummaryStatusDegraded},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(device) //nolint:errcheck
	}))
	defer srv.Close()

	client, err := apiclient.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	result, err := e2e.CheckDeviceOnlineWithContext(context.Background(), client, "test-device")
	require.NoError(t, err)
	require.Nil(t, result, "non-online device must return nil")
}

func TestCheckDeviceOnlineReturnsDeviceWhenOnline(t *testing.T) {
	device := v1beta1.Device{
		Metadata: v1beta1.ObjectMeta{Name: strPtr("test-device")},
		Status: &v1beta1.DeviceStatus{
			Summary: v1beta1.DeviceSummaryStatus{Status: v1beta1.DeviceSummaryStatusOnline},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(device) //nolint:errcheck
	}))
	defer srv.Close()

	client, err := apiclient.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	result, err := e2e.CheckDeviceOnlineWithContext(context.Background(), client, "test-device")
	require.NoError(t, err)
	require.NotNil(t, result, "online device must be returned")
}

func TestPreCanceledContextReturnsImmediately(t *testing.T) {
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := apiclient.NewClientWithResponses(srv.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled

	_, err = e2e.WaitForEnrollmentRequestWithContext(ctx, client, "test-id", 50*time.Millisecond)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func strPtr(s string) *string { return &s }
