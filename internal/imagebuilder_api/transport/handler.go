package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/api/server"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// TransportHandler implements the generated ServerInterface for ImageBuilder API
type TransportHandler struct {
	service service.Service
	log     logrus.FieldLogger
}

// Make sure we conform to ServerInterface
var _ server.ServerInterface = (*TransportHandler)(nil)

// NewTransportHandler creates a new TransportHandler
func NewTransportHandler(svc service.Service, log logrus.FieldLogger) *TransportHandler {
	return &TransportHandler{
		service: svc,
		log:     log,
	}
}

// OrgIDFromContext extracts the organization ID from the context.
// Falls back to the default organization ID if not present.
func OrgIDFromContext(ctx context.Context) uuid.UUID {
	if orgID, ok := util.GetOrgIdFromContext(ctx); ok {
		return orgID
	}
	return store.NullOrgId
}

// ListImageBuilds handles GET /api/v1/imagebuilds
func (h *TransportHandler) ListImageBuilds(w http.ResponseWriter, r *http.Request, params api.ListImageBuildsParams) {
	body, status := h.service.ImageBuild().List(r.Context(), OrgIDFromContext(r.Context()), params)
	SetResponse(w, body, status)
}

// CreateImageBuild handles POST /api/v1/imagebuilds
func (h *TransportHandler) CreateImageBuild(w http.ResponseWriter, r *http.Request) {
	var imageBuild api.ImageBuild
	if err := json.NewDecoder(r.Body).Decode(&imageBuild); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.service.ImageBuild().Create(r.Context(), OrgIDFromContext(r.Context()), imageBuild)
	SetResponse(w, body, status)
}

// GetImageBuild handles GET /api/v1/imagebuilds/{name}
func (h *TransportHandler) GetImageBuild(w http.ResponseWriter, r *http.Request, name string, params api.GetImageBuildParams) {
	withExports := false
	if params.WithExports != nil {
		withExports = *params.WithExports
	}
	body, status := h.service.ImageBuild().Get(r.Context(), OrgIDFromContext(r.Context()), name, withExports)
	SetResponse(w, body, status)
}

// ReplaceImageBuild handles PUT /api/v1/imagebuilds/{name}
// ImageBuild is immutable, so this just calls Create with the name from the path.
// If the resource already exists, Create will return a conflict error.
func (h *TransportHandler) ReplaceImageBuild(w http.ResponseWriter, r *http.Request, name string) {
	var imageBuild api.ImageBuild
	if err := json.NewDecoder(r.Body).Decode(&imageBuild); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	// Set the name from the path parameter
	imageBuild.Metadata.Name = &name

	body, status := h.service.ImageBuild().Create(r.Context(), OrgIDFromContext(r.Context()), imageBuild)
	SetResponse(w, body, status)
}

// DeleteImageBuild handles DELETE /api/v1/imagebuilds/{name}
func (h *TransportHandler) DeleteImageBuild(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.service.ImageBuild().Delete(r.Context(), OrgIDFromContext(r.Context()), name)
	SetResponse(w, body, status)
}

// ListImageExports handles GET /api/v1/imageexports
func (h *TransportHandler) ListImageExports(w http.ResponseWriter, r *http.Request, params api.ListImageExportsParams) {
	body, status := h.service.ImageExport().List(r.Context(), OrgIDFromContext(r.Context()), params)
	SetResponse(w, body, status)
}

// CreateImageExport handles POST /api/v1/imageexports
func (h *TransportHandler) CreateImageExport(w http.ResponseWriter, r *http.Request) {
	var imageExport api.ImageExport
	if err := json.NewDecoder(r.Body).Decode(&imageExport); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.service.ImageExport().Create(r.Context(), OrgIDFromContext(r.Context()), imageExport)
	SetResponse(w, body, status)
}

// GetImageExport handles GET /api/v1/imageexports/{name}
func (h *TransportHandler) GetImageExport(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.service.ImageExport().Get(r.Context(), OrgIDFromContext(r.Context()), name)
	SetResponse(w, body, status)
}

// DeleteImageExport handles DELETE /api/v1/imageexports/{name}
func (h *TransportHandler) DeleteImageExport(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.service.ImageExport().Delete(r.Context(), OrgIDFromContext(r.Context()), name)
	SetResponse(w, body, status)
}

// DownloadImageExport handles GET /api/v1/imageexports/{name}/download
func (h *TransportHandler) DownloadImageExport(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	orgId := OrgIDFromContext(ctx)

	download, err := h.service.ImageExport().Download(ctx, orgId, name)
	if err != nil {
		status := downloadErrorToStatus(err, name)
		SetResponse(w, nil, status)
		return
	}

	// Handle redirect
	if download.RedirectURL != "" {
		statusCode := download.StatusCode
		if statusCode == 0 {
			// Default to 302 if status code not set
			statusCode = http.StatusFound
		}
		w.Header().Set("Location", download.RedirectURL)
		w.WriteHeader(statusCode)
		return
	}

	// Handle blob streaming
	if download.BlobReader != nil {
		defer download.BlobReader.Close()

		// Copy all headers from registry response
		for key, values := range download.Headers {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		// Set status code
		statusCode := download.StatusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		w.WriteHeader(statusCode)

		// Stream the blob content
		_, err := io.Copy(w, download.BlobReader)
		if err != nil {
			h.log.WithError(err).WithField("name", name).Error("failed to stream blob content")
			// Response may have already been written, so we can't change status
		}
		return
	}

	// Should not reach here, but handle gracefully
	h.log.WithField("name", name).Error("download returned neither redirect nor blob")
	status := v1beta1.Status{
		Code:    int32(http.StatusInternalServerError),
		Message: "Invalid download response",
	}
	SetResponse(w, nil, status)
}

// downloadErrorToStatus converts download errors to appropriate API status codes
func downloadErrorToStatus(err error, name string) v1beta1.Status {
	// Check for store errors (resource not found, etc.)
	if status := service.StoreErrorToApiStatus(err, false, string(api.ResourceKindImageExport), &name); status.Code != 0 {
		return status
	}

	// Check for external service errors (should return 503 Service Unavailable)
	if errors.Is(err, service.ErrExternalServiceUnavailable) {
		return service.StatusServiceUnavailable(err.Error())
	}

	// Check for validation errors (should return 4xx)
	if errors.Is(err, service.ErrImageExportNotReady) ||
		errors.Is(err, service.ErrImageExportStatusNotReady) ||
		errors.Is(err, service.ErrImageExportReadyConditionNotFound) ||
		errors.Is(err, service.ErrImageExportManifestDigestNotSet) ||
		errors.Is(err, service.ErrInvalidManifestDigest) ||
		errors.Is(err, service.ErrInvalidManifestLayerCount) ||
		errors.Is(err, service.ErrRepositoryNotFound) {
		return service.StatusBadRequest(err.Error())
	}

	// Default to internal server error (5xx)
	return service.StatusInternalServerError(err.Error())
}

// SetResponse writes the response body and status to the response writer
func SetResponse(w http.ResponseWriter, body any, status v1beta1.Status) {
	code := int(status.Code)

	// Never write a body for 204/304 (and generally 1xx), per RFC 7231
	if code == http.StatusNoContent || code == http.StatusNotModified || (code >= 100 && code < 200) {
		w.WriteHeader(code)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// Encode body into a buffer first to catch encoding errors before writing the response
	var buf bytes.Buffer
	var err error

	if body != nil && code >= 200 && code < 300 {
		err = json.NewEncoder(&buf).Encode(body)
	} else {
		err = json.NewEncoder(&buf).Encode(status)
	}

	if err != nil {
		// If encoding fails, send an internal server error response
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Now that encoding is successful, write the status and response
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// SetParseFailureResponse writes a parse failure response
func SetParseFailureResponse(w http.ResponseWriter, err error) {
	status := v1beta1.Status{
		Code:    int32(http.StatusBadRequest),
		Message: fmt.Sprintf("can't decode JSON body: %v", err),
	}
	SetResponse(w, nil, status)
}
