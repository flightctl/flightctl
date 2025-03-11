package agenttransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	agentServer "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/transport"
	"github.com/sirupsen/logrus"
)

type AgentTransportHandler struct {
	serviceHandler *service.ServiceHandler
	log            logrus.FieldLogger
}

// Make sure we conform to servers Service interface
var _ agentServer.Transport = (*AgentTransportHandler)(nil)

func ValidateDeviceAccessFromContext(ctx context.Context, name string, log logrus.FieldLogger) error {
	cn, ok := ctx.Value(middleware.TLSCommonNameContextKey).(string)
	if !ok {
		log.Warningf("an attempt to access device %q without a CN in tls certificate has been detected", name)
		return errors.New("no common name in certificate")
	}
	if expectedCn, err := crypto.CNFromDeviceFingerprint(name); err != nil {
		return err
	} else if cn != expectedCn {
		log.Warningf("an attempt to access device %q with a certificate with CN %q has been detected", name, cn)
		return errors.New("invalid tls CN for device")
	}
	// all good, you shall pass
	return nil
}

func ValidateEnrollmentAccessFromContext(ctx context.Context, log logrus.FieldLogger) error {
	cn, ok := ctx.Value(middleware.TLSCommonNameContextKey).(string)
	if !ok {
		return errors.New("no common name in certificate")
	}
	if cn != crypto.ClientBootstrapCommonName &&
		!strings.HasPrefix(cn, crypto.ClientBootstrapCommonNamePrefix) {
		log.Errorf("an attempt to perform enrollment with a certificate with CN %q has been detected", cn)
		return errors.New("invalid tls CN for enrollment request")
	}
	// all good, you shall pass
	return nil
}

func NewAgentTransportHandler(serviceHandler *service.ServiceHandler, log logrus.FieldLogger) *AgentTransportHandler {
	return &AgentTransportHandler{serviceHandler: serviceHandler, log: log}
}

// (GET /api/v1/devices/{name}/rendered)
func (s *AgentTransportHandler) GetRenderedDevice(w http.ResponseWriter, r *http.Request, name string, params api.GetRenderedDeviceParams) {

	if err := ValidateDeviceAccessFromContext(r.Context(), name, s.log); err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	body, status := s.serviceHandler.GetRenderedDevice(r.Context(), name, params)
	transport.SetResponse(w, body, status)
}

// (PUT /api/v1/devices/{name}/status)
func (s *AgentTransportHandler) ReplaceDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {

	if err := ValidateDeviceAccessFromContext(r.Context(), name, s.log); err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	var device api.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	body, status := s.serviceHandler.ReplaceDeviceStatus(r.Context(), name, device)
	transport.SetResponse(w, body, status)
}

// (POST /api/v1/enrollmentrequests)
func (s *AgentTransportHandler) CreateEnrollmentRequest(w http.ResponseWriter, r *http.Request) {

	if err := ValidateEnrollmentAccessFromContext(r.Context(), s.log); err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	var er api.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	body, status := s.serviceHandler.CreateEnrollmentRequest(r.Context(), er)
	transport.SetResponse(w, body, status)
}

// (GET /api/v1/enrollmentrequests/{name})
func (s *AgentTransportHandler) ReadEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {

	if err := ValidateEnrollmentAccessFromContext(r.Context(), s.log); err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	body, status := s.serviceHandler.GetEnrollmentRequest(r.Context(), name)
	transport.SetResponse(w, body, status)
}
