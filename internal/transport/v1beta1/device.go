package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
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
	body, status := deviceservice.CreateDeviceFromUntrusted(r.Context(), h.device, transport.OrgIDFromContext(r.Context()), domainDevice)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/devices)
func (h *TransportHandler) ListDevices(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListDevicesParams) {
	domainParams := h.converter.Device().ListParamsToDomain(params)
	body, status := h.device.ListDevices(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams, nil)
	apiResult := h.converter.Device().ListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/devices/{name})
func (h *TransportHandler) GetDevice(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.device.GetDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name)
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
	body, status := deviceservice.ReplaceDeviceFromUntrusted(r.Context(), h.device, transport.OrgIDFromContext(r.Context()), name, domainDevice, nil, true)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/devices/{name})
func (h *TransportHandler) DeleteDevice(w http.ResponseWriter, r *http.Request, name string) {
	status := h.device.DeleteDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	h.SetResponse(w, nil, status)
}

// (GET /api/v1/devices/{name}/status)
func (h *TransportHandler) GetDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.device.GetDeviceStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/devices/{name}/lastseen)
func (h *TransportHandler) GetDeviceLastSeen(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.device.GetDeviceLastSeen(r.Context(), transport.OrgIDFromContext(r.Context()), name)
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
	body, status := h.device.ReplaceDeviceStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainDevice, true)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/devices/{name}/rendered)
func (h *TransportHandler) GetRenderedDevice(w http.ResponseWriter, r *http.Request, name string, params apiv1beta1.GetRenderedDeviceParams) {
	domainParams := h.converter.Device().GetRenderedParamsToDomain(params)
	body, status := h.device.GetRenderedDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainParams)
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
	body, status := h.device.PatchDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch, true)
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
	body, status := h.device.PatchDeviceStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
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
	body, status := h.device.DecommissionDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainDecom)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (POST /api/v1/devices/{name}/applications/{appname}/actions/stop)
func (h *TransportHandler) StopDeviceApplication(w http.ResponseWriter, r *http.Request, name string, appName string) {
	body, status := h.device.StopDeviceApplication(r.Context(), transport.OrgIDFromContext(r.Context()), name, appName)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (POST /api/v1/devices/{name}/applications/{appname}/actions/start)
func (h *TransportHandler) StartDeviceApplication(w http.ResponseWriter, r *http.Request, name string, appName string) {
	body, status := h.device.StartDeviceApplication(r.Context(), transport.OrgIDFromContext(r.Context()), name, appName)
	apiResult := h.converter.Device().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (POST /api/v1/devices/{name}/applications/{appname}/actions/restart)
func (h *TransportHandler) RestartDeviceApplication(w http.ResponseWriter, r *http.Request, name string, appName string) {
	body, status := h.device.RestartDeviceApplication(r.Context(), transport.OrgIDFromContext(r.Context()), name, appName)
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
	response, status := h.device.ResumeDevices(r.Context(), transport.OrgIDFromContext(r.Context()), domainRequest)
	apiResult := h.converter.Device().ResumeResponseFromDomain(response)
	h.SetResponse(w, apiResult, status)
}
