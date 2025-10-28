package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/sirupsen/logrus"
)

type contextKey string

type action string

const (
	actionNil              action = ""
	actionGet              action = "get"
	actionPost             action = "create"
	actionList             action = "list"
	actionPut              action = "update"
	actionPatch            action = "patch"
	actionDelete           action = "delete"
	actionDeleteCollection action = "deletecollection"

	resourceNil string = ""
)

const (
	errForbidden                      = "Forbidden"
	errAuthorizationServerUnavailable = "Authorization server unavailable"
	errBadRequest                     = "Unable to verify request"
)

var defaultActions = map[string]action{
	http.MethodGet:    actionGet,
	http.MethodPost:   actionPost,
	http.MethodPut:    actionPut,
	http.MethodPatch:  actionPatch,
	http.MethodDelete: actionDelete,
}

var apiVersionPattern = regexp.MustCompile(`^v[1-9]+$`)

// stringToAction converts a string to an action type
func stringToAction(s string) action {
	return action(s)
}

func CreateAuthNMiddleware(authN common.AuthNMiddleware, log logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			// Skip authentication for public auth endpoints
			if isPublicAuthEndpoint(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			authToken, err := authN.GetAuthToken(r)
			if err != nil {
				log.WithError(err).Error("failed to get auth token")
				writeResponse(w, api.StatusBadRequest("failed to get auth token"), log)
				return
			}
			log.Debugf("Auth middleware: got auth token: %s", authToken)
			err = authN.ValidateToken(r.Context(), authToken)
			if err != nil {
				log.WithError(err).Error("failed to validate token")
				writeResponse(w, api.StatusUnauthorized("failed to validate token"), log)
				return
			}
			log.Debugf("Auth middleware: token validated successfully")
			ctx := context.WithValue(r.Context(), consts.TokenCtxKey, authToken)
			identity, err := authN.GetIdentity(ctx, authToken)
			if err != nil {
				log.WithError(err).Error("failed to get identity")
			} else {
				ctx = context.WithValue(ctx, consts.IdentityCtxKey, identity)
				log.Debugf("Auth middleware: set identity %s in context", identity.GetUsername())
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		}
		return http.HandlerFunc(fn)
	}
}

func CreateAuthZMiddleware(authZ AuthZMiddleware, log logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			var (
				resource string
				action   action
			)

			// First, try to get metadata from the API metadata registry using existing Chi context
			if metadata, found := server.GetEndpointMetadata(r); found {
				if metadata.Resource != "" && metadata.Action != "" {
					resource = metadata.Resource
					action = stringToAction(metadata.Action)
				}
			}

			// Fallback to existing logic if no metadata found
			if resource == "" || action == actionNil {
				if r.URL.Path == "/api/version" {
					resource = "version"
					var ok bool
					if action, ok = defaultActions[r.Method]; !ok {
						action = actionNil
					}
				} else {
					parts := strings.Split(r.URL.Path, "/")
					// /, /api, /api/v{api-version} and /api/v{api-version}/auth don't require permissions
					matchesAPIVPath := false
					if len(parts) == 3 {
						matchesAPIVPath = apiVersionPattern.MatchString(parts[2])
					}
					if len(parts) < 3 || matchesAPIVPath || (len(parts) >= 4 && parts[3] == "auth") {
						next.ServeHTTP(w, r)
						return
					}

					parts = parts[3:]
					resource, action = extractResourceAndAction(parts, r.Method)
				}
			}

			if resource == resourceNil || action == actionNil {
				log.Errorf("Unable to extract resource and action from %s and %s", r.URL.Path, r.Method)
				http.Error(w, errBadRequest, http.StatusBadRequest)
				return
			}

			// Add HTTP request to context for authorization checks
			ctx := context.WithValue(r.Context(), contextKey("http_request"), r)

			log.Debugf("AuthZMiddleware: checking authorization for path=%s, method=%s, resource=%s, action=%s",
				r.URL.Path, r.Method, resource, action)

			if !isAllowed(ctx, authZ, log, resource, action, w) {
				// http.Error was called in isAllowed
				log.Debugf("AuthZMiddleware: authorization denied for path=%s, method=%s, resource=%s, action=%s",
					r.URL.Path, r.Method, resource, action)
				return
			}

			log.Debugf("AuthZMiddleware: authorization granted for path=%s, method=%s, resource=%s, action=%s",
				r.URL.Path, r.Method, resource, action)

			// If authorized, proceed to the next handler
			next.ServeHTTP(w, r)
		}
		return http.HandlerFunc(fn)
	}
}

func isAllowed(ctx context.Context, authZ AuthZMiddleware, log logrus.FieldLogger, resource string, action action, w http.ResponseWriter) bool {
	// Perform permission check
	allowed, err := authZ.CheckPermission(ctx, resource, string(action))
	if err != nil {
		log.WithError(err).Error("failed to check permission")

		// Check if this is a client-side error (e.g., invalid token claims)
		if flterrors.IsClientAuthError(err) {
			http.Error(w, errBadRequest, http.StatusBadRequest)
		} else {
			http.Error(w, errAuthorizationServerUnavailable, http.StatusServiceUnavailable)
		}
		return false
	}
	if allowed {
		return true
	}

	http.Error(w, errForbidden, http.StatusForbidden)
	return false
}

// isPublicAuthEndpoint checks if the given path is a public auth endpoint that doesn't require authentication
func isPublicAuthEndpoint(path string) bool {
	publicEndpoints := []string{
		"/api/v1/auth/config",
		"/api/v1/auth/.well-known/openid-configuration",
		"/api/v1/auth/jwks",
		"/api/v1/auth/authorize",
		"/api/v1/auth/login",
		"/api/v1/auth/token",
	}
	for _, endpoint := range publicEndpoints {
		if path == endpoint {
			return true
		}
	}
	return false
}

func extractResourceAndAction(parts []string, method string) (string, action) {
	if len(parts) == 0 {
		return resourceNil, actionNil
	}

	// Handle according to the URL structure
	// e.g., "device", "devices", "devices/{name}", "devices/{name}/sub-action", "devices/{name}/sub-action/{sub-name}"
	resource := strings.ToLower(parts[0])
	action, ok := defaultActions[method]
	if !ok {
		return resourceNil, actionNil
	}
	switch len(parts) {
	case 1: // resources
		switch method {
		case http.MethodGet:
			action = actionList
		case http.MethodDelete:
			action = actionDeleteCollection
		}
	case 2: // resources/{name}
		// No changes
	case 3:
		// resources/{name}/sub-action
		subAction := strings.ToLower(parts[2])
		resource += "/" + subAction
		if action == actionDelete {
			action = actionDeleteCollection
		}
	case 4:
		// resources/{name}/sub-action/{name}
		subAction := strings.ToLower(parts[2])
		resource += "/" + subAction
	}

	return resource, action
}

func writeResponse(w http.ResponseWriter, status api.Status, log logrus.FieldLogger) {
	resp, err := json.Marshal(status)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(int(status.Code))
	if _, err := w.Write(resp); err != nil {
		log.WithError(err).Warn("failed to write response")
	}
}
