package login

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/openshift/osincli"
)

type OIDCDirectResponse struct {
	AccessToken string `json:"access_token"`
}

type OIDC struct {
	ClientId           string
	CAFile             string
	InsecureSkipVerify bool
	ConfigUrl          string
}

func NewOIDCConfig(caFile, clientId, authUrl string, insecure bool) OIDC {
	return OIDC{
		CAFile:             caFile,
		InsecureSkipVerify: insecure,
		ClientId:           clientId,
		ConfigUrl:          fmt.Sprintf("%s/.well-known/openid-configuration", authUrl),
	}
}

func (k OIDC) getOAuth2Client(callback string) (*osincli.Client, error) {
	oauthServerResponse, err := GetOAuth2Config(k.ConfigUrl, k.CAFile, k.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}

	config := &osincli.ClientConfig{
		ClientId:           k.ClientId,
		AuthorizeUrl:       oauthServerResponse.AuthEndpoint,
		TokenUrl:           oauthServerResponse.TokenEndpoint,
		ErrorsInStatusCode: true,
		RedirectUrl:        callback,
		Scope:              "openid",
	}

	client, err := osincli.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create oidc client: %w", err)
	}

	tlsConfig, err := getAuthClientTlsConfig(k.CAFile, k.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}
	client.Transport = &http.Transport{TLSClientConfig: tlsConfig}

	return client, nil
}

func (o OIDC) authHeadless(username, password string) (string, error) {
	oauthResponse, err := GetOAuth2Config(o.ConfigUrl, o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return "", err
	}

	param := url.Values{}
	param.Add("client_id", o.ClientId)
	param.Add("username", username)
	param.Add("password", password)
	param.Add("grant_type", "password")
	payload := bytes.NewBufferString(param.Encode())

	req, err := http.NewRequest(http.MethodPost, oauthResponse.TokenEndpoint, payload)
	if err != nil {
		return "", fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	tlsConfig, err := getAuthClientTlsConfig(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return "", err
	}

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send http request: %w", err)
	}

	var bodyBytes []byte
	if resp.Body != nil {
		defer resp.Body.Close()
		bodyBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("failed to read OIDC response: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		if bodyBytes == nil {
			return "", fmt.Errorf("unexpected return code: %v", resp.StatusCode)
		}
		return "", fmt.Errorf("unexpected return code: %v: %s", resp.StatusCode, string(bodyBytes))
	}

	if bodyBytes == nil {
		return "", fmt.Errorf("OIDC response body is empty")
	}

	directResponse := OIDCDirectResponse{}
	if err := json.Unmarshal(bodyBytes, &directResponse); err != nil {
		return "", fmt.Errorf("failed to parse OIDC response: %w", err)
	}

	return directResponse.AccessToken, nil
}

func (o OIDC) Auth(web bool, username, password string) (string, error) {
	if web {
		return oauth2AuthCodeFlow(o.getOAuth2Client)
	}
	return o.authHeadless(username, password)
}
