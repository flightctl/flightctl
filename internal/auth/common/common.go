package common

import (
	"net/http"
	"strings"
)

type ctxKeyAuthHeader string

const (
	AuthHeader  string           = "Authorization"
	TokenCtxKey ctxKeyAuthHeader = "TokenCtxKey"
)

type AuthConfig struct {
	Type string
	Url  string
}

func ParseAuthToken(authHeader string) (string, bool) {
	authToken := strings.Split(authHeader, "Bearer ")
	if len(authToken) != 2 {
		return "", false
	}
	return authToken[1], true
}

func GetAuthToken(r *http.Request) (string, bool) {
	authHeader := r.Header.Get(AuthHeader)
	return authHeader, authHeader != ""
}
