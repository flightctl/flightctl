package login

import (
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

type GetClientFunc func(callbackURL string) (*osincli.Client, string, error)

type PKCEInfo struct {
	CodeChallenge       string
	CodeChallengeMethod string
}

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
		return AuthInfo{}, err
	}

	treq := client.NewAccessRequest(osincli.AUTHORIZATION_CODE, areqdata)
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

func generateState() (string, error) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(stateBytes), nil
}

// oauth2AuthCodeFlow performs the auth flow with a local proxy workaround
func oauth2AuthCodeFlow(getClient GetClientFunc) (AuthInfo, error) {

	state, err := generateState()
	if err != nil {
		return AuthInfo{}, fmt.Errorf("state gen failed: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil {
		return AuthInfo{}, fmt.Errorf("port 8080 required but unavailable: %w", err)
	}
	defer listener.Close()

	callbackURL := "http://localhost:8080/callback"
	client, _, err := getClient(callbackURL)
	if err != nil {
		return AuthInfo{}, err
	}

	authReq := client.NewAuthorizeRequest(osincli.CODE)
	loginURL := authReq.GetAuthorizeUrlWithParams(state)

	var pkceInfo *PKCEInfo
	if codeChallenge := loginURL.Query().Get("code_challenge"); codeChallenge != "" {
		pkceInfo = &PKCEInfo{
			CodeChallenge:       codeChallenge,
			CodeChallengeMethod: loginURL.Query().Get("code_challenge_method"),
		}
	}

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

	startURL := loginURL.String()

	if pkceInfo != nil {
		baseURL := fmt.Sprintf("%s://%s", loginURL.Scheme, loginURL.Host)
		ph := &proxyHandler{
			client:     client,
			targetBase: baseURL,
			pkceVal:    pkceInfo.CodeChallenge,
			pkceMethod: pkceInfo.CodeChallengeMethod,
		}
		mux.Handle("/proxy/", http.StripPrefix("/proxy", ph))
		mux.Handle("/proxy", http.StripPrefix("/proxy", ph))
		startURL = fmt.Sprintf("http://localhost:8080/proxy%s?%s", loginURL.Path, loginURL.RawQuery)
	}

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			done <- fmt.Errorf("server error: %w", err)
		}
	}()
	defer server.Close()

	fmt.Printf("Opening login: %s\n", startURL)
	if err := browser.OpenURL(startURL); err != nil {
		return AuthInfo{}, fmt.Errorf("browser open failed: %w", err)
	}

	return result, <-done
}

type proxyHandler struct {
	client     *osincli.Client
	targetBase string
	pkceVal    string
	pkceMethod string
}

func (p *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := p.targetBase + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequest(r.Method, target, r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Proxy Error: failed to create request: %v", err), http.StatusInternalServerError)
		return
	}
	for k, v := range r.Header {
		req.Header[k] = v
	}

	if strings.HasSuffix(req.URL.Path, "/auth/authorize") {
		q := req.URL.Query()
		if q.Get("code_challenge") == "" {
			q.Set("code_challenge", p.pkceVal)
			q.Set("code_challenge_method", p.pkceMethod)
			req.URL.RawQuery = q.Encode()
		}
	}

	cli := &http.Client{
		Transport:     p.client.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := cli.Do(req)
	if err != nil {
		http.Error(w, "Proxy Error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc := resp.Header.Get("Location"); loc != "" {
			if u, err := url.Parse(loc); err == nil {
				if strings.Contains(u.Path, "/auth/authorize") && u.Query().Get("code_challenge") == "" {
					q := u.Query()
					q.Set("code_challenge", p.pkceVal)
					q.Set("code_challenge_method", p.pkceMethod)
					u.RawQuery = q.Encode()
					loc = u.String()
				}
				if strings.HasPrefix(loc, p.targetBase) {
					loc = "http://localhost:8080/proxy" + loc[len(p.targetBase):]
				} else if strings.HasPrefix(loc, "/") {
					loc = "http://localhost:8080/proxy" + loc
				}
				resp.Header.Set("Location", loc)
			}
		}
	}

	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/html") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Proxy Error: failed to read response body: %v", err), http.StatusBadGateway)
			return
		}
		bodyStr := string(body)
		if idx := strings.LastIndex(bodyStr, "</body>"); idx != -1 {
			js := p.generatePatchJS()
			bodyStr = bodyStr[:idx] + js + bodyStr[idx:]
			resp.Header.Set("Content-Length", fmt.Sprint(len(bodyStr)))
			copyHeaders(w, resp.Header)
			w.WriteHeader(resp.StatusCode)
			if _, err := w.Write([]byte(bodyStr)); err != nil {
				return
			}
			return
		}
	}

	copyHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return
	}
}

func copyHeaders(w http.ResponseWriter, h http.Header) {
	for k, v := range h {
		if k != "Connection" && k != "Transfer-Encoding" && k != "Content-Length" {
			w.Header()[k] = v
		}
	}
}

func (p *proxyHandler) generatePatchJS() string {
	return fmt.Sprintf(`<script>(function(){var c='%s',m='%s',b='http://localhost:8080/proxy';
if(c){var o=window.fetch;window.fetch=function(){var a=arguments,u=a[0];
if(typeof u==='string'&&u.indexOf('/api/v1/auth/login')!==-1){
u=u[0]==='/'?b+u:b+(new URL(u)).pathname+(new URL(u)).search;a[0]=u;}
var p=o.apply(this,a);if(typeof u==='string'&&u.indexOf('/api/v1/auth/login')!==-1){
return p.then(function(r){return r.clone().text().then(function(t){
if(t&&t.indexOf('/api/v1/auth/authorize')!==-1){try{var uo=new URL(t.trim(),window.location.origin);
if(!uo.searchParams.has('code_challenge')){uo.searchParams.set('code_challenge',c);
uo.searchParams.set('code_challenge_method',m);}
return new Response(b+uo.pathname+uo.search,{status:r.status,headers:r.headers});
}catch(e){return r;}}return r;});});}return p;};}})();</script>`, p.pkceVal, p.pkceMethod)
}

func oauth2RefreshTokenFlow(refreshToken string, getClient GetClientFunc) (AuthInfo, error) {
	ret := AuthInfo{}

	if refreshToken == "" {
		return ret, fmt.Errorf("refresh token is required")
	}

	// callback url is not used in refresh token flow, but has to be set, otherwise osincli fails
	// the value should not matter
	client, _, err := getClient("http://127.0.0.1")
	if err != nil {
		return ret, err
	}

	// Create a refresh token request
	req := client.NewAccessRequest(osincli.REFRESH_TOKEN, &osincli.AuthorizeData{Code: refreshToken})

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
