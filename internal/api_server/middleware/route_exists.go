package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func CreateRouteExistsMiddleware(router chi.Router) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			routeContext := chi.RouteContext(r.Context())
			if !router.Match(routeContext, r.Method, r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		}
		return http.HandlerFunc(fn)
	}

}
