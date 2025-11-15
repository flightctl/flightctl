package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

// AuthToken handles OAuth2 token exchange requests
// (POST /api/v1/auth/token)
func (h *TransportHandler) AuthToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Check if auth is configured
	if _, ok := h.authN.(auth.NilAuth); ok {
		SetResponse(w, nil, api.StatusAuthNotConfigured("Auth not configured"))
		return
	}

	// Parse the token request from the body
	var tokenReq api.TokenRequest
	contentType := r.Header.Get("Content-Type")

	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		// Parse form data
		if err := r.ParseForm(); err != nil {
			SetResponse(w, nil, api.StatusBadRequest(err.Error()))
			return
		}

		// Convert form values to TokenRequest
		grantType := api.TokenRequestGrantType(r.FormValue("grant_type"))
		tokenReq.GrantType = grantType
		tokenReq.ProviderName = r.FormValue("provider_name")
		if code := r.FormValue("code"); code != "" {
			tokenReq.Code = &code
		}
		if refreshToken := r.FormValue("refresh_token"); refreshToken != "" {
			tokenReq.RefreshToken = &refreshToken
		}
		if scope := r.FormValue("scope"); scope != "" {
			tokenReq.Scope = &scope
		}
		if codeVerifier := r.FormValue("code_verifier"); codeVerifier != "" {
			tokenReq.CodeVerifier = &codeVerifier
		}
	} else {
		// Parse JSON
		if err := json.NewDecoder(r.Body).Decode(&tokenReq); err != nil {
			SetResponse(w, nil, api.StatusBadRequest("Failed to decode request: "+err.Error()))
			return
		}
	}

	// Call auth token proxy to process the token request
	orgId := getOrgIdFromContext(r.Context())
	tokenResp, status := h.authTokenProxy.ProxyTokenRequest(r.Context(), orgId, &tokenReq)

	// For OAuth2 errors in the token response, return 400 with the response body
	if tokenResp != nil && tokenResp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(tokenResp)
		return
	}

	SetResponse(w, tokenResp, status)
}

// getOrgIdFromContext extracts the organization ID from the context
func getOrgIdFromContext(ctx context.Context) uuid.UUID {
	if orgId, ok := util.GetOrgIdFromContext(ctx); ok {
		return orgId
	}
	return uuid.Nil
}
