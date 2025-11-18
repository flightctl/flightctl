package identity

const (
	AuthTypeK8s    = "k8s"
	AuthTypeOIDC   = "OIDC"
	AuthTypeOAuth2 = "OAuth2"
	AuthTypeAAP    = "AAPGateway"
)

// Issuer represents the source that produced an identity
type Issuer struct {
	// Type of the issuer (OIDC, AAP, K8s, etc.)
	Type string `json:"type"`

	// ID of the issuer (e.g., OIDC issuer URL, K8s cluster name, AAP instance ID)
	ID string `json:"id"`
}

// NewIssuer creates a new Issuer
func NewIssuer(issuerType, issuerID string) *Issuer {
	return &Issuer{
		Type: issuerType,
		ID:   issuerID,
	}
}

// String returns a string representation of the issuer
func (i *Issuer) String() string {
	if i.ID != "" {
		return i.Type + ":" + i.ID
	}
	return i.Type
}

// IsOIDC returns true if this is an OIDC issuer
func (i *Issuer) IsOIDC() bool {
	return i.Type == AuthTypeOIDC
}

// IsAAP returns true if this is an AAP issuer
func (i *Issuer) IsAAP() bool {
	return i.Type == AuthTypeAAP
}

// IsK8s returns true if this is a K8s issuer
func (i *Issuer) IsK8s() bool {
	return i.Type == AuthTypeK8s
}

// IsOAuth2 returns true if this is an OAuth2 issuer
func (i *Issuer) IsOAuth2() bool {
	return i.Type == AuthTypeOAuth2
}
