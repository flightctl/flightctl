package auth

import (
	"context"
	"net/http"
)

type NilAuth struct{}

func (a NilAuth) AuthHandler(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	}
	return http.HandlerFunc(fn)
}

func (NilAuth) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	return true, nil
}
