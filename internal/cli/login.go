package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/cli/login"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type LoginOptions struct {
	GlobalOptions
	AccessToken        string
	Web                bool
	ClientId           string
	InsecureSkipVerify bool
	CAFile             string
	AuthCAFile         string
	Username           string
	Password           string
	authConfig         *v1alpha1.AuthConfig
	clientConfig       *client.Config
}

func DefaultLoginOptions() *LoginOptions {
	return &LoginOptions{
		GlobalOptions:      DefaultGlobalOptions(),
		AccessToken:        "",
		Web:                false,
		ClientId:           "",
		InsecureSkipVerify: false,
		CAFile:             "",
		AuthCAFile:         "",
		Username:           "",
		Password:           "",
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

	fs.StringVarP(&o.AccessToken, "token", "t", o.AccessToken, "Bearer token for authentication to the API server")
	fs.BoolVarP(&o.Web, "web", "w", o.Web, "Login via browser")
	fs.StringVarP(&o.ClientId, "client-id", "", o.ClientId, "ClientId to be used for Oauth2 requests")
	fs.StringVarP(&o.CAFile, "certificate-authority", "", o.CAFile, "Path to a cert file for the certificate authority")
	fs.StringVarP(&o.AuthCAFile, "auth-certificate-authority", "", o.AuthCAFile, "Path to a cert file for the auth certificate authority")
	fs.BoolVarP(&o.InsecureSkipVerify, "insecure-skip-tls-verify", "k", o.InsecureSkipVerify, "If true, the server's certificate will not be checked for validity. This will make your HTTPS connections insecure")
	fs.StringVarP(&o.Username, "username", "u", o.Username, "Username for server")
	fs.StringVarP(&o.Password, "password", "p", o.Password, "Password for server")
}

func (o *LoginOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}
	return nil
}

func (o *LoginOptions) Validate(args []string) error {
	if err := o.GlobalOptions.ValidateCmd(args); err != nil {
		return err
	}
	parsedUrl, err := url.Parse(args[0])
	if err != nil {
		return fmt.Errorf("API URL is not a valid URL: %w", err)
	}
	if parsedUrl.Scheme != "https" {
		return fmt.Errorf("the API URL must use HTTPS for secure communication. Please ensure the API URL starts with 'https://' and try again")
	}
	if parsedUrl.Host == "" {
		return fmt.Errorf("API URL is not a valid URL")
	}

	clientConfig, err := o.getClientConfig(args[0])
	if err != nil {
		return err
	}
	o.clientConfig = clientConfig
	authConfig, err := o.getAuthConfig()
	if err != nil {
		return fmt.Errorf("failed to get auth info: %w", err)
	}
	if authConfig == nil {
		// auth disabled
		return nil
	}
	o.authConfig = authConfig

	if strIsEmpty(o.AccessToken) && strIsEmpty(o.Password) && strIsEmpty(o.Username) && !o.Web {
		fmt.Println("You must provide one of the following options to log in:")
		fmt.Println("  --token=<token>")
		fmt.Println("  --username=<username> and --password=<password>")
		fmt.Print("  --web (to log in via your browser)\n\n")
		if o.authConfig.AuthType == "k8s" && !strIsEmpty(o.authConfig.AuthURL) {
			oauth2 := login.NewK8sOAuth2Config(o.AuthCAFile, o.ClientId, o.authConfig.AuthURL, o.InsecureSkipVerify)
			oauthConfig, err := oauth2.GetOAuth2Config()
			if err != nil {
				return fmt.Errorf("could not get oauth config: %w", err)
			}
			fmt.Printf("To obtain the token, you can visit %s/request\n", oauthConfig.TokenEndpoint)
			fmt.Printf("Then login via \"flightctl login %s --token=<token>\"\n\n", args[0])
			fmt.Printf("Alternatively, use \"flightctl login %s --web\" to login via your browser\n\n", args[0])
		}
		return fmt.Errorf("not enough options specified")
	}

	if !strIsEmpty(o.AccessToken) && (!strIsEmpty(o.Username) || !strIsEmpty(o.Password) || o.Web) {
		return fmt.Errorf("--token cannot be used along with --username, --password or --web")
	}

	if o.Web && (!strIsEmpty(o.Username) || !strIsEmpty(o.Password) || !strIsEmpty(o.AccessToken)) {
		return fmt.Errorf("--web cannot be used along with --username, --password or --token")
	}

	if (!strIsEmpty(o.Username) && strIsEmpty(o.Password)) || (strIsEmpty(o.Username) && !strIsEmpty(o.Password)) {
		return fmt.Errorf("both --username and --password need to be provided")
	}

	return nil
}

func (o *LoginOptions) Run(ctx context.Context, args []string) error {
	var (
		authCAFile string
		err        error
	)
	if o.authConfig == nil {
		fmt.Println("Auth is disabled")
		if err := o.clientConfig.Persist(o.ConfigFilePath); err != nil {
			return fmt.Errorf("persisting client config: %w", err)
		}
		return nil
	}

	token := o.AccessToken
	var authInfo login.AuthInfo
	if token == "" {
		if o.authConfig.AuthURL == "" {
			fmt.Printf("You must obtain API token, then login via \"flightctl login %s --token=<token>\"\n", o.clientConfig.Service.Server)
			return fmt.Errorf("must provide --token")
		}
		var authProvider login.AuthProvider
		switch o.authConfig.AuthType {
		case "OIDC":
			if o.ClientId == "" {
				o.ClientId = "flightctl"
			}
			authProvider = login.NewOIDCConfig(o.AuthCAFile, o.ClientId, o.authConfig.AuthURL, o.InsecureSkipVerify)
		case "k8s":
			if o.ClientId == "" {
				if o.Username != "" {
					o.ClientId = "openshift-challenging-client"
				} else {
					o.ClientId = "openshift-cli-client"
				}
			}
			authProvider = login.NewK8sOAuth2Config(o.AuthCAFile, o.ClientId, o.authConfig.AuthURL, o.InsecureSkipVerify)
		}

		if authProvider == nil {
			return fmt.Errorf("unknown auth provider. You can try logging in using \"flightctl login %s --token=<token>\"", o.clientConfig.Service.Server)
		}

		authInfo, err = authProvider.Auth(o.Web, o.Username, o.Password)
		if err != nil {
			return err
		}
		token = authInfo.AccessToken
		o.clientConfig.AuthInfo.RefreshToken = authInfo.RefreshToken
		if authInfo.ExpiresIn != nil {
			o.clientConfig.AuthInfo.AccessTokenExpiry = time.Now().Add(time.Duration(*authInfo.ExpiresIn) * time.Second).Format(time.RFC3339Nano)
		}
	}
	if token == "" {
		return fmt.Errorf("failed to retrieve auth token")
	}
	authCAFile, err = filepath.Abs(o.AuthCAFile)
	if err != nil && authCAFile != "" {
		return fmt.Errorf("failed to get the absolute path of %s: %w", o.AuthCAFile, err)
	}
	o.clientConfig.AuthInfo.AuthType = o.authConfig.AuthType
	o.clientConfig.AuthInfo.AccessToken = token
	o.clientConfig.AuthInfo.AuthCAFile = authCAFile
	o.clientConfig.AuthInfo.ClientId = o.ClientId
	o.clientConfig.AuthInfo.AuthURL = o.authConfig.AuthURL
	c, err := client.NewFromConfig(o.clientConfig)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	headerVal := "Bearer " + token
	res, err := c.AuthValidateWithResponse(ctx, &v1alpha1.AuthValidateParams{Authorization: &headerVal})
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

	err = o.clientConfig.Persist(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("persisting client config: %w", err)
	}
	fmt.Println("Login successful.")
	return nil
}

func (o *LoginOptions) getAuthConfig() (*v1alpha1.AuthConfig, error) {
	httpClient, err := client.NewHTTPClientFromConfig(o.clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client: %w", err)
	}
	c, err := apiClient.NewClientWithResponses(o.clientConfig.Service.Server, apiClient.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}
	resp, err := c.AuthConfigWithResponse(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get auth info: %w", err)
	}

	respCode := resp.StatusCode()

	if respCode == http.StatusTeapot {
		return nil, nil
	}

	if respCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response code: %v", respCode)
	}

	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response. Please verify that the API URL is correct")
	}

	return resp.JSON200, nil
}

func (o *LoginOptions) getClientConfig(apiUrl string) (*client.Config, error) {
	config := &client.Config{
		Service: client.Service{
			Server:             apiUrl,
			InsecureSkipVerify: o.InsecureSkipVerify,
		},
	}

	if o.CAFile != "" {
		caData, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA file: %w", err)
		}
		config.Service.CertificateAuthorityData = caData
	}

	return config, nil
}
