package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/auth/common"
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
	authProvider       login.AuthProvider
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
			if err := o.Init(args); err != nil {
				return err
			}
			if err := o.ValidateAuthProvider(args); err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			return o.Run(ctx, args)
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
	fs.StringVarP(&o.ClientId, "client-id", "", o.ClientId, "ClientId to be used for OAuth2 requests")
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
	defaultConfigPath, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		return fmt.Errorf("could not get user config directory: %w", err)
	}
	if o.ConfigFilePath != defaultConfigPath {
		fmt.Printf("Using a non-default configuration file path: %s (Default: %s)\n", o.ConfigFilePath, defaultConfigPath)
	}
	return nil
}

func (o *LoginOptions) Init(args []string) error {
	var err error
	// Use trimmed URL for client config to handle whitespace
	trimmedURL := strings.TrimSpace(args[0])
	o.clientConfig, err = o.getClientConfig(trimmedURL)
	if err != nil {
		return err
	}
	return nil
}

func (o *LoginOptions) Validate(args []string) error {
	if err := o.GlobalOptions.ValidateCmd(args); err != nil {
		return err
	}

	// Trim whitespace from the URL
	trimmedURL := strings.TrimSpace(args[0])

	parsedUrl, err := url.Parse(trimmedURL)
	if err != nil {
		return fmt.Errorf("API URL is not a valid URL: %w", err)
	}

	// Check for missing protocol first for clearer guidance
	if !strings.HasPrefix(strings.ToLower(trimmedURL), "http") {
		return fmt.Errorf("API URL is missing the protocol. Please ensure the API URL starts with 'https://'")
	}

	// Enforce HTTPS scheme (case-insensitive)
	if strings.ToLower(parsedUrl.Scheme) != "https" {
		return fmt.Errorf("the API URL must use HTTPS for secure communication. Please ensure the API URL starts with 'https://' and try again")
	}

	// Check for valid hostname
	if parsedUrl.Hostname() == "" {
		return fmt.Errorf("API URL is missing a valid hostname. Please provide a complete URL with hostname")
	}

	// Check for embedded credentials (userinfo)
	if parsedUrl.User != nil {
		return fmt.Errorf("API URL must not include username or password. Please provide only the hostname and optionally a port")
	}

	// Check for extra path components that might indicate user error
	if parsedUrl.Path != "" && parsedUrl.Path != "/" {
		// Suggest removing the path component
		host := parsedUrl.Hostname()
		// Bracket IPv6 when no port is present
		correctedURL := "https://" + host
		if strings.Count(host, ":") > 1 {
			correctedURL = "https://[" + host + "]"
		}
		if parsedUrl.Port() != "" {
			correctedURL = "https://" + net.JoinHostPort(host, parsedUrl.Port())
		}
		return fmt.Errorf("API URL contains an unexpected path component '%s'. The API URL should only contain the hostname and optionally a port. Try: %s", parsedUrl.Path, correctedURL)
	}

	// Check for query parameters
	if parsedUrl.RawQuery != "" {
		// Suggest removing the query parameters
		host := parsedUrl.Hostname()
		// Bracket IPv6 when no port is present
		correctedURL := "https://" + host
		if strings.Count(host, ":") > 1 {
			correctedURL = "https://[" + host + "]"
		}
		if parsedUrl.Port() != "" {
			correctedURL = "https://" + net.JoinHostPort(host, parsedUrl.Port())
		}
		return fmt.Errorf("API URL contains unexpected query parameters '?%s'. The API URL should only contain the hostname and optionally a port. Try: %s", parsedUrl.RawQuery, correctedURL)
	}

	// Check for fragments
	if parsedUrl.Fragment != "" {
		// Suggest removing the fragment
		host := parsedUrl.Hostname()
		// Bracket IPv6 when no port is present
		correctedURL := "https://" + host
		if strings.Count(host, ":") > 1 {
			correctedURL = "https://[" + host + "]"
		}
		if parsedUrl.Port() != "" {
			correctedURL = "https://" + net.JoinHostPort(host, parsedUrl.Port())
		}
		return fmt.Errorf("API URL contains an unexpected fragment '#%s'. The API URL should only contain the hostname and optionally a port. Try: %s", parsedUrl.Fragment, correctedURL)
	}

	// Check for common URL format issues
	if strings.Contains(parsedUrl.Host, "//") {
		return fmt.Errorf("API URL contains double slashes in the hostname. This is likely a formatting error. Please ensure the URL format is: https://hostname[:port]")
	}

	if !login.StrIsEmpty(o.AccessToken) && (!login.StrIsEmpty(o.Username) || !login.StrIsEmpty(o.Password) || o.Web) {
		return fmt.Errorf("--token cannot be used along with --username, --password or --web")
	}

	if o.Web && (!login.StrIsEmpty(o.Username) || !login.StrIsEmpty(o.Password) || !login.StrIsEmpty(o.AccessToken)) {
		return fmt.Errorf("--web cannot be used along with --username, --password or --token")
	}

	if (!login.StrIsEmpty(o.Username) && login.StrIsEmpty(o.Password)) || (login.StrIsEmpty(o.Username) && !login.StrIsEmpty(o.Password)) {
		return fmt.Errorf("both --username and --password need to be provided")
	}

	return nil
}

func (o *LoginOptions) ValidateAuthProvider(args []string) error {
	// Auth provider validation will be done in Run method after auth config is fetched
	return nil
}

func (o *LoginOptions) Run(ctx context.Context, args []string) error {
	var (
		authCAFile string
		err        error
	)

	// Get auth config with timeout-aware context
	o.authConfig, err = o.getAuthConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get auth info: %w", err)
	}
	if o.authConfig == nil {
		// auth disabled
		fmt.Println("Auth is disabled")
		if err := o.clientConfig.Persist(o.ConfigFilePath); err != nil {
			return fmt.Errorf("persisting client config: %w", err)
		}
		return nil
	}

	// Set up ClientId if not provided
	if o.ClientId == "" {
		switch o.authConfig.AuthType {
		case common.AuthTypeK8s:
			if o.Username != "" {
				o.ClientId = "openshift-challenging-client"
			} else {
				o.ClientId = "openshift-cli-client"
			}
		case common.AuthTypeOIDC:
			o.ClientId = "flightctl"
		}
	}

	// Create auth provider
	o.authProvider, err = client.CreateAuthProvider(client.AuthInfo{
		AuthProvider: &client.AuthProviderConfig{
			Name: o.authConfig.AuthType,
			Config: map[string]string{
				client.AuthUrlKey:      o.authConfig.AuthURL,
				client.AuthCAFileKey:   o.AuthCAFile,
				client.AuthClientIdKey: o.ClientId,
			},
		},
		OrganizationsEnabled: o.authConfig.AuthOrganizationsConfig.Enabled,
	}, o.InsecureSkipVerify)
	if err != nil {
		return fmt.Errorf("creating auth provider: %w", err)
	}

	// Validate auth provider
	validateArgs := login.ValidateArgs{
		ApiUrl:      args[0],
		ClientId:    o.ClientId,
		AccessToken: o.AccessToken,
		Username:    o.Username,
		Password:    o.Password,
		Web:         o.Web,
	}
	if err := o.authProvider.Validate(validateArgs); err != nil {
		return err
	}

	if o.AccessToken == "" && o.authConfig.AuthURL == "" {
		fmt.Printf("You must obtain API token, then login via \"flightctl login %s --token=<token>\"\n", o.clientConfig.Service.Server)
		return fmt.Errorf("must provide --token")
	}

	token := o.AccessToken
	if token == "" {
		authInfo, err := o.authProvider.Auth(o.Web, o.Username, o.Password)
		if err != nil {
			return err
		}
		token = authInfo.AccessToken
		o.clientConfig.AuthInfo.AuthProvider.Config[client.AuthRefreshTokenKey] = authInfo.RefreshToken
		if authInfo.ExpiresIn != nil {
			o.clientConfig.AuthInfo.AuthProvider.Config[client.AuthAccessTokenExpiryKey] = time.Unix(time.Now().Unix()+*authInfo.ExpiresIn, 0).Format(time.RFC3339Nano)
		}
	}
	if token == "" {
		return fmt.Errorf("failed to retrieve auth token")
	}
	o.clientConfig.AuthInfo.Token = token

	if o.AuthCAFile != "" {
		authCAFile, err = filepath.Abs(o.AuthCAFile)
		if err != nil {
			return fmt.Errorf("failed to get the absolute path of %s: %w", o.AuthCAFile, err)
		}
	}
	o.clientConfig.AuthInfo.AuthProvider.Config[client.AuthCAFileKey] = authCAFile

	c, err := client.NewFromConfig(o.clientConfig, o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	headerVal := "Bearer " + token
	res, err := c.AuthValidateWithResponse(ctx, &v1alpha1.AuthValidateParams{Authorization: &headerVal})
	if err != nil {
		return fmt.Errorf("validating token: %w", err)
	}
	if err := validateHttpResponse(res.Body, res.StatusCode(), http.StatusOK); err != nil {
		return err
	}

	err = o.clientConfig.Persist(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("persisting client config: %w", err)
	}

	fmt.Println("Login successful.")
	return nil
}

func (o *LoginOptions) getAuthConfig(ctx context.Context) (*v1alpha1.AuthConfig, error) {
	httpClient, err := client.NewHTTPClientFromConfig(o.clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client: %w", err)
	}
	c, err := apiClient.NewClientWithResponses(o.clientConfig.Service.Server, apiClient.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}
	resp, err := c.AuthConfigWithResponse(ctx)
	if err != nil {
		// Enhanced error handling for network issues
		errMsg := err.Error()
		if strings.Contains(errMsg, "connection refused") {
			return nil, fmt.Errorf("cannot connect to the API server at %s. The server may be down or not accessible. Please verify the URL and try again", o.clientConfig.Service.Server)
		}
		if strings.Contains(errMsg, "no such host") || strings.Contains(errMsg, "dns") {
			return nil, fmt.Errorf("cannot resolve hostname for %s. Please check the URL and ensure the hostname is correct", o.clientConfig.Service.Server)
		}
		if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline exceeded") {
			return nil, fmt.Errorf("connection to %s timed out. Please check your network connection and try again", o.clientConfig.Service.Server)
		}
		if strings.Contains(errMsg, "certificate") || strings.Contains(errMsg, "tls") {
			return nil, fmt.Errorf("TLS certificate error when connecting to %s. Provide a CA bundle with --certificate-authority=<path-to-ca.crt> or, for development only, use --insecure-skip-tls-verify", o.clientConfig.Service.Server)
		}
		return nil, fmt.Errorf("failed to get auth info from %s: %w", o.clientConfig.Service.Server, err)
	}

	respCode := resp.StatusCode()

	if respCode == http.StatusTeapot {
		return nil, nil
	}

	if respCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response code %v from %s. Please verify that the API URL is correct and the server is running", respCode, o.clientConfig.Service.Server)
	}

	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response from %s. Please verify that the API URL is correct and points to a valid Flight Control API server", o.clientConfig.Service.Server)
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
