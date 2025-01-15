package common

type ctxKeyAuthHeader string

const (
	AuthHeader     string           = "Authorization"
	TokenCtxKey    ctxKeyAuthHeader = "TokenCtxKey"
	IdentityCtxKey ctxKeyAuthHeader = "IdentityCtxKey"
)

type AuthConfig struct {
	Type string
	Url  string
}

type Identity struct {
	Username string
	UID      string
	Groups   []string
}
