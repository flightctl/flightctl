package agenttransport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	agentServer "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/transport"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
)

type AgentTransportHandler struct {
	serviceHandler service.Service
	ca             *crypto.CAClient
	log            logrus.FieldLogger
}

// Make sure we conform to servers Service interface
var _ agentServer.Transport = (*AgentTransportHandler)(nil)

func NewAgentTransportHandler(serviceHandler service.Service, ca *crypto.CAClient, log logrus.FieldLogger) *AgentTransportHandler {
	return &AgentTransportHandler{serviceHandler: serviceHandler, ca: ca, log: log}
}

// (GET /api/v1/devices/{name}/rendered)
func (s *AgentTransportHandler) GetRenderedDevice(w http.ResponseWriter, r *http.Request, name string, params api.GetRenderedDeviceParams) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	identity := ctx.Value(consts.IdentityCtxKey).(*middleware.AgentIdentity)
	fingerprint := identity.GetUsername() // This is the device fingerprint for agents

	// Validate that the authenticated device matches the requested device name
	if fingerprint != name {
		s.log.Errorf("attempt to access device %q with certificate fingerprint %q has been detected", name, fingerprint)
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	body, status := s.serviceHandler.GetRenderedDevice(ctx, fingerprint, params)
	transport.SetResponse(w, body, status)
}

// (PUT /api/v1/devices/{name}/status)
func (s *AgentTransportHandler) ReplaceDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	identity := ctx.Value(consts.IdentityCtxKey).(*middleware.AgentIdentity)
	fingerprint := identity.GetUsername() // This is the device fingerprint for agents

	// Validate that the authenticated device matches the requested device name
	if fingerprint != name {
		s.log.Errorf("attempt to access device %q with certificate fingerprint %q has been detected", name, fingerprint)
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	var device api.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	body, status := s.serviceHandler.ReplaceDeviceStatus(ctx, fingerprint, device)
	transport.SetResponse(w, body, status)
}

// (PATCH) /api/v1/devices/{name}/status)
func (s *AgentTransportHandler) PatchDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	identity := ctx.Value(consts.IdentityCtxKey).(*middleware.AgentIdentity)
	fingerprint := identity.GetUsername() // This is the device fingerprint for agents

	// Validate that the authenticated device matches the requested device name
	if fingerprint != name {
		s.log.Errorf("attempt to access device %q with certificate fingerprint %q has been detected", name, fingerprint)
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	body, status := s.serviceHandler.PatchDeviceStatus(ctx, fingerprint, patch)
	transport.SetResponse(w, body, status)
}

// (POST /api/v1/enrollmentrequests)
func (s *AgentTransportHandler) CreateEnrollmentRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract enrollment identity from context (set by middleware)
	// The middleware should have already validated the certificate and set the identity
	_ = ctx.Value(consts.IdentityCtxKey).(*middleware.EnrollmentIdentity)

	var er api.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	body, status := s.serviceHandler.CreateEnrollmentRequest(ctx, er)
	transport.SetResponse(w, body, status)
}

// (GET /api/v1/enrollmentrequests/{name})
func (s *AgentTransportHandler) GetEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	// Extract enrollment identity from context (set by middleware)
	// The middleware should have already validated the certificate and set the identity
	_ = ctx.Value(consts.IdentityCtxKey).(*middleware.EnrollmentIdentity)

	body, status := s.serviceHandler.GetEnrollmentRequest(ctx, name)
	transport.SetResponse(w, body, status)
}

// (POST /api/v1/certificatesigningrequests)
func (s *AgentTransportHandler) CreateCertificateSigningRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	identity := ctx.Value(consts.IdentityCtxKey).(*middleware.AgentIdentity)
	fingerprint := identity.GetUsername() // This is the device fingerprint for agents

	device, st := s.serviceHandler.GetDevice(ctx, fingerprint)
	if st.Code != http.StatusOK {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	if device.Status != nil && device.Status.Conditions != nil {
		if c := api.FindStatusCondition(device.Status.Conditions, api.ConditionTypeDeviceDecommissioning); c != nil {
			status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
			s.log.WithField("device", fingerprint).Warn("device is decommissioning; rejecting CSR")
			transport.SetResponse(w, status, status)
			return
		}
	}

	var request api.CertificateSigningRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	request.Status = nil
	service.NilOutManagedObjectMetaProperties(&request.Metadata)
	request.Metadata.Owner = util.SetResourceOwner(api.DeviceKind, fingerprint)
	csr, status := s.serviceHandler.CreateCertificateSigningRequest(context.WithValue(ctx, consts.InternalRequestCtxKey, true), request)
	if status.Code != http.StatusCreated && status.Code != http.StatusOK {
		transport.SetResponse(w, status, status)
		return
	}

	failedTPMVerification := api.IsStatusConditionFalse(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestTPMVerified)

	// auto approve for DeviceSvcClientSignerName if it pass the signer verification we're good
	// if tpm verification explicitly fails, a manual approval is required
	if csr.Spec.SignerName == s.ca.Cfg.DeviceSvcClientSignerName && !failedTPMVerification {
		if _, status := s.autoApprove(ctx, csr); status.Code != http.StatusOK {
			status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
			transport.SetResponse(w, status, status)
			return
		}
	}
	transport.SetResponse(w, csr, status)
}

// (GET /api/v1/certificatesigningrequests/{name})
func (s *AgentTransportHandler) GetCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	identity := ctx.Value(consts.IdentityCtxKey).(*middleware.AgentIdentity)
	fingerprint := identity.GetUsername() // This is the device fingerprint for agents

	csr, status := s.serviceHandler.GetCertificateSigningRequest(ctx, name)
	if status.Code != http.StatusOK {
		transport.SetResponse(w, csr, status)
		return
	}

	// Check that the CSR belongs to the requesting device
	expectedOwner := util.SetResourceOwner(api.DeviceKind, fingerprint)
	if csr.Metadata.Owner == nil || *csr.Metadata.Owner != *expectedOwner {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	transport.SetResponse(w, csr, status)
}

func (s *AgentTransportHandler) autoApprove(ctx context.Context, csr *api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	if api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
		return csr, api.StatusOK()
	}

	api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
		Type:    api.ConditionTypeCertificateSigningRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "Approved",
		Message: fmt.Sprintf("Auto-approved by %s", csr.Spec.SignerName),
	})
	api.RemoveStatusCondition(&csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied)
	api.RemoveStatusCondition(&csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestFailed)

	return s.serviceHandler.UpdateCertificateSigningRequestApproval(ctx, *csr.Metadata.Name, *csr)
}
