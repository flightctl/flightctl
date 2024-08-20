package common

type ctxKeyAuthHeader string

const (
	AuthHeader  string           = "Authorization"
	TokenCtxKey ctxKeyAuthHeader = "TokenCtxKey"
)

type AuthConfig struct {
	Type string
	Url  string
}
