package transport

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// (POST /api/v1/authproviders)
func (h *TransportHandler) CreateAuthProvider(w http.ResponseWriter, r *http.Request) {
	var authProvider api.AuthProvider
	if err := json.NewDecoder(r.Body).Decode(&authProvider); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.CreateAuthProvider(r.Context(), authProvider)
	SetResponse(w, body, status)
}

// (GET /api/v1/authproviders)
func (h *TransportHandler) ListAuthProviders(w http.ResponseWriter, r *http.Request, params api.ListAuthProvidersParams) {
	body, status := h.serviceHandler.ListAuthProviders(r.Context(), params)
	SetResponse(w, body, status)
}

// (GET /api/v1/authproviders/{name})
func (h *TransportHandler) GetAuthProvider(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetAuthProvider(r.Context(), name)
	SetResponse(w, body, status)
}

// (PUT /api/v1/authproviders/{name})
func (h *TransportHandler) ReplaceAuthProvider(w http.ResponseWriter, r *http.Request, name string) {
	var authProvider api.AuthProvider
	if err := json.NewDecoder(r.Body).Decode(&authProvider); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceAuthProvider(r.Context(), name, authProvider)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/authproviders/{name})
func (h *TransportHandler) PatchAuthProvider(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchAuthProvider(r.Context(), name, patch)
	SetResponse(w, body, status)
}

// (DELETE /api/v1/authproviders/{name})
func (h *TransportHandler) DeleteAuthProvider(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteAuthProvider(r.Context(), name)
	SetResponse(w, nil, status)
}

// (POST /api/v1/auth/authorize)
func (h *TransportHandler) AuthAuthorizePost(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement OAuth2 authorization flow
	// This is a placeholder implementation for OAuth2 authorization
	http.Error(w, "OAuth2 authorization not yet implemented", http.StatusNotImplemented)
}
