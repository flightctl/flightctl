package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/flightctl/flightctl/internal/transport"
)

// (POST /api/v1/repositories)
func (h *TransportHandler) CreateRepository(w http.ResponseWriter, r *http.Request) {
	var rs apiv1beta1.Repository
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainRepo := h.converter.Repository().ToDomain(rs)
	body, status := repositoryservice.CreateRepositoryFromUntrusted(r.Context(), h.repository, transport.OrgIDFromContext(r.Context()), domainRepo)
	apiResult := h.converter.Repository().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/repositories)
func (h *TransportHandler) ListRepositories(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListRepositoriesParams) {
	domainParams := h.converter.Repository().ListParamsToDomain(params)
	body, status := h.repository.ListRepositories(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.Repository().ListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/repositories/{name})
func (h *TransportHandler) GetRepository(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.repository.GetRepository(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Repository().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/repositories/{name})
func (h *TransportHandler) ReplaceRepository(w http.ResponseWriter, r *http.Request, name string) {
	var rs apiv1beta1.Repository
	if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainRepo := h.converter.Repository().ToDomain(rs)
	body, status := repositoryservice.ReplaceRepositoryFromUntrusted(r.Context(), h.repository, transport.OrgIDFromContext(r.Context()), name, domainRepo)
	apiResult := h.converter.Repository().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/repositories/{name})
func (h *TransportHandler) DeleteRepository(w http.ResponseWriter, r *http.Request, name string) {
	status := h.repository.DeleteRepository(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	h.SetResponse(w, nil, status)
}

// (PATCH /api/v1/repositories/{name})
func (h *TransportHandler) PatchRepository(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.repository.PatchRepository(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.Repository().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (POST /api/v1/repositories/{name}/check-oci-tag)
func (h *TransportHandler) CheckRepositoryOciTag(w http.ResponseWriter, r *http.Request, name string) {
	var req apiv1beta1.CheckRepositoryOciTagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	result, status := h.repository.CheckRepositoryOciTag(r.Context(), transport.OrgIDFromContext(r.Context()), name, req.ImageName, req.Tag)
	if result == nil {
		h.SetResponse(w, nil, status)
		return
	}

	apiResp := apiv1beta1.CheckRepositoryOciResult{
		Accessible: result.Accessible,
	}
	if !result.Accessible {
		if result.ErrorCode != 0 {
			apiResp.ErrorCode = &result.ErrorCode
		}
		if result.ErrorMessage != "" {
			apiResp.ErrorMessage = &result.ErrorMessage
		}
	}
	h.SetResponse(w, apiResp, status)
}

// (POST /api/v1/repositories/{name}/check-oci-image)
func (h *TransportHandler) CheckRepositoryOciImage(w http.ResponseWriter, r *http.Request, name string) {
	var req apiv1beta1.CheckRepositoryOciImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	result, status := h.repository.CheckRepositoryOciImage(r.Context(), transport.OrgIDFromContext(r.Context()), name, req.ImageName)
	if result == nil {
		h.SetResponse(w, nil, status)
		return
	}

	apiResp := apiv1beta1.CheckRepositoryOciResult{
		Accessible: result.Accessible,
	}
	if !result.Accessible {
		if result.ErrorCode != 0 {
			apiResp.ErrorCode = &result.ErrorCode
		}
		if result.ErrorMessage != "" {
			apiResp.ErrorMessage = &result.ErrorMessage
		}
	}
	h.SetResponse(w, apiResp, status)
}
