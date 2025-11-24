package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

// AuthToken handles OAuth2 token exchange requests
// (POST /api/v1/auth/{providername}/token)
func (h *TransportHandler) AuthToken(w http.ResponseWriter, r *http.Request, providername string) {
	w.Header().Set("Content-Type", "application/json")

	// Check if auth is configured
	if _, ok := h.authN.(auth.NilAuth); ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(createTokenErrorResponse("server_error", "Auth not configured"))
		return
	}

	// Check if token proxy is configured
	if h.authTokenProxy == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(createTokenErrorResponse("server_error", "Token proxy not configured"))
		return
	}

	// Parse the token request from the body
	var tokenReq api.TokenRequest
	contentType := r.Header.Get("Content-Type")

	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		// Parse form data
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(createTokenErrorResponse("invalid_request", err.Error()))
			return
		}

		// Convert form values to TokenRequest
		grantType := api.TokenRequestGrantType(r.FormValue("grant_type"))
		tokenReq.GrantType = grantType
		tokenReq.ClientId = r.FormValue("client_id")
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
		if redirectUri := r.FormValue("redirect_uri"); redirectUri != "" {
			tokenReq.RedirectUri = &redirectUri
		}
	} else {
		// Parse JSON
		if err := json.NewDecoder(r.Body).Decode(&tokenReq); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(createTokenErrorResponse("invalid_request", "Failed to decode request: "+err.Error()))
			return
		}
	}

	// Call auth token proxy to process the token request
	orgId := getOrgIdFromContext(r.Context())
	tokenResp, httpStatus := h.authTokenProxy.ProxyTokenRequest(r.Context(), orgId, providername, &tokenReq)

	// OAuth2 token endpoint returns 200 for success, 400 for all errors
	// Token response includes error fields for error cases
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(tokenResp)
}

// getOrgIdFromContext extracts the organization ID from the context
func getOrgIdFromContext(ctx context.Context) uuid.UUID {
	if orgId, ok := util.GetOrgIdFromContext(ctx); ok {
		return orgId
	}
	return uuid.Nil
}

// createTokenErrorResponse creates an OAuth2 error response
func createTokenErrorResponse(errorCode, errorDescription string) api.TokenResponse {
	return api.TokenResponse{
		Error:            &errorCode,
		ErrorDescription: &errorDescription,
	}
}
