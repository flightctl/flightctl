package agenttransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	agentServer "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/crypto/signer"
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

func ValidateDeviceAccessFromContext(ctx context.Context, name string, ca *crypto.CAClient, log logrus.FieldLogger) (string, error) {
	if s := ca.PeerCertificateSignerFromCtx(ctx); s != nil && s.Name() != ca.Cfg.DeviceEnrollmentSignerName {
		log.Warnf("unexpected client certificate signer: expected %q, got %q", ca.Cfg.DeviceEnrollmentSignerName, s.Name())
		return "", fmt.Errorf("unexpected client certificate signer: expected %q, got %q", ca.Cfg.DeviceEnrollmentSignerName, s.Name())
	}

	peerCertificate, err := ca.PeerCertificateFromCtx(ctx)
	if err != nil {
		return "", err
	}

	fingerprint, err := signer.DeviceFingerprintFromCN(ca.Cfg, peerCertificate.Subject.CommonName)
	if err != nil {
		return "", err
	}

	// If a specific device name is expected, check that it matches the CN-derived fingerprint
	if name != "" && fingerprint != name {
		log.Errorf("attempt to access device %q with certificate fingerprint %q has been detected", name, fingerprint)
		return "", errors.New("invalid TLS CN for device")
	}
	// all good, you shall pass
	return fingerprint, nil
}

func ValidateEnrollmentAccessFromContext(ctx context.Context, ca *crypto.CAClient, log logrus.FieldLogger) error {
	signer := ca.PeerCertificateSignerFromCtx(ctx)

	got := "<nil>"
	if signer != nil {
		got = signer.Name()
	}

	if signer == nil || signer.Name() != ca.Cfg.ClientBootstrapSignerName {
		log.Warnf("unexpected client certificate signer: expected %q, got %q", ca.Cfg.ClientBootstrapSignerName, got)
		return fmt.Errorf("unexpected client certificate signer: expected %q, got %q", ca.Cfg.ClientBootstrapSignerName, got)
	}
	// all good, you shall pass
	return nil
}

func NewAgentTransportHandler(serviceHandler service.Service, ca *crypto.CAClient, log logrus.FieldLogger) *AgentTransportHandler {
	return &AgentTransportHandler{serviceHandler: serviceHandler, ca: ca, log: log}
}

// (GET /api/v1/devices/{name}/rendered)
func (s *AgentTransportHandler) GetRenderedDevice(w http.ResponseWriter, r *http.Request, name string, params api.GetRenderedDeviceParams) {
	ctx := r.Context()

	fingerprint, err := ValidateDeviceAccessFromContext(ctx, name, s.ca, s.log)
	if err != nil {
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

	fingerprint, err := ValidateDeviceAccessFromContext(ctx, name, s.ca, s.log)
	if err != nil {
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

	fingerprint, err := ValidateDeviceAccessFromContext(ctx, name, s.ca, s.log)
	if err != nil {
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

	if err := ValidateEnrollmentAccessFromContext(ctx, s.ca, s.log); err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

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

	if err := ValidateEnrollmentAccessFromContext(ctx, s.ca, s.log); err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	body, status := s.serviceHandler.GetEnrollmentRequest(ctx, name)
	transport.SetResponse(w, body, status)
}

// (POST /api/v1/certificatesigningrequests)
func (s *AgentTransportHandler) CreateCertificateSigningRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	fingerprint, err := ValidateDeviceAccessFromContext(ctx, "", s.ca, s.log)
	if err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

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

	// auto approve for DeviceSvcClientSignerName if it pass the signer verification we're good
	if csr.Spec.SignerName == s.ca.Cfg.DeviceSvcClientSignerName {
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

	fingerprint, err := ValidateDeviceAccessFromContext(ctx, "", s.ca, s.log)
	if err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

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
