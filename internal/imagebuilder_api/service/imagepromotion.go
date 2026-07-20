package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	ibstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/jsonpatch"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// ImagePromotionService handles CRUD for ImagePromotion resources.
// State-machine evaluation and catalog publishing are handled asynchronously
// by the imagebuilder worker in response to events emitted here.
type ImagePromotionService interface {
	Create(ctx context.Context, orgId uuid.UUID, promotion domain.ImagePromotion) (*domain.ImagePromotion, domain.Status)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, domain.Status)
	List(ctx context.Context, orgId uuid.UUID, params domain.ListImagePromotionsParams) (*domain.ImagePromotionList, domain.Status)
	Delete(ctx context.Context, orgId uuid.UUID, name string) domain.Status
	// Replace implements PUT /api/v1/imagepromotions/{name}. Only spec.source.exportFormats
	// (append-only) and metadata.labels may be changed. All other spec fields must be identical.
	Replace(ctx context.Context, orgId uuid.UUID, name string, promotion domain.ImagePromotion) (*domain.ImagePromotion, domain.Status)
	// Patch implements PATCH /api/v1/imagepromotions/{name} using RFC 6902 JSON Patch.
	// Only operations targeting /spec/source/exportFormats or /metadata/labels are accepted.
	Patch(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.ImagePromotion, domain.Status)
	// Internal methods (not exposed via API)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, promotion *domain.ImagePromotion) (*domain.ImagePromotion, error)
	ListPendingForBuild(ctx context.Context, orgId uuid.UUID, imageBuildRef string) ([]domain.ImagePromotion, error)
}

type imagePromotionService struct {
	store           ibstore.ImagePromotionStore
	imageBuildStore ibstore.ImageBuildStore
	catalogs        catalogservice.Service
	queueProducer   queues.QueueProducer
	log             logrus.FieldLogger
}

// NewImagePromotionService creates a new ImagePromotionService.
func NewImagePromotionService(
	store ibstore.ImagePromotionStore,
	imageBuildStore ibstore.ImageBuildStore,
	catalogs catalogservice.Service,
	queueProducer queues.QueueProducer,
	log logrus.FieldLogger,
) ImagePromotionService {
	return &imagePromotionService{
		store:           store,
		imageBuildStore: imageBuildStore,
		catalogs:        catalogs,
		queueProducer:   queueProducer,
		log:             log,
	}
}

// Create validates and persists a new ImagePromotion in WaitingForArtifacts state, then
// enqueues an evaluation event for the worker to process asynchronously.
func (s *imagePromotionService) Create(ctx context.Context, orgId uuid.UUID, promotion domain.ImagePromotion) (*domain.ImagePromotion, domain.Status) {
	NilOutManagedObjectMetaProperties(&promotion.Metadata)
	promotion.Status = nil
	setGenerationOnCreate(&promotion.Metadata)

	errs, internalErr := s.validateCreate(ctx, orgId, &promotion)
	if internalErr != nil {
		return nil, StatusInternalServerError(internalErr.Error())
	} else if len(errs) > 0 {
		return nil, StatusBadRequest(errors.Join(errs...).Error())
	}

	promotion.Status = &domain.ImagePromotionStatus{}
	setPromotionReadyCondition(&promotion, domain.ImagePromotionConditionReasonWaitingForArtifacts,
		conditionMessageForReason(domain.ImagePromotionConditionReasonWaitingForArtifacts))

	result, err := s.store.Create(ctx, orgId, &promotion)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, true, string(domain.ResourceKindImagePromotion), promotion.Metadata.Name)
	}

	s.enqueuePromotionEvent(ctx, orgId, result, true)
	return result, StatusCreated()
}

// Get retrieves an ImagePromotion by name.
func (s *imagePromotionService) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, domain.Status) {
	result, err := s.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, string(domain.ResourceKindImagePromotion), &name)
	}
	return result, StatusOK()
}

// List retrieves a list of ImagePromotions.
func (s *imagePromotionService) List(ctx context.Context, orgId uuid.UUID, params domain.ListImagePromotionsParams) (*domain.ImagePromotionList, domain.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if !IsStatusOK(status) {
		return nil, status
	}

	result, err := s.store.List(ctx, orgId, *listParams)
	if err == nil {
		return result, StatusOK()
	}

	var se *selector.SelectorError
	switch {
	case selector.AsSelectorError(err, &se):
		return nil, StatusBadRequest(se.Error())
	default:
		return nil, StatusInternalServerError(err.Error())
	}
}

// Delete deletes an ImagePromotion by name.
func (s *imagePromotionService) Delete(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	_, err := s.store.Delete(ctx, orgId, name)
	return StoreErrorToApiStatus(err, false, string(domain.ResourceKindImagePromotion), &name)
}

// Replace implements PUT /api/v1/imagepromotions/{name}.
// Only spec.source.exportFormats (append-only) and metadata.labels may be changed.
// When new export formats are added to a post-publish promotion, the promotion is
// moved back to WaitingForArtifacts and an evaluation event is enqueued for the worker.
func (s *imagePromotionService) Replace(ctx context.Context, orgId uuid.UUID, name string, submitted domain.ImagePromotion) (*domain.ImagePromotion, domain.Status) {
	current, err := s.store.Get(ctx, orgId, name)
	if err != nil {
		if isNotFound(err) {
			submitted.Metadata.Name = &name
			return s.Create(ctx, orgId, submitted)
		}
		return nil, StoreErrorToApiStatus(err, false, string(domain.ResourceKindImagePromotion), &name)
	}
	if lo.FromPtrOr(current.Metadata.Name, "") != lo.FromPtrOr(submitted.Metadata.Name, "") {
		return nil, StatusBadRequest("metadata.name is immutable after creation")
	}
	if err := validateNotTerminalOrPublishing(current); err != nil {
		return nil, StatusBadRequest(err.Error())
	}
	if err := validateImmutableSpecFields(current, &submitted); err != nil {
		return nil, StatusBadRequest(err.Error())
	}
	newFormats, err := computeAppendOnlyFormats(current.Spec.Source.ExportFormats, submitted.Spec.Source.ExportFormats)
	if err != nil {
		return nil, StatusBadRequest(err.Error())
	}
	if len(newFormats) > 0 {
		err, internalErr := s.validateFormatsNotAlreadyPublished(ctx, orgId, current, newFormats)
		if internalErr != nil {
			return nil, StatusInternalServerError(internalErr.Error())
		}
		if err != nil {
			return nil, StatusBadRequest(err.Error())
		}
	}

	current.Metadata.Labels = submitted.Metadata.Labels
	merged := append(lo.FromPtrOr(current.Spec.Source.ExportFormats, nil), newFormats...)
	if len(merged) > 0 {
		current.Spec.Source.ExportFormats = &merged
	} else {
		current.Spec.Source.ExportFormats = nil
	}

	_, errs, internalErr := s.validate(ctx, orgId, current)
	if internalErr != nil {
		return nil, StatusInternalServerError(internalErr.Error())
	} else if len(errs) > 0 {
		return nil, StatusBadRequest(errors.Join(errs...).Error())
	}

	previousReason := getPromotionReadyReason(current)
	moveToWaiting := len(newFormats) > 0

	if moveToWaiting && previousReason != string(domain.ImagePromotionConditionReasonWaitingForArtifacts) {
		setPromotionReadyCondition(current, domain.ImagePromotionConditionReasonWaitingForArtifacts,
			conditionMessageForReason(domain.ImagePromotionConditionReasonWaitingForArtifacts))
	}

	// Adding an export format is the only spec change Replace allows (labels are not part of
	// spec); everything else was already locked by validateImmutableSpecFields above.
	current.Metadata.Generation = incrementGenerationOnSpecChange(current.Metadata.Generation, len(newFormats) > 0)

	updated, err := s.store.Update(ctx, orgId, current)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, string(domain.ResourceKindImagePromotion), &name)
	}

	if moveToWaiting {
		s.enqueuePromotionEvent(ctx, orgId, updated, false)
	}
	return updated, StatusOK()
}

// Patch implements PATCH /api/v1/imagepromotions/{name} using RFC 6902 JSON Patch.
// It applies the patch to the current resource and delegates to Replace for all validation and
// state-transition logic.
func (s *imagePromotionService) Patch(ctx context.Context, orgId uuid.UUID, name string, ops domain.PatchRequest) (*domain.ImagePromotion, domain.Status) {
	current, err := s.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, string(domain.ResourceKindImagePromotion), &name)
	}

	var submitted domain.ImagePromotion
	if err := jsonpatch.Apply(*current, &submitted, ops); err != nil {
		return nil, StatusBadRequest(err.Error())
	}

	return s.Replace(ctx, orgId, name, submitted)
}

func (s *imagePromotionService) UpdateStatus(ctx context.Context, orgId uuid.UUID, promotion *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	return s.store.UpdateStatus(ctx, orgId, promotion)
}

func (s *imagePromotionService) ListPendingForBuild(ctx context.Context, orgId uuid.UUID, imageBuildRef string) ([]domain.ImagePromotion, error) {
	return s.store.ListPendingForBuild(ctx, orgId, imageBuildRef)
}

// enqueuePromotionEvent enqueues an ImagePromotion event to the ImageBuildTaskQueue for the
// worker to pick up and evaluate asynchronously.
func (s *imagePromotionService) enqueuePromotionEvent(ctx context.Context, orgId uuid.UUID, promotion *domain.ImagePromotion, isCreate bool) {
	if s.queueProducer == nil || promotion == nil || promotion.Metadata.Name == nil {
		return
	}

	event := common.GetResourceCreatedOrUpdatedSuccessEvent(
		ctx, isCreate,
		coredomain.ResourceKind(string(domain.ResourceKindImagePromotion)),
		*promotion.Metadata.Name, nil, s.log, nil,
	)
	if event == nil {
		return
	}

	eventWithOrgId := worker_client.EventWithOrgId{OrgId: orgId, Event: *event}
	payload, err := json.Marshal(eventWithOrgId)
	if err != nil {
		s.log.WithError(err).Error("failed to marshal ImagePromotion event")
		return
	}

	var timestamp int64
	if event.Metadata.CreationTimestamp != nil {
		timestamp = event.Metadata.CreationTimestamp.UnixMicro()
	} else {
		timestamp = time.Now().UnixMicro()
	}

	if err := s.queueProducer.Enqueue(ctx, payload, timestamp); err != nil {
		s.log.WithError(err).WithField("promotion", *promotion.Metadata.Name).
			Error("failed to enqueue ImagePromotion evaluation event")
	}
}

// validateNotTerminalOrPublishing returns an error if the promotion is in a terminal or
// in-progress Publishing state where updates are not permitted.
func validateNotTerminalOrPublishing(promotion *domain.ImagePromotion) error {
	reason := getPromotionReadyReason(promotion)
	switch reason {
	case string(domain.ImagePromotionConditionReasonFailed):
		return fmt.Errorf("cannot update a promotion in Failed state; create a new ImagePromotion to retry")
	case string(domain.ImagePromotionConditionReasonBuildFailed):
		return fmt.Errorf("cannot update a promotion whose build has failed; create a new ImagePromotion to retry")
	case string(domain.ImagePromotionConditionReasonBuildCanceled):
		return fmt.Errorf("cannot update a promotion whose build was canceled; create a new ImagePromotion to retry")
	case string(domain.ImagePromotionConditionReasonPublishing):
		return fmt.Errorf("cannot update a promotion while it is in Publishing state; try again after it completes")
	}
	return nil
}

// validateImmutableSpecFields checks that all spec fields other than exportFormats are unchanged.
func validateImmutableSpecFields(current, submitted *domain.ImagePromotion) error {
	if current.Spec.Source.ImageBuildRef != submitted.Spec.Source.ImageBuildRef {
		return fmt.Errorf("spec.source.imageBuildRef is immutable after creation")
	}
	currentDisc, _ := current.Spec.Target.Discriminator()
	submittedDisc, _ := submitted.Spec.Target.Discriminator()
	if currentDisc != submittedDisc {
		return fmt.Errorf("spec.target.type is immutable after creation")
	}

	switch currentDisc {
	case string(domain.ImagePromotionTargetTypeNewCatalogItem):
		cur, err1 := current.Spec.Target.AsNewCatalogItemTarget()
		sub, err2 := submitted.Spec.Target.AsNewCatalogItemTarget()
		if err1 != nil || err2 != nil || !reflect.DeepEqual(cur, sub) {
			return fmt.Errorf("spec.target fields other than type are immutable after creation")
		}
	case string(domain.ImagePromotionTargetTypeExistingCatalogItem):
		cur, err1 := current.Spec.Target.AsExistingCatalogItemTarget()
		sub, err2 := submitted.Spec.Target.AsExistingCatalogItemTarget()
		if err1 != nil || err2 != nil || !reflect.DeepEqual(cur, sub) {
			return fmt.Errorf("spec.target fields other than type are immutable after creation")
		}
	}
	return nil
}

// computeAppendOnlyFormats validates that the submitted list is a superset of the current list
// and returns the newly added formats. Returns an error if formats are removed.
func computeAppendOnlyFormats(current, submitted *[]domain.ExportFormatType) ([]domain.ExportFormatType, error) {
	currentSet := make(map[domain.ExportFormatType]bool)
	for _, f := range lo.FromPtrOr(current, nil) {
		currentSet[f] = true
	}

	submittedList := lo.FromPtrOr(submitted, nil)

	for f := range currentSet {
		found := false
		for _, sf := range submittedList {
			if sf == f {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("export format %q cannot be removed from an existing promotion", f)
		}
	}

	seen := make(map[domain.ExportFormatType]bool)
	var newFormats []domain.ExportFormatType
	for _, f := range submittedList {
		if seen[f] {
			return nil, fmt.Errorf("duplicate export format %q", f)
		}
		seen[f] = true
		if !currentSet[f] {
			newFormats = append(newFormats, f)
		}
	}
	return newFormats, nil
}

// validateFormatsNotAlreadyPublished checks that none of the new formats are already present
// in the CatalogItemVersion. Called during PUT/PATCH when adding additional export formats.
// Returns validation error and internal error.
func (s *imagePromotionService) validateFormatsNotAlreadyPublished(ctx context.Context, orgId uuid.UUID, promotion *domain.ImagePromotion, newFormats []domain.ExportFormatType) (error, error) {
	reason := getPromotionReadyReason(promotion)

	initialPublishDone := reason == string(domain.ImagePromotionConditionReasonCompleted) ||
		reason == string(domain.ImagePromotionConditionReasonAmendmentFailed)

	if !initialPublishDone {
		return nil, nil
	}

	discriminator, err := promotion.Spec.Target.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine target type: %w", err)
	}

	var catalogName string
	var itemName string
	var version string

	switch discriminator {
	case string(domain.ImagePromotionTargetTypeNewCatalogItem):
		target, err := promotion.Spec.Target.AsNewCatalogItemTarget()
		if err != nil {
			return nil, fmt.Errorf("corrupt NewCatalogItem target: %w", err)
		}
		catalogName, itemName, version = target.CatalogName, target.CatalogItemName, target.Version
	case string(domain.ImagePromotionTargetTypeExistingCatalogItem):
		target, err := promotion.Spec.Target.AsExistingCatalogItemTarget()
		if err != nil {
			return nil, fmt.Errorf("corrupt ExistingCatalogItem target: %w", err)
		}
		catalogName, itemName, version = target.CatalogName, target.CatalogItemName, target.Version
	default:
		return nil, fmt.Errorf("unknown target type: %s", discriminator)
	}

	item, status := s.catalogs.GetCatalogItem(ctx, orgId, catalogName, itemName)
	if err := statusToErr(status); err != nil {
		return nil, fmt.Errorf("failed to get CatalogItem: %w", err)
	}
	for _, v := range item.Spec.Versions {
		if v.Version != version {
			continue
		}
		for _, format := range newFormats {
			artifactType, err := exportFormatToCatalogItemArtifactType(format)
			if err != nil {
				return fmt.Errorf("unsupported export format %q", format), nil
			}
			if _, exists := v.References[string(artifactType)]; exists {
				return fmt.Errorf("format %q is already published in CatalogItemVersion %s", format, version), nil
			}
		}
	}
	return nil, nil
}

// validate checks metadata and the imageBuildRef reference for an ImagePromotion.
// Target catalog/item existence is NOT checked here; call validateCreate for that.
// Returns validation errors (bad request) and an internal error separately.
// The returned *domain.ImageBuild is provided for callers that need the loaded build object.
func (s *imagePromotionService) validate(ctx context.Context, orgId uuid.UUID, promotion *domain.ImagePromotion) (*domain.ImageBuild, []error, error) {
	var errs []error

	errs = append(errs, validation.ValidateResourceName(promotion.Metadata.Name)...)
	errs = append(errs, validation.ValidateLabels(promotion.Metadata.Labels)...)
	errs = append(errs, validation.ValidateAnnotations(promotion.Metadata.Annotations)...)

	imageBuildRef := promotion.Spec.Source.ImageBuildRef
	var imageBuild *domain.ImageBuild
	var err error
	if imageBuildRef == "" {
		errs = append(errs, errors.New("spec.source.imageBuildRef is required"))
	} else {
		imageBuild, err = s.imageBuildStore.Get(ctx, orgId, imageBuildRef)
		if isNotFound(err) {
			errs = append(errs, fmt.Errorf("spec.source.imageBuildRef: ImageBuild %q not found", imageBuildRef))
		} else if err != nil {
			return nil, nil, fmt.Errorf("failed to get ImageBuild %q: %w", imageBuildRef, err)
		} else if isBuildTerminalFailure(imageBuild) {
			reason := "Unknown"
			if imageBuild != nil && imageBuild.Status != nil && imageBuild.Status.Conditions != nil {
				cond := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
				if cond != nil {
					reason = cond.Reason
				}
			}
			errs = append(errs, fmt.Errorf("ImageBuild %s is in terminal state %s; create a new build", imageBuildRef, reason))
		}
	}

	if promotion.Spec.Source.ExportFormats != nil {
		formats := *promotion.Spec.Source.ExportFormats
		if len(formats) == 0 {
			errs = append(errs, errors.New("spec.source.exportFormats must not be empty if specified"))
		} else {
			seen := make(map[domain.ExportFormatType]bool)
			for _, format := range formats {
				if _, fmtErr := exportFormatToCatalogItemArtifactType(format); fmtErr != nil {
					errs = append(errs, fmt.Errorf("spec.source.exportFormats: unsupported format %q", format))
				}
				if seen[format] {
					errs = append(errs, fmt.Errorf("spec.source.exportFormats: duplicate format %q", format))
				}
				seen[format] = true
			}
		}
	}

	return imageBuild, errs, nil
}

// validateCreate runs the full validation for a new ImagePromotion, including target catalog/item
// existence checks that are only meaningful at creation time.
func (s *imagePromotionService) validateCreate(ctx context.Context, orgId uuid.UUID, promotion *domain.ImagePromotion) ([]error, error) {
	_, errs, internalErr := s.validate(ctx, orgId, promotion)
	if internalErr != nil {
		return nil, internalErr
	}

	discriminator, err := promotion.Spec.Target.Discriminator()
	if err != nil {
		errs = append(errs, fmt.Errorf("spec.target: failed to determine target type: %w", err))
		return errs, nil
	}

	switch discriminator {
	case string(domain.ImagePromotionTargetTypeNewCatalogItem):
		target, err := promotion.Spec.Target.AsNewCatalogItemTarget()
		if err != nil {
			errs = append(errs, fmt.Errorf("spec.target: invalid NewCatalogItem target: %w", err))
			return errs, nil
		}
		if target.CatalogName == "" || target.CatalogItemName == "" || target.Version == "" {
			errs = append(errs, errors.New("catalogName, catalogItemName, and version are required"))
		} else {
			// Catalog must exist.
			if _, status := s.catalogs.GetCatalog(ctx, orgId, target.CatalogName); isNotFound(statusToErr(status)) {
				errs = append(errs, fmt.Errorf("Catalog %s not found", target.CatalogName))
			} else if err := statusToErr(status); err != nil {
				return nil, err
			} else {
				// CatalogItem must NOT exist.
				_, status = s.catalogs.GetCatalogItem(ctx, orgId, target.CatalogName, target.CatalogItemName)
				err := statusToErr(status)
				if err == nil {
					errs = append(errs, fmt.Errorf("CatalogItem %s already exists in catalog %s; use type ExistingCatalogItem to add a new version", target.CatalogItemName, target.CatalogName))
				} else if !isNotFound(err) {
					return nil, err
				}
			}
		}

	case string(domain.ImagePromotionTargetTypeExistingCatalogItem):
		target, err := promotion.Spec.Target.AsExistingCatalogItemTarget()
		if err != nil {
			errs = append(errs, fmt.Errorf("spec.target: invalid ExistingCatalogItem target: %w", err))
			return errs, nil
		}
		if target.CatalogName == "" || target.CatalogItemName == "" || target.Version == "" {
			errs = append(errs, errors.New("catalogName, catalogItemName, and version are required"))
		} else {
			// Catalog must exist.
			if _, status := s.catalogs.GetCatalog(ctx, orgId, target.CatalogName); isNotFound(statusToErr(status)) {
				errs = append(errs, fmt.Errorf("Catalog %s not found", target.CatalogName))
			} else if err := statusToErr(status); err != nil {
				return nil, err
			} else {
				// CatalogItem must exist.
				existingItem, status := s.catalogs.GetCatalogItem(ctx, orgId, target.CatalogName, target.CatalogItemName)
				err := statusToErr(status)
				if isNotFound(err) {
					errs = append(errs, fmt.Errorf("CatalogItem %s not found in catalog %s", target.CatalogItemName, target.CatalogName))
				} else if err != nil {
					return nil, err
				} else {
					// Version must NOT already exist in the item.
					for _, v := range existingItem.Spec.Versions {
						if v.Version == target.Version {
							errs = append(errs, fmt.Errorf("version %s already exists in CatalogItem %s", target.Version, target.CatalogItemName))
							break
						}
					}
				}
			}
		}

	default:
		errs = append(errs, fmt.Errorf("unknown target type: %s", discriminator))
	}

	return errs, nil
}

// ---- condition helpers ----

func getPromotionReadyReason(promotion *domain.ImagePromotion) string {
	if promotion == nil || promotion.Status == nil || promotion.Status.Conditions == nil {
		return ""
	}
	cond := domain.FindImagePromotionStatusCondition(*promotion.Status.Conditions, domain.ImagePromotionConditionTypeReady)
	if cond == nil {
		return ""
	}
	return cond.Reason
}

func setPromotionReadyCondition(promotion *domain.ImagePromotion, reason domain.ImagePromotionConditionReason, message string) {
	if promotion.Status == nil {
		promotion.Status = &domain.ImagePromotionStatus{}
	}
	if promotion.Status.Conditions == nil {
		promotion.Status.Conditions = &[]domain.ImagePromotionCondition{}
	}
	condStatus := coredomain.ConditionStatusFalse
	if reason == domain.ImagePromotionConditionReasonCompleted {
		condStatus = coredomain.ConditionStatusTrue
	}
	domain.SetImagePromotionStatusCondition(promotion.Status.Conditions, domain.ImagePromotionCondition{
		Type:               domain.ImagePromotionConditionTypeReady,
		Status:             condStatus,
		Reason:             string(reason),
		Message:            message,
		LastTransitionTime: time.Now().UTC(),
	})
}

func conditionMessageForReason(reason domain.ImagePromotionConditionReason) string {
	switch reason {
	case domain.ImagePromotionConditionReasonWaitingForArtifacts:
		return "Waiting for all required artifacts to become available"
	case domain.ImagePromotionConditionReasonPublishing:
		return "Publishing artifacts to catalog"
	case domain.ImagePromotionConditionReasonCompleted:
		return "Successfully published to catalog"
	case domain.ImagePromotionConditionReasonFailed:
		return "Promotion failed"
	case domain.ImagePromotionConditionReasonBuildFailed:
		return "ImageBuild failed; no image reference will be produced"
	case domain.ImagePromotionConditionReasonBuildCanceled:
		return "ImageBuild was canceled; no image reference will be produced"
	case domain.ImagePromotionConditionReasonAmendmentFailed:
		return "Amendment failed; initial promotion is intact but additional format could not be added"
	default:
		return string(reason)
	}
}

// ---- small validation helpers ----

func exportFormatToCatalogItemArtifactType(format domain.ExportFormatType) (coredomain.CatalogItemArtifactType, error) {
	switch format {
	case domain.ExportFormatTypeQCOW2:
		return coredomain.CatalogItemArtifactTypeQcow2, nil
	case domain.ExportFormatTypeISO:
		return coredomain.CatalogItemArtifactTypeIso, nil
	case domain.ExportFormatTypeVMDK:
		return coredomain.CatalogItemArtifactTypeVmdk, nil
	case domain.ExportFormatTypeQCOW2DiskContainer:
		return coredomain.CatalogItemArtifactTypeQcow2DiskContainer, nil
	default:
		return "", fmt.Errorf("unsupported export format: %s", format)
	}
}

func isBuildTerminalFailure(build *domain.ImageBuild) bool {
	if build == nil || build.Status == nil || build.Status.Conditions == nil {
		return false
	}
	cond := domain.FindImageBuildStatusCondition(*build.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if cond == nil {
		return false
	}
	return cond.Reason == string(domain.ImageBuildConditionReasonFailed) || cond.Reason == string(domain.ImageBuildConditionReasonCanceled)
}

func isNotFound(err error) bool {
	return errors.Is(err, flterrors.ErrResourceNotFound) || errors.Is(err, flterrors.ErrParentResourceNotFound)
}
