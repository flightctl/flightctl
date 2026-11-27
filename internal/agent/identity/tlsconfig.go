package identity

import (
	"crypto/tls"
	"fmt"
	"net/url"

	base_client "github.com/flightctl/flightctl/internal/client"
	certutil "k8s.io/client-go/util/cert"
)

// baseTLSConfig builds the verification policy shared by every identity
// path: the service CA as RootCAs, the backend hostname as ServerName and
// SNI, and TLS 1.3 as the floor.
//
// It deliberately leaves NextProtos empty. The caller sets it, because the
// right value differs by transport: gRPC needs h2, and the agent link's
// WebSocket dial fails outright if h2 is advertised.
//
// It also leaves Certificates empty; each provider attaches its own, because
// that is the one part that genuinely differs between a key on disk and a key
// held in a TPM.
func baseTLSConfig(config *base_client.Config) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		//nolint:gosec // preserved from the existing client configuration surface
		InsecureSkipVerify: config.Service.InsecureSkipVerify,
	}

	if config.Service.TLSServerName != "" {
		tlsConfig.ServerName = config.Service.TLSServerName
	} else {
		u, err := url.Parse(config.Service.Server)
		if err != nil {
			return nil, fmt.Errorf("parsing server URL %q: %w", config.Service.Server, err)
		}
		tlsConfig.ServerName = u.Hostname()
	}
	if tlsConfig.ServerName == "" {
		return nil, fmt.Errorf("could not determine server name from %q", config.Service.Server)
	}

	if len(config.Service.CertificateAuthorityData) > 0 {
		caPool, err := certutil.NewPoolFromBytes(config.Service.CertificateAuthorityData)
		if err != nil {
			return nil, fmt.Errorf("parsing CA certificates: %w", err)
		}
		tlsConfig.RootCAs = caPool
	}

	return tlsConfig, nil
}

// flattenConfig returns a copy of config with any file references resolved to
// inline data, leaving the caller's config untouched.
func flattenConfig(config *base_client.Config) (*base_client.Config, error) {
	flattened := config.DeepCopy()
	if err := flattened.Flatten(); err != nil {
		return nil, fmt.Errorf("flattening client config: %w", err)
	}
	return flattened, nil
}
