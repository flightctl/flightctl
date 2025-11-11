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
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    *int64 `json:"expires_in"` // ExpiresIn in seconds
}

type OIDC struct {
	ClientId             string
	CAFile               string
	InsecureSkipVerify   bool
	ConfigUrl            string
	RequestOrganizations bool
}

func NewOIDCConfig(caFile, clientId, authUrl string, requestOrganizations bool, insecure bool) OIDC {
	return OIDC{
		CAFile:               caFile,
		InsecureSkipVerify:   insecure,
		ClientId:             clientId,
		ConfigUrl:            fmt.Sprintf("%s/.well-known/openid-configuration", authUrl),
		RequestOrganizations: requestOrganizations,
	}
}

func (o OIDC) getOIDCClient(callback string) (*osincli.Client, string, error) {
	oauthServerResponse, err := getOAuth2Config(o.ConfigUrl, o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return nil, "", err
	}

	config := &osincli.ClientConfig{
		ClientId:           o.ClientId,
		AuthorizeUrl:       oauthServerResponse.AuthEndpoint,
		TokenUrl:           oauthServerResponse.TokenEndpoint,
		ErrorsInStatusCode: true,
		RedirectUrl:        callback,
		Scope:              o.getScopes(),
	}

	client, err := osincli.NewClient(config)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create oidc client: %w", err)
	}

	tlsConfig, err := getAuthClientTlsConfig(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return nil, "", err
	}
	client.Transport = &http.Transport{TLSClientConfig: tlsConfig}

	return client, o.ClientId, nil
}

func (o OIDC) authHeadless(username, password string) (AuthInfo, error) {
	oauthResponse, err := getOAuth2Config(o.ConfigUrl, o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return AuthInfo{}, err
	}

	param := url.Values{}
	param.Add("client_id", o.ClientId)
	param.Add("username", username)
	param.Add("password", password)
	param.Add("grant_type", "password")
	param.Add("scope", o.getScopes())
	payload := bytes.NewBufferString(param.Encode())

	req, err := http.NewRequest(http.MethodPost, oauthResponse.TokenEndpoint, payload)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	tlsConfig, err := getAuthClientTlsConfig(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return AuthInfo{}, err
	}

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}
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

	return AuthInfo(directResponse), nil
}

func (o OIDC) getScopes() string {
	scopes := "openid"
	if o.RequestOrganizations {
		scopes += " organization:*"
	}
	return scopes
}

func (o OIDC) Renew(refreshToken string) (AuthInfo, error) {
	return oauth2RefreshTokenFlow(refreshToken, o.getOIDCClient)
}

func (o OIDC) Auth(web bool, username, password string) (AuthInfo, error) {
	if web {
		return oauth2AuthCodeFlow(o.getOIDCClient)
	}
	return o.authHeadless(username, password)
}

func (o OIDC) Validate(args ValidateArgs) error {
	if StrIsEmpty(args.AccessToken) && StrIsEmpty(args.Password) && StrIsEmpty(args.Username) && !args.Web {
		fmt.Println("You must provide one of the following options to log in:")
		fmt.Println("  --token=<token>")
		fmt.Println("  --username=<username> and --password=<password>")
		fmt.Println("  --web (to log in via your browser)")
		return fmt.Errorf("not enough options specified")
	}
	return nil
}
