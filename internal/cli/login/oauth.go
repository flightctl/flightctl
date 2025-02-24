package login

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/openshift/osincli"
	"github.com/pkg/browser"
)

type GetClientFunc func(callbackURL string) (*osincli.Client, error)

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

func oauth2AuthCodeFlow(getClient GetClientFunc) (string, error) {
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

	client, err := getClient(callback)
	if err != nil {
		return token, err
	}
	authorizeRequest := client.NewAuthorizeRequest(osincli.CODE)

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

func GetOAuth2Config(configUrl, caFile string, insecure bool) (OauthServerResponse, error) {
	oauthResponse := OauthServerResponse{}
	req, err := http.NewRequest(http.MethodGet, configUrl, nil)
	if err != nil {
		return oauthResponse, fmt.Errorf("failed to create http request: %w", err)
	}

	tlsConfig, err := getAuthClientTlsConfig(caFile, insecure)
	if err != nil {
		return oauthResponse, err
	}

	httpClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
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
