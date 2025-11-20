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
	"github.com/flightctl/flightctl/internal/cli/login"
	"github.com/flightctl/flightctl/internal/client"
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

	// Validate authentication flag conflicts
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

// ensureClientID extracts the ClientId from the provider config based on auth type
func (o *LoginOptions) ensureClientID() error {
	if o.ClientId != "" {
		return nil
	}
	provider := o.getDefaultProvider()
	if provider == nil {
		return fmt.Errorf("no authentication provider found")
	}
	// Get the provider type from the spec
	providerType, err := provider.Spec.Discriminator()
	if err != nil {
		return fmt.Errorf("failed to determine provider type: %w", err)
	}
	switch providerType {
	case string(v1alpha1.K8s):
		if o.Username != "" {
			o.ClientId = "openshift-challenging-client"
		} else {
			o.ClientId = "openshift-cli-client"
		}
		return nil
	case string(v1alpha1.Oidc):
		if oidcSpec, err := provider.Spec.AsOIDCProviderSpec(); err == nil {
			if oidcSpec.ClientId != "" {
				o.ClientId = oidcSpec.ClientId
				return nil
			}
		}
		return fmt.Errorf("client ID is required for OIDC authentication but was not provided by the API")
	case string(v1alpha1.Oauth2):
		if oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec(); err == nil {
			if oauth2Spec.ClientId != "" {
				o.ClientId = oauth2Spec.ClientId
				return nil
			}
		}
		return fmt.Errorf("client ID is required for OAuth2 authentication but was not provided by the API")
	}
	return nil
}

// getDefaultProvider returns the default authentication provider
func (o *LoginOptions) getDefaultProvider() *v1alpha1.AuthProvider {
	if o.authConfig == nil || o.authConfig.Providers == nil {
		return nil
	}
	// Find the default provider by name
	if o.authConfig.DefaultProvider != nil && *o.authConfig.DefaultProvider != "" {
		for _, p := range *o.authConfig.Providers {
			if p.Metadata.Name != nil && *p.Metadata.Name == *o.authConfig.DefaultProvider {
				return &p
			}
		}
	}
	// If no default is set, return the first provider
	if len(*o.authConfig.Providers) > 0 {
		return &(*o.authConfig.Providers)[0]
	}
	return nil
}

// createAuthProvider creates and assigns the auth provider from current config
func (o *LoginOptions) createAuthProvider() error {
	provider := o.getDefaultProvider()
	if provider == nil || provider.Metadata.Name == nil {
		return fmt.Errorf("no valid authentication provider found")
	}

	providerType, err := provider.Spec.Discriminator()
	if err != nil {
		return fmt.Errorf("failed to get provider type: %w", err)
	}

	authURL := o.extractAuthURL(provider)
	scopes := o.extractScopes(provider)
	providerName := ""
	if provider.Metadata.Name != nil {
		providerName = *provider.Metadata.Name
	}

	orgEnabled := false
	if o.authConfig.OrganizationsEnabled != nil {
		orgEnabled = *o.authConfig.OrganizationsEnabled
	}

	configMap := map[string]string{
		client.AuthUrlKey:      authURL,
		client.AuthCAFileKey:   o.AuthCAFile,
		client.AuthClientIdKey: o.ClientId,
	}
	if scopes != "" {
		configMap[client.AuthScopesKey] = scopes
	}
	if o.clientConfig.Service.Server != "" {
		configMap[client.AuthServerUrlKey] = o.clientConfig.Service.Server
	}
	if providerName != "" {
		configMap[client.AuthProviderNameKey] = providerName
	}

	authProvider, err := client.CreateAuthProvider(client.AuthInfo{
		AuthProvider: &client.AuthProviderConfig{
			Name:   *provider.Metadata.Name,
			Type:   providerType,
			Config: configMap,
		},
		OrganizationsEnabled: orgEnabled,
	}, o.InsecureSkipVerify)
	if err != nil {
		return fmt.Errorf("creating auth provider: %w", err)
	}
	o.authProvider = authProvider
	return nil
}

// extractAuthURL extracts the authentication URL from an AuthProvider based on its type
func (o *LoginOptions) extractAuthURL(provider *v1alpha1.AuthProvider) string {
	providerType, _ := provider.Spec.Discriminator()
	switch providerType {
	case string(v1alpha1.K8s):
		if k8sSpec, err := provider.Spec.AsK8sProviderSpec(); err == nil {
			return k8sSpec.ApiUrl
		}
	case string(v1alpha1.Oidc):
		if oidcSpec, err := provider.Spec.AsOIDCProviderSpec(); err == nil {
			return oidcSpec.Issuer
		}
	case "aap":
		if aapSpec, err := provider.Spec.AsAapProviderSpec(); err == nil {
			if aapSpec.ExternalApiUrl != nil {
				return *aapSpec.ExternalApiUrl
			}
			return aapSpec.ApiUrl
		}
	case string(v1alpha1.Oauth2):
		if oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec(); err == nil {
			return oauth2Spec.AuthorizationUrl
		}
	}
	return ""
}

// extractScopes extracts scopes from an AuthProvider as a space-delimited string
// Returns empty string if scopes are not configured (provider will use default)
func (o *LoginOptions) extractScopes(provider *v1alpha1.AuthProvider) string {
	providerType, _ := provider.Spec.Discriminator()
	switch providerType {
	case string(v1alpha1.Oidc):
		if oidcSpec, err := provider.Spec.AsOIDCProviderSpec(); err == nil {
			if oidcSpec.Scopes != nil && len(*oidcSpec.Scopes) > 0 {
				return strings.Join(*oidcSpec.Scopes, " ")
			}
		}
	case string(v1alpha1.Oauth2):
		if oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec(); err == nil {
			if oauth2Spec.Scopes != nil && len(*oauth2Spec.Scopes) > 0 {
				return strings.Join(*oauth2Spec.Scopes, " ")
			}
		}
	case "openshift":
		if openshiftSpec, err := provider.Spec.AsOpenShiftProviderSpec(); err == nil {
			if openshiftSpec.Scopes != nil && len(*openshiftSpec.Scopes) > 0 {
				return strings.Join(*openshiftSpec.Scopes, " ")
			}
		}
	}
	return ""
}

// setAuthProviderInClientConfig writes the provider info into the client config
func (o *LoginOptions) setAuthProviderInClientConfig() {
	provider := o.getDefaultProvider()
	if provider == nil || provider.Metadata.Name == nil {
		return
	}

	providerType, err := provider.Spec.Discriminator()
	if err != nil {
		return
	}

	authURL := o.extractAuthURL(provider)

	o.clientConfig.AuthInfo.AuthProvider = &client.AuthProviderConfig{
		Name: *provider.Metadata.Name,
		Type: providerType,
		Config: map[string]string{
			client.AuthUrlKey:      authURL,
			client.AuthClientIdKey: o.ClientId,
		},
	}
}

// fetchToken retrieves an access token, handling Auth TLS prompts and retries as needed
func (o *LoginOptions) fetchToken() (string, error) {
	if o.AccessToken != "" {
		return o.AccessToken, nil
	}

	authInfo, err := o.authProvider.Auth(o.Web, o.Username, o.Password)
	if err != nil {
		// Check if this is an Auth certificate issue
		provider := o.getDefaultProvider()
		authURL := o.extractAuthURL(provider)
		if o.authConfig != nil && provider != nil && authURL != "" {
			errorInfo := classifyTLSError(err)
			if errorInfo.Type != TLSErrorUnknown {
				// Offer interactive prompt to proceed insecurely
				if o.shouldOfferInsecurePrompt() && o.promptUseInsecure(errorInfo) {
					// enable insecure and recreate provider, then retry once
					o.enableInsecure()
					if err := o.createAuthProvider(); err != nil {
						return "", fmt.Errorf("authentication failed\n%s", formatTLSErrorForAuth(errorInfo))
					}
					authInfo, err = o.authProvider.Auth(o.Web, o.Username, o.Password)
				}
				if err != nil {
					authErrMsg := formatTLSErrorForAuth(errorInfo)
					return "", fmt.Errorf("authentication failed\n%s", authErrMsg)
				}
			} else {
				return "", err
			}
		} else {
			return "", err
		}
	}

	token := authInfo.AccessToken
	o.clientConfig.AuthInfo.AuthProvider.Config[client.AuthRefreshTokenKey] = authInfo.RefreshToken
	if authInfo.ExpiresIn != nil {
		o.clientConfig.AuthInfo.AuthProvider.Config[client.AuthAccessTokenExpiryKey] = time.Unix(time.Now().Unix()+*authInfo.ExpiresIn, 0).Format(time.RFC3339Nano)
	}
	if token == "" {
		return "", fmt.Errorf("failed to retrieve auth token")
	}
	return token, nil
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
	c, err := client.NewFromConfig(o.clientConfig, o.ConfigFilePath)
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}

	headerVal := "Bearer " + token
	res, err := c.AuthValidateWithResponse(ctx, &v1alpha1.AuthValidateParams{Authorization: &headerVal})
	if err != nil {
		// Translate TLS errors during token validation and offer interactive prompt
		errorInfo := classifyTLSError(err)
		if errorInfo.Type != TLSErrorUnknown && o.shouldOfferInsecurePrompt() && !o.InsecureSkipVerify {
			if o.promptUseInsecure(errorInfo) {
				o.enableInsecure()
				c, cerr := client.NewFromConfig(o.clientConfig, o.ConfigFilePath)
				if cerr == nil {
					res, err = c.AuthValidateWithResponse(ctx, &v1alpha1.AuthValidateParams{Authorization: &headerVal})
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
	// Extract client ID from provider config
	if err := o.ensureClientID(); err != nil {
		return err
	}

	// Create auth provider
	if err := o.createAuthProvider(); err != nil {
		return err
	}

	// Validate auth provider
	validateArgs := login.ValidateArgs{
		ApiUrl:      o.clientConfig.Service.Server,
		ClientId:    o.ClientId,
		AccessToken: o.AccessToken,
		Username:    o.Username,
		Password:    o.Password,
		Web:         o.Web,
	}
	if err := o.authProvider.Validate(validateArgs); err != nil {
		return err
	}

	provider := o.getDefaultProvider()
	providerAuthURL := ""
	if provider != nil {
		providerAuthURL = o.extractAuthURL(provider)
	}
	if o.AccessToken == "" && providerAuthURL == "" {
		fmt.Printf("You must obtain API token, then login via \"flightctl login %s --token=<token>\"\n", o.clientConfig.Service.Server)
		return fmt.Errorf("must provide --token")
	}

	o.setAuthProviderInClientConfig()

	// Retrieve token (or use provided)
	token, err := o.fetchToken()
	if err != nil {
		return err
	}
	o.clientConfig.AuthInfo.Token = token

	// Resolve auth CA file path if provided
	authCAFile, err = o.getAbsAuthCAFile()
	if err != nil {
		return err
	}
	o.clientConfig.AuthInfo.AuthProvider.Config[client.AuthCAFileKey] = authCAFile

	// Validate token with API server (handles TLS prompt/retry)
	c, err := o.validateTokenWithServer(ctx, token)
	if err != nil {
		return err
	}

	// Auto-select organization if enabled and user has access to only one
	if o.authConfig.OrganizationsEnabled != nil && *o.authConfig.OrganizationsEnabled {
		if response, err := c.ListOrganizationsWithResponse(ctx); err == nil && response.StatusCode() == http.StatusOK && response.JSON200 != nil && len(response.JSON200.Items) == 1 {
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
	}

	if err := o.clientConfig.Persist(o.ConfigFilePath); err != nil {
		return fmt.Errorf("persisting client config: %w", err)
	}

	fmt.Println("Login successful.")
	return nil
}

func (o *LoginOptions) getAuthConfig(ctx context.Context) (*v1alpha1.AuthConfig, error) {
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
