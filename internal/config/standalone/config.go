package standalone

const (
	AuthTypeNone   = "none"
	AuthTypeOIDC   = "oidc"
	AuthTypeAAP    = "aap"
	AuthTypeOAuth2 = "oauth2"
)

type Config struct {
	Global GlobalConfig `json:"global"`
}

type GlobalConfig struct {
	BaseDomain string     `json:"baseDomain"`
	Auth       AuthConfig `json:"auth"`
}

type AuthConfig struct {
	Type                  string     `json:"type"`
	InsecureSkipTlsVerify bool       `json:"insecureSkipTlsVerify,omitempty"`
	AAP                   *AAPConfig `json:"aap,omitempty"`
}

type AAPConfig struct {
	ClientID string `json:"clientId,omitempty"`
	ApiURL   string `json:"apiUrl,omitempty"`
	Token    string `json:"token,omitempty"`
}
