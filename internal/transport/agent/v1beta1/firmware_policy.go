package agenttransportv1beta1

import (
	"io"
	"net/http"

	agentapi "github.com/flightctl/flightctl/api/agent/v1beta1"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/internal/transport"
)

func (s *AgentTransportHandler) authorizePolicyDevice(w http.ResponseWriter, r *http.Request, name string) bool {
	identity, ok := r.Context().Value(consts.IdentityCtxKey).(*middleware.AgentIdentity)
	if !ok || identity == nil || identity.GetUsername() != name {
		s.SetResponse(w, nil, domain.StatusUnauthorized("authenticated device identity does not match request"))
		return false
	}
	if identity.GetOrgID() != transport.OrgIDFromContext(r.Context()).String() {
		s.SetResponse(w, nil, domain.StatusForbidden("authenticated organization does not match request"))
		return false
	}
	return true
}

func (s *AgentTransportHandler) GetFirmwarePolicy(w http.ResponseWriter, r *http.Request, name string, params agentapi.GetFirmwarePolicyParams) {
	if !s.authorizePolicyDevice(w, r, name) {
		return
	}
	var revision int64
	var digest string
	if params.KnownRevision != nil {
		revision = *params.KnownRevision
	}
	if params.KnownDigest != nil {
		digest = *params.KnownDigest
	}
	view, status := s.device.GetFirmwarePolicy(r.Context(), transport.OrgIDFromContext(r.Context()), name, revision, digest)
	if view == nil {
		s.SetResponse(w, nil, status)
		return
	}
	s.SetResponse(w, agentapi.FirmwarePolicy{Revision: view.Revision, Digest: view.Digest, Document: view.Document}, status)
}

func (s *AgentTransportHandler) AcknowledgeFirmwarePolicy(w http.ResponseWriter, r *http.Request, name string) {
	if !s.authorizePolicyDevice(w, r, name) {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil {
		s.SetResponse(w, nil, domain.StatusBadRequest("invalid policy receipt"))
		return
	}
	receipt, err := firmwarepolicy.ParseReceipt(data)
	if err != nil {
		s.SetResponse(w, nil, domain.StatusBadRequest("invalid policy receipt"))
		return
	}
	status := s.device.AcknowledgeFirmwarePolicy(r.Context(), transport.OrgIDFromContext(r.Context()), name, receipt)
	s.SetResponse(w, nil, status)
}
