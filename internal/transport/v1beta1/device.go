package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (POST /api/v1/devices)
func (h *TransportHandler) CreateDevice(w http.ResponseWriter, r *http.Request) {
	var device apiv1beta1.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainDevice := h.converter.Device().ToDomain(device)
	body, status := h.serviceHandler.CreateDevice(r.Context(), transport.OrgIDFromContext(r.Context()), domainDevice)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/devices)
func (h *TransportHandler) ListDevices(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListDevicesParams) {
	domainParams := h.converter.Device().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListDevices(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams, nil)
	apiResult := h.converter.Device().ListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/devices/{name})
func (h *TransportHandler) GetDevice(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/devices/{name})
func (h *TransportHandler) ReplaceDevice(w http.ResponseWriter, r *http.Request, name string) {
	var device apiv1beta1.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainDevice := h.converter.Device().ToDomain(device)
	body, status := h.serviceHandler.ReplaceDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainDevice, nil)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/devices/{name})
func (h *TransportHandler) DeleteDevice(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	h.SetResponse(w, nil, status)
}

// (GET /api/v1/devices/{name}/status)
func (h *TransportHandler) GetDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetDeviceStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/devices/{name}/lastseen)
func (h *TransportHandler) GetDeviceLastSeen(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetDeviceLastSeen(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Device().LastSeenFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/devices/{name}/status)
func (h *TransportHandler) ReplaceDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	var device apiv1beta1.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainDevice := h.converter.Device().ToDomain(device)
	body, status := h.serviceHandler.ReplaceDeviceStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainDevice)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/devices/{name}/rendered)
func (h *TransportHandler) GetRenderedDevice(w http.ResponseWriter, r *http.Request, name string, params apiv1beta1.GetRenderedDeviceParams) {
	domainParams := h.converter.Device().GetRenderedParamsToDomain(params)
	body, status := h.serviceHandler.GetRenderedDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainParams)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/devices/{name})
func (h *TransportHandler) PatchDevice(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/devices/{name}/status)
func (h *TransportHandler) PatchDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchDeviceStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/devices/{name}/decommission)
func (h *TransportHandler) DecommissionDevice(w http.ResponseWriter, r *http.Request, name string) {
	var decom apiv1beta1.DeviceDecommission
	if err := json.NewDecoder(r.Body).Decode(&decom); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainDecom := h.converter.Device().DecommissionToDomain(decom)
	body, status := h.serviceHandler.DecommissionDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainDecom)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (POST /api/v1/deviceactions/resume)
func (h *TransportHandler) ResumeDevices(w http.ResponseWriter, r *http.Request) {
	var request apiv1beta1.DeviceResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainRequest := h.converter.Device().ResumeRequestToDomain(request)
	response, status := h.serviceHandler.ResumeDevices(r.Context(), transport.OrgIDFromContext(r.Context()), domainRequest)
	apiResult := h.converter.Device().ResumeResponseFromDomain(response)
	h.SetResponse(w, apiResult, status)
}
