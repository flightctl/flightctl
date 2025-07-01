package agenttransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	agentServer "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/transport"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
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
	cn, ok := ctx.Value(consts.TLSCommonNameCtxKey).(string)
	if !ok || cn == "" {
		log.Warningf("an attempt to access device %q without a CN in tls certificate has been detected", name)
		return "", errors.New("missing common name in TLS certificate")
	}

	// Ensure CN is a valid device CN
	fingerprint, err := ca.DeviceFingerprintFromCN(cn)
	if err != nil {
		log.Warn("invalid TLS common name format for device")
		return "", errors.New("invalid TLS common name format")
	}

	if s := ca.GetSignerFromCtx(ctx); s != nil && s.Name() != ca.Cfg.DeviceEnrollmentSignerName {
		log.Warnf("unexpected signer: expected %q, got %q", ca.Cfg.DeviceEnrollmentSignerName, s.Name())
		return "", fmt.Errorf("unexpected signer: expected %q, got %q", ca.Cfg.DeviceEnrollmentSignerName, s.Name())
	}

	// If a specific device name is expected, check that it matches the CN-derived fingerprint
	if name != "" && fingerprint != name {
		log.Warningf("attempt to access device %q with certificate CN %q has been detected", name, cn)
		return "", errors.New("invalid TLS CN for device")
	}

	log.Debugf("device access validated using CN fingerprint: %s", fingerprint)
	return fingerprint, nil
}

func ValidateEnrollmentAccessFromContext(ctx context.Context, ca *crypto.CAClient, log logrus.FieldLogger) error {
	cn, ok := ctx.Value(consts.TLSCommonNameCtxKey).(string)
	if !ok {
		return errors.New("no common name in certificate")
	}

	if s := ca.GetSignerFromCtx(ctx); s != nil && s.Name() != ca.Cfg.ClientBootstrapSignerName {
		log.Warnf("unexpected signer: expected %q, got %q", ca.Cfg.ClientBootstrapSignerName, s.Name())
		return fmt.Errorf("unexpected signer: expected %q, got %q", ca.Cfg.ClientBootstrapSignerName, s.Name())
	}

	if cn != ca.Cfg.ClientBootstrapCommonName &&
		!strings.HasPrefix(cn, ca.Cfg.ClientBootstrapCommonNamePrefix) {
		log.Errorf("an attempt to perform enrollment with a certificate with CN %q has been detected", cn)
		return errors.New("invalid tls CN for enrollment request")
	}
	// all good, you shall pass
	return nil
}

func NewAgentTransportHandler(serviceHandler service.Service, ca *crypto.CAClient, log logrus.FieldLogger) *AgentTransportHandler {
	return &AgentTransportHandler{serviceHandler: serviceHandler, ca: ca, log: log}
}

// (GET /api/v1/devices/{name}/rendered)
func (s *AgentTransportHandler) GetRenderedDevice(w http.ResponseWriter, r *http.Request, name string, params api.GetRenderedDeviceParams) {
	fingerprint, err := ValidateDeviceAccessFromContext(r.Context(), name, s.ca, s.log)
	if err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	body, status := s.serviceHandler.GetRenderedDevice(r.Context(), fingerprint, params)
	transport.SetResponse(w, body, status)
}

// (PUT /api/v1/devices/{name}/status)
func (s *AgentTransportHandler) ReplaceDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	fingerprint, err := ValidateDeviceAccessFromContext(r.Context(), name, s.ca, s.log)
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

	body, status := s.serviceHandler.ReplaceDeviceStatus(r.Context(), fingerprint, device)
	transport.SetResponse(w, body, status)
}

// (PATCH) /api/v1/devices/{name}/status)
func (s *AgentTransportHandler) PatchDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	fingerprint, err := ValidateDeviceAccessFromContext(r.Context(), name, s.ca, s.log)
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

	body, status := s.serviceHandler.PatchDeviceStatus(r.Context(), fingerprint, patch)
	transport.SetResponse(w, body, status)
}

// (POST /api/v1/enrollmentrequests)
func (s *AgentTransportHandler) CreateEnrollmentRequest(w http.ResponseWriter, r *http.Request) {
	if err := ValidateEnrollmentAccessFromContext(r.Context(), s.ca, s.log); err != nil {
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
	if err := ValidateEnrollmentAccessFromContext(r.Context(), s.ca, s.log); err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	body, status := s.serviceHandler.GetEnrollmentRequest(r.Context(), name)
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
		if c := api.FindStatusCondition(device.Status.Conditions, api.DeviceDecommissioning); c != nil {
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

	//TODO
	if request.Spec.ExpirationSeconds == nil {
		request.Spec.ExpirationSeconds = lo.ToPtr[int32](60 * 60 * 24 * 365)
	}

	request.Status = nil
	service.NilOutManagedObjectMetaProperties(&request.Metadata)
	request.Metadata.Owner = util.SetResourceOwner(api.DeviceKind, fingerprint)
	csr, status := s.serviceHandler.CreateCertificateSigningRequest(context.WithValue(ctx, consts.InternalRequestCtxKey, true), request)
	if status.Code != http.StatusCreated && status.Code != http.StatusOK {
		transport.SetResponse(w, status, status)
		return
	}

	// auto approve for DeviceSvcClientSignerName if it pass the signer verificaiton we're good
	if csr.Spec.SignerName == s.ca.Cfg.DeviceSvcClientSignerName {
		if _, status := s.autoApprove(ctx, csr); status.Code != http.StatusOK {
			status := api.StatusInternalServerError(http.StatusText(http.StatusInternalServerError))
			transport.SetResponse(w, status, status)
			return
		}
	}
	transport.SetResponse(w, csr, status)
}

func (s *AgentTransportHandler) autoApprove(ctx context.Context, csr *api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	if api.IsStatusConditionTrue(csr.Status.Conditions, api.CertificateSigningRequestApproved) {
		return csr, api.StatusOK()
	}

	api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
		Type:    api.CertificateSigningRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "Approved",
		Message: fmt.Sprintf("Auto-approved by %s", csr.Spec.SignerName),
	})
	api.RemoveStatusCondition(&csr.Status.Conditions, api.CertificateSigningRequestDenied)
	api.RemoveStatusCondition(&csr.Status.Conditions, api.CertificateSigningRequestFailed)

	return s.serviceHandler.UpdateCertificateSigningRequestApproval(ctx, *csr.Metadata.Name, *csr)
}

// (GET /api/v1/certificatesigningrequests/{name})
func (s *AgentTransportHandler) GetCertificateSigningRequest(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()

	fingerprint, err := ValidateDeviceAccessFromContext(r.Context(), name, s.ca, s.log)
	if err != nil {
		status := api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized))
		transport.SetResponse(w, status, status)
		return
	}

	cn, ok := ctx.Value(consts.TLSCommonNameCtxKey).(string)
	if !ok || cn == "" {
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
