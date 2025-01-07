package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/RangelReale/osincli"
	"github.com/flightctl/flightctl/api/v1alpha1"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	certutil "k8s.io/client-go/util/cert"
)

type LoginOptions struct {
	GlobalOptions
	Token              string
	Web                bool
	ClientId           string
	InsecureSkipVerify bool
	CAFile             string
	AuthCAFile         string
}

func DefaultLoginOptions() *LoginOptions {
	return &LoginOptions{
		GlobalOptions:      DefaultGlobalOptions(),
		Token:              "",
		Web:                false,
		ClientId:           "",
		InsecureSkipVerify: false,
		CAFile:             "",
		AuthCAFile:         "",
	}
}

func NewCmdLogin() *cobra.Command {
	o := DefaultLoginOptions()
	cmd := &cobra.Command{
		Use:   "login [URL] [flags]",
		Short: "Login to flight control",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), args)
		},
		SilenceUsage: true,
	}

	o.Bind(cmd.Flags())
	return cmd
}

func (o *LoginOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVarP(&o.Token, "token", "t", o.Token, "Bearer token for authentication to the API server")
	fs.BoolVarP(&o.Web, "web", "w", o.Web, "Login via browser")
	fs.StringVarP(&o.ClientId, "client-id", "", o.ClientId, "ClientId to be used for Oauth2 requests")
	fs.StringVarP(&o.CAFile, "certificate-authority", "", o.CAFile, "Path to a cert file for the certificate authority")
	fs.StringVarP(&o.AuthCAFile, "auth-certificate-authority", "", o.AuthCAFile, "Path to a cert file for the auth certificate authority")
	fs.BoolVarP(&o.InsecureSkipVerify, "insecure-skip-tls-verify", "", o.InsecureSkipVerify, "If true, the server's certificate will not be checked for validity. This will make your HTTPS connections insecure")
}

func (o *LoginOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}
	return nil
}

func (o *LoginOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}
	return nil
}

type OauthServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
	AuthEndpoint  string `json:"authorization_endpoint"`
}

func (o *LoginOptions) getAuthClientTransport() (*http.Transport, error) {
	authTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: o.InsecureSkipVerify, //nolint:gosec
		},
	}

	if o.AuthCAFile != "" {
		caData, err := os.ReadFile(o.AuthCAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read Auth CA file: %w", err)
		}
		caPool, err := certutil.NewPoolFromBytes(caData)
		if err != nil {
			return nil, fmt.Errorf("failed parsing Auth CA certs: %w", err)
		}

		authTransport.TLSClientConfig.RootCAs = caPool
	}

	return authTransport, nil
}

func (o *LoginOptions) getOauthConfig(oauthConfigUrl string) (OauthServerResponse, error) {
	oauthResponse := OauthServerResponse{}
	req, err := http.NewRequest(http.MethodGet, oauthConfigUrl, nil)
	if err != nil {
		return oauthResponse, fmt.Errorf("failed to create http request: %w", err)
	}

	transport, err := o.getAuthClientTransport()
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

func (o *LoginOptions) getOauth2Token(oauthConfigUrl string, clientId string, scope string) (string, error) {
	token := ""

	oauthResponse, err := o.getOauthConfig(oauthConfigUrl)
	if err != nil {
		return token, err
	}

	// find free port
	listener, err := net.Listen("tcp", "")
	if err != nil {
		return token, fmt.Errorf("failed to open listener: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	done := make(chan error, 1)
	mux := http.NewServeMux()
	callback := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	var authorizeRequest *osincli.AuthorizeRequest
	var client *osincli.Client

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		areqdata, err := authorizeRequest.HandleRequest(r)
		if err != nil {
			_, _ = w.Write([]byte(fmt.Sprintf("ERROR: %s\n", err)))
			done <- err
			return
		}

		treq := client.NewAccessRequest(osincli.AUTHORIZATION_CODE, areqdata)
		// exchange the authorize token for the access token
		ad, err := treq.GetToken()
		if err != nil {
			_, err = w.Write([]byte(fmt.Sprintf("ERROR: %s\n", err)))
			if err != nil {
				fmt.Println("failed to write response %w", err)
			}
			done <- err
			return
		}
		_, err = w.Write([]byte("Login successful. You can close this window and return to CLI."))
		if err != nil {
			fmt.Println("failed to write response %w", err)
		}
		token = ad.AccessToken
		done <- nil
	})

	go func() {
		err = http.Serve(listener, mux) // #nosec G114
		if err != nil {
			fmt.Println("failed to start local http server %w", err)
		}
	}()

	config := &osincli.ClientConfig{
		ClientId:           clientId,
		AuthorizeUrl:       oauthResponse.AuthEndpoint,
		TokenUrl:           oauthResponse.TokenEndpoint,
		RedirectUrl:        callback,
		ErrorsInStatusCode: true,
		Scope:              scope,
	}

	client, err = osincli.NewClient(config)

	transport, err := o.getAuthClientTransport()
	if err != nil {
		return token, err
	}
	client.Transport = transport
	if err != nil {
		return token, fmt.Errorf("failed to create oauth2 client: %w", err)
	}

	authorizeRequest = client.NewAuthorizeRequest(osincli.CODE)

	loginUrl := authorizeRequest.GetAuthorizeUrl().String()
	fmt.Printf("Opening login URL in default browser: %s\n", loginUrl)
	err = browser.OpenURL(loginUrl)
	if err != nil {
		return token, fmt.Errorf("failed to open URL in default browser: %w", err)
	}

	err = <-done
	return token, err
}

func (o *LoginOptions) Run(ctx context.Context, args []string) error {
	config := &client.Config{
		Service: client.Service{
			Server:             args[0],
			InsecureSkipVerify: o.InsecureSkipVerify,
		},
	}

	if o.CAFile != "" {
		caData, err := os.ReadFile(o.CAFile)
		if err != nil {
			return fmt.Errorf("failed to read CA file: %w", err)
		}
		config.Service.CertificateAuthorityData = caData
	}

	httpClient, err := client.NewHTTPClientFromConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create http client: %w", err)
	}
	c, err := apiClient.NewClientWithResponses(config.Service.Server, apiClient.WithHTTPClient(httpClient))
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	resp, err := c.AuthConfigWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("failed to get auth info: %w", err)
	}

	respCode := resp.StatusCode()

	if respCode == http.StatusTeapot {
		fmt.Println("Auth is disabled")
		err = config.Persist(o.ConfigFilePath)
		if err != nil {
			return fmt.Errorf("persisting client config: %w", err)
		}
		return nil
	}

	if respCode != http.StatusOK {
		fmt.Printf("Unexpected response code: %v\n", respCode)
		return nil
	}

	token := ""

	if resp.JSON200.AuthType == "OIDC" {
		if o.Web {
			clientId := "flightctl"
			if o.ClientId != "" {
				clientId = o.ClientId
			}
			token, err = o.getOauth2Token(fmt.Sprintf("%s/.well-known/openid-configuration", resp.JSON200.AuthURL), clientId, "openid")
			if err != nil {
				return err
			}
		} else if o.Token == "" {
			fmt.Printf("You must obtain an API token or use \"flightctl login %s --web\" to login via your browser\n", config.Service.Server)
			return nil
		} else {
			token = o.Token
		}
	} else if resp.JSON200.AuthType == "OpenShift" {
		oauthConfigUrl := fmt.Sprintf("%s/.well-known/oauth-authorization-server", resp.JSON200.AuthURL)
		if o.Web {
			clientId := "openshift-cli-client"
			if o.ClientId != "" {
				clientId = o.ClientId
			}
			token, err = o.getOauth2Token(oauthConfigUrl, clientId, "")
			if err != nil {
				return err
			}
		} else if o.Token == "" {
			oauthConfig, err := o.getOauthConfig(oauthConfigUrl)
			if err != nil {
				return fmt.Errorf("could not get oauth config: %w", err)
			}

			fmt.Printf("You must obtain an API token by visiting %s/request\n", oauthConfig.TokenEndpoint)
			fmt.Printf("Then login via \"flightctl login %s --token=<token>\"\n", config.Service.Server)
			fmt.Printf("Alternatively, use \"flightctl login %s --web\" to login via your browser\n", config.Service.Server)
			return fmt.Errorf("no token provided")
		} else {
			token = o.Token
		}
	} else {
		if o.Token != "" {
			token = o.Token
		} else {
			fmt.Printf("Unknown auth provider. You can try logging in using \"flightctl login %s --token=<token>\"\n", config.Service.Server)
			return fmt.Errorf("unknown auth provider")
		}
	}
	c, err = client.NewFromConfig(config)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	headerVal := "Bearer " + token
	res, err := c.AuthValidateWithResponse(ctx, &v1alpha1.AuthValidateParams{Authentication: &headerVal})
	if err != nil {
		return fmt.Errorf("validating token: %w", err)
	}

	statusCode := res.StatusCode()

	if statusCode == http.StatusUnauthorized {
		return fmt.Errorf("the token provided is invalid or expired")
	}

	if statusCode == http.StatusInternalServerError && res.JSON500 != nil {
		return fmt.Errorf("%v: %v", statusCode, res.JSON500.Message)
	}

	if statusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %v", res.StatusCode())
	}

	config.AuthInfo.Token = token
	err = config.Persist(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("persisting client config: %w", err)
	}
	fmt.Println("Login successful.")
	return nil
}
