package login

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/openshift/osincli"
	"github.com/pkg/browser"
)

type GetClientFunc func(callbackURL string) (*osincli.Client, error)

func generatePKCEVerifier() (verifier string, challenge string, err error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])
	return verifier, challenge, nil
}

func getOAuth2AccessToken(client *osincli.Client, authorizeRequest *osincli.AuthorizeRequest, r *http.Request) (AuthInfo, error) {
	areqdata, err := authorizeRequest.HandleRequest(r)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to handle authorization request: %w", err)
	}

	treq := client.NewAccessRequest(osincli.AUTHORIZATION_CODE, areqdata)
	// exchange the authorize token for the access token
	ad, err := treq.GetToken()
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to exchange authorization code for access token: %w", err)
	}

	expiresIn, err := getExpiresIn(ad.ResponseData)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to parse token expiration: %w", err)
	}
	idTokenString := getIdToken(ad.ResponseData)
	return AuthInfo{
		AccessToken:  ad.AccessToken,
		RefreshToken: ad.RefreshToken,
		IdToken:      idTokenString,
		ExpiresIn:    expiresIn,
	}, nil
}

func generateState() (string, error) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(stateBytes), nil
}

// oauth2AuthCodeFlow performs the auth flow with a local proxy workaround
func oauth2AuthCodeFlow(getClient GetClientFunc, port int) (AuthInfo, error) {

	state, err := generateState()
	if err != nil {
		return AuthInfo{}, fmt.Errorf("state gen failed: %w", err)
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return AuthInfo{}, fmt.Errorf("port %d required but unavailable: %w", port, err)
	}
	defer listener.Close()

	callbackURL := fmt.Sprintf("http://localhost:%d/callback", port)
	client, err := getClient(callbackURL)
	if err != nil {
		return AuthInfo{}, err
	}

	authReq := client.NewAuthorizeRequest(osincli.CODE)

	done := make(chan error, 1)
	var result AuthInfo
	mux := http.NewServeMux()

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		// Validate state parameter to prevent CSRF attacks
		receivedState := r.URL.Query().Get("state")
		if receivedState == "" {
			http.Error(w, "Authentication failed: missing state parameter", http.StatusBadRequest)
			done <- fmt.Errorf("authentication failed: missing state parameter")
			return
		}

		// Use constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(state), []byte(receivedState)) != 1 {
			http.Error(w, "Authentication failed: state parameter mismatch", http.StatusBadRequest)
			done <- fmt.Errorf("authentication failed: state parameter mismatch")
			return
		}

		result, err = getOAuth2AccessToken(client, authReq, r)
		if err != nil {
			fmt.Fprintf(w, "ERROR: %s", err)
			done <- err
			return
		}
		_, _ = w.Write([]byte("Login successful. Return to CLI."))
		done <- nil
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		serveErr := server.Serve(listener) // #nosec G114
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			fmt.Printf("failed to start local http server %s\n", serveErr.Error())
			// Try to send the error, but don't block if done is already populated
			select {
			case done <- serveErr:
			default:
			}
		}
	}()

	loginUrl := authReq.GetAuthorizeUrlWithParams(state).String()
	fmt.Printf("Opening login URL in default browser: %s\n", loginUrl)
	err = browser.OpenURL(loginUrl)
	if err != nil {
		return result, fmt.Errorf("failed to open URL in default browser: %w", err)
	}

	err = <-done

	// Shutdown the server gracefully with a timeout to allow the response to be sent
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)

	return result, err
}

func oauth2RefreshTokenFlow(refreshToken string, getClient GetClientFunc) (AuthInfo, error) {
	ret := AuthInfo{}

	if refreshToken == "" {
		return ret, fmt.Errorf("refresh token is required")
	}

	// callback url is not used in refresh token flow, but has to be set, otherwise osincli fails
	// the value should not matter
	client, err := getClient("http://127.0.0.1")
	if err != nil {
		return ret, fmt.Errorf("failed to create OAuth2 client: %w", err)
	}

	// Create a refresh token request
	req := client.NewAccessRequest(osincli.REFRESH_TOKEN, &osincli.AuthorizeData{Code: refreshToken})

	// Exchange refresh token for a new access token
	accessData, err := req.GetToken()
	if err != nil {
		// Try to extract OAuth2 error details if available
		if oauthErr, ok := err.(*osincli.Error); ok {
			return ret, fmt.Errorf("OAuth2 error: %s - %s", oauthErr.Id, oauthErr.Description)
		}
		return ret, fmt.Errorf("failed to exchange refresh token: %w", err)
	}

	// Check for OAuth2 error in response (when server returns 200 with error fields)
	if errorCode, ok := accessData.ResponseData["error"]; ok {
		errorDesc := "no description"
		if desc, ok := accessData.ResponseData["error_description"]; ok {
			if desc != nil {
				errorDesc = fmt.Sprintf("%v", desc)
			}
		}
		return ret, fmt.Errorf("OAuth2 error: %v - %s", errorCode, errorDesc)
	}

	// Verify we have an access token
	if accessData.AccessToken == "" {
		return ret, fmt.Errorf("no access_token in refresh response")
	}

	expiresIn, err := getExpiresIn(accessData.ResponseData)
	if err != nil {
		return ret, fmt.Errorf("failed to parse token expiration: %w", err)
	}

	idTokenString := getIdToken(accessData.ResponseData)

	ret.AccessToken = accessData.AccessToken
	ret.RefreshToken = accessData.RefreshToken // May be empty if not returned
	ret.IdToken = idTokenString
	ret.ExpiresIn = expiresIn

	return ret, nil
}

// getOIDCDiscoveryConfig fetches the OIDC discovery document
func getOIDCDiscoveryConfig(discoveryUrl, caFile string, insecure bool) (OIDCDiscoveryResponse, error) {
	oidcResponse := OIDCDiscoveryResponse{}
	req, err := http.NewRequest(http.MethodGet, discoveryUrl, nil)
	if err != nil {
		return oidcResponse, fmt.Errorf("failed to create http request: %w", err)
	}

	tlsConfig, err := getAuthClientTlsConfig(caFile, insecure)
	if err != nil {
		return oidcResponse, err
	}

	httpClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	res, err := httpClient.Do(req)
	if err != nil {
		return oidcResponse, fmt.Errorf("failed to fetch OIDC discovery config: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return oidcResponse, fmt.Errorf("failed to fetch OIDC discovery config, status: %d", res.StatusCode)
	}
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return oidcResponse, fmt.Errorf("failed to read OIDC discovery config: %w", err)
	}
	if err := json.Unmarshal(bodyBytes, &oidcResponse); err != nil {
		return oidcResponse, fmt.Errorf("failed to parse OIDC discovery config: %w", err)
	}
	return oidcResponse, nil
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

// getIdToken safely extracts the id_token from response data without panicking
func getIdToken(ret osincli.ResponseData) string {
	idTokenRaw, ok := ret["id_token"]
	if !ok {
		return ""
	}

	if idTokenRaw == nil {
		return ""
	}

	switch v := idTokenRaw.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// oauth2PasswordFlow performs the password grant flow
func oauth2PasswordFlow(tokenURL, clientID, username, password, scope, caFile string, insecure bool) (AuthInfo, error) {
	tlsConfig, err := getAuthClientTlsConfig(caFile, insecure)
	if err != nil {
		return AuthInfo{}, err
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	data := url.Values{}
	data.Set("grant_type", "password")
	data.Set("username", username)
	data.Set("password", password)
	data.Set("client_id", clientID)
	if scope != "" {
		data.Set("scope", scope)
	}

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to request token: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return AuthInfo{}, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return AuthInfo{}, fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResponse map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &tokenResponse); err != nil {
		return AuthInfo{}, fmt.Errorf("failed to parse token response: %w", err)
	}

	accessToken, _ := tokenResponse["access_token"].(string)
	if accessToken == "" {
		return AuthInfo{}, fmt.Errorf("no access token in response")
	}

	refreshToken, _ := tokenResponse["refresh_token"].(string)
	idToken, _ := tokenResponse["id_token"].(string)

	var expiresIn *int64
	if expiresInRaw, ok := tokenResponse["expires_in"]; ok {
		switch v := expiresInRaw.(type) {
		case float64:
			exp := int64(v)
			expiresIn = &exp
		case int64:
			expiresIn = &v
		case string:
			if exp, err := strconv.ParseInt(v, 10, 64); err == nil {
				expiresIn = &exp
			}
		}
	}

	return AuthInfo{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IdToken:      idToken,
		ExpiresIn:    expiresIn,
	}, nil
}
