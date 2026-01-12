package v1

import (
	"encoding/json"
	"net/http"

	v1 "github.com/flightctl/flightctl/api/v1"
	"github.com/flightctl/flightctl/internal/api/conversion"
	"github.com/flightctl/flightctl/internal/transport"
)

// ListDevices handles GET /api/v1/devices for v1 API.
func (h *TransportHandler) ListDevices(w http.ResponseWriter, r *http.Request, params v1.ListDevicesParams) {
	// Convert v1 params to v1beta1
	v1beta1Params := conversion.ListDevicesParamsV1ToV1Beta1(params)

	// Call the service
	body, status := h.serviceHandler.ListDevices(r.Context(), transport.OrgIDFromContext(r.Context()), v1beta1Params, nil)

	// Check for error
	if status.Code != 0 && status.Code != http.StatusOK {
		v1Status := conversion.StatusV1Beta1ToV1(status)
		setV1Response(w, nil, v1Status)
		return
	}

	// Convert response to v1
	if body != nil {
		v1List, err := conversion.DeviceListV1Beta1ToV1(*body)
		if err != nil {
			setErrorResponse(w, http.StatusInternalServerError, "conversion error: "+err.Error())
			return
		}
		setV1SuccessResponse(w, http.StatusOK, v1List)
		return
	}

	setErrorResponse(w, http.StatusInternalServerError, "unexpected nil response")
}

// CreateDevice handles POST /api/v1/devices for v1 API.
func (h *TransportHandler) CreateDevice(w http.ResponseWriter, r *http.Request) {
	var device v1.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		setErrorResponse(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Convert v1 device to v1beta1
	v1beta1Device, err := conversion.DeviceV1ToV1Beta1(device)
	if err != nil {
		setErrorResponse(w, http.StatusBadRequest, "conversion error: "+err.Error())
		return
	}

	// Call the service
	body, status := h.serviceHandler.CreateDevice(r.Context(), transport.OrgIDFromContext(r.Context()), v1beta1Device)

	// Check for error
	if status.Code != 0 && status.Code != http.StatusCreated {
		v1Status := conversion.StatusV1Beta1ToV1(status)
		setV1Response(w, nil, v1Status)
		return
	}

	// Convert response to v1
	if body != nil {
		v1Device, err := conversion.DeviceV1Beta1ToV1(*body)
		if err != nil {
			setErrorResponse(w, http.StatusInternalServerError, "conversion error: "+err.Error())
			return
		}
		setV1SuccessResponse(w, http.StatusCreated, v1Device)
		return
	}

	setErrorResponse(w, http.StatusInternalServerError, "unexpected nil response")
}

// GetDevice handles GET /api/v1/devices/{name} for v1 API.
func (h *TransportHandler) GetDevice(w http.ResponseWriter, r *http.Request, name string) {
	// Call the service
	body, status := h.serviceHandler.GetDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name)

	// Check for error
	if status.Code != 0 && status.Code != http.StatusOK {
		v1Status := conversion.StatusV1Beta1ToV1(status)
		setV1Response(w, nil, v1Status)
		return
	}

	// Convert response to v1
	if body != nil {
		v1Device, err := conversion.DeviceV1Beta1ToV1(*body)
		if err != nil {
			setErrorResponse(w, http.StatusInternalServerError, "conversion error: "+err.Error())
			return
		}
		setV1SuccessResponse(w, http.StatusOK, v1Device)
		return
	}

	setErrorResponse(w, http.StatusInternalServerError, "unexpected nil response")
}

// ReplaceDevice handles PUT /api/v1/devices/{name} for v1 API.
func (h *TransportHandler) ReplaceDevice(w http.ResponseWriter, r *http.Request, name string) {
	var device v1.Device
	if err := json.NewDecoder(r.Body).Decode(&device); err != nil {
		setErrorResponse(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Convert v1 device to v1beta1
	v1beta1Device, err := conversion.DeviceV1ToV1Beta1(device)
	if err != nil {
		setErrorResponse(w, http.StatusBadRequest, "conversion error: "+err.Error())
		return
	}

	// Call the service
	body, status := h.serviceHandler.ReplaceDevice(r.Context(), transport.OrgIDFromContext(r.Context()), name, v1beta1Device, nil)

	// Check for error
	if status.Code != 0 && status.Code != http.StatusOK {
		v1Status := conversion.StatusV1Beta1ToV1(status)
		setV1Response(w, nil, v1Status)
		return
	}

	// Convert response to v1
	if body != nil {
		v1Device, err := conversion.DeviceV1Beta1ToV1(*body)
		if err != nil {
			setErrorResponse(w, http.StatusInternalServerError, "conversion error: "+err.Error())
			return
		}
		setV1SuccessResponse(w, http.StatusOK, v1Device)
		return
	}

	setErrorResponse(w, http.StatusInternalServerError, "unexpected nil response")
}
