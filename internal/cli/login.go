package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
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

const (
	tlsUnknownAuthority = "certificate signed by unknown authority"
	tlsExpiredCert      = "certificate has expired"
	tlsSelfSigned       = "certificate is self-signed"
	tlsHostnameMismatch = "certificate is valid for"
)

type TLSErrorType int

const (
	TLSErrorUnknown TLSErrorType = iota
	TLSErrorUnknownAuthority
	TLSErrorExpired
	TLSErrorSelfSigned
	TLSErrorHostnameMismatch
	TLSErrorGeneric
)

type TLSErrorInfo struct {
	Type     TLSErrorType
	Cause    string
	RawError string
}

// classifyTLSError analyzes an error and returns TLS error information
func classifyTLSError(err error) TLSErrorInfo {
	if err == nil {
		return TLSErrorInfo{Type: TLSErrorUnknown}
	}

	errStr := err.Error()

	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return TLSErrorInfo{
			Type:     TLSErrorHostnameMismatch,
			Cause:    "certificate hostname mismatch",
			RawError: errStr,
		}
	}

	var uaErr x509.UnknownAuthorityError
	if errors.As(err, &uaErr) {
		// Distinguish self-signed vs generally untrusted
		if uaErr.Cert != nil {
			sigErr := uaErr.Cert.CheckSignatureFrom(uaErr.Cert)
			if sigErr == nil {
				return TLSErrorInfo{
					Type:     TLSErrorSelfSigned,
					Cause:    "certificate is self-signed",
					RawError: errStr,
				}
			}
		}
		return TLSErrorInfo{
			Type:     TLSErrorUnknownAuthority,
			Cause:    "certificate not trusted",
			RawError: errStr,
		}
	}

	var invErr x509.CertificateInvalidError
	if errors.As(err, &invErr) {
		switch invErr.Reason {
		case x509.Expired:
			return TLSErrorInfo{
				Type:     TLSErrorExpired,
				Cause:    "certificate has expired",
				RawError: errStr,
			}
		default:
			// Fall through to generic handling below
		}
	}

	// Fallback: no structured TLS match; return generic
	return TLSErrorInfo{Type: TLSErrorGeneric, Cause: "certificate verification failed", RawError: errStr}
}

// formatTLSErrorForGeneral formats TLS errors for general API endpoints
func formatTLSErrorForGeneral(errorInfo TLSErrorInfo) string {
	cause := fmt.Sprintf("Cause: %s", errorInfo.Cause)

	examplesCA := "     flightctl login <API_URL> <login options> --certificate-authority=/path/to/ca.crt"
	examplesInsecure := "     flightctl login <API_URL> <login options> --insecure-skip-tls-verify\n" +
		"     WARNING: Skipping certificate verification makes your HTTPS connection insecure and could expose your credentials and data to interception."

	switch errorInfo.Type {
	case TLSErrorUnknownAuthority, TLSErrorSelfSigned:
		return cause + "\n" +
			"Options (choose one):\n" +
			"  1. Provide a trusted CA certificate (recommended)\n" + examplesCA + "\n" +
			"  2. Skip certificate verification (not recommended)\n" + examplesInsecure

	case TLSErrorExpired:
		return cause + "\n" +
			"Options (choose one):\n" +
			"  1. Contact your administrator to renew the certificate (recommended)\n" +
			"     After renewal, re-run your login command.\n" +
			"  2. Skip certificate verification (not recommended)\n" + examplesInsecure

	case TLSErrorHostnameMismatch:
		return cause + "\n" +
			"Options (choose one):\n" +
			"  1. Use the correct hostname or update the server certificate to include this host (recommended)\n" +
			"     Then re-run your login command.\n" +
			"  2. Skip certificate verification (not recommended)\n" + examplesInsecure

	default:
		return fmt.Sprintf("Cause: %s (%s)\n", errorInfo.Cause, errorInfo.RawError) +
			"Options (choose one):\n" +
			"  1. Provide a trusted CA certificate (recommended)\n" + examplesCA + "\n" +
			"  2. Skip certificate verification (not recommended)\n" + examplesInsecure
	}
}

// formatTLSErrorForOAuth formats TLS errors for OAuth endpoints
func formatTLSErrorForOAuth(errorInfo TLSErrorInfo) string {
	cause := fmt.Sprintf("Cause: %s", errorInfo.Cause)

	examplesCA := "\n     flightctl login <API_URL> <login options> --auth-certificate-authority=/path/to/auth-ca.crt"
	examplesInsecure := "\n     flightctl login <API_URL> <login options> --insecure-skip-tls-verify\n" +
		"     WARNING: Skipping certificate verification makes your HTTPS connection insecure and could expose your credentials and data to interception."

	return cause + "\n" +
		"Options (choose one):\n" +
		"  1. Provide OAuth server CA certificate (recommended)\n" + examplesCA + "\n" +
		"  2. Skip certificate verification (not recommended)\n" + examplesInsecure
}

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
	o.clientConfig, err = o.getClientConfig(args[0])
	if err != nil {
		return err
	}
	o.authConfig, err = o.getAuthConfig()
	if err != nil {
		return fmt.Errorf("failed to get auth info: %w", err)
	}
	if o.authConfig == nil {
		// auth disabled
		return nil
	}

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
	return err
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
	if o.authProvider == nil {
		return nil
	}

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
	return nil
}

func (o *LoginOptions) Run(ctx context.Context, args []string) error {
	var (
		authCAFile string
		err        error
	)
	if o.authProvider == nil {
		fmt.Println("Auth is disabled")
		if err := o.clientConfig.Persist(o.ConfigFilePath); err != nil {
			return fmt.Errorf("persisting client config: %w", err)
		}
		return nil
	}
	o.clientConfig.AuthInfo.AuthProvider = &client.AuthProviderConfig{
		Name: o.authConfig.AuthType,
		Config: map[string]string{
			client.AuthUrlKey:      o.authConfig.AuthURL,
			client.AuthClientIdKey: o.ClientId,
		},
	}

	token := o.AccessToken
	if token == "" {
		authInfo, err := o.authProvider.Auth(o.Web, o.Username, o.Password)
		if err != nil {
			// Check if this is an OAuth certificate issue
			if o.authConfig != nil && o.authConfig.AuthURL != "" {
				errorInfo := classifyTLSError(err)
				if errorInfo.Type != TLSErrorUnknown {
					// Offer interactive prompt to proceed insecurely
					if o.shouldOfferInsecurePrompt() && o.promptUseInsecureForOAuth(errorInfo) {
						// enable insecure and recreate provider, then retry once
						o.enableInsecure()
						if err := o.recreateAuthProvider(); err != nil {
							return fmt.Errorf("OAuth authentication failed\n%s", formatTLSErrorForOAuth(errorInfo))
						}
						authInfo, err = o.authProvider.Auth(o.Web, o.Username, o.Password)
					}
					if err != nil {
						oauthErrMsg := formatTLSErrorForOAuth(errorInfo)
						return fmt.Errorf("OAuth authentication failed\n%s", oauthErrMsg)
					}
				} else {
					return err
				}
			} else {
				return err
			}
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
		// Translate TLS errors during token validation and offer interactive prompt
		errorInfo := classifyTLSError(err)
		if errorInfo.Type != TLSErrorUnknown && o.shouldOfferInsecurePrompt() && !o.InsecureSkipVerify {
			if o.promptUseInsecureForGeneral(errorInfo) {
				o.enableInsecure()
				c, cerr := client.NewFromConfig(o.clientConfig, o.ConfigFilePath)
				if cerr == nil {
					res, err = c.AuthValidateWithResponse(ctx, &v1alpha1.AuthValidateParams{Authorization: &headerVal})
				}
			}
		}
		if err != nil {
			friendlyErr := getUserFriendlyTLSError(err)
			return fmt.Errorf("validating token:\n%s", friendlyErr)
		}
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

func (o *LoginOptions) getAuthConfig() (*v1alpha1.AuthConfig, error) {
	// First attempt
	httpClient, err := client.NewHTTPClientFromConfig(o.clientConfig)
	if err != nil {
		// Translate TLS configuration errors and optionally prompt to proceed insecurely
		errorInfo := classifyTLSError(err)
		if errorInfo.Type != TLSErrorUnknown && o.shouldOfferInsecurePrompt() && !o.InsecureSkipVerify {
			if o.promptUseInsecureForGeneral(errorInfo) {
				o.enableInsecure()
				// rebuild client with insecure
				httpClient, err = client.NewHTTPClientFromConfig(o.clientConfig)
			}
		}
		if err != nil {
			friendlyErr := getUserFriendlyTLSError(err)
			return nil, fmt.Errorf("failed to create http client:\n%s", friendlyErr)
		}
	}
	c, err := apiClient.NewClientWithResponses(o.clientConfig.Service.Server, apiClient.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}
	resp, err := c.AuthConfigWithResponse(context.Background())
	if err != nil {
		// Translate TLS connection errors with user-friendly messages and optionally prompt
		errorInfo := classifyTLSError(err)
		if errorInfo.Type != TLSErrorUnknown && o.shouldOfferInsecurePrompt() && !o.InsecureSkipVerify {
			if o.promptUseInsecureForGeneral(errorInfo) {
				o.enableInsecure()
				// retry once
				httpClient, herr := client.NewHTTPClientFromConfig(o.clientConfig)
				if herr == nil {
					c, herr = apiClient.NewClientWithResponses(o.clientConfig.Service.Server, apiClient.WithHTTPClient(httpClient))
					if herr == nil {
						resp, err = c.AuthConfigWithResponse(context.Background())
					}
				}
			}
		}
		if err != nil {
			friendlyErr := getUserFriendlyTLSError(err)
			return nil, fmt.Errorf("failed to get auth info:\n%s", friendlyErr)
		}
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

// getUserFriendlyTLSError translates cryptic TLS errors into actionable messages
func getUserFriendlyTLSError(err error) string {
	errorInfo := classifyTLSError(err)
	return formatTLSErrorForGeneral(errorInfo)
}

// shouldOfferInsecurePrompt returns true if we should attempt to prompt the user to proceed insecurely
func (o *LoginOptions) shouldOfferInsecurePrompt() bool {
	// Only prompt if stdin and stdout are TTYs and no token-only non-interactive mode was requested
	return isTerminal(os.Stdin.Fd()) && isTerminal(os.Stdout.Fd())
}

// isTerminal checks whether the given file descriptor is a terminal without external deps
func isTerminal(fd uintptr) bool {
	fi, err := os.Stdin.Stat()
	if fd == os.Stdout.Fd() {
		fi, err = os.Stdout.Stat()
	}
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// enableInsecure flips the config to insecure and updates related fields
func (o *LoginOptions) enableInsecure() {
	o.InsecureSkipVerify = true
	o.clientConfig.Service.InsecureSkipVerify = true
}

// recreateAuthProvider rebuilds the auth provider with the current insecurity setting
func (o *LoginOptions) recreateAuthProvider() error {
	provider, err := client.CreateAuthProvider(client.AuthInfo{
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
		return err
	}
	o.authProvider = provider
	return nil
}

// promptUseInsecureForGeneral shows an oc-like warning and asks for confirmation for general API connections
func (o *LoginOptions) promptUseInsecureForGeneral(errorInfo TLSErrorInfo) bool {
	fmt.Println("The server's certificate could not be verified (" + errorInfo.Cause + ").")
	fmt.Println("You can bypass the certificate check, but any data you send to the server could be intercepted by others.")
	fmt.Print("Use insecure connections? (y/N): ")
	var resp string
	_, _ = fmt.Scanln(&resp)
	resp = strings.TrimSpace(strings.ToLower(resp))
	return resp == "y" || resp == "yes"
}

// promptUseInsecureForOAuth shows a warning specific to OAuth endpoints
func (o *LoginOptions) promptUseInsecureForOAuth(errorInfo TLSErrorInfo) bool {
	fmt.Println("The authentication server's certificate could not be verified (" + errorInfo.Cause + ").")
	fmt.Println("You can bypass the certificate check, but any data you send to the server could be intercepted by others.")
	fmt.Print("Use insecure connections for authentication? (y/N): ")
	var resp string
	_, _ = fmt.Scanln(&resp)
	resp = strings.TrimSpace(strings.ToLower(resp))
	return resp == "y" || resp == "yes"
}
