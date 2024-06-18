package auth

import (
	"context"
	"net/http"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
)

const (
	XRHIDENTITY = "x-rh-identity"
)

type contextKey string

const identityContextKey contextKey = "identity"

func ParseHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get(XRHIDENTITY)
		ctx := r.Context()
		if header == "" {
			errString := "Missing authentication"
			w.WriteHeader(http.StatusForbidden)
			if _, err := w.Write([]byte(errString)); err != nil {
				logrus.Errorf("Failed to write response: %v", err)
			}
			logrus.Errorf("missing the %s header", XRHIDENTITY)
			return
		} else {
			identity, err := util.ParseXRHIdentityHeader(header)
			if err != nil {
				logrus.Errorln("Error parsing X-RH-IDENTITY header: ", err)
				w.WriteHeader(http.StatusInternalServerError)
				if _, err := w.Write([]byte("Internal server error")); err != nil {
					logrus.Errorf("Failed to write response: %v", err)
				}
				return
			}
			ctx = context.WithValue(ctx, identityContextKey, identity)
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
