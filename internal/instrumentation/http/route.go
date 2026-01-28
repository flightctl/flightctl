package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.28.0"
)

// RouteMetricAttributes returns a metric attribute function that resolves chi routes
// using the provided router instance.
func RouteMetricAttributes(routes chi.Routes) func(*http.Request) []attribute.KeyValue {
	return func(r *http.Request) []attribute.KeyValue {
		if r == nil {
			return nil
		}

		if route := matchRoutePattern(routes, r); route != "" {
			return []attribute.KeyValue{semconv.HTTPRoute(route)}
		}

		return nil
	}
}

// RouteSpanNameFormatter returns an otelhttp span-name formatter that swaps the
// default operation name with the matching chi route pattern (when available).
func RouteSpanNameFormatter(routes chi.Routes) func(string, *http.Request) string {
	return func(operation string, r *http.Request) string {
		if route := matchRoutePattern(routes, r); route != "" {
			return route
		}
		return operation
	}
}

// WithComponentAttribute appends a stable component label to an existing metric attributes fn.
func WithComponentAttribute(base func(*http.Request) []attribute.KeyValue, component string) func(*http.Request) []attribute.KeyValue {
	return func(r *http.Request) []attribute.KeyValue {
		var attrs []attribute.KeyValue
		if base != nil {
			attrs = base(r)
		}
		return append(attrs, attribute.String("http_component", component))
	}
}

func matchRoutePattern(routes chi.Routes, r *http.Request) string {
	if r == nil {
		return ""
	}

	if routes != nil {
		rctx := chi.NewRouteContext()
		if routes.Match(rctx, r.Method, r.URL.Path) {
			if route := rctx.RoutePattern(); route != "" {
				return route
			}
		}
	}

	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}
