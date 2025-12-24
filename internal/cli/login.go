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

	"github.com/flightctl/flightctl/api/v1beta1"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/cli/display"
	"github.com/flightctl/flightctl/internal/cli/login"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

	if strings.Contains(errStr, "x509:") || strings.Contains(errStr, "tls:") {
		return TLSErrorInfo{Type: TLSErrorGeneric, Cause: "certificate verification failed", RawError: errStr}
	}
	return TLSErrorInfo{Type: TLSErrorUnknown, RawError: errStr}
}

// formatTLSErrorForGeneral formats TLS errors for general API endpoints
func formatTLSErrorForGeneral(errorInfo TLSErrorInfo) string {
	cause := fmt.Sprintf("Cause: %s", errorInfo.Cause)

	examplesCA := "     flightctl login <API_URL> <login options> --certificate-authority=/path/to/ca.crt"
	examplesInsecure := "     flightctl login <API_URL> <login options> --insecure-skip-tls-verify\n" +
		"     WARNING: Skipping certificate verification makes your HTTPS connection insecure and could expose your credentials and data to interception."

	switch errorInfo.Type {
	case TLSErrorUnknown:
		// Not a TLS error we can classify; surface the original error
		return errorInfo.RawError

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
		return fmt.Sprintf("Cause: %s (%s)\n", errorInfo.Cause, errorInfo.RawError)
	}
}

// formatTLSErrorForAuth formats TLS errors for Auth endpoints
func formatTLSErrorForAuth(errorInfo TLSErrorInfo) string {
	cause := fmt.Sprintf("Cause: %s", errorInfo.Cause)

	examplesCA := "\n     flightctl login <API_URL> <login options> --auth-certificate-authority=/path/to/auth-ca.crt"
	examplesInsecure := "\n     flightctl login <API_URL> <login options> --insecure-skip-tls-verify\n" +
		"     WARNING: Skipping certificate verification makes your HTTPS connection insecure and could expose your credentials and data to interception."

	return cause + "\n" +
		"Options (choose one):\n" +
		"  1. Provide Auth server CA certificate (recommended)\n" + examplesCA + "\n" +
		"  2. Skip certificate verification (not recommended)\n" + examplesInsecure
}

type LoginOptions struct {
	GlobalOptions
	AccessToken        string
	Provider           string
	InsecureSkipVerify bool
	CAFile             string
	AuthCAFile         string
	CallbackPort       int
	Username           string
	Password           string
	Web                bool
	ShowProviders      bool
	authConfig         *v1beta1.AuthConfig
	authProvider       login.AuthProvider
	clientConfig       *client.Config
}

func DefaultLoginOptions() *LoginOptions {
	return &LoginOptions{
		GlobalOptions:      DefaultGlobalOptions(),
		AccessToken:        "",
		Provider:           "",
		InsecureSkipVerify: false,
		CAFile:             "",
		AuthCAFile:         "",
		CallbackPort:       8080,
		Username:           "",
		Password:           "",
		Web:                false,
		ShowProviders:      false,
	}
}

func NewCmdLogin() *cobra.Command {
	o := DefaultLoginOptions()
	cmd := &cobra.Command{
		Use:   "login [URL] [flags]",
		Short: "Login to flight control",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// If no arguments provided, show help
			if len(args) == 0 {
				return cmd.Help()
			}

			// If "help" is provided as an argument, show help
			if args[0] == "help" {
				return cmd.Help()
			}

			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			if err := o.Init(args); err != nil {
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
	fs.StringVarP(&o.Provider, "provider", "", o.Provider, "Name of the authentication provider to use")
	fs.StringVarP(&o.CAFile, "certificate-authority", "", o.CAFile, "Path to a cert file for the certificate authority")
	fs.StringVarP(&o.AuthCAFile, "auth-certificate-authority", "", o.AuthCAFile, "Path to a cert file for the auth certificate authority")
	fs.BoolVarP(&o.InsecureSkipVerify, "insecure-skip-tls-verify", "k", o.InsecureSkipVerify, "If true, the server's certificate will not be checked for validity. This will make your HTTPS connections insecure")
	fs.IntVarP(&o.CallbackPort, "callback-port", "", o.CallbackPort, "Port to use for OAuth callback")
	fs.StringVarP(&o.Username, "username", "u", o.Username, "Username for password flow authentication")
	fs.StringVarP(&o.Password, "password", "p", o.Password, "Password for password flow authentication")
	fs.BoolVarP(&o.Web, "web", "", o.Web, "Use web-based authorization code flow (default is password flow if username/password provided)")
	fs.BoolVarP(&o.ShowProviders, "show-providers", "", o.ShowProviders, "List available authentication providers")
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

// validateShowProvidersExclusion ensures --show-providers is not used with other login options
func (o *LoginOptions) validateShowProvidersExclusion() error {
	if !o.ShowProviders {
		return nil
	}

	conflicts := map[bool]string{
		!login.StrIsEmpty(o.AccessToken):                               "--token",
		!login.StrIsEmpty(o.Provider):                                  "--provider",
		!login.StrIsEmpty(o.AuthCAFile):                                "--auth-certificate-authority",
		!login.StrIsEmpty(o.Username) || !login.StrIsEmpty(o.Password): "--username or --password",
		o.Web: "--web",
	}

	for condition, flag := range conflicts {
		if condition {
			return fmt.Errorf("--show-providers cannot be used with %s", flag)
		}
	}
	return nil
}

func (o *LoginOptions) Validate(args []string) error {
	if err := o.GlobalOptions.ValidateCmd(args); err != nil {
		return err
	}

	if err := o.validateShowProvidersExclusion(); err != nil {
		return err
	}

	// Validate mutual exclusion: --token cannot be used with --provider or --auth-certificate-authority
	if !login.StrIsEmpty(o.AccessToken) {
		if !login.StrIsEmpty(o.Provider) {
			return fmt.Errorf("--token cannot be used with --provider")
		}
		if !login.StrIsEmpty(o.AuthCAFile) {
			return fmt.Errorf("--token cannot be used with --auth-certificate-authority")
		}
		if !login.StrIsEmpty(o.Username) || !login.StrIsEmpty(o.Password) {
			return fmt.Errorf("--token cannot be used with --username or --password")
		}
	}

	// Validate username/password are provided together
	if (!login.StrIsEmpty(o.Username) && login.StrIsEmpty(o.Password)) ||
		(login.StrIsEmpty(o.Username) && !login.StrIsEmpty(o.Password)) {
		return fmt.Errorf("--username and --password must be used together")
	}

	// Validate --web cannot be used with username/password
	if o.Web && (!login.StrIsEmpty(o.Username) || !login.StrIsEmpty(o.Password)) {
		return fmt.Errorf("--web cannot be used with --username or --password")
	}

	return nil
}

// getAuthProvider creates and returns the appropriate auth provider based on the authentication method
func (o *LoginOptions) getAuthProvider(ctx context.Context) (login.AuthProvider, client.AuthInfo, error) {
	// Get auth config with timeout-aware context for web-based auth
	var err error
	o.authConfig, err = o.getAuthConfig(ctx)
	if err != nil {
		return nil, client.AuthInfo{}, fmt.Errorf("failed to get auth info: %w", err)
	}
	if o.authConfig == nil || o.authConfig.Providers == nil || len(*o.authConfig.Providers) == 0 {
		return nil, client.AuthInfo{}, fmt.Errorf("no authentication providers configured on the server")
	}
	// If token is provided, create token provider
	if o.AccessToken != "" {
		return login.NewTokenAuth(o.AccessToken), client.AuthInfo{
			AccessToken: o.AccessToken,
			TokenToUse:  client.TokenToUseAccessToken,
		}, nil
	}

	// Create web-based auth provider from config
	var providerName string = lo.FromPtr((*o.authConfig.Providers)[0].Metadata.Name)
	if o.authConfig.DefaultProvider != nil {
		providerName = lo.FromPtr(o.authConfig.DefaultProvider)
	}
	if o.Provider != "" {
		providerName = o.Provider
	}
	var provider *v1beta1.AuthProvider
	for _, p := range *o.authConfig.Providers {
		if p.Metadata.Name != nil && *p.Metadata.Name == providerName {
			provider = &p
			break
		}
	}
	if provider == nil {
		availableProviders := lo.Map(*o.authConfig.Providers, func(p v1beta1.AuthProvider, _ int) string {
			return *p.Metadata.Name
		})
		availableProvidersStr := strings.Join(availableProviders, ", ")
		return nil, client.AuthInfo{}, fmt.Errorf("no auth provider found for name: %s. Available providers: %s", providerName, availableProvidersStr)
	}

	authInfo := client.AuthInfo{
		AuthProvider: &client.AuthProviderConfig{
			AuthProvider:       *provider,
			CAFile:             o.AuthCAFile,
			InsecureSkipVerify: o.InsecureSkipVerify,
		},
		OrganizationsEnabled: true,
	}
	authProvider, err := client.CreateAuthProviderWithCredentials(authInfo, o.InsecureSkipVerify, o.clientConfig.Service.Server, o.CallbackPort, o.Username, o.Password, o.Web)
	if err != nil {
		return nil, client.AuthInfo{}, fmt.Errorf("creating auth provider: %w", err)
	}
	return authProvider, authInfo, nil
}

// fetchAuthInfo retrieves the auth info, handling Auth TLS prompts and retries as needed
func (o *LoginOptions) fetchAuthInfo() (*login.AuthInfo, error) {

	authInfo, err := o.authProvider.Auth()
	if err != nil {
		// Check if this is an Auth certificate issue

		errorInfo := classifyTLSError(err)
		if errorInfo.Type != TLSErrorUnknown {
			// Offer interactive prompt to proceed insecurely
			if o.shouldOfferInsecurePrompt() && o.promptUseInsecure(errorInfo) {
				// enable insecure and recreate provider, then retry once
				o.enableInsecure()
				o.authProvider.SetInsecureSkipVerify(true)
				authInfo, err = o.authProvider.Auth()
			}
			if err != nil {
				authErrMsg := formatTLSErrorForAuth(errorInfo)
				return nil, fmt.Errorf("authentication failed\n%s", authErrMsg)
			}

		} else {
			return nil, err
		}
	}
	return &authInfo, nil
}

func (o *LoginOptions) getAbsAuthCAFile() (string, error) {
	if o.AuthCAFile == "" {
		return "", nil
	}
	authCAFile, err := filepath.Abs(o.AuthCAFile)
	if err != nil {
		return "", fmt.Errorf("failed to get the absolute path of %s: %w", o.AuthCAFile, err)
	}
	return authCAFile, nil
}

// validateTokenWithServer validates the token with the server, handling TLS prompts and a single retry
func (o *LoginOptions) validateTokenWithServer(ctx context.Context, token string) (*apiClient.ClientWithResponses, error) {
	// Create HTTP client without the GetAccessToken request editor to avoid potential deadlock
	httpClient, err := client.NewHTTPClientFromConfig(o.clientConfig)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP client: %w", err)
	}
	tokenEditor := apiClient.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(common.AuthHeader, fmt.Sprintf("Bearer %s", token))
		return nil
	})
	// Create API client with just the HTTP client and organization, no auto-token injection
	c, err := apiClient.NewClientWithResponses(
		o.clientConfig.Service.Server,
		apiClient.WithHTTPClient(httpClient),
		client.WithOrganization(o.clientConfig.Organization),
		tokenEditor,
		client.WithUserAgentHeader("flightctl-cli"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}

	// Add a 30-second timeout for the token validation call
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	headerVal := "Bearer " + token
	res, err := c.AuthValidateWithResponse(timeoutCtx, &v1beta1.AuthValidateParams{Authorization: &headerVal})
	if err != nil {
		// Translate TLS errors during token validation and offer interactive prompt
		errorInfo := classifyTLSError(err)
		if errorInfo.Type != TLSErrorUnknown && o.shouldOfferInsecurePrompt() && !o.InsecureSkipVerify {
			if o.promptUseInsecure(errorInfo) {
				o.enableInsecure()
				c, cerr := client.NewFromConfig(o.clientConfig, o.ConfigFilePath, client.WithUserAgentHeader("flightctl-cli"))
				if cerr == nil {
					res, err = c.AuthValidateWithResponse(ctx, &v1beta1.AuthValidateParams{Authorization: &headerVal})
				}
			}
		}
		if err != nil {
			friendlyErr := getUserFriendlyTLSError(err)
			return nil, fmt.Errorf("validating token:\n%s", friendlyErr)
		}
	}
	if err := validateHttpResponse(res.Body, res.StatusCode(), http.StatusOK); err != nil {
		return nil, err
	}
	return c, nil
}

func (o *LoginOptions) Run(ctx context.Context, args []string) error {
	// Handle --show-providers flag
	if o.ShowProviders {
		return o.showProviders(ctx)
	}

	var (
		authCAFile string
		err        error
	)

	// Get the appropriate auth provider (token-based or web-based)
	o.authProvider, o.clientConfig.AuthInfo, err = o.getAuthProvider(ctx)
	if err != nil {
		return err
	}
	err = o.authProvider.Validate(login.ValidateArgs{
		ApiUrl:      o.clientConfig.Service.Server,
		AccessToken: o.AccessToken,
	})
	if err != nil {
		return fmt.Errorf("validating auth provider: %w", err)
	}

	// Retrieve token (or use provided)
	authInfo, err := o.fetchAuthInfo()
	if err != nil {
		return err
	}
	if authInfo.ExpiresIn != nil {
		expiryTime := time.Now().Add(time.Duration(*authInfo.ExpiresIn) * time.Second)
		o.clientConfig.AuthInfo.AccessTokenExpiry = expiryTime.Format(time.RFC3339Nano)
	}
	o.clientConfig.AuthInfo.AccessToken = authInfo.AccessToken
	o.clientConfig.AuthInfo.RefreshToken = authInfo.RefreshToken
	o.clientConfig.AuthInfo.IdToken = authInfo.IdToken
	o.clientConfig.AuthInfo.TokenToUse = client.TokenToUseType(authInfo.TokenToUse)

	// Resolve auth CA file path if provided (only for web-based auth)
	if o.AccessToken == "" {
		authCAFile, err = o.getAbsAuthCAFile()
		if err != nil {
			return err
		}
		if o.clientConfig.AuthInfo.AuthProvider != nil {
			o.clientConfig.AuthInfo.AuthProvider.CAFile = authCAFile
		}
	}
	token := authInfo.AccessToken
	if authInfo.TokenToUse == login.TokenToUseIdToken {
		token = authInfo.IdToken
	}

	// Validate token with API server (handles TLS prompt/retry)
	c, err := o.validateTokenWithServer(ctx, token)
	if err != nil {
		return err
	}

	// Auto-select organization if enabled and user has access to only one
	if response, err := c.ListOrganizationsWithResponse(ctx, &v1beta1.ListOrganizationsParams{}); err == nil && response.StatusCode() == http.StatusOK && response.JSON200 != nil {

		if len(response.JSON200.Items) == 0 {
			return fmt.Errorf("no organizations found")
		}

		org := response.JSON200.Items[0]
		if org.Metadata.Name != nil {
			orgName := *org.Metadata.Name
			o.clientConfig.Organization = orgName

			displayName := ""
			if org.Spec != nil && org.Spec.DisplayName != nil {
				displayName = *org.Spec.DisplayName
			}

			if displayName != "" {
				fmt.Printf("Auto-selected organization: %s %s\n", orgName, displayName)
			} else {
				fmt.Printf("Auto-selected organization: %s\n", orgName)
			}
		}
	}

	if err := o.clientConfig.Persist(o.ConfigFilePath); err != nil {
		return fmt.Errorf("persisting client config: %w", err)
	}

	fmt.Println("Login successful.")
	return nil
}

// showProviders lists all available authentication providers
func (o *LoginOptions) showProviders(ctx context.Context) error {
	authConfig, err := o.getAuthConfig(ctx)
	if err != nil {
		return err
	}

	if authConfig == nil || authConfig.Providers == nil || len(*authConfig.Providers) == 0 {
		fmt.Println("No authentication providers configured")
		return nil
	}

	// Create formatter and display options (empty string defaults to table format)
	formatter := display.NewFormatter("")
	options := display.FormatOptions{
		Kind:   v1beta1.AuthConfigKind,
		Writer: os.Stdout,
	}

	return formatter.Format(authConfig, options)
}

func (o *LoginOptions) getAuthConfig(ctx context.Context) (*v1beta1.AuthConfig, error) {
	httpClient, err := client.NewHTTPClientFromConfig(o.clientConfig)
	if err != nil {
		// Translate TLS configuration errors and optionally prompt to proceed insecurely
		errorInfo := classifyTLSError(err)
		if errorInfo.Type != TLSErrorUnknown && o.shouldOfferInsecurePrompt() && !o.InsecureSkipVerify {
			if o.promptUseInsecure(errorInfo) {
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
	resp, err := c.AuthConfigWithResponse(ctx)
	if err != nil {
		// Enhanced error handling for network issues
		errMsg := err.Error()
		if strings.Contains(errMsg, "connection refused") {
			// Add URL validation suggestions
			validationErr := o.validateURLFormat(o.clientConfig.Service.Server)
			if validationErr != nil {
				return nil, fmt.Errorf("cannot connect to the API server at %s. The server may be down or not accessible. %s", o.clientConfig.Service.Server, validationErr.Error())
			}
			return nil, fmt.Errorf("cannot connect to the API server at %s. The server may be down or not accessible. Please verify the URL and try again", o.clientConfig.Service.Server)
		}
		if strings.Contains(errMsg, "no such host") || strings.Contains(errMsg, "dns") {
			// Add URL validation suggestions
			validationErr := o.validateURLFormat(o.clientConfig.Service.Server)
			if validationErr != nil {
				return nil, fmt.Errorf("cannot resolve hostname for %s. %s", o.clientConfig.Service.Server, validationErr.Error())
			}
			return nil, fmt.Errorf("cannot resolve hostname for %s. Please check the URL and ensure the hostname is correct", o.clientConfig.Service.Server)
		}
		if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline exceeded") {
			return nil, fmt.Errorf("connection to %s timed out. Please check your network connection and try again", o.clientConfig.Service.Server)
		}

		// Translate TLS connection errors with user-friendly messages and optionally prompt
		errorInfo := classifyTLSError(err)
		if errorInfo.Type != TLSErrorUnknown && o.shouldOfferInsecurePrompt() && !o.InsecureSkipVerify {
			if o.promptUseInsecure(errorInfo) {
				o.enableInsecure()
				// retry once
				httpClient, herr := client.NewHTTPClientFromConfig(o.clientConfig)
				if herr == nil {
					c, herr = apiClient.NewClientWithResponses(o.clientConfig.Service.Server, apiClient.WithHTTPClient(httpClient))
					if herr == nil {
						resp, err = c.AuthConfigWithResponse(ctx)
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

// getUserFriendlyTLSError translates cryptic TLS errors into actionable messages
func getUserFriendlyTLSError(err error) string {
	errorInfo := classifyTLSError(err)
	if errorInfo.Type == TLSErrorUnknown {
		return err.Error()
	}
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

// promptUseInsecure shows a warning and asks for confirmation to bypass certificate verification
func (o *LoginOptions) promptUseInsecure(errorInfo TLSErrorInfo) bool {
	fmt.Println("The server's certificate could not be verified (" + errorInfo.Cause + ").")
	fmt.Println("You can bypass the certificate check, but any data you send to the server could be intercepted by others.")
	fmt.Print("Use insecure connections? (y/N): ")
	var resp string
	_, _ = fmt.Scanln(&resp)
	resp = strings.TrimSpace(strings.ToLower(resp))
	return resp == "y" || resp == "yes"
}

// validateURLFormat provides helpful suggestions when URL format issues are detected after failed login attempts
func (o *LoginOptions) validateURLFormat(urlStr string) error {
	parsedUrl, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("%s format issue detected: %w. Please ensure the URL follows the format: https://hostname[:port]", urlStr, err)
	}

	// Check for common URL format issues that might not be caught by basic validation
	if strings.Contains(parsedUrl.Host, "//") {
		return fmt.Errorf("%s contains double slashes in the hostname. This is likely a formatting error. Please ensure the URL format is: https://hostname[:port]", urlStr)
	}

	// Check for path components that might indicate user error
	if parsedUrl.Path != "" && parsedUrl.Path != "/" {
		host := parsedUrl.Hostname()
		correctedURL := parsedUrl.Scheme + "://" + host
		if parsedUrl.Port() != "" {
			correctedURL = parsedUrl.Scheme + "://" + host + ":" + parsedUrl.Port()
		}
		return fmt.Errorf("%s contains path component '%s' which may not be needed. Try: %s", urlStr, parsedUrl.Path, correctedURL)
	}

	// Check for query parameters that might indicate user error
	if parsedUrl.RawQuery != "" {
		host := parsedUrl.Hostname()
		correctedURL := parsedUrl.Scheme + "://" + host
		if parsedUrl.Port() != "" {
			correctedURL = parsedUrl.Scheme + "://" + host + ":" + parsedUrl.Port()
		}
		return fmt.Errorf("%s contains query parameters '?%s' which may not be needed. Try: %s", urlStr, parsedUrl.RawQuery, correctedURL)
	}

	// Check for fragments that might indicate user error
	if parsedUrl.Fragment != "" {
		host := parsedUrl.Hostname()
		correctedURL := parsedUrl.Scheme + "://" + host
		if parsedUrl.Port() != "" {
			correctedURL = parsedUrl.Scheme + "://" + host + ":" + parsedUrl.Port()
		}
		return fmt.Errorf("%s contains fragment '#%s' which may not be needed. Try: %s", urlStr, parsedUrl.Fragment, correctedURL)
	}

	return nil
}
