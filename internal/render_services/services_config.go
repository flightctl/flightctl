package renderservices

import (
	"os"

	"sigs.k8s.io/yaml"
)

// global:
//   baseDomain:
//   auth:
//     type: none # aap, oidc or none
//     insecureSkipTlsVerify: false
//     aap:
//       apiUrl:
//       externalApiUrl:
//       oAuthApplicationClientId:
//       oAuthToken:
//     oidc:
//       oidcAuthority:
//       externalOidcAuthority:
//       oidcClientId:
//   organizations:
//     # Enable IdP-provided organizations support
//     enabled: false

type Config struct {
	Global GlobalConfig `json:"global"`
}

type GlobalConfig struct {
	BaseDomain string     `json:"baseDomain"`
	Auth       AuthConfig `json:"auth"`
}

type AuthConfig struct {
	Type                  string `json:"type"`
	InsecureSkipTlsVerify bool   `json:"insecureSkipTlsVerify"`
}

func unmarshalServicesConfig(configFile string) (*Config, error) {
	content, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}

	var config Config
	err = yaml.Unmarshal(content, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}
