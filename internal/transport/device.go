package transport

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// (POST /api/v1/devices)
func (h *TransportHandler) CreateDevice(w http.ResponseWriter, r *http.Request) {
	var device api.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.CreateDevice(r.Context(), device)
	SetResponse(w, body, status)
}

// (GET /api/v1/devices)
func (h *TransportHandler) ListDevices(w http.ResponseWriter, r *http.Request, params api.ListDevicesParams) {
	body, status := h.serviceHandler.ListDevices(r.Context(), params, nil)
	SetResponse(w, body, status)
}

// (GET /api/v1/devices/{name})
func (h *TransportHandler) GetDevice(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetDevice(r.Context(), name)
	SetResponse(w, body, status)
}

// (PUT /api/v1/devices/{name})
func (h *TransportHandler) ReplaceDevice(w http.ResponseWriter, r *http.Request, name string) {
	var device api.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceDevice(r.Context(), name, device, nil)
	SetResponse(w, body, status)
}

// (DELETE /api/v1/devices/{name})
func (h *TransportHandler) DeleteDevice(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteDevice(r.Context(), name)
	SetResponse(w, nil, status)
}

// (GET /api/v1/devices/{name}/status)
func (h *TransportHandler) GetDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetDeviceStatus(r.Context(), name)
	SetResponse(w, body, status)
}

// (GET /api/v1/devices/{name}/lastseen)
func (h *TransportHandler) GetDeviceLastSeen(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetDeviceLastSeen(r.Context(), name)
	SetResponse(w, body, status)
}

// (PUT /api/v1/devices/{name}/status)
func (h *TransportHandler) ReplaceDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	var device api.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceDeviceStatus(r.Context(), name, device)
	SetResponse(w, body, status)
}

// (GET /api/v1/devices/{name}/rendered)
func (h *TransportHandler) GetRenderedDevice(w http.ResponseWriter, r *http.Request, name string, params api.GetRenderedDeviceParams) {
	body, status := h.serviceHandler.GetRenderedDevice(r.Context(), name, params)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/devices/{name})
func (h *TransportHandler) PatchDevice(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchDevice(r.Context(), name, patch)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/devices/{name}/status)
func (h *TransportHandler) PatchDeviceStatus(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchDeviceStatus(r.Context(), name, patch)
	SetResponse(w, body, status)
}

// (PUT /api/v1/devices/{name}/decommission)
func (h *TransportHandler) DecommissionDevice(w http.ResponseWriter, r *http.Request, name string) {
	var decom api.DeviceDecommission
	if err := json.NewDecoder(r.Body).Decode(&decom); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.DecommissionDevice(r.Context(), name, decom)
	SetResponse(w, body, status)
}

// (POST /api/v1/deviceactions/resume)
func (h *TransportHandler) ResumeDevices(w http.ResponseWriter, r *http.Request) {
	var request api.DeviceResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	response, status := h.serviceHandler.ResumeDevices(r.Context(), request)
	SetResponse(w, response, status)
}
