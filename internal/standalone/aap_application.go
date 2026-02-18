package standalone

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	standaloneconfig "github.com/flightctl/flightctl/internal/config/standalone"
	"github.com/flightctl/flightctl/internal/quadlet/renderer"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/sirupsen/logrus"
)

type OAuthApplicationCreator interface {
	CreateOAuthApplication(ctx context.Context, token string, req *aap.AAPOAuthApplicationRequest) (*aap.AAPOAuthApplicationResponse, error)
}

type CreateAAPClientOptions struct {
	AAPConfig       *standaloneconfig.AAPConfig
	BaseDomain      string
	InsecureSkipTLS bool
	CACertFile      string
	Logger          logrus.FieldLogger
}

func CreateAAPClient(opts CreateAAPClientOptions) (*aap.AAPGatewayClient, error) {
	tlsConfig, err := buildTLSConfig(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to build TLS config: %w", err)
	}

	return aap.NewAAPGatewayClient(aap.AAPGatewayClientOptions{
		GatewayUrl:      opts.AAPConfig.ApiURL,
		TLSClientConfig: tlsConfig,
	})
}

func buildTLSConfig(opts CreateAAPClientOptions) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: opts.InsecureSkipTLS, //nolint:gosec
	}

	// Load CA certificate if provided and not skipping TLS verification
	if !opts.InsecureSkipTLS && opts.CACertFile != "" {
		if _, err := os.Stat(opts.CACertFile); err == nil {
			caCert, err := os.ReadFile(opts.CACertFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read CA certificate file %s: %w", opts.CACertFile, err)
			}

			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to parse CA certificate from %s", opts.CACertFile)
			}
			tlsConfig.RootCAs = caCertPool
			opts.Logger.Infof("Using CA certificate from %s", opts.CACertFile)
		} else if opts.CACertFile != renderer.DefaultAuthCACertPath {
			opts.Logger.Warnf("configured CA cert file not found: %s - using system CAs", opts.CACertFile)
		}
	}

	return tlsConfig, nil
}

type CreateAAPApplicationOptions struct {
	Client       OAuthApplicationCreator
	Logger       logrus.FieldLogger
	AAPConfig    *standaloneconfig.AAPConfig
	BaseDomain   string
	AppName      string
	Organization int
	OutputFile   string
}

// CreateAAPApplication creates an OAuth application in AAP Gateway and writes
// the client_id to the specified output file.
func CreateAAPApplication(ctx context.Context, opts CreateAAPApplicationOptions) error {
	request := buildOAuthApplicationRequest(opts.BaseDomain, opts.AppName, opts.Organization)
	clientID, err := createOAuthApplication(ctx, opts.Client, opts.AAPConfig.Token, request)
	if err != nil {
		return fmt.Errorf("failed to create OAuth application: %w", err)
	}

	opts.Logger.Info("OAuth application created successfully")

	if err := writeClientIDToFile(clientID, opts.OutputFile); err != nil {
		return fmt.Errorf("failed to write client_id to %s: %w", opts.OutputFile, err)
	}

	opts.Logger.Infof("AAP OAuth client_id saved to %s", opts.OutputFile)
	return nil
}

func buildOAuthApplicationRequest(baseDomain string, appName string, organization int) *aap.AAPOAuthApplicationRequest {
	redirectURIs := fmt.Sprintf("https://%s:443/callback http://127.0.0.1/callback", baseDomain)
	appURL := fmt.Sprintf("https://%s:443", baseDomain)

	return &aap.AAPOAuthApplicationRequest{
		Name:                   appName,
		Organization:           organization,
		AuthorizationGrantType: "authorization-code",
		ClientType:             "public",
		RedirectURIs:           redirectURIs,
		AppURL:                 appURL,
	}
}

func createOAuthApplication(ctx context.Context, client OAuthApplicationCreator, token string, request *aap.AAPOAuthApplicationRequest) (string, error) {
	response, err := client.CreateOAuthApplication(ctx, token, request)
	if err != nil {
		return "", err
	}

	if response.ClientID == "" {
		return "", fmt.Errorf("AAP returned empty client_id")
	}

	return response.ClientID, nil
}

func writeClientIDToFile(clientID string, outputFile string) error {
	return os.WriteFile(outputFile, []byte(clientID), 0600)
}
