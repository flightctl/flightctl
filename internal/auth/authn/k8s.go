package authn

import (
	"context"
	"net/http"
	"strings"
)

type K8sAuthN struct{}

type ctxKeyAuthHeader string

const K8sTokenKey ctxKeyAuthHeader = "k8s-token"

func (k8s K8sAuthN) AuthHandler(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		authToken := strings.Split(authHeader, "Bearer ")
		if len(authToken) != 2 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), K8sTokenKey, authToken[1])
		next.ServeHTTP(w, r.WithContext(ctx))
	}
	return http.HandlerFunc(fn)
}
