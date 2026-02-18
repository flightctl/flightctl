package aap

import (
	"context"
)

type AAPOAuthApplicationRequest struct {
	Name                   string `json:"name"`
	Organization           int    `json:"organization"`
	AuthorizationGrantType string `json:"authorization_grant_type"`
	ClientType             string `json:"client_type"`
	RedirectURIs           string `json:"redirect_uris"`
	AppURL                 string `json:"app_url"`
}

type AAPOAuthApplicationResponse struct {
	ID                     int    `json:"id"`
	Name                   string `json:"name"`
	ClientID               string `json:"client_id"`
	ClientSecret           string `json:"client_secret,omitempty"`
	ClientType             string `json:"client_type"`
	AuthorizationGrantType string `json:"authorization_grant_type"`
	RedirectURIs           string `json:"redirect_uris"`
	AppURL                 string `json:"app_url"`
	Organization           int    `json:"organization"`
}

// CreateOAuthApplication creates a new OAuth application in AAP Gateway
// POST /api/gateway/v1/applications/
func (a *AAPGatewayClient) CreateOAuthApplication(ctx context.Context, token string, request *AAPOAuthApplicationRequest) (*AAPOAuthApplicationResponse, error) {
	endpoint := a.buildEndpoint("/api/gateway/v1/applications/", nil)
	return post[AAPOAuthApplicationResponse](a, ctx, endpoint, token, request)
}
