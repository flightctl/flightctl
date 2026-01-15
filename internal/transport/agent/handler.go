package agenttransport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/api/convert"
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
	converter      convert.Converter
	ca             *crypto.CAClient
	log            logrus.FieldLogger
}

// Make sure we conform to servers Service interface
var _ agentServer.Transport = (*AgentTransportHandler)(nil)

func NewAgentTransportHandler(serviceHandler service.Service, converter convert.Converter, ca *crypto.CAClient, log logrus.FieldLogger) *AgentTransportHandler {
	return &AgentTransportHandler{serviceHandler: serviceHandler, converter: converter, ca: ca, log: log}
}

// (GET /api/v1/devices/{name}/rendered)
func (s *AgentTransportHandler) GetRenderedDevice(w http.ResponseWriter, r *http.Request, name string, params api.GetRenderedDeviceParams) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	val := ctx.Value(consts.IdentityCtxKey)
	if val == nil {
		s.log.Error("agent identity is missing from context")
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}
	identity, ok := val.(*middleware.AgentIdentity)
	if !ok {
		s.log.Error("invalid agent identity type in context")
		status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
		transport.SetResponse(w, status, status)
		return
	}
	fingerprint := identity.GetUsername() // This is the device fingerprint for agents

	// Validate that the authenticated device matches the requested device name
	if fingerprint != name {
		s.log.Errorf("attempt to access device %q with certificate fingerprint %q has been detected", name, fingerprint)
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	domainParams := s.converter.V1beta1().Device().GetRenderedParamsToDomain(params)
	body, status := s.serviceHandler.GetRenderedDevice(ctx, transport.OrgIDFromContext(ctx), fingerprint, domainParams)
	apiResult := s.converter.V1beta1().Device().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/devices/{name}/status)
func (s *AgentTransportHandler) ReplaceDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	val := ctx.Value(consts.IdentityCtxKey)
	if val == nil {
		s.log.Error("agent identity is missing from context")
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}
	identity, ok := val.(*middleware.AgentIdentity)
	if !ok {
		s.log.Error("invalid agent identity type in context")
		status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
		transport.SetResponse(w, status, status)
		return
	}
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

	domainDevice := s.converter.V1beta1().Device().ToDomain(device)
	body, status := s.serviceHandler.ReplaceDeviceStatus(ctx, transport.OrgIDFromContext(ctx), fingerprint, domainDevice)
	apiResult := s.converter.V1beta1().Device().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PATCH) /api/v1/devices/{name}/status)
func (s *AgentTransportHandler) PatchDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	val := ctx.Value(consts.IdentityCtxKey)
	if val == nil {
		s.log.Error("agent identity is missing from context")
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}
	identity, ok := val.(*middleware.AgentIdentity)
	if !ok {
		s.log.Error("invalid agent identity type in context")
		status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
		transport.SetResponse(w, status, status)
		return
	}
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

	domainPatch := s.converter.V1beta1().Common().PatchRequestToDomain(patch)
	body, status := s.serviceHandler.PatchDeviceStatus(ctx, transport.OrgIDFromContext(ctx), fingerprint, domainPatch)
	apiResult := s.converter.V1beta1().Device().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (POST /api/v1/enrollmentrequests)
func (s *AgentTransportHandler) CreateEnrollmentRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract enrollment identity from context (set by middleware)
	// The middleware should have already validated the certificate and set the identity
	val := ctx.Value(consts.IdentityCtxKey)
	if val == nil {
		s.log.Error("enrollment identity is missing from context")
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}
	_, ok := val.(*middleware.EnrollmentIdentity)
	if !ok {
		s.log.Error("invalid enrollment identity type in context")
		status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
		transport.SetResponse(w, status, status)
		return
	}

	var er api.EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainER := s.converter.V1beta1().EnrollmentRequest().ToDomain(er)
	body, status := s.serviceHandler.CreateEnrollmentRequest(ctx, transport.OrgIDFromContext(ctx), domainER)
	apiResult := s.converter.V1beta1().EnrollmentRequest().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/enrollmentrequests/{name})
func (s *AgentTransportHandler) GetEnrollmentRequest(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	// Extract enrollment identity from context (set by middleware)
	// The middleware should have already validated the certificate and set the identity
	val := ctx.Value(consts.IdentityCtxKey)
	if val == nil {
		s.log.Error("enrollment identity is missing from context")
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}
	_, ok := val.(*middleware.EnrollmentIdentity)
	if !ok {
		s.log.Error("invalid enrollment identity type in context")
		status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
		transport.SetResponse(w, status, status)
		return
	}

	body, status := s.serviceHandler.GetEnrollmentRequest(ctx, transport.OrgIDFromContext(ctx), name)
	apiResult := s.converter.V1beta1().EnrollmentRequest().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (POST /api/v1/certificatesigningrequests)
func (s *AgentTransportHandler) CreateCertificateSigningRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	val := ctx.Value(consts.IdentityCtxKey)
	if val == nil {
		s.log.Error("agent identity is missing from context")
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}
	identity, ok := val.(*middleware.AgentIdentity)
	if !ok {
		s.log.Error("invalid agent identity type in context")
		status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
		transport.SetResponse(w, status, status)
		return
	}
	fingerprint := identity.GetUsername() // This is the device fingerprint for agents

	device, st := s.serviceHandler.GetDevice(ctx, transport.OrgIDFromContext(ctx), fingerprint)
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
	domainCSR := s.converter.V1beta1().CertificateSigningRequest().ToDomain(request)
	csr, status := s.serviceHandler.CreateCertificateSigningRequest(context.WithValue(ctx, consts.InternalRequestCtxKey, true), transport.OrgIDFromContext(ctx), domainCSR)
	if status.Code != http.StatusCreated && status.Code != http.StatusOK {
		transport.SetResponse(w, status, status)
		return
	}

	failedTPMVerification := api.IsStatusConditionFalse(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestTPMVerified)

	// Auto-approve for renewal and device-svc-client CSRs if signer verification passed.
	// If TPM verification explicitly fails, a manual approval is required.
	if (csr.Spec.SignerName == s.ca.Cfg.DeviceManagementRenewalSignerName ||
		csr.Spec.SignerName == s.ca.Cfg.DeviceSvcClientSignerName) && !failedTPMVerification {
		if _, status := s.autoApprove(ctx, csr); status.Code != http.StatusOK {
			status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
			transport.SetResponse(w, status, status)
			return
		}
	}
	apiResult := s.converter.V1beta1().CertificateSigningRequest().FromDomain(csr)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/certificatesigningrequests/{name})
func (s *AgentTransportHandler) GetCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	// Extract device fingerprint from context (set by middleware)
	val := ctx.Value(consts.IdentityCtxKey)
	if val == nil {
		s.log.Error("agent identity is missing from context")
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}
	identity, ok := val.(*middleware.AgentIdentity)
	if !ok {
		s.log.Error("invalid agent identity type in context")
		status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
		transport.SetResponse(w, status, status)
		return
	}
	fingerprint := identity.GetUsername() // This is the device fingerprint for agents

	csr, status := s.serviceHandler.GetCertificateSigningRequest(ctx, transport.OrgIDFromContext(ctx), name)
	if status.Code != http.StatusOK {
		apiResult := s.converter.V1beta1().CertificateSigningRequest().FromDomain(csr)
		transport.SetResponse(w, apiResult, status)
		return
	}

	// Check that the CSR belongs to the requesting device
	expectedOwner := util.SetResourceOwner(api.DeviceKind, fingerprint)
	if csr.Metadata.Owner == nil || *csr.Metadata.Owner != *expectedOwner {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	apiResult := s.converter.V1beta1().CertificateSigningRequest().FromDomain(csr)
	transport.SetResponse(w, apiResult, status)
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

	return s.serviceHandler.UpdateCertificateSigningRequestApproval(ctx, transport.OrgIDFromContext(ctx), *csr.Metadata.Name, *csr)
}
