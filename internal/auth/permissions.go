package auth

import (
	"context"
	"net/http"
	"strings"

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

func CreatePermissionsMiddleware(log logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			var (
				resource string
				action   action
			)
			if r.URL.Path == "/api/version" {
				resource = "version"
				action = actionGet
			} else {
				parts := strings.Split(r.URL.Path, "/")
				// /, /api, /api/v{api-version} and /api/v{version}/auth don't require permissions
				if len(parts) < 3 || (len(parts) >= 4 && parts[3] == "auth") {
					next.ServeHTTP(w, r)
					return
				}

				// Extract resource and action from the request
				// Skip /api/v{api-version}, but not for /api/version does
				resource, action = extractResourceAndAction(parts[3:], r.Method)
				if resource == resourceNil || action == actionNil {
					log.Errorf("Unable to extract resource and action from %s and %s", r.URL.Path, r.Method)
					http.Error(w, errBadRequest, http.StatusBadRequest)
					return
				}
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
