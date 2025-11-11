package login

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/openshift/osincli"
)

const (
	// csrfTokenHeader is a marker header that indicates we are not a browser that got tricked into requesting basic auth
	// Corresponds to the header expected by basic-auth challenging authenticators
	// copied from github.com/openshift/library-go/pkg/oauth/tokenrequest/request_token.go
	csrfTokenHeader = "X-CSRF-Token" //nolint:gosec
)

type K8sOauth struct {
	ClientId           string
	CAFile             string
	InsecureSkipVerify bool
	ConfigUrl          string
}

func NewK8sOAuth2Config(caFile, clientId, authUrl string, insecure bool) K8sOauth {
	return K8sOauth{
		CAFile:             caFile,
		InsecureSkipVerify: insecure,
		ClientId:           clientId,
		ConfigUrl:          fmt.Sprintf("%s/.well-known/oauth-authorization-server", authUrl),
	}
}

func (k K8sOauth) getOAuth2Client(callback string) (*osincli.Client, string, error) {
	oauthServerResponse, err := getOAuth2Config(k.ConfigUrl, k.CAFile, k.InsecureSkipVerify)
	if err != nil {
		return nil, "", err
	}

	redirectUrl := callback
	if redirectUrl == "" {
		redirectUrl = oauthServerResponse.TokenEndpoint + "/implicit"
	}

	config := &osincli.ClientConfig{
		ClientId:           k.ClientId,
		AuthorizeUrl:       oauthServerResponse.AuthEndpoint,
		TokenUrl:           oauthServerResponse.TokenEndpoint,
		ErrorsInStatusCode: true,
		RedirectUrl:        redirectUrl,
	}

	client, err := osincli.NewClient(config)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create oauth2 client: %w", err)
	}

	tlsConfig, err := getAuthClientTlsConfig(k.CAFile, k.InsecureSkipVerify)
	if err != nil {
		return nil, "", err
	}
	client.Transport = &http.Transport{
		TLSClientConfig: tlsConfig,
	}

	return client, k.ClientId, nil
}

func (k K8sOauth) authHeadless(username, password string) (AuthInfo, error) {
	ret := AuthInfo{}
	client, clientId, err := k.getOAuth2Client("")
	if err != nil {
		return ret, err
	}
	authorizeRequest := client.NewAuthorizeRequest(osincli.CODE)
	requestURL := authorizeRequest.GetAuthorizeUrl().String()
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return ret, err
	}
	req.Header.Set(csrfTokenHeader, "1")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	resp, err := client.Transport.RoundTrip(req)

	if err != nil {
		return ret, err
	}

	if resp.StatusCode == http.StatusFound {
		redirectURL := resp.Header.Get("Location")

		req, err := http.NewRequest(http.MethodGet, redirectURL, nil)
		if err != nil {
			return ret, err
		}

		// osincli automatically sends redirect_uri from ClientConfig.RedirectUrl
		return getOAuth2AccessToken(client, clientId, authorizeRequest, req)
	}
	return ret, fmt.Errorf("unexpected http code: %v", resp.StatusCode)
}

func (k K8sOauth) Auth(web bool, username, password string) (AuthInfo, error) {
	if web {
		return oauth2AuthCodeFlow(k.getOAuth2Client)
	}
	return k.authHeadless(username, password)
}

func (k K8sOauth) Renew(refreshToken string) (AuthInfo, error) {
	return oauth2RefreshTokenFlow(refreshToken, k.getOAuth2Client)
}

func (k K8sOauth) Validate(args ValidateArgs) error {
	if StrIsEmpty(args.AccessToken) && StrIsEmpty(args.Password) && StrIsEmpty(args.Username) && !args.Web {
		fmt.Println("You must provide one of the following options to log in:")
		fmt.Println("  --token=<token>")
		fmt.Println("  --username=<username> and --password=<password>")
		fmt.Println("  --web (to log in via your browser)")
		if !StrIsEmpty(k.ConfigUrl) {
			oauthConfig, err := getOAuth2Config(k.ConfigUrl, k.CAFile, k.InsecureSkipVerify)
			if err != nil {
				return fmt.Errorf("could not get oauth config: %w", err)
			}
			fmt.Print("\n\n")
			fmt.Printf("To obtain the token, you can visit %s/request\n", oauthConfig.TokenEndpoint)
			fmt.Printf("Then login via \"flightctl login %s --token=<token>\"\n\n", args.ApiUrl)
			fmt.Printf("Alternatively, use \"flightctl login %s --web\" to login via your browser\n\n", args.ApiUrl)
		}
		return fmt.Errorf("not enough options specified")
	}

	return nil
}
