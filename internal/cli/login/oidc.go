package login

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type OIDCDirectResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    *int32 `json:"expires_in"` // ExpiresIn in seconds
}

type OIDC struct {
	OAuth2
}

func NewOIDCConfig(caFile, clientId, authUrl string, insecure bool) OIDC {
	return OIDC{
		OAuth2: OAuth2{
			CAFile:             caFile,
			InsecureSkipVerify: insecure,
			ConfigUrl:          fmt.Sprintf("%s/.well-known/openid-configuration", authUrl),
			ClientId:           clientId,
		},
	}
}

func (o OIDC) authHeadless(username, password string) (AuthInfo, error) {
	oauthResponse, err := o.GetOAuth2Config()
	if err != nil {
		return AuthInfo{}, err
	}

	param := url.Values{}
	param.Add("client_id", o.ClientId)
	param.Add("username", username)
	param.Add("password", password)
	param.Add("grant_type", "password")
	payload := bytes.NewBufferString(param.Encode())

	req, err := http.NewRequest(http.MethodPost, oauthResponse.TokenEndpoint, payload)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	transport, err := getAuthClientTransport(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return AuthInfo{}, err
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to send http request: %w", err)
	}

	var bodyBytes []byte
	if resp.Body != nil {
		defer resp.Body.Close()
		bodyBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			return AuthInfo{}, fmt.Errorf("failed to read OIDC response: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		if bodyBytes == nil {
			return AuthInfo{}, fmt.Errorf("unexpected return code: %v", resp.StatusCode)
		}
		return AuthInfo{}, fmt.Errorf("unexpected return code: %v: %s", resp.StatusCode, string(bodyBytes))
	}

	if bodyBytes == nil {
		return AuthInfo{}, fmt.Errorf("OIDC response body is empty")
	}

	directResponse := OIDCDirectResponse{}
	if err := json.Unmarshal(bodyBytes, &directResponse); err != nil {
		return AuthInfo{}, fmt.Errorf("failed to parse OIDC response: %w", err)
	}

	return AuthInfo{
		AccessToken:  directResponse.AccessToken,
		RefreshToken: directResponse.RefreshToken,
		ExpiresIn:    directResponse.ExpiresIn,
	}, nil
}

func (o OIDC) Renew(refreshToken string) (AuthInfo, error) {
	if refreshToken == "" {
		return AuthInfo{}, fmt.Errorf("refresh token is required")
	}

	oauthResponse, err := o.GetOAuth2Config()
	if err != nil {
		return AuthInfo{}, err
	}

	// Prepare request parameters
	param := url.Values{}
	param.Add("client_id", o.ClientId)
	param.Add("refresh_token", refreshToken)
	param.Add("grant_type", "refresh_token")

	payload := bytes.NewBufferString(param.Encode())

	req, err := http.NewRequest(http.MethodPost, oauthResponse.TokenEndpoint, payload)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	transport, err := getAuthClientTransport(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return AuthInfo{}, err
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to read OIDC response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return AuthInfo{}, fmt.Errorf("unexpected status code: %v, response: %s", resp.StatusCode, string(bodyBytes))
	}

	directResponse := OIDCDirectResponse{}
	if err := json.Unmarshal(bodyBytes, &directResponse); err != nil {
		return AuthInfo{}, fmt.Errorf("failed to parse OIDC response: %w", err)
	}

	return AuthInfo{
		AccessToken:  directResponse.AccessToken,
		RefreshToken: directResponse.RefreshToken, // May be empty if the server does not return a new one
		ExpiresIn:    directResponse.ExpiresIn,
	}, nil
}

func (o OIDC) Auth(web bool, username, password string) (AuthInfo, error) {
	if web {
		return o.authWeb("openid")
	}
	return o.authHeadless(username, password)
}
