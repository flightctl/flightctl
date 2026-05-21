package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/oci"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

func (h *ServiceHandler) CreateRepository(ctx context.Context, orgId uuid.UUID, repository domain.Repository) (*domain.Repository, domain.Status) {
	// don't set fields that are managed by the service
	repository.Status = nil
	NilOutManagedObjectMetaProperties(&repository.Metadata)

	if errs := repository.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Repository().Create(ctx, orgId, &repository, h.callbackRepositoryUpdated)
	return result, StoreErrorToApiStatus(err, true, domain.RepositoryKind, repository.Metadata.Name)
}

func (h *ServiceHandler) ListRepositories(ctx context.Context, orgId uuid.UUID, params domain.ListRepositoriesParams) (*domain.RepositoryList, domain.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.Repository().List(ctx, orgId, *listParams)
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*domain.Repository, domain.Status) {
	result, err := h.store.Repository().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, domain.RepositoryKind, &name)
}

func (h *ServiceHandler) ReplaceRepository(ctx context.Context, orgId uuid.UUID, name string, repository domain.Repository) (*domain.Repository, domain.Status) {
	// don't overwrite fields that are managed by the service for external requests
	if !IsInternalRequest(ctx) {
		repository.Status = nil
		NilOutManagedObjectMetaProperties(&repository.Metadata)
	}

	if errs := repository.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *repository.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	// Preserve sensitive data from existing repository if the new one contains masked placeholders
	existingRepo, err := h.store.Repository().Get(ctx, orgId, name)
	if err == nil {
		if preserveErr := repository.PreserveSensitiveData(existingRepo); preserveErr != nil {
			return nil, domain.StatusInternalServerError(preserveErr.Error())
		}
	}

	result, created, err := h.store.Repository().CreateOrUpdate(ctx, orgId, &repository, h.callbackRepositoryUpdated)
	return result, StoreErrorToApiStatus(err, created, domain.RepositoryKind, &name)
}

func (h *ServiceHandler) DeleteRepository(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	err := h.store.Repository().Delete(ctx, orgId, name, h.callbackRepositoryDeleted)
	return StoreErrorToApiStatus(err, false, domain.RepositoryKind, &name)
}

func (h *ServiceHandler) PatchRepository(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Repository, domain.Status) {
	currentObj, err := h.store.Repository().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, domain.RepositoryKind, &name)
	}

	newObj := &domain.Repository{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/repositories/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	// Preserve sensitive data from existing repository if the new one contains masked placeholders
	if preserveErr := newObj.PreserveSensitiveData(currentObj); preserveErr != nil {
		return nil, domain.StatusInternalServerError(preserveErr.Error())
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	result, err := h.store.Repository().Update(ctx, orgId, newObj, h.callbackRepositoryUpdated)
	return result, StoreErrorToApiStatus(err, false, domain.RepositoryKind, &name)
}

func (h *ServiceHandler) ReplaceRepositoryStatusByError(ctx context.Context, orgId uuid.UUID, name string, repository domain.Repository, err error) (*domain.Repository, domain.Status) {
	if name != lo.FromPtr(repository.Metadata.Name) {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	// This is the only Condition for Repository
	changed := domain.SetStatusConditionByError(&repository.Status.Conditions, domain.ConditionTypeRepositoryAccessible, "Accessible", "Inaccessible", err)
	if !changed {
		// Nothing to do
		return &repository, domain.StatusOK()
	}

	result, err := h.store.Repository().UpdateStatus(ctx, orgId, &repository, h.callbackRepositoryUpdated)
	return result, StoreErrorToApiStatus(err, false, domain.RepositoryKind, &name)
}

func (h *ServiceHandler) GetRepositoryFleetReferences(ctx context.Context, orgId uuid.UUID, name string) (*domain.FleetList, domain.Status) {
	result, err := h.store.Repository().GetFleetRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, domain.RepositoryKind, &name)
}

func (h *ServiceHandler) GetRepositoryDeviceReferences(ctx context.Context, orgId uuid.UUID, name string) (*domain.DeviceList, domain.Status) {
	result, err := h.store.Repository().GetDeviceRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, domain.RepositoryKind, &name)
}

// callbackRepositoryUpdated is the repository-specific callback that handles repository update events
func (h *ServiceHandler) callbackRepositoryUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleRepositoryUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackRepositoryDeleted is the repository-specific callback that handles repository deletion events
func (h *ServiceHandler) callbackRepositoryDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

func (h *ServiceHandler) CheckRepositoryOciTag(ctx context.Context, orgId uuid.UUID, repositoryName, imageName, tag string) (*domain.OciRegistryCheckResult, domain.Status) {
	if !validation.OciImageNameRegexp.MatchString(imageName) {
		return nil, domain.StatusBadRequest(fmt.Sprintf("invalid imageName %q: must not include a tag or digest and must match %s", imageName, validation.OciImageNameFmt))
	}
	if !validation.OciImageTagRegexp.MatchString(tag) {
		return nil, domain.StatusBadRequest(fmt.Sprintf("invalid tag %q: must match %s", tag, validation.OciImageTagFmt))
	}

	repoRef, status := h.resolveOciRepoRef(ctx, orgId, repositoryName, imageName)
	if repoRef == nil {
		return nil, status
	}

	h.log.WithField("repository", repositoryName).
		WithField("imageName", imageName).
		WithField("tag", tag).
		Debug("checking tag in OCI registry")

	resolveCtx, resolveCancel := context.WithTimeout(ctx, 30*time.Second)
	defer resolveCancel()
	_, err := repoRef.Resolve(resolveCtx, tag)
	if err != nil {
		code, msg := extractOciError(err)
		h.log.WithField("repository", repositoryName).WithError(err).Debug("tag not accessible in registry")
		return &domain.OciRegistryCheckResult{Accessible: false, ErrorCode: code, ErrorMessage: msg}, domain.StatusOK()
	}

	return &domain.OciRegistryCheckResult{Accessible: true}, domain.StatusOK()
}

// errStopTagList is a sentinel used to stop ORAS tag-list pagination after the first page.
var errStopTagList = errors.New("stop tag list")

func (h *ServiceHandler) CheckRepositoryOciImage(ctx context.Context, orgId uuid.UUID, repositoryName, imageName string) (*domain.OciRegistryCheckResult, domain.Status) {
	if !validation.OciImageNameRegexp.MatchString(imageName) {
		return nil, domain.StatusBadRequest(fmt.Sprintf("invalid imageName %q: must not include a tag or digest and must match %s", imageName, validation.OciImageNameFmt))
	}

	repoRef, status := h.resolveOciRepoRef(ctx, orgId, repositoryName, imageName)
	if repoRef == nil {
		return nil, status
	}

	h.log.WithField("repository", repositoryName).
		WithField("imageName", imageName).
		Debug("checking OCI image repository accessibility")

	// Request at most one page to confirm the repository is accessible without fetching all tags.
	repoRef.TagListPageSize = 1
	tagsCtx, tagsCancel := context.WithTimeout(ctx, 30*time.Second)
	defer tagsCancel()
	err := repoRef.Tags(tagsCtx, "", func(_ []string) error { return errStopTagList })
	if err != nil && !errors.Is(err, errStopTagList) {
		code, msg := extractOciError(err)
		h.log.WithField("repository", repositoryName).WithError(err).Debug("OCI image repository not accessible")
		return &domain.OciRegistryCheckResult{Accessible: false, ErrorCode: code, ErrorMessage: msg}, domain.StatusOK()
	}

	return &domain.OciRegistryCheckResult{Accessible: true}, domain.StatusOK()
}

// extractOciError extracts the HTTP status code and human-readable message from an OCI registry error.
// Returns (0, err.Error()) when the error is not an HTTP-level response (e.g. network timeout).
func extractOciError(err error) (int, string) {
	var errResp *errcode.ErrorResponse
	if errors.As(err, &errResp) {
		msg := errResp.Errors.Error()
		if msg == "<nil>" || msg == "" {
			msg = http.StatusText(errResp.StatusCode)
		}
		return errResp.StatusCode, msg
	}
	if errors.Is(err, errdef.ErrNotFound) {
		return http.StatusNotFound, "not found"
	}
	return 0, err.Error()
}

// resolveOciRepoRef fetches the Repository resource and builds a configured ORAS remote.Repository.
func (h *ServiceHandler) resolveOciRepoRef(ctx context.Context, orgId uuid.UUID, repositoryName, imageName string) (*remote.Repository, domain.Status) {
	repo, err := h.store.Repository().Get(ctx, orgId, repositoryName)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil, domain.StatusResourceNotFound(domain.RepositoryKind, repositoryName)
		}
		return nil, domain.StatusInternalServerError(fmt.Sprintf("failed to get Repository %q: %v", repositoryName, err))
	}

	repoType, err := repo.Spec.Discriminator()
	if err != nil {
		return nil, domain.StatusInternalServerError(fmt.Sprintf("failed to determine repository type for %q: %v", repositoryName, err))
	}
	if repoType != string(domain.OciRepoSpecTypeOci) {
		return nil, domain.StatusBadRequest(fmt.Sprintf("repository %q is of type %q, not OCI", repositoryName, repoType))
	}

	ociSpecVal, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		return nil, domain.StatusInternalServerError(fmt.Sprintf("failed to decode OCI spec for repository %q: %v", repositoryName, err))
	}
	ociSpec := &ociSpecVal

	fullRef := strings.TrimRight(ociSpec.Registry, "/") + "/" + strings.TrimLeft(imageName, "/")
	repoRef, err := oci.BuildOciRepoRef(ociSpec, fullRef)
	if err != nil {
		return nil, domain.StatusBadRequest(fmt.Sprintf("invalid repository reference %q: %v", fullRef, err))
	}
	return repoRef, domain.StatusOK()
}
