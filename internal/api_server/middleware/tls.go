package middleware

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/sirupsen/logrus"
)

type contextKey string

const TLSCommonNameContextKey contextKey = "tls-cn"

func NewHTTPServerWithTLSContext(router http.Handler, log logrus.FieldLogger, address string) *http.Server {
	return &http.Server{
		Addr:         address,
		Handler:      router,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			tc := c.(*tls.Conn)
			// We need to ensure TLS handshake is complete before
			// we try to get anything useful from the ConnectionState
			// tls delays handshake until the first Read of Write
			err := tc.Handshake()
			if err != nil {
				log.Errorf("TLS handshake error: %v", err)
				return ctx
			}

			cs := tc.ConnectionState()
			if len(cs.PeerCertificates) == 0 {
				log.Warningf("Warning no TLS Peer Certificates: %v", err)
				return ctx
			}
			peerCertificate := cs.PeerCertificates[0]
			return context.WithValue(ctx, TLSCommonNameContextKey, peerCertificate.Subject.CommonName)
		},
	}
}

// NewTLSListener returns a new TLS listener. If the address is empty, it will
// listen on localhost's next available port.
func NewTLSListener(address string, tlsConfig *tls.Config) (net.Listener, error) {
	if address == "" {
		address = "localhost:0"
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(ln, tlsConfig), nil
}

func AdminTLSValidator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		cn, ok := r.Context().Value(TLSCommonNameContextKey).(string)
		if !ok || cn != crypto.AdminCommonName {
			http.Error(w, "AdminTLSValidatorMiddleware: Invalid client certificate", http.StatusUnauthorized)
			return
		}

		// all good, it shall pass
		next.ServeHTTP(w, r)
	})
}
