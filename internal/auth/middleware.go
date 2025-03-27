package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/sirupsen/logrus"
)

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

func CreateAuthNMiddleware(log logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/config" {
				next.ServeHTTP(w, r)
				return
			}
			authToken, err := authN.GetAuthToken(r)
			if err != nil {
				log.WithError(err).Error("failed to get auth token")
				writeResponse(w, api.StatusBadRequest("failed to get auth token"), log)
				return
			}
			err = authN.ValidateToken(r.Context(), authToken)
			if err != nil {
				log.WithError(err).Error("failed to validate token")
				writeResponse(w, api.StatusUnauthorized("failed to validate token"), log)
				return
			}
			ctx := context.WithValue(r.Context(), common.TokenCtxKey, authToken)
			identity, err := authN.GetIdentity(ctx, authToken)
			if err != nil {
				log.WithError(err).Error("failed to get identity")
			} else {
				ctx = context.WithValue(ctx, common.IdentityCtxKey, identity)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		}
		return http.HandlerFunc(fn)
	}
}

func CreateAuthZMiddleware(log logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			var (
				resource string
				action   action
			)
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

			if resource == resourceNil || action == actionNil {
				log.Errorf("Unable to extract resource and action from %s and %s", r.URL.Path, r.Method)
				http.Error(w, errBadRequest, http.StatusBadRequest)
				return
			}

			if !isAllowed(r.Context(), resource, action, w) {
				// http.Error was called in isAllowed
				return
			}

			// If authorized, proceed to the next handler
			next.ServeHTTP(w, r)
		}
		return http.HandlerFunc(fn)
	}
}

func isAllowed(ctx context.Context, resource string, action action, w http.ResponseWriter) bool {
	// Perform permission check
	allowed, err := GetAuthZ().CheckPermission(ctx, resource, string(action))
	if err != nil {
		http.Error(w, errAuthorizationServerUnavailable, http.StatusServiceUnavailable)
		return false
	}
	if allowed {
		return true
	}

	http.Error(w, errForbidden, http.StatusForbidden)
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
