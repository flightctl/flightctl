package common

type ctxKeyAuthHeader string

const (
	AuthHeader  string           = "Authorization"
	TokenCtxKey ctxKeyAuthHeader = "TokenCtxKey"
)
