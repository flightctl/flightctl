package service

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/issuer"
)

// AuthToken handles OAuth2 token requests
func (h *ServiceHandler) AuthToken(ctx context.Context, req api.TokenRequest) (*api.TokenResponse, api.Status) {
	if h.oidcIssuer == nil {
		return nil, api.StatusInternalServerError("OIDC issuer not configured")
	}

	tokenResponse, err := h.oidcIssuer.Token(ctx, &req)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	return tokenResponse, api.StatusOK()
}

// AuthUserInfo handles OIDC UserInfo requests
func (h *ServiceHandler) AuthUserInfo(ctx context.Context, accessToken string) (*api.UserInfoResponse, api.Status) {
	if h.oidcIssuer == nil {
		return nil, api.StatusInternalServerError("OIDC issuer not configured")
	}

	userInfoResponse, err := h.oidcIssuer.UserInfo(ctx, accessToken)
	if err != nil {
		return nil, api.StatusUnauthorized(err.Error())
	}

	return userInfoResponse, api.StatusOK()
}

// AuthJWKS handles JWKS requests
func (h *ServiceHandler) AuthJWKS(ctx context.Context) (*api.JWKSResponse, api.Status) {
	if h.oidcIssuer == nil {
		return nil, api.StatusInternalServerError("OIDC issuer not configured")
	}

	jwks, err := h.oidcIssuer.GetJWKS()
	if err != nil {
		return nil, api.StatusInternalServerError(err.Error())
	}

	return jwks, api.StatusOK()
}

// AuthOpenIDConfiguration handles OpenID Connect discovery requests
func (h *ServiceHandler) AuthOpenIDConfiguration(ctx context.Context) (*api.OpenIDConfiguration, api.Status) {
	if h.oidcIssuer == nil {
		return nil, api.StatusInternalServerError("OIDC issuer not configured")
	}

	// Use the UI URL as the base URL for OIDC configuration
	baseURL := h.uiUrl
	if baseURL == "" {
		baseURL = "http://localhost:8080" // fallback
	}

	config, err := h.oidcIssuer.GetOpenIDConfiguration(baseURL)
	if err != nil {
		return nil, api.StatusInternalServerError(err.Error())
	}

	return config, api.StatusOK()
}

// AuthAuthorize handles OAuth2 authorization requests
func (h *ServiceHandler) AuthAuthorize(ctx context.Context, params api.AuthAuthorizeParams) (*issuer.AuthorizeResponse, api.Status) {
	if h.oidcIssuer == nil {
		return nil, api.StatusInternalServerError("OIDC issuer not configured")
	}

	// Call the OIDC issuer's Authorize method
	// The issuer returns a structured response indicating HTML or redirect
	response, err := h.oidcIssuer.Authorize(ctx, &params)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	return response, api.StatusOK()
}

// AuthLogin handles login form submission
func (h *ServiceHandler) AuthLogin(ctx context.Context, username, password, clientID, redirectURI, state string) (*api.Status, api.Status) {
	h.log.Infof("AuthLogin: handling login for user %s, clientID=%s, redirectURI=%s", username, clientID, redirectURI)

	if h.oidcIssuer == nil {
		h.log.Errorf("AuthLogin: OIDC issuer not configured")
		return nil, api.StatusInternalServerError("OIDC issuer not configured")
	}

	// Call the OIDC issuer's Login method
	h.log.Infof("AuthLogin: calling OIDC issuer Login for user %s", username)
	response, err := h.oidcIssuer.Login(ctx, username, password, clientID, redirectURI, state)
	if err != nil {
		h.log.Errorf("AuthLogin: OIDC issuer Login failed for user %s - %v", username, err)
		return nil, api.StatusBadRequest(err.Error())
	}

	h.log.Infof("AuthLogin: login successful for user %s, redirect URL=%s", username, response)

	// Return the response (redirect URL)
	return &api.Status{
		Message: response,
	}, api.StatusOK()
}

// GetAuthConfig returns the complete authentication configuration including all available providers
// The auth config is already fully populated by the AuthN middleware (including dynamic providers)
func (h *ServiceHandler) GetAuthConfig(ctx context.Context, authConfig *api.AuthConfig) (*api.AuthConfig, api.Status) {
	// The auth config from the middleware already includes all static and dynamic providers
	return authConfig, api.StatusOK()
}
