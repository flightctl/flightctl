package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/config/standalone"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

type CreateAAPApplicationOptions struct {
	Config       string
	OutputFile   string
	CACertFile   string
	AppName      string
	Organization int
}

func NewAAPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aap [command]",
		Short: "AAP integration commands",
		Long:  "Commands for integrating with Ansible Automation Platform (AAP)",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(0)
		},
	}

	cmd.AddCommand(NewCreateAAPApplicationCommand())

	return cmd
}

func NewCreateAAPApplicationCommand() *cobra.Command {
	opts := &CreateAAPApplicationOptions{}

	cmd := &cobra.Command{
		Use:   "create-oauth-application",
		Short: "Create an OAuth application in AAP",
		Long: `Create an OAuth application in AAP Gateway for Flight Control.

This command reads the service configuration to get AAP settings and creates
an OAuth application if one is not already configured. Requires setting the aap.apiUrl and aap.token in the service configuration. 
The resulting client_id is written to the specified output file.

The command is idempotent: it will skip creation if:
- A clientId is already set in the service configuration
- The output file already exists`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return opts.Run()
		},
	}

	cmd.Flags().StringVar(&opts.Config, "config", "/etc/flightctl/service-config.yaml", "Path to the service configuration file")
	cmd.Flags().StringVar(&opts.OutputFile, "output-file", "/etc/flightctl/pki/aap-client-id", "Output file path for the client_id")
	cmd.Flags().StringVar(&opts.CACertFile, "ca-cert-file", "/etc/flightctl/pki/auth/ca.crt", "Path to CA certificate file for AAP TLS verification")
	cmd.Flags().StringVar(&opts.AppName, "app-name", "Flight Control", "Name for the OAuth application")
	cmd.Flags().IntVar(&opts.Organization, "organization", 1, "AAP organization ID")

	return cmd
}

func (o *CreateAAPApplicationOptions) Run() error {
	if _, err := os.Stat(o.OutputFile); err == nil {
		fmt.Fprintln(os.Stderr, "AAP OAuth client_id file already exists at", o.OutputFile, "- skipping creation")
		return nil
	}

	configData, err := os.ReadFile(o.Config)
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", o.Config, err)
	}

	var config standalone.Config
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return fmt.Errorf("failed to parse config YAML from %s: %w", o.Config, err)
	}

	if config.Global.Auth.Type != standalone.AuthTypeAAP {
		fmt.Fprintln(os.Stderr, "Auth type is", config.Global.Auth.Type, "not 'aap', skipping AAP application creation")
		return nil
	}

	aapConfig := config.Global.Auth.AAP
	if aapConfig == nil {
		fmt.Fprintln(os.Stderr, "No AAP config found, skipping AAP application creation")
		return nil
	}

	if aapConfig.ClientId != "" {
		fmt.Fprintln(os.Stderr, "AAP clientId is already configured, skipping creation")
		return nil
	}

	if aapConfig.ApiUrl == "" {
		return fmt.Errorf("AAP apiUrl is required but not configured")
	}

	if aapConfig.Token == "" {
		fmt.Fprintln(os.Stderr, "AAP token is not configured, skipping AAP application creation")
		return nil
	}

	// Get baseDomain, with fallback to hostname
	baseDomain := config.Global.BaseDomain
	if baseDomain == "" {
		hostname, err := o.getHostnameFQDN()
		if err != nil {
			return fmt.Errorf("baseDomain not set and failed to get hostname: %w", err)
		}
		baseDomain = hostname
	}

	insecureSkipTLS := config.Global.Auth.InsecureSkipTlsVerify

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: insecureSkipTLS, //nolint:gosec
	}

	// Load CA certificate if provided and not skipping TLS verification
	if !insecureSkipTLS && o.CACertFile != "" {
		if _, err := os.Stat(o.CACertFile); err == nil {
			caCert, err := os.ReadFile(o.CACertFile)
			if err != nil {
				return fmt.Errorf("failed to read CA certificate file %s: %w", o.CACertFile, err)
			}

			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCert) {
				return fmt.Errorf("failed to parse CA certificate from %s", o.CACertFile)
			}
			tlsConfig.RootCAs = caCertPool
			fmt.Fprintln(os.Stderr, "Using CA certificate from", o.CACertFile)
		}
	}

	client, err := aap.NewAAPGatewayClient(aap.AAPGatewayClientOptions{
		GatewayUrl:      aapConfig.ApiUrl,
		TLSClientConfig: tlsConfig,
	})
	if err != nil {
		return fmt.Errorf("failed to create AAP client: %w", err)
	}

	redirectURIs := fmt.Sprintf("https://%s:443/callback http://127.0.0.1/callback", baseDomain)
	appURL := fmt.Sprintf("https://%s:443", baseDomain)

	request := &aap.AAPOAuthApplicationRequest{
		Name:                   o.AppName,
		Organization:           o.Organization,
		AuthorizationGrantType: "authorization-code",
		ClientType:             "public",
		RedirectURIs:           redirectURIs,
		AppURL:                 appURL,
	}

	fmt.Fprintln(os.Stderr, "Creating OAuth application", o.AppName, "in AAP")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := client.CreateOAuthApplication(ctx, aapConfig.Token, request)
	if err != nil {
		return fmt.Errorf("failed to create OAuth application: %w", err)
	}

	if response.ClientID == "" {
		return fmt.Errorf("AAP returned empty client_id")
	}

	fmt.Fprintln(os.Stderr, "OAuth application created successfully")

	// Write client_id to output file
	if err := os.WriteFile(o.OutputFile, []byte(response.ClientID), 0600); err != nil {
		return fmt.Errorf("failed to write client_id to %s: %w", o.OutputFile, err)
	}

	fmt.Fprintln(os.Stderr, "AAP OAuth client_id saved to", o.OutputFile)
	return nil
}

func (o *CreateAAPApplicationOptions) getHostnameFQDN() (string, error) {
	// Try "hostname -f" first for FQDN
	cmd := exec.Command("hostname", "-f")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		hostname := strings.TrimSpace(string(output))
		if hostname != "" {
			return strings.ToLower(hostname), nil
		}
	}

	// Fallback to "hostname" (short name)
	cmd = exec.Command("hostname")
	output, err = cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute hostname command: %w", err)
	}

	hostname := strings.TrimSpace(string(output))
	if hostname == "" {
		return "", fmt.Errorf("hostname command returned empty value")
	}

	return strings.ToLower(hostname), nil
}
