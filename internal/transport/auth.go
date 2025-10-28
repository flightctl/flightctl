package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/consts"
)

// (POST /api/v1/auth/token)
func (h *TransportHandler) AuthToken(w http.ResponseWriter, r *http.Request) {
	var req api.TokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	// Get OIDC provider from service (this would need to be added to service interface)
	// For now, we'll assume it's available through the service handler
	// This is a placeholder - the actual implementation would depend on how
	// the OIDC provider is integrated into the service layer
	body, status := h.serviceHandler.AuthToken(r.Context(), req)
	SetResponse(w, body, status)
}

// (GET /api/v1/auth/userinfo)
func (h *TransportHandler) AuthUserInfo(w http.ResponseWriter, r *http.Request) {
	// Get access token from Authorization header
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		SetResponse(w, nil, api.StatusUnauthorized("invalid_token"))
		return
	}

	accessToken := strings.TrimPrefix(authHeader, "Bearer ")

	body, status := h.serviceHandler.AuthUserInfo(r.Context(), accessToken)
	SetResponse(w, body, status)
}

// (GET /api/v1/auth/jwks)
func (h *TransportHandler) AuthJWKS(w http.ResponseWriter, r *http.Request) {
	body, status := h.serviceHandler.AuthJWKS(r.Context())
	SetResponse(w, body, status)
}

// (GET /api/v1/auth/.well-known/openid_configuration)
func (h *TransportHandler) AuthOpenIDConfiguration(w http.ResponseWriter, r *http.Request) {
	body, status := h.serviceHandler.AuthOpenIDConfiguration(r.Context())
	SetResponse(w, body, status)
}

// (GET /api/v1/auth/authorize)
func (h *TransportHandler) AuthAuthorize(w http.ResponseWriter, r *http.Request, params api.AuthAuthorizeParams) {
	// Extract session information from HTTP request and add to context
	ctx := h.extractSessionContext(r.Context(), r)

	body, status := h.serviceHandler.AuthAuthorize(ctx, params)
	SetResponse(w, body, status)
}

// (POST /api/v1/auth/login)
func (h *TransportHandler) AuthLogin(w http.ResponseWriter, r *http.Request, params api.AuthLoginParams) {
	// Parse form data for login form submission
	if err := r.ParseForm(); err != nil {
		SetResponse(w, nil, api.StatusBadRequest("Failed to parse form data"))
		return
	}

	// Extract form parameters
	username := r.FormValue("username")
	password := r.FormValue("password")
	clientID := r.FormValue("client_id")
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")

	// Validate required fields
	if username == "" || password == "" || clientID == "" || redirectURI == "" {
		SetResponse(w, nil, api.StatusBadRequest("Missing required fields"))
		return
	}

	// Extract session information from HTTP request and add to context
	ctx := h.extractSessionContext(r.Context(), r)

	body, status := h.serviceHandler.AuthLogin(ctx, username, password, clientID, redirectURI, state)
	SetResponse(w, body, status)
}

// (POST /api/v1/auth/login)
func (h *TransportHandler) AuthLoginPost(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement OAuth2 login flow
	// This is a placeholder implementation for OAuth2 login
	http.Error(w, "OAuth2 login not yet implemented", http.StatusNotImplemented)
}

// (GET /api/v1/auth/config)
func (h *TransportHandler) AuthConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, ok := h.authN.(auth.NilAuth); ok {
		SetResponse(w, nil, api.StatusAuthNotConfigured("Auth not configured"))
		return
	}

	authConfig := h.authN.GetAuthConfig()

	conf := api.AuthConfig{
		AuthType: authConfig.Type,
		AuthURL:  authConfig.Url,
		AuthOrganizationsConfig: api.AuthOrganizationsConfig{
			Enabled: authConfig.OrganizationsConfig.Enabled,
		},
	}
	SetResponse(w, conf, api.StatusOK())
}

// (GET /api/v1/auth/validate)
func (h *TransportHandler) AuthValidate(w http.ResponseWriter, r *http.Request, params api.AuthValidateParams) {
	// auth middleware already checked the token validity
	SetResponse(w, nil, api.StatusOK())
}

// extractSessionContext extracts session information from HTTP request and adds it to context
func (h *TransportHandler) extractSessionContext(ctx context.Context, r *http.Request) context.Context {
	// Extract session ID from query parameter
	if sessionID := r.URL.Query().Get("session_id"); sessionID != "" {
		ctx = context.WithValue(ctx, consts.SessionIDCtxKey, sessionID)
	}

	// Extract session cookie
	if cookie, err := r.Cookie("session_id"); err == nil && cookie.Value != "" {
		ctx = context.WithValue(ctx, consts.SessionCookieCtxKey, cookie.Value)
	}

	// Extract authorization header
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		ctx = context.WithValue(ctx, consts.AuthorizationCtxKey, authHeader)
	}

	// Extract other session-related headers
	if sessionHeader := r.Header.Get("X-Session-ID"); sessionHeader != "" {
		ctx = context.WithValue(ctx, consts.SessionIDCtxKey, sessionHeader)
	}

	return ctx
}

// Helper function to create string pointer
func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
