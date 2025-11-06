//go:build linux

package pam_issuer_server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	pamapi "github.com/flightctl/flightctl/api/v1alpha1/pam-issuer"
	"github.com/flightctl/flightctl/internal/auth/issuer/pam"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// Handler implements the PAM issuer API handlers
type Handler struct {
	log         logrus.FieldLogger
	cfg         *config.Config
	pamProvider *pam.PAMOIDCProvider
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	closeOnce   sync.Once
	isRunning   bool
	isClosed    bool
	mu          sync.Mutex
}

// NewHandler creates a new PAM issuer handler
func NewHandler(
	log logrus.FieldLogger,
	cfg *config.Config,
	ca *crypto.CAClient,
) (*Handler, error) {
	if cfg.Auth == nil || cfg.Auth.PAMOIDCIssuer == nil {
		return nil, fmt.Errorf("PAM OIDC issuer not configured")
	}

	pamProvider, err := pam.NewPAMOIDCProvider(ca, cfg.Auth.PAMOIDCIssuer)
	if err != nil {
		return nil, fmt.Errorf("failed to create PAM OIDC provider: %w", err)
	}

	h := &Handler{
		log:         log,
		cfg:         cfg,
		pamProvider: pamProvider,
	}

	return h, nil
}

// Run starts the background cleanup goroutine
func (h *Handler) Run(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.isRunning {
		return fmt.Errorf("handler is already running")
	}
	if h.isClosed {
		return fmt.Errorf("handler is closed")
	}

	childCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	h.isRunning = true

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer func() {
			h.mu.Lock()
			h.isRunning = false
			h.mu.Unlock()
		}()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				h.pamProvider.CleanupExpiredCodes()
				h.pamProvider.CleanupExpiredSessions()
			case <-childCtx.Done():
				return
			}
		}
	}()

	return nil
}

// Close cleans up resources. It is safe to call multiple times (idempotent).
func (h *Handler) Close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.isClosed = true
		h.mu.Unlock()

		if h.cancel != nil {
			h.cancel()
		}
		h.wg.Wait()
		if h.pamProvider != nil {
			h.pamProvider.Close()
		}
	})
}

// Helper function to write JSON response
func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logrus.Errorf("Failed to encode JSON response: %v", err)
	}
}

// Helper function to write error response
func writeError(w http.ResponseWriter, statusCode int, message string) {
	status := pamapi.Status{
		ApiVersion: "v1alpha1",
		Kind:       "Status",
		Code:       int32(statusCode), //nolint:gosec // HTTP status codes are always within int32 range
		Message:    message,
		Reason:     "Error",
		Status:     "Failure",
	}
	writeJSON(w, statusCode, status)
}

// extractSessionContext extracts session information from HTTP request and adds it to context
func (h *Handler) extractSessionContext(ctx context.Context, r *http.Request) context.Context {
	// Extract session cookie (standard method for OAuth2/OIDC session management)
	if cookie, err := r.Cookie("session_id"); err == nil && cookie.Value != "" {
		ctx = context.WithValue(ctx, pam.SessionCookieCtxKey, cookie.Value)
	}

	return ctx
}

// AuthOpenIDConfiguration handles OpenID Connect discovery endpoint
// (GET /api/v1/auth/.well-known/openid-configuration)
func (h *Handler) AuthOpenIDConfiguration(w http.ResponseWriter, r *http.Request) {
	v1Config, err := h.pamProvider.GetOpenIDConfiguration()
	if err != nil {
		h.log.Errorf("Failed to get OpenID configuration: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Convert v1alpha1 OpenIDConfiguration to pamapi OpenIDConfiguration
	config := pamapi.OpenIDConfiguration{
		Issuer:                            v1Config.Issuer,
		AuthorizationEndpoint:             v1Config.AuthorizationEndpoint,
		TokenEndpoint:                     v1Config.TokenEndpoint,
		UserinfoEndpoint:                  v1Config.UserinfoEndpoint,
		JwksUri:                           v1Config.JwksUri,
		ResponseTypesSupported:            v1Config.ResponseTypesSupported,
		GrantTypesSupported:               v1Config.GrantTypesSupported,
		ScopesSupported:                   v1Config.ScopesSupported,
		ClaimsSupported:                   v1Config.ClaimsSupported,
		IdTokenSigningAlgValuesSupported:  v1Config.IdTokenSigningAlgValuesSupported,
		TokenEndpointAuthMethodsSupported: v1Config.TokenEndpointAuthMethodsSupported,
	}
	writeJSON(w, http.StatusOK, &config)
}

// AuthAuthorize handles OAuth2 authorization endpoint
// (GET /api/v1/auth/authorize)
func (h *Handler) AuthAuthorize(w http.ResponseWriter, r *http.Request, params pamapi.AuthAuthorizeParams) {
	// Extract session information from HTTP request and add to context
	ctx := h.extractSessionContext(r.Context(), r)

	// Call the OIDC issuer's Authorize method directly with pamapi types
	response, err := h.pamProvider.Authorize(ctx, &params)
	if err != nil {
		h.log.Errorf("Authorization failed: %v", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// OIDC spec: authorization endpoint returns HTML (login form) or redirect
	switch response.Type {
	case pam.AuthorizeResponseTypeHTML:
		// Return HTML login form with correct content-type
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(response.Content)); err != nil {
			h.log.Errorf("Failed to write response: %v", err)
		}

	case pam.AuthorizeResponseTypeRedirect:
		// Return 302 redirect to callback with authorization code
		w.Header().Set("Location", response.Content)
		w.WriteHeader(http.StatusFound)

	default:
		// Fallback: return as JSON (shouldn't happen)
		writeError(w, http.StatusInternalServerError, "Invalid response type")
	}
}

// AuthLogin handles GET request to login form
// (GET /api/v1/auth/login)
func (h *Handler) AuthLogin(w http.ResponseWriter, r *http.Request, params pamapi.AuthLoginParams) {
	// Return HTML login form
	loginForm := h.pamProvider.GetLoginForm(params.ClientId, params.RedirectUri, lo.FromPtrOr(params.State, ""))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(loginForm)); err != nil {
		h.log.Errorf("Failed to write response: %v", err)
	}
}

// AuthLoginPost handles POST request to login form
// (POST /api/v1/auth/login)
func (h *Handler) AuthLoginPost(w http.ResponseWriter, r *http.Request) {
	// Parse form data
	if err := r.ParseForm(); err != nil {
		h.log.Errorf("Failed to parse form data: %v", err)
		writeError(w, http.StatusBadRequest, "Failed to parse form data")
		return
	}

	// Extract required fields from form
	username := r.FormValue("username")
	password := r.FormValue("password")
	clientID := r.FormValue("client_id")
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")

	h.log.Infof("Login request - username=%s, clientID=%s, redirectURI=%s", username, clientID, redirectURI)

	// Validate required fields
	if username == "" || password == "" || clientID == "" || redirectURI == "" {
		h.log.Warnf("Missing required fields - username=%v, hasPassword=%v, clientID=%v, redirectURI=%v",
			username != "", password != "", clientID != "", redirectURI != "")
		writeError(w, http.StatusBadRequest, "Missing required fields")
		return
	}

	// Call PAM provider's Login method
	loginResult, err := h.pamProvider.Login(r.Context(), username, password, clientID, redirectURI, state)
	if err != nil {
		h.log.Errorf("Login failed for user %s: %v", username, err)
		// Return just the error message for the JavaScript to display
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		if _, writeErr := w.Write([]byte("Invalid username or password")); writeErr != nil {
			h.log.Errorf("Failed to write response: %v", writeErr)
		}
		return
	}

	// Set secure HTTP-only cookie with session ID
	cookie := &http.Cookie{
		Name:     "session_id",
		Value:    loginResult.SessionID,
		Path:     "/",
		MaxAge:   1800, // 30 minutes (matches session expiration)
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, cookie)

	// Return the redirect URL for the JavaScript to follow
	h.log.Infof("Login successful for user %s, redirecting to %s", username, loginResult.RedirectURL)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write([]byte(loginResult.RedirectURL)); writeErr != nil {
		h.log.Errorf("Failed to write response: %v", writeErr)
	}
}

// AuthToken handles OAuth2 token endpoint
// (POST /api/v1/auth/token)
func (h *Handler) AuthToken(w http.ResponseWriter, r *http.Request) {
	var req pamapi.TokenRequest

	// Support both JSON and form-encoded requests
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			h.log.Errorf("Failed to parse form data: %v", err)
			writeError(w, http.StatusBadRequest, "Failed to parse form data")
			return
		}

		// Convert form values to TokenRequest
		grantType := pamapi.TokenRequestGrantType(r.FormValue("grant_type"))
		req.GrantType = grantType
		if username := r.FormValue("username"); username != "" {
			req.Username = &username
		}
		if password := r.FormValue("password"); password != "" {
			req.Password = &password
		}
		if refreshToken := r.FormValue("refresh_token"); refreshToken != "" {
			req.RefreshToken = &refreshToken
		}
		if code := r.FormValue("code"); code != "" {
			req.Code = &code
		}
		if clientID := r.FormValue("client_id"); clientID != "" {
			req.ClientId = &clientID
		}
		if clientSecret := r.FormValue("client_secret"); clientSecret != "" {
			req.ClientSecret = &clientSecret
		}
		if scope := r.FormValue("scope"); scope != "" {
			req.Scope = &scope
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.log.Errorf("Failed to decode JSON request: %v", err)
			writeError(w, http.StatusBadRequest, "Failed to decode request")
			return
		}
	}

	// Extract session information from HTTP request and add to context
	ctx := h.extractSessionContext(r.Context(), r)

	// Call PAM provider's Token method directly with pamapi types
	tokenResponse, err := h.pamProvider.Token(ctx, &req)
	if err != nil {
		h.log.Errorf("Token request failed: %v", err)
		errorCode := "invalid_request"
		errDesc := err.Error()
		tokenResponse = &pamapi.TokenResponse{
			Error:            &errorCode,
			ErrorDescription: &errDesc,
		}
		writeJSON(w, http.StatusBadRequest, tokenResponse)
		return
	}

	// Check if the TokenResponse itself contains an OAuth2 error
	if tokenResponse.Error != nil {
		// Return 400 Bad Request for OAuth2 errors
		writeJSON(w, http.StatusBadRequest, tokenResponse)
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse)
}

// AuthUserInfo handles OIDC UserInfo endpoint
// (GET /api/v1/auth/userinfo)
func (h *Handler) AuthUserInfo(w http.ResponseWriter, r *http.Request) {
	// Get access token from Authorization header
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		h.log.Warnf("Invalid authorization header")
		errorCode := "invalid_token"
		userInfoResponse := pamapi.UserInfoResponse{
			Error: &errorCode,
		}
		writeJSON(w, http.StatusUnauthorized, userInfoResponse)
		return
	}

	accessToken := strings.TrimPrefix(authHeader, "Bearer ")

	// Call PAM provider's UserInfo method
	v1UserInfoResponse, err := h.pamProvider.UserInfo(r.Context(), accessToken)
	if err != nil {
		h.log.Errorf("UserInfo request failed: %v", err)
		errorCode := "invalid_token"
		userInfoResponse := pamapi.UserInfoResponse{
			Error: &errorCode,
		}
		writeJSON(w, http.StatusUnauthorized, &userInfoResponse)
		return
	}

	// Convert v1alpha1 UserInfoResponse to pamapi UserInfoResponse
	userInfoResponse := pamapi.UserInfoResponse{
		Sub:               v1UserInfoResponse.Sub,
		PreferredUsername: v1UserInfoResponse.PreferredUsername,
		Name:              v1UserInfoResponse.Name,
		Email:             v1UserInfoResponse.Email,
		EmailVerified:     v1UserInfoResponse.EmailVerified,
		Roles:             v1UserInfoResponse.Roles,
		Organizations:     v1UserInfoResponse.Organizations,
		Error:             v1UserInfoResponse.Error,
	}

	writeJSON(w, http.StatusOK, &userInfoResponse)
}

// AuthJWKS handles JWKS endpoint
// (GET /api/v1/auth/jwks)
func (h *Handler) AuthJWKS(w http.ResponseWriter, r *http.Request) {
	v1Jwks, err := h.pamProvider.GetJWKS()
	if err != nil {
		h.log.Errorf("Failed to get JWKS: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Convert v1alpha1 JWKSResponse to pamapi JWKSResponse
	jwks := pamapi.JWKSResponse{
		Keys: v1Jwks.Keys,
	}
	writeJSON(w, http.StatusOK, &jwks)
}
