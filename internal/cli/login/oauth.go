package login

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/RangelReale/osincli"
	"github.com/pkg/browser"
)

const (
	csrfTokenHeader = "X-CSRF-Token" //nolint:gosec
)

type OAuth2 struct {
	ConfigUrl          string
	ClientId           string
	CAFile             string
	InsecureSkipVerify bool
}

func NewK8sOAuth2Config(caFile, clientId, authUrl string, insecure bool) OAuth2 {
	return OAuth2{
		CAFile:             caFile,
		InsecureSkipVerify: insecure,
		ConfigUrl:          fmt.Sprintf("%s/.well-known/oauth-authorization-server", authUrl),
		ClientId:           clientId,
	}
}

func (o OAuth2) GetOAuth2Config() (OauthServerResponse, error) {
	oauthResponse := OauthServerResponse{}
	req, err := http.NewRequest(http.MethodGet, o.ConfigUrl, nil)
	if err != nil {
		return oauthResponse, fmt.Errorf("failed to create http request: %w", err)
	}

	transport, err := getAuthClientTransport(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return oauthResponse, err
	}

	httpClient := http.Client{
		Transport: transport,
	}

	res, err := httpClient.Do(req)
	if err != nil {
		return oauthResponse, fmt.Errorf("failed to fetch oidc config: %w", err)
	}

	defer res.Body.Close()
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return oauthResponse, fmt.Errorf("failed to read oidc config: %w", err)
	}
	if err := json.Unmarshal(bodyBytes, &oauthResponse); err != nil {
		return oauthResponse, fmt.Errorf("failed to parse oidc config: %w", err)
	}
	return oauthResponse, nil
}

func getOAuth2AccessToken(client *osincli.Client, authorizeRequest *osincli.AuthorizeRequest, r *http.Request) (string, error) {
	areqdata, err := authorizeRequest.HandleRequest(r)
	if err != nil {
		return "", err
	}

	treq := client.NewAccessRequest(osincli.AUTHORIZATION_CODE, areqdata)
	// exchange the authorize token for the access token
	ad, err := treq.GetToken()

	if err != nil {
		return "", err
	}

	return ad.AccessToken, nil
}

func (o OAuth2) getOAuth2Client(scope string, callback string) (*osincli.Client, *osincli.AuthorizeRequest, error) {
	oauthResponse, err := o.GetOAuth2Config()
	if err != nil {
		return nil, nil, err
	}

	redirectUrl := callback
	if redirectUrl == "" {
		redirectUrl = oauthResponse.TokenEndpoint + "/implicit"
	}

	config := &osincli.ClientConfig{
		ClientId:           o.ClientId,
		AuthorizeUrl:       oauthResponse.AuthEndpoint,
		TokenUrl:           oauthResponse.TokenEndpoint,
		ErrorsInStatusCode: true,
		Scope:              scope,
		RedirectUrl:        redirectUrl,
	}

	client, err := osincli.NewClient(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create oauth2 client: %w", err)
	}

	transport, err := getAuthClientTransport(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return nil, nil, err
	}
	client.Transport = transport

	return client, client.NewAuthorizeRequest(osincli.CODE), nil
}

func (o OAuth2) authHeadless(username, password string) (string, error) {
	client, authorizeRequest, err := o.getOAuth2Client("", "")
	if err != nil {
		return "", err
	}
	requestURL := authorizeRequest.GetAuthorizeUrl().String()
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(csrfTokenHeader, "1")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	resp, err := client.Transport.RoundTrip(req)

	if err != nil {
		return "", err
	}

	if resp.StatusCode == http.StatusFound {
		redirectURL := resp.Header.Get("Location")

		req, err := http.NewRequest(http.MethodGet, redirectURL, nil)
		if err != nil {
			return "", err
		}

		return getOAuth2AccessToken(client, authorizeRequest, req)
	}
	return "", fmt.Errorf("unexpected http code: %v", resp.StatusCode)
}

func (o OAuth2) authWeb(scope string) (string, error) {
	token := ""

	// find free port
	listener, err := net.Listen("tcp", "")
	if err != nil {
		return token, fmt.Errorf("failed to open listener: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	done := make(chan error, 1)
	mux := http.NewServeMux()
	callback := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	client, authorizeRequest, err := o.getOAuth2Client(scope, callback)
	if err != nil {
		return token, err
	}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		token, err = getOAuth2AccessToken(client, authorizeRequest, r)
		if err != nil {
			_, err = w.Write([]byte(fmt.Sprintf("ERROR: %s\n", err)))
			if err != nil {
				fmt.Printf("failed to write response %s\n", err.Error())
			}
			done <- err
			return
		}
		_, err = w.Write([]byte("Login successful. You can close this window and return to CLI."))
		if err != nil {
			fmt.Printf("failed to write response %s\n", err.Error())
		}
		done <- nil
	})

	go func() {
		err = http.Serve(listener, mux) // #nosec G114
		if err != nil {
			fmt.Printf("failed to start local http server %s\n", err.Error())
		}
	}()

	loginUrl := authorizeRequest.GetAuthorizeUrl().String()
	fmt.Printf("Opening login URL in default browser: %s\n", loginUrl)
	err = browser.OpenURL(loginUrl)
	if err != nil {
		return token, fmt.Errorf("failed to open URL in default browser: %w", err)
	}

	err = <-done
	return token, err
}

func (o OAuth2) Auth(web bool, username, password string) (string, error) {
	if web {
		return o.authWeb("")
	}
	return o.authHeadless(username, password)
}
