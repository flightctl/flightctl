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

	pamapi "github.com/flightctl/flightctl/api/v1beta1/pam-issuer"
	"github.com/flightctl/flightctl/internal/auth/oidc/pam"
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

// Helper function to write error response (now using OAuth2Error for all endpoints)
func writeError(w http.ResponseWriter, statusCode int, message string) {
	// Map HTTP status codes to OAuth2 error codes
	errorCode := pamapi.ServerError
	if statusCode == http.StatusBadRequest {
		errorCode = pamapi.InvalidRequest
	} else if statusCode == http.StatusUnauthorized {
		errorCode = pamapi.InvalidClient
	}

	oauth2Error := &pamapi.OAuth2Error{
		Code:             errorCode,
		ErrorDescription: lo.ToPtr(message),
	}
	writeJSON(w, statusCode, oauth2Error)
}

// Helper function to write OAuth2 error response (RFC 6749 Section 5.2)
func writeOAuth2Error(w http.ResponseWriter, errorCode pamapi.OAuth2ErrorError, errorDescription string) {
	// Determine HTTP status code based on error type
	statusCode := http.StatusBadRequest
	if errorCode == pamapi.InvalidClient {
		// Per RFC 6749, invalid_client should return 401
		statusCode = http.StatusUnauthorized
	}

	oauth2Error := &pamapi.OAuth2Error{
		Code:             errorCode,
		ErrorDescription: &errorDescription,
	}
	writeJSON(w, statusCode, oauth2Error)
}

// extractSessionContext extracts session information from HTTP request and adds it to context
func (h *Handler) extractSessionContext(ctx context.Context, r *http.Request) context.Context {
	// Extract auth cookie (standard method for OAuth2/OIDC session management)
	// This cookie can contain either pending auth or authenticated session data
	if cookie, err := r.Cookie(pam.CookieNameAuth); err == nil && cookie.Value != "" {
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

	// Convert v1beta1 OpenIDConfiguration to pamapi OpenIDConfiguration
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
		CodeChallengeMethodsSupported:     v1Config.CodeChallengeMethodsSupported,
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
		// Set encrypted cookie if pending auth was created
		if response.SessionID != "" {
			pendingAuthExpiration := h.pamProvider.GetPendingAuthExpiration()
			cookie := &http.Cookie{
				Name:     pam.CookieNameAuth,
				Value:    response.SessionID, // This is the encrypted cookie value
				Path:     "/",
				MaxAge:   int(pendingAuthExpiration.Seconds()),
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
			}
			http.SetCookie(w, cookie)
		}
		// Return HTML login form with correct content-type
		// SNYK-SUPPRESSION: response.Content comes from GetLoginForm which uses html/template to safely escape all user input, preventing XSS
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
	// Extract parameters from generated params (standardized with authorize endpoint)
	codeChallenge := lo.FromPtrOr(params.CodeChallenge, "")
	codeChallengeMethod := ""
	if params.CodeChallengeMethod != nil {
		codeChallengeMethod = string(*params.CodeChallengeMethod)
	}
	scope := lo.FromPtrOr(params.Scope, "")

	// Create pending auth request (IsLoggedIn is false, Username is empty)
	pendingAuthExpiration := h.pamProvider.GetPendingAuthExpiration()
	pendingReq := &pam.EncryptedAuthData{
		ClientID:            params.ClientId,
		RedirectURI:         params.RedirectUri,
		Scope:               scope,
		State:               lo.FromPtrOr(params.State, ""),
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ExpiresAt:           time.Now().Add(pendingAuthExpiration).Unix(),
		Username:            "", // Empty for pending auth
		IsLoggedIn:          false,
		LoginTime:           0, // Not set for pending auth
	}

	// Encrypt and create cookie
	encryptedCookie, err := h.pamProvider.EncryptCookieData(pendingReq)
	if err != nil {
		h.log.Errorf("Failed to encrypt cookie data: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		if _, writeErr := w.Write([]byte(`<!DOCTYPE html><html><head><title>Error</title></head><body><h1>Error</h1><p>Failed to generate login form.</p></body></html>`)); writeErr != nil {
			h.log.Errorf("Failed to write error response: %v", writeErr)
		}
		return
	}

	// Set encrypted cookie
	cookie := &http.Cookie{
		Name:     pam.CookieNameAuth,
		Value:    encryptedCookie,
		Path:     "/",
		MaxAge:   int(pendingAuthExpiration.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, cookie)

	// Get login form (no parameters needed - they're in the cookie)
	loginForm := h.pamProvider.GetLoginForm()

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

	// Extract encrypted cookie (set when login form was returned)
	encryptedCookie := ""
	if cookie, err := r.Cookie(pam.CookieNameAuth); err == nil && cookie.Value != "" {
		encryptedCookie = cookie.Value
	}

	h.log.Infof("Login request - username=%s", username)

	// Validate required fields
	if username == "" || password == "" {
		h.log.Warnf("Missing required fields - username=%v, hasPassword=%v",
			username != "", password != "")
		writeError(w, http.StatusBadRequest, "Missing required fields")
		return
	}

	// If encrypted cookie is missing, return error
	if encryptedCookie == "" {
		h.log.Warnf("Missing encrypted cookie - login expired")
		writeError(w, http.StatusBadRequest, "Your login session has expired. Please refresh the page and try again.")
		return
	}

	// Call PAM provider's Login method with encrypted cookie
	// All authorization parameters are stored in the encrypted cookie
	loginResult, err := h.pamProvider.Login(r.Context(), username, password, encryptedCookie)
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

	// Set secure HTTP-only cookie with encrypted session data
	// This replaces the pending auth cookie with the authenticated session cookie
	sessionExpiration := h.pamProvider.GetSessionExpiration()
	cookie := &http.Cookie{
		Name:     pam.CookieNameAuth,
		Value:    loginResult.SessionID, // This is the encrypted session cookie value
		Path:     "/",
		MaxAge:   int(sessionExpiration.Seconds()),
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
			writeOAuth2Error(w, pamapi.InvalidRequest, "Failed to parse form data")
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
		if codeVerifier := r.FormValue("code_verifier"); codeVerifier != "" {
			req.CodeVerifier = &codeVerifier
		}
		if redirectUri := r.FormValue("redirect_uri"); redirectUri != "" {
			req.RedirectUri = &redirectUri
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.log.Errorf("Failed to decode JSON request: %v", err)
			writeOAuth2Error(w, pamapi.InvalidRequest, "Failed to decode request body")
			return
		}
	}

	// Extract session information from HTTP request and add to context
	ctx := h.extractSessionContext(r.Context(), r)

	// Call PAM provider's Token method directly with pamapi types
	tokenResponse, err := h.pamProvider.Token(ctx, &req)
	if err != nil {
		// Check if it's an OAuth2 error (type assertion)
		if oauth2Err, ok := pamapi.IsOAuth2Error(err); ok {
			// OAuth2 error - return with proper HTTP status and JSON
			statusCode := http.StatusBadRequest
			if oauth2Err.Code == pamapi.InvalidClient {
				statusCode = http.StatusUnauthorized
			}
			writeJSON(w, statusCode, oauth2Err)
			return
		}
		// System error - shouldn't happen, but handle it
		h.log.Errorf("Token request failed with system error: %v", err)
		writeOAuth2Error(w, pamapi.ServerError, "Internal server error processing token request")
		return
	}

	// Success - return token response
	writeJSON(w, http.StatusOK, tokenResponse)
}

// AuthUserInfo handles OIDC UserInfo endpoint
// (GET /api/v1/auth/userinfo)
func (h *Handler) AuthUserInfo(w http.ResponseWriter, r *http.Request) {
	// Get access token from Authorization header
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		h.log.Warnf("Invalid authorization header")
		oauth2Error := &pamapi.OAuth2Error{
			Code:             "invalid_token",
			ErrorDescription: lo.ToPtr("Missing or invalid Authorization header"),
		}
		writeJSON(w, http.StatusUnauthorized, oauth2Error)
		return
	}

	accessToken := strings.TrimPrefix(authHeader, "Bearer ")

	// Call PAM provider's UserInfo method
	userInfoResponse, err := h.pamProvider.UserInfo(r.Context(), accessToken)
	if err != nil {
		// Check if it's an OAuth2 error (type assertion)
		if oauth2Err, ok := pamapi.IsOAuth2Error(err); ok {
			// OAuth2 error - return with 401
			h.log.Warnf("UserInfo request failed: %v", err)
			writeJSON(w, http.StatusUnauthorized, oauth2Err)
			return
		}
		// System error - shouldn't happen, but handle it
		h.log.Errorf("UserInfo request failed with system error: %v", err)
		writeOAuth2Error(w, pamapi.ServerError, "Internal server error retrieving user information")
		return
	}

	// Return userinfo response directly
	writeJSON(w, http.StatusOK, userInfoResponse)
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

	// Convert v1beta1 JWKSResponse to pamapi JWKSResponse
	jwks := pamapi.JWKSResponse{
		Keys: v1Jwks.Keys,
	}
	writeJSON(w, http.StatusOK, &jwks)
}
