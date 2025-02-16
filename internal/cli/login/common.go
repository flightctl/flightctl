package login

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"

	certutil "k8s.io/client-go/util/cert"
)

type OauthServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
	AuthEndpoint  string `json:"authorization_endpoint"`
}

func getAuthClientTransport(authCAFile string, insecureSkipVerify bool) (*http.Transport, error) {
	authTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecureSkipVerify, //nolint:gosec
		},
	}

	if authCAFile != "" {
		caData, err := os.ReadFile(authCAFile)
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

type AuthProvider interface {
	Auth(web bool, username, password string) (string, error)
}
