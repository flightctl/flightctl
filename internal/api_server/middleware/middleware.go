package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/reqid"
	chi "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
)

// RequestSizeLimiter returns a middleware that limits the URL length and the number of request headers.
func RequestSizeLimiter(maxURLLength int, maxNumHeaders int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.URL.String()) > maxURLLength {
				http.Error(w, fmt.Sprintf("URL too long, exceeds %d characters", maxURLLength), http.StatusRequestURITooLong)
				return
			}
			if len(r.Header) > maxNumHeaders {
				http.Error(w, fmt.Sprintf("Request has too many headers, exceeds %d", maxNumHeaders), http.StatusRequestHeaderFieldsTooLarge)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(chi.RequestIDHeader)
		if requestID == "" {
			requestID = reqid.NextRequestID()
		}
		ctx := context.WithValue(r.Context(), chi.RequestIDKey, requestID)
		w.Header().Set(chi.RequestIDHeader, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func AddEventMetadataToCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-api")
		userName := "none"
		if auth.GetConfiguredAuthType() != auth.AuthTypeNil {
			identity, err := common.GetIdentity(ctx)
			if err == nil && identity != nil {
				userName = identity.Username
			}
		}
		ctx = context.WithValue(ctx, consts.EventActorCtxKey, fmt.Sprintf("user:%s", userName))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func AddOrgIDToCtx(orgStore store.Organization) func(http.Handler) http.Handler {
	// Create a TTL cache with 5-minute expiration for organization validation
	cache := ttlcache.New[uuid.UUID, bool](
		ttlcache.WithTTL[uuid.UUID, bool](5 * time.Minute),
	)
	go cache.Start()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			orgIDParam := r.URL.Query().Get("org_id")

			var orgID uuid.UUID
			if orgIDParam != "" {
				parsedID, err := uuid.Parse(orgIDParam)
				if err != nil {
					http.Error(w, fmt.Sprintf("Invalid org_id parameter: %s", err.Error()), http.StatusBadRequest)
					return
				}
				orgID = parsedID
			} else {
				// Fall back to the default organization.
				orgID = store.NullOrgId
			}

			// Check cache first
			if item := cache.Get(orgID); item != nil {
				ctx = util.WithOrganizationID(ctx, orgID)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Not in cache, validate that the organization exists from the store
			_, err := orgStore.GetByID(ctx, orgID)
			if err != nil {
				if errors.Is(err, flterrors.ErrResourceNotFound) {
					http.Error(w, fmt.Sprintf("Organization not found: %s", orgID), http.StatusNotFound)
					return
				}
				http.Error(w, fmt.Sprintf("Failed to validate organization: %s", err.Error()), http.StatusInternalServerError)
				return
			}

			cache.Set(orgID, true, ttlcache.DefaultTTL)
			ctx = util.WithOrganizationID(ctx, orgID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
