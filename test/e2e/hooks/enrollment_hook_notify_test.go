package hooks

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	testinfra "github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	enrollmentNotifyPath              = "/enrollment-approved"
	enrollmentNotifyTimeout           = 2 * time.Minute
	enrollmentNotifyReadHeaderTimeout = 10 * time.Second
	enrollmentNotifyReadTimeout       = 30 * time.Second
	enrollmentNotifyMaxBodySize       = 64 * 1024
)

type enrollmentNotifyRequest struct {
	Method       string
	Path         string
	Body         []byte
	responseCode chan int
}

type enrollmentNotifyServer struct {
	server       *http.Server
	listener     net.Listener
	received     chan enrollmentNotifyRequest
	release      chan struct{}
	releaseOnce  sync.Once
	responseCode chan int
}

var _ = Describe("Enrollment hook notify and fleet gate", Label(enrollmentHookScenarioLabel), Serial, func() {

	It("transitions NotifyPending to Pending and gates fleet/rendered state", Label("EDM-5712", "90608", enrollmentHookAgentLabel, enrollmentHookSlowLabel), func() {
		if !testinfra.IsQuadletEnvironment() {
			Skip("notify webhook E2E requires a VM/Quadlet environment with host endpoint reachability")
		}

		harness := e2e.GetWorkerHarness()
		notifyServer, err := newEnrollmentNotifyServer()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(notifyServer.Close)

		policy := newEnrollmentHookPolicy(notifyServer.URL(), v1beta1.FailurePolicyBlock)
		statusCode, err := createEnrollmentHookPolicy(harness, policy)
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusCreated))

		labels := enrollmentHookLabels(harness)
		Expect(harness.CreateFleetWithSelector(enrollmentHookFleetName, labels)).To(Succeed())
		enrollmentID, err := waitForEnrollmentRequest(harness)
		Expect(err).ToNot(HaveOccurred())
		statusCode, err = approveEnrollmentRequest(harness, enrollmentID, labels)
		Expect(err).ToNot(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusOK))

		condition, err := waitForEnrollmentHooksCondition(harness, enrollmentID, v1beta1.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonNotifyPending)
		Expect(err).ToNot(HaveOccurred())
		Expect(condition.Reason).To(Equal(v1beta1.EnrollmentHooksReasonNotifyPending))
		Expect(harness.PathDoesNotExistOnDevice(enrollmentHookAfterMarker)).To(Succeed())

		request, err := notifyServer.WaitForRequest(enrollmentNotifyTimeout)
		Expect(err).ToNot(HaveOccurred())
		Expect(request.Method).To(Equal(http.MethodPost))
		Expect(request.Path).To(Equal(enrollmentNotifyPath))
		Expect(request.Body).ToNot(BeEmpty())
		Expect(validateEnrollmentApprovedPayload(request.Body, enrollmentID, labels)).To(Succeed())

		device, err := harness.GetDevice(enrollmentID)
		Expect(err).ToNot(HaveOccurred())
		Expect(device.Metadata.Owner).To(BeNil())
		renderedStatus, err := waitForStatusCode(harness, enrollmentID, http.StatusConflict, getRenderedDeviceStatusCode)
		Expect(err).ToNot(HaveOccurred())
		Expect(renderedStatus).To(Equal(http.StatusConflict))

		notifyServer.Release()
		responseStatus, err := request.WaitForResponse(enrollmentNotifyTimeout)
		Expect(err).ToNot(HaveOccurred())
		Expect(responseStatus).To(Equal(http.StatusNoContent))

		condition, err = waitForEnrollmentHooksCondition(harness, enrollmentID, v1beta1.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonPending)
		Expect(err).ToNot(HaveOccurred())
		Expect(condition.Reason).To(Equal(v1beta1.EnrollmentHooksReasonPending))

		Expect(harness.WriteAgentFile(enrollmentHookReleaseFile, "")).To(Succeed())
		condition, err = waitForEnrollmentHooksCondition(harness, enrollmentID, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)
		Expect(err).ToNot(HaveOccurred())
		Expect(condition.Reason).To(Equal(v1beta1.EnrollmentHooksReasonSucceeded))
		Expect(harness.PathExistsOnDevice(enrollmentHookAfterMarker)).To(Succeed())
	})
})

func newEnrollmentNotifyServer() (*enrollmentNotifyServer, error) {
	host := auxiliary.GetHostIP()
	if host == "" {
		return nil, fmt.Errorf("auxiliary host IP is empty")
	}
	certDir := filepath.Join(util.GetTopLevelDir(), "bin", "e2e-certs", "pki", "CA")
	cert, err := tls.LoadX509KeyPair(filepath.Join(certDir, "registry.crt"), filepath.Join(certDir, "registry.key"))
	if err != nil {
		return nil, fmt.Errorf("load E2E TLS certificate from %s: %w", certDir, err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, fmt.Errorf("listen for enrollment notify server on %s: %w", host, err)
	}
	notify := &enrollmentNotifyServer{
		listener:     listener,
		received:     make(chan enrollmentNotifyRequest, 1),
		release:      make(chan struct{}),
		responseCode: make(chan int, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(enrollmentNotifyPath, notify.handle)
	notify.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: enrollmentNotifyReadHeaderTimeout,
		ReadTimeout:       enrollmentNotifyReadTimeout,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	go func() {
		if serveErr := notify.server.ServeTLS(listener, "", ""); serveErr != nil && serveErr != http.ErrServerClosed {
			GinkgoWriter.Printf("Enrollment notify server stopped with error: %v\n", serveErr)
		}
	}()
	return notify, nil
}

func (s *enrollmentNotifyServer) URL() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return "https://" + s.listener.Addr().String() + enrollmentNotifyPath
}

func (s *enrollmentNotifyServer) handle(writer http.ResponseWriter, request *http.Request) {
	requestBody := make([]byte, 0)
	var err error
	if request.Body != nil {
		request.Body = http.MaxBytesReader(writer, request.Body, enrollmentNotifyMaxBodySize)
		requestBody, err = readRequestBody(request)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
	}
	received := enrollmentNotifyRequest{
		Method:       request.Method,
		Path:         request.URL.Path,
		Body:         requestBody,
		responseCode: s.responseCode,
	}
	select {
	case s.received <- received:
	default:
		GinkgoWriter.Printf("Dropping enrollment notify request because the receive channel is full\n")
	}
	select {
	case <-s.release:
	case <-request.Context().Done():
		GinkgoWriter.Printf("Enrollment notify request canceled before release: %v\n", request.Context().Err())
		return
	}
	statusCode := http.StatusNoContent
	writer.WriteHeader(statusCode)
	select {
	case s.responseCode <- statusCode:
	default:
		GinkgoWriter.Printf("Dropping duplicate enrollment notify response status %d\n", statusCode)
	}
}

func (s *enrollmentNotifyServer) WaitForRequest(timeout time.Duration) (enrollmentNotifyRequest, error) {
	if s == nil {
		return enrollmentNotifyRequest{}, fmt.Errorf("notify server is nil")
	}
	select {
	case request := <-s.received:
		return request, nil
	case <-time.After(timeout):
		return enrollmentNotifyRequest{}, fmt.Errorf("timed out waiting for enrollment notify request")
	}
}

// WaitForResponse waits until the notify server has written its HTTP response.
func (r enrollmentNotifyRequest) WaitForResponse(timeout time.Duration) (int, error) {
	if r.responseCode == nil {
		return 0, fmt.Errorf("notify response channel is nil")
	}
	select {
	case statusCode := <-r.responseCode:
		return statusCode, nil
	case <-time.After(timeout):
		return 0, fmt.Errorf("timed out waiting for enrollment notify response")
	}
}

func (s *enrollmentNotifyServer) Release() {
	if s != nil {
		s.releaseOnce.Do(func() { close(s.release) })
	}
}

func (s *enrollmentNotifyServer) Close() {
	if s == nil {
		return
	}
	s.Release()
	if s.server != nil {
		if err := s.server.Close(); err != nil {
			GinkgoWriter.Printf("Warning: failed to close enrollment notify server: %v\n", err)
		}
	}
}

func readRequestBody(request *http.Request) (body []byte, err error) {
	if request == nil || request.Body == nil {
		return nil, fmt.Errorf("notify request body is nil")
	}
	defer func() {
		if closeErr := request.Body.Close(); closeErr != nil {
			GinkgoWriter.Printf("Warning: failed to close enrollment notify request body: %v\n", closeErr)
			if err == nil {
				err = fmt.Errorf("close enrollment notify request body: %w", closeErr)
			}
		}
	}()
	return io.ReadAll(request.Body)
}

func validateEnrollmentApprovedPayload(body []byte, enrollmentID string, labels map[string]string) error {
	var payload struct {
		APIVersion string                     `json:"apiVersion"`
		Kind       string                     `json:"kind"`
		Device     map[string]json.RawMessage `json:"device"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("decode webhook payload: %w", err)
	}
	if payload.APIVersion != "v1beta1" {
		return fmt.Errorf("unexpected payload apiVersion %q", payload.APIVersion)
	}
	if payload.Kind != "EnrollmentApproved" {
		return fmt.Errorf("unexpected payload kind %q", payload.Kind)
	}
	if len(payload.Device) == 0 {
		return fmt.Errorf("webhook payload device subset is empty")
	}
	if _, ok := payload.Device["metadata"]; !ok {
		return fmt.Errorf("webhook payload device metadata is missing")
	}
	if _, ok := payload.Device["spec"]; ok {
		return fmt.Errorf("webhook payload must not include device spec")
	}
	if _, ok := payload.Device["status"]; ok {
		return fmt.Errorf("webhook payload must not include device status")
	}
	metadata := map[string]json.RawMessage{}
	if err := json.Unmarshal(payload.Device["metadata"], &metadata); err != nil {
		return fmt.Errorf("decode webhook device metadata: %w", err)
	}
	if name, ok := metadata["name"]; !ok || !strings.Contains(string(name), enrollmentID) {
		return fmt.Errorf("webhook device metadata name does not contain enrollment ID %q", enrollmentID)
	}
	labelsJSON, ok := metadata["labels"]
	if !ok {
		return fmt.Errorf("webhook device metadata labels are missing")
	}
	var receivedLabels map[string]string
	if err := json.Unmarshal(labelsJSON, &receivedLabels); err != nil {
		return fmt.Errorf("decode webhook device labels: %w", err)
	}
	for key, value := range labels {
		if receivedLabels[key] != value {
			return fmt.Errorf("webhook device label %q=%q, expected %q", key, receivedLabels[key], value)
		}
	}
	return nil
}
