package middleware

import (
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
	"github.com/go-chi/chi/v5"
)

func CreateRouteExistsMiddleware(router chi.Router) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			routeContext := chi.RouteContext(r.Context())
			if !router.Match(routeContext, r.Method, r.URL.Path) {
				transport.SetResponse(w, nil, api.StatusNotFound(fmt.Sprintf("route not found: %s %s", r.Method, r.URL.Path)))
				return
			}
			next.ServeHTTP(w, r)
		}
		return http.HandlerFunc(fn)
	}

}
