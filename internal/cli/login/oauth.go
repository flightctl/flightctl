package login

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strconv"

	"github.com/openshift/osincli"
	"github.com/pkg/browser"
)

type GetClientFunc func(callbackURL string) (*osincli.Client, string, error)

func getOAuth2AccessToken(client *osincli.Client, clientId string, authorizeRequest *osincli.AuthorizeRequest, r *http.Request) (AuthInfo, error) {
	areqdata, err := authorizeRequest.HandleRequest(r)
	if err != nil {
		return AuthInfo{}, err
	}

	treq := client.NewAccessRequest(osincli.AUTHORIZATION_CODE, areqdata)

	if clientId != "" {
		if treq.CustomParameters == nil {
			treq.CustomParameters = make(map[string]string)
		}
		treq.CustomParameters["client_id"] = clientId
	}

	// exchange the authorize token for the access token
	ad, err := treq.GetToken()

	if err != nil {
		return AuthInfo{}, err
	}

	expiresIn, err := getExpiresIn(ad.ResponseData)
	if err != nil {
		return AuthInfo{}, err
	}

	return AuthInfo{
		AccessToken:  ad.AccessToken,
		RefreshToken: ad.RefreshToken,
		ExpiresIn:    expiresIn,
	}, nil
}

func oauth2AuthCodeFlow(getClient GetClientFunc) (AuthInfo, error) {
	ret := AuthInfo{}

	// find free port
	listener, err := net.Listen("tcp", "")
	if err != nil {
		return ret, fmt.Errorf("failed to open listener: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	done := make(chan error, 1)
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux} // #nosec G112
	defer server.Close()
	callback := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	client, clientId, err := getClient(callback)
	if err != nil {
		return ret, err
	}
	authorizeRequest := client.NewAuthorizeRequest(osincli.CODE)

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		ret, err = getOAuth2AccessToken(client, clientId, authorizeRequest, r)
		if err != nil {
			_, err = fmt.Fprintf(w, "ERROR: %s\n", err)
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
		err = server.Serve(listener) // #nosec G114
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("failed to start local http server %s\n", err.Error())
		}
	}()

	loginUrl := authorizeRequest.GetAuthorizeUrl().String()
	fmt.Printf("Opening login URL in default browser: %s\n", loginUrl)
	err = browser.OpenURL(loginUrl)
	if err != nil {
		return ret, fmt.Errorf("failed to open URL in default browser: %w", err)
	}

	err = <-done
	return ret, err
}

func oauth2RefreshTokenFlow(refreshToken string, getClient GetClientFunc) (AuthInfo, error) {
	ret := AuthInfo{}

	if refreshToken == "" {
		return ret, fmt.Errorf("refresh token is required")
	}

	// callback url is not used in refresh token flow, but has to be set, otherwise osincli fails
	// the value should not matter
	client, clientId, err := getClient("http://127.0.0.1")
	if err != nil {
		return ret, err
	}

	// Create a refresh token request
	req := client.NewAccessRequest(osincli.REFRESH_TOKEN, &osincli.AuthorizeData{Code: refreshToken})

	// Explicitly add client_id to the refresh token request
	// This is required per OAuth 2.0 spec even for public clients
	if clientId != "" {
		if req.CustomParameters == nil {
			req.CustomParameters = make(map[string]string)
		}
		req.CustomParameters["client_id"] = clientId
	}

	// Exchange refresh token for a new access token
	accessData, err := req.GetToken()
	if err != nil {
		return ret, fmt.Errorf("failed to refresh token: %w", err)
	}
	expiresIn, err := getExpiresIn(accessData.ResponseData)
	if err != nil {
		return ret, fmt.Errorf("failed to refresh token: %w", err)
	}

	ret.AccessToken = accessData.AccessToken
	ret.RefreshToken = accessData.RefreshToken // May be empty if not returned
	ret.ExpiresIn = expiresIn

	return ret, nil
}

func getOAuth2Config(configUrl, caFile string, insecure bool) (OauthServerResponse, error) {
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
		return oauthResponse, fmt.Errorf("failed to fetch oauth2 config: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return oauthResponse, fmt.Errorf("failed to fetch oauth2 config, status: %d", res.StatusCode)
	}

	defer res.Body.Close()
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return oauthResponse, fmt.Errorf("failed to read oauth2 config: %w", err)
	}
	if err := json.Unmarshal(bodyBytes, &oauthResponse); err != nil {
		return oauthResponse, fmt.Errorf("failed to parse oauth2 config: %w", err)
	}
	return oauthResponse, nil
}

// based on GetToken() from osincli which parses the expires_in to int32 that may overflow
func getExpiresIn(ret osincli.ResponseData) (*int64, error) {
	expires_in_raw, ok := ret["expires_in"]
	if ok {
		rv := reflect.ValueOf(expires_in_raw)
		switch rv.Kind() {
		case reflect.Float64:
			expiration := int64(rv.Float())
			return &expiration, nil
		case reflect.String:
			// if string convert to integer
			ei, err := strconv.ParseInt(rv.String(), 10, 64)
			if err != nil {
				return nil, err
			}
			return &ei, nil
		default:
			return nil, errors.New("invalid parameter value")
		}
	}
	return nil, nil
}
