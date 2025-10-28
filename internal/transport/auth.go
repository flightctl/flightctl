package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/issuer"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/sirupsen/logrus"
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

	authorizeResp, status := h.serviceHandler.AuthAuthorize(ctx, params)

	// Check for error response
	if authorizeResp == nil {
		SetResponse(w, nil, status)
		return
	}

	// OIDC spec: authorization endpoint returns HTML (login form) or redirect
	switch authorizeResp.Type {
	case issuer.AuthorizeResponseTypeHTML:
		// Return HTML login form with correct content-type
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(authorizeResp.Content))

	case issuer.AuthorizeResponseTypeRedirect:
		// Return 302 redirect to callback with authorization code
		w.Header().Set("Location", authorizeResp.Content)
		w.WriteHeader(http.StatusFound)

	default:
		// Fallback: return as JSON (shouldn't happen)
		SetResponse(w, &api.Status{Message: authorizeResp.Content}, status)
	}
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
	ctx := r.Context()

	// Parse form data
	if err := r.ParseForm(); err != nil {
		logrus.Errorf("AuthLoginPost: failed to parse form data - %v", err)
		SetResponse(w, nil, api.StatusBadRequest("Failed to parse form data"))
		return
	}

	// Extract required fields from form
	username := r.FormValue("username")
	password := r.FormValue("password")
	clientID := r.FormValue("client_id")
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")

	logrus.Infof("AuthLoginPost: received login request - username=%s, clientID=%s, redirectURI=%s, state=%s, hasPassword=%v",
		username, clientID, redirectURI, state, password != "")

	// Validate required fields
	if username == "" || password == "" || clientID == "" || redirectURI == "" {
		logrus.Warnf("AuthLoginPost: missing required fields - username=%v, hasPassword=%v, clientID=%v, redirectURI=%v",
			username != "", password != "", clientID != "", redirectURI != "")
		SetResponse(w, nil, api.StatusBadRequest("Missing required fields"))
		return
	}

	// Call service handler
	logrus.Infof("AuthLoginPost: calling service handler for user %s", username)
	body, status := h.serviceHandler.AuthLogin(ctx, username, password, clientID, redirectURI, state)

	logrus.Infof("AuthLoginPost: service handler returned - status=%d, hasBody=%v, message=%s",
		status.Code, body != nil, func() string {
			if body != nil {
				return body.Message
			}
			return ""
		}())

	// OIDC spec: login endpoint should redirect to authorization endpoint with session
	// Check if response contains a redirect URL
	if body != nil && body.Message != "" {
		if strings.HasPrefix(body.Message, "http://") ||
			strings.HasPrefix(body.Message, "https://") ||
			strings.HasPrefix(body.Message, "/") {
			// Return 302 redirect
			logrus.Infof("AuthLoginPost: redirecting to %s", body.Message)
			w.Header().Set("Location", body.Message)
			w.WriteHeader(http.StatusFound)
			return
		}
	}

	// Default: return as JSON (for errors)
	logrus.Warnf("AuthLoginPost: returning error response - status=%d", status.Code)
	SetResponse(w, body, status)
}

// (GET /api/v1/auth/config)
func (h *TransportHandler) AuthConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, ok := h.authN.(auth.NilAuth); ok {
		SetResponse(w, nil, api.StatusAuthNotConfigured("Auth not configured"))
		return
	}

	authConfig := h.authN.GetAuthConfig()
	body, status := h.serviceHandler.GetAuthConfig(r.Context(), authConfig)
	SetResponse(w, body, status)
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
