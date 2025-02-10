package middleware

import "net/http"

func AAPResponseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Location", "__gateway_no_rewrite__=1")
		next.ServeHTTP(w, r)
	})
}
