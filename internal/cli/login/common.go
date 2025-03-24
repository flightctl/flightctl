package login

import (
	"crypto/tls"
	"fmt"
	"os"
	"strings"

	certutil "k8s.io/client-go/util/cert"
)

type OauthServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
	AuthEndpoint  string `json:"authorization_endpoint"`
}

func getAuthClientTlsConfig(authCAFile string, insecureSkipVerify bool) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: insecureSkipVerify, //nolint:gosec
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

		tlsConfig.RootCAs = caPool
	}

	return tlsConfig, nil
}

type AuthInfo struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    *int64
}

type ValidateArgs struct {
	ApiUrl      string
	ClientId    string
	AccessToken string
	Username    string
	Password    string
	Web         bool
}

type AuthProvider interface {
	Auth(web bool, username, password string) (AuthInfo, error)
	Renew(refreshToken string) (AuthInfo, error)
	Validate(args ValidateArgs) error
}

func StrIsEmpty(str string) bool {
	return len(strings.TrimSpace(str)) == 0
}
