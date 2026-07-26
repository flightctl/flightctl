package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// errTransientExportLookup is wrapped into errors produced by resolveArtifactReferences
// and computeArtifactsStatus when the underlying export-store lookup fails transiently.
// Call sites use errors.Is to distinguish retryable lookup failures from permanent data errors.
var errTransientExportLookup = errors.New("transient export lookup")

// processImagePromotion handles an ImagePromotion ResourceCreated or ResourceUpdated event.
// It loads the promotion and, if it is in a pending state, evaluates it for readiness.
func (c *Consumer) processImagePromotion(ctx context.Context, eventWithOrgId worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	promotionName := eventWithOrgId.Event.InvolvedObject.Name
	orgID := eventWithOrgId.OrgId

	log = log.WithField("imagePromotion", promotionName).WithField("orgId", orgID)
	log.Info("Evaluating ImagePromotion")

	promotion, status := c.imageBuilderService.ImagePromotion().Get(ctx, orgID, promotionName)
	if !imagebuilderapi.IsStatusOK(status) {
		if status.Code == 404 {
			log.Debug("ImagePromotion not found, possibly deleted; skipping")
			return nil
		}
		return fmt.Errorf("failed to get ImagePromotion %q: %s", promotionName, status.Message)
	}

	reason := getPromotionReadyReasonWorker(promotion)
	if reason != string(domain.ImagePromotionConditionReasonWaitingForArtifacts) &&
		reason != string(domain.ImagePromotionConditionReasonAmendmentFailed) &&
		reason != string(domain.ImagePromotionConditionReasonPublishing) {
		log.Debugf("promotion is in state %q, skipping evaluation", reason)
		return nil
	}

	imageBuild, buildStatus := c.imageBuilderService.ImageBuild().Get(ctx, orgID, promotion.Spec.Source.ImageBuildRef, false)
	if !imagebuilderapi.IsStatusOK(buildStatus) {
		if buildStatus.Code == 404 {
			log.WithField("imageBuildRef", promotion.Spec.Source.ImageBuildRef).
				Warn("ImageBuild not found for promotion evaluation; skipping")
			return nil
		}
		return fmt.Errorf("failed to get ImageBuild %q for promotion evaluation: %s", promotion.Spec.Source.ImageBuildRef, buildStatus.Message)
	}

	return c.evaluateAndTransition(ctx, orgID, promotion, imageBuild)
}

// enqueuePromotionsForBuild lists all pending promotions for the given ImageBuild and enqueues
// a ResourceUpdated event for each, so processImagePromotion is the sole handler of evaluation logic.
func (c *Consumer) enqueuePromotionsForBuild(ctx context.Context, orgID uuid.UUID, imageBuildRef string, log logrus.FieldLogger) error {
	promotions, err := c.imageBuilderService.ImagePromotion().ListPendingForBuild(ctx, orgID, imageBuildRef)
	if err != nil {
		return fmt.Errorf("failed to list pending promotions for build %s: %w", imageBuildRef, err)
	}
	if len(promotions) == 0 {
		return nil
	}

	var errs []error
	for i := range promotions {
		promotionName := lo.FromPtr(promotions[i].Metadata.Name)
		if promotionName == "" {
			log.Warn("ImagePromotion has empty name, skipping")
			continue
		}
		event := coredomain.GetBaseEvent(
			ctx,
			coredomain.ResourceKind(string(domain.ResourceKindImagePromotion)),
			promotionName,
			coredomain.EventReasonResourceUpdated,
			fmt.Sprintf("%s is pending re-evaluation.", string(domain.ResourceKindImagePromotion)),
			nil,
		)
		if err := c.enqueueEvent(ctx, orgID, event, log); err != nil {
			errs = append(errs, fmt.Errorf("failed to enqueue ImagePromotion %q: %w", promotionName, err))
			continue
		}
		log.WithField("imagePromotion", promotionName).Info("Enqueued ImagePromotion for evaluation")
	}
	return errors.Join(errs...)
}

// failPromotionsForBuild transitions all pending promotions for a build to Failed.
// Called when the ImageBuild itself fails or is canceled.
func (c *Consumer) failPromotionsForBuild(ctx context.Context, orgID uuid.UUID, imageBuildRef string, reason domain.ImagePromotionConditionReason, message string) error {
	promotions, err := c.imageBuilderService.ImagePromotion().ListPendingForBuild(ctx, orgID, imageBuildRef)
	if err != nil {
		return fmt.Errorf("failed to list pending promotions for build %s: %w", imageBuildRef, err)
	}
	var errs []error
	for i := range promotions {
		if err := c.transitionToFailed(ctx, orgID, &promotions[i], reason, message); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// evaluateAndTransition checks if a pending promotion can transition to Publishing.
// Returns an error on transient failures so callers can requeue.
func (c *Consumer) evaluateAndTransition(ctx context.Context, orgID uuid.UUID, promotion *domain.ImagePromotion, imageBuild *domain.ImageBuild) error {
	currentReason := getPromotionReadyReasonWorker(promotion)

	if currentReason == string(domain.ImagePromotionConditionReasonAmendmentFailed) {
		return c.evaluateAmendmentFailed(ctx, orgID, promotion, imageBuild)
	}

	allReady, _, err := c.computeArtifactsStatus(ctx, orgID, promotion, imageBuild)
	if err != nil {
		c.log.WithError(err).WithField("promotion", lo.FromPtr(promotion.Metadata.Name)).
			Warn("failed to compute promotion artifact status")
		return fmt.Errorf("failed to compute artifact status for promotion %q: %w", lo.FromPtr(promotion.Metadata.Name), err)
	}

	if !allReady {
		_, err := c.imageBuilderService.ImagePromotion().UpdateStatus(ctx, orgID, promotion)
		if err != nil {
			c.log.WithError(err).WithField("promotion", lo.FromPtr(promotion.Metadata.Name)).
				Warn("failed to persist artifact status update")
			return fmt.Errorf("failed to update status for promotion %q: %w", lo.FromPtr(promotion.Metadata.Name), err)
		}
		return nil
	}

	return c.transitionToPublishing(ctx, orgID, promotion, imageBuild)
}

// evaluateAmendmentFailed re-evaluates an AmendmentFailed promotion for newly-ready export formats.
func (c *Consumer) evaluateAmendmentFailed(ctx context.Context, orgID uuid.UUID, promotion *domain.ImagePromotion, imageBuild *domain.ImageBuild) error {
	var readyFormats []domain.ExportFormatType
	if promotion.Status != nil && promotion.Status.ArtifactStatuses != nil {
		for _, as := range *promotion.Status.ArtifactStatuses {
			if as.Published || as.Format == "container" {
				continue
			}
			format := domain.ExportFormatType(as.Format)
			export, err := c.resolveLatestCompletedExport(ctx, orgID, promotion.Spec.Source.ImageBuildRef, format)
			if err != nil {
				c.log.WithError(err).WithField("promotion", lo.FromPtr(promotion.Metadata.Name)).
					Warn("failed to check export completion for amendment re-evaluation")
				return fmt.Errorf("failed to check export completion for promotion %q: %w", lo.FromPtr(promotion.Metadata.Name), err)
			}
			if export != nil {
				readyFormats = append(readyFormats, format)
			}
		}
	}

	if len(readyFormats) == 0 {
		return nil
	}

	return c.executeAmendmentPublish(ctx, orgID, promotion, imageBuild, readyFormats)
}

// transitionToPublishing sets the Publishing condition, performs catalog writes, and
// transitions to Completed or Failed.
func (c *Consumer) transitionToPublishing(ctx context.Context, orgID uuid.UUID, promotion *domain.ImagePromotion, imageBuild *domain.ImageBuild) error {
	now := time.Now().UTC()
	if promotion.Status == nil {
		promotion.Status = &domain.ImagePromotionStatus{}
	}
	if promotion.Status.Conditions == nil {
		promotion.Status.Conditions = &[]domain.ImagePromotionCondition{}
	}
	domain.SetImagePromotionStatusCondition(promotion.Status.Conditions, domain.ImagePromotionCondition{
		Type:               domain.ImagePromotionConditionTypeReady,
		Status:             coredomain.ConditionStatusFalse,
		Reason:             string(domain.ImagePromotionConditionReasonPublishing),
		Message:            conditionMessageForReasonWorker(domain.ImagePromotionConditionReasonPublishing),
		LastTransitionTime: now,
	})

	promotionName := lo.FromPtr(promotion.Metadata.Name)
	promotion, err := c.imageBuilderService.ImagePromotion().UpdateStatus(ctx, orgID, promotion)
	if err != nil {
		c.log.WithError(err).WithField("promotion", promotionName).
			Error("failed to update promotion status to Publishing")
		return fmt.Errorf("failed to update promotion %q status to Publishing: %w", promotionName, err)
	}

	references, resolvedExports, err := c.resolveArtifactReferences(ctx, orgID, promotion, imageBuild)
	if err != nil {
		if errors.Is(err, errTransientExportLookup) {
			return err
		}
		return c.transitionToFailed(ctx, orgID, promotion, domain.ImagePromotionConditionReasonFailed,
			fmt.Sprintf("failed to resolve artifact references: %v", err))
	}

	var baseURI string
	if imageBuild.Status != nil {
		baseURI = extractBaseURIWorker(imageBuild.Status.ImageReference)
	}

	discriminator, _ := promotion.Spec.Target.Discriminator()
	var publishErr error

	switch discriminator {
	case string(domain.ImagePromotionTargetTypeNewCatalogItem):
		target, err := promotion.Spec.Target.AsNewCatalogItemTarget()
		if err != nil {
			return c.transitionToFailed(ctx, orgID, promotion, domain.ImagePromotionConditionReasonFailed, "invalid target: "+err.Error())
		}
		publishErr = c.createNewCatalogItem(ctx, orgID, target, references, baseURI)
	case string(domain.ImagePromotionTargetTypeExistingCatalogItem):
		target, err := promotion.Spec.Target.AsExistingCatalogItemTarget()
		if err != nil {
			return c.transitionToFailed(ctx, orgID, promotion, domain.ImagePromotionConditionReasonFailed, "invalid target: "+err.Error())
		}
		publishErr = c.appendVersionToCatalogItem(ctx, orgID, target, references, baseURI)
	default:
		publishErr = fmt.Errorf("unknown target type: %s", discriminator)
	}

	if publishErr != nil {
		return c.transitionToFailed(ctx, orgID, promotion, domain.ImagePromotionConditionReasonFailed, publishErr.Error())
	}

	return c.transitionToCompleted(ctx, orgID, promotion, resolvedExports)
}

// executeAmendmentPublish patches an existing CatalogItemVersion with new artifact refs.
func (c *Consumer) executeAmendmentPublish(ctx context.Context, orgID uuid.UUID, promotion *domain.ImagePromotion, imageBuild *domain.ImageBuild, newFormats []domain.ExportFormatType) error {
	newReferences := make(map[string]string)
	resolvedExports := make(map[string]string)

	for _, format := range newFormats {
		export, err := c.resolveLatestCompletedExport(ctx, orgID, promotion.Spec.Source.ImageBuildRef, format)
		if err != nil {
			return err
		}
		if export == nil {
			return c.transitionToAmendmentFailed(ctx, orgID, promotion, fmt.Sprintf("failed to resolve export for image build %s format %s", promotion.Spec.Source.ImageBuildRef, format))
		}
		artifactType, err := exportFormatToCatalogItemArtifactTypeWorker(format)
		if err != nil {
			return c.transitionToAmendmentFailed(ctx, orgID, promotion, fmt.Sprintf("unsupported export format %s", format))
		}
		ref := resolveExportRefWorker(export)
		if ref == "" {
			return c.transitionToAmendmentFailed(ctx, orgID, promotion, fmt.Sprintf("ImageExport for format %s has no manifest digest", format))
		}
		newReferences[string(artifactType)] = ref
		if export.Metadata.Name != nil {
			resolvedExports[string(format)] = *export.Metadata.Name
		}
	}

	var baseURI string
	if imageBuild.Status != nil {
		baseURI = extractBaseURIWorker(imageBuild.Status.ImageReference)
	}

	discriminator, _ := promotion.Spec.Target.Discriminator()

	var catalogName, itemName, version string
	switch discriminator {
	case string(domain.ImagePromotionTargetTypeNewCatalogItem):
		target, err := promotion.Spec.Target.AsNewCatalogItemTarget()
		if err != nil {
			return c.transitionToAmendmentFailed(ctx, orgID, promotion, "invalid target: "+err.Error())
		}
		catalogName, itemName, version = target.CatalogName, target.CatalogItemName, target.Version
	case string(domain.ImagePromotionTargetTypeExistingCatalogItem):
		target, err := promotion.Spec.Target.AsExistingCatalogItemTarget()
		if err != nil {
			return c.transitionToAmendmentFailed(ctx, orgID, promotion, "invalid target: "+err.Error())
		}
		catalogName, itemName, version = target.CatalogName, target.CatalogItemName, target.Version
	default:
		return c.transitionToAmendmentFailed(ctx, orgID, promotion, fmt.Sprintf("unknown target type: %s", discriminator))
	}

	if err := c.patchCatalogItemVersion(ctx, orgID, catalogName, itemName, version, newReferences, baseURI); err != nil {
		return c.transitionToAmendmentFailed(ctx, orgID, promotion, err.Error())
	}

	return c.transitionToAmendmentCompleted(ctx, orgID, promotion, resolvedExports)
}

// patchCatalogItemVersion adds new artifact references to an existing CatalogItemVersion.
func (c *Consumer) patchCatalogItemVersion(ctx context.Context, orgID uuid.UUID, catalogName, itemName, version string, newRefs map[string]string, baseURI string) error {
	existing, err := c.catalogStore.GetItem(ctx, orgID, catalogName, itemName)
	if err != nil {
		return fmt.Errorf("failed to get CatalogItem %s: %w", itemName, err)
	}

	for artifactType := range newRefs {
		found := false
		for _, a := range existing.Spec.Artifacts {
			if string(a.Type) == artifactType {
				found = true
				break
			}
		}
		if !found {
			existing.Spec.Artifacts = append(existing.Spec.Artifacts, coredomain.CatalogItemArtifact{
				Type: coredomain.CatalogItemArtifactType(artifactType),
				Uri:  baseURI,
			})
		}
	}

	found := false
	for i := range existing.Spec.Versions {
		v := &existing.Spec.Versions[i]
		if v.Version != version {
			continue
		}
		found = true
		if v.References == nil {
			v.References = make(map[string]string)
		}
		qualifiedRefs := qualifyReferencesForURIMismatchWorker(newRefs, existing.Spec.Artifacts, baseURI)
		for artifactType, ref := range qualifiedRefs {
			if _, exists := v.References[artifactType]; exists {
				return fmt.Errorf("artifact type %q is already present in CatalogItemVersion %s", artifactType, version)
			}
			v.References[artifactType] = ref
		}
		break
	}
	if !found {
		return fmt.Errorf("target CatalogItem version %q not found", version)
	}

	_, _, err = c.catalogStore.CreateOrUpdateItem(ctx, orgID, catalogName, existing)
	if err != nil {
		return fmt.Errorf("failed to patch CatalogItem: %w", err)
	}
	return nil
}

// createNewCatalogItem creates a brand new CatalogItem via the core service layer.
func (c *Consumer) createNewCatalogItem(ctx context.Context, orgID uuid.UUID, target domain.NewCatalogItemTarget, references map[string]string, baseURI string) error {
	artifacts := referencesToArtifactsWorker(references, baseURI)
	version := coredomain.CatalogItemVersion{
		Version:    target.Version,
		Channels:   []string{"testing"},
		References: references,
		Readme:     target.Readme,
	}
	systemCategory := coredomain.CatalogItemCategorySystem
	itemSpec := coredomain.CatalogItemSpec{
		Type:      coredomain.CatalogItemTypeOS,
		Category:  &systemCategory,
		Artifacts: artifacts,
		Versions:  []coredomain.CatalogItemVersion{version},
	}
	if target.DisplayName != nil {
		itemSpec.DisplayName = target.DisplayName
	}
	item := coredomain.CatalogItem{
		Metadata: coredomain.CatalogItemMeta{
			Name: lo.ToPtr(target.CatalogItemName),
		},
		Spec: itemSpec,
	}

	_, err := c.catalogStore.CreateItem(ctx, orgID, target.CatalogName, &item)
	if err != nil {
		if errors.Is(err, flterrors.ErrDuplicateName) {
			// The item already exists. Check whether our specific version+references are
			// already present, which means a previous attempt wrote it before crashing.
			// References are deterministic (derived from immutable build/export digests),
			// so a match unambiguously identifies our own previous write.
			existing, getErr := c.catalogStore.GetItem(ctx, orgID, target.CatalogName, target.CatalogItemName)
			if getErr == nil {
				for _, v := range existing.Spec.Versions {
					if v.Version == target.Version && referencesEqualWorker(v.References, references) {
						c.log.WithField("catalogItem", target.CatalogItemName).
							Info("CatalogItem already contains our version with matching references; idempotent success on retry")
						return nil
					}
				}
			}
			return fmt.Errorf("CatalogItem %s already exists with different content: %w", target.CatalogItemName, err)
		}
		return fmt.Errorf("failed to create CatalogItem: %w", err)
	}
	return nil
}

// appendVersionToCatalogItem appends a version entry to an existing CatalogItem.
func (c *Consumer) appendVersionToCatalogItem(ctx context.Context, orgID uuid.UUID, target domain.ExistingCatalogItemTarget, references map[string]string, baseURI string) error {
	existing, err := c.catalogStore.GetItem(ctx, orgID, target.CatalogName, target.CatalogItemName)
	if err != nil {
		return fmt.Errorf("failed to get CatalogItem %s: %w", target.CatalogItemName, err)
	}

	// Check whether the target version already exists. If so, verify that the stored
	// references match what we would write: a match means a previous attempt wrote them
	// before crashing (references are deterministic from immutable build/export digests).
	for _, v := range existing.Spec.Versions {
		if v.Version == target.Version {
			// Use the current artifact list (which our previous write already updated) to
			// compute what the qualified refs would be, then compare with what is stored.
			existingQualifiedRefs := qualifyReferencesForURIMismatchWorker(references, existing.Spec.Artifacts, baseURI)
			if referencesEqualWorker(v.References, existingQualifiedRefs) {
				c.log.WithField("catalogItem", target.CatalogItemName).WithField("version", target.Version).
					Info("version already present with matching references; idempotent success on retry")
				return nil
			}
			return fmt.Errorf("version %s already exists in CatalogItem %s with different references", target.Version, target.CatalogItemName)
		}
	}

	existingArtifacts := existing.Spec.Artifacts
	newArtifactTypes := referencesToArtifactsWorker(references, baseURI)
	for _, na := range newArtifactTypes {
		found := false
		for _, ea := range existingArtifacts {
			if ea.Type == na.Type {
				found = true
				break
			}
		}
		if !found {
			existingArtifacts = append(existingArtifacts, na)
		}
	}
	existing.Spec.Artifacts = existingArtifacts

	versionRefs := qualifyReferencesForURIMismatchWorker(references, existing.Spec.Artifacts, baseURI)

	newVersion := coredomain.CatalogItemVersion{
		Version:    target.Version,
		Channels:   []string{"testing"},
		References: versionRefs,
	}
	if target.Replaces != nil {
		newVersion.Replaces = target.Replaces
	}
	if target.Skips != nil {
		newVersion.Skips = target.Skips
	}
	if target.SkipRange != nil {
		newVersion.SkipRange = target.SkipRange
	}
	if target.Readme != nil {
		newVersion.Readme = target.Readme
	}
	existing.Spec.Versions = append(existing.Spec.Versions, newVersion)

	_, _, err = c.catalogStore.CreateOrUpdateItem(ctx, orgID, target.CatalogName, existing)
	if err != nil {
		return fmt.Errorf("failed to update CatalogItem: %w", err)
	}
	return nil
}

// resolveArtifactReferences builds the references map and resolvedExport names per format.
func (c *Consumer) resolveArtifactReferences(ctx context.Context, orgID uuid.UUID, promotion *domain.ImagePromotion, imageBuild *domain.ImageBuild) (map[string]string, map[string]string, error) {
	references := make(map[string]string)
	resolvedExports := make(map[string]string)

	containerRef := ""
	if imageBuild != nil {
		containerRef = imageBuild.Spec.Destination.ImageTag
	}
	if containerRef == "" {
		return nil, nil, fmt.Errorf("ImageBuild %s has no image reference", promotion.Spec.Source.ImageBuildRef)
	}
	references[string(coredomain.CatalogItemArtifactTypeContainer)] = containerRef

	if promotion.Spec.Source.ExportFormats != nil {
		for _, format := range *promotion.Spec.Source.ExportFormats {
			export, err := c.resolveLatestCompletedExport(ctx, orgID, promotion.Spec.Source.ImageBuildRef, format)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: failed to resolve export for format %s: %w", errTransientExportLookup, format, err)
			}
			if export == nil {
				return nil, nil, fmt.Errorf("no completed ImageExport found for ImageBuild %s and format %s", promotion.Spec.Source.ImageBuildRef, format)
			}
			artifactType, err := exportFormatToCatalogItemArtifactTypeWorker(format)
			if err != nil {
				return nil, nil, err
			}
			if export.Metadata.Name == nil {
				return nil, nil, fmt.Errorf("ImageExport for format %s has no name", format)
			}
			ref := resolveExportRefWorker(export)
			if ref == "" {
				return nil, nil, fmt.Errorf("ImageExport %s for format %s has no manifest digest", *export.Metadata.Name, format)
			}
			references[string(artifactType)] = ref
			resolvedExports[string(format)] = *export.Metadata.Name
		}
	}

	return references, resolvedExports, nil
}

// computeArtifactsStatus rebuilds the promotion's artifact statuses from the current imageBuild
// and export states, preserving already-published entries. Mutates current.Status.ArtifactStatuses.
//
// Returns:
//   - allUnpublishedReady: true when all unpublished artifacts have completed artifacts.
//   - unpublishedFormats: export formats not yet published.
func (c *Consumer) computeArtifactsStatus(ctx context.Context, orgID uuid.UUID, current *domain.ImagePromotion, imageBuild *domain.ImageBuild) (allUnpublishedReady bool, unpublishedFormats []domain.ExportFormatType, err error) {
	if current.Status == nil {
		current.Status = &domain.ImagePromotionStatus{}
	}

	published := make(map[string]domain.ArtifactPromotionStatus)
	for _, as := range lo.FromPtrOr(current.Status.ArtifactStatuses, nil) {
		if as.Published {
			published[as.Format] = as
		}
	}

	var containerEntry domain.ArtifactPromotionStatus
	if prev, ok := published["container"]; ok {
		containerEntry = prev
		allUnpublishedReady = true
	} else {
		containerReady := false
		if imageBuild != nil && imageBuild.Status != nil && imageBuild.Status.Conditions != nil {
			cond := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
			if cond != nil {
				containerReady = cond.Status == coredomain.ConditionStatusTrue && cond.Reason == string(domain.ImageBuildConditionReasonCompleted)
			}
		}
		containerEntry = domain.ArtifactPromotionStatus{Format: "container", Ready: containerReady}
		allUnpublishedReady = containerReady
	}
	statuses := []domain.ArtifactPromotionStatus{containerEntry}

	for _, format := range lo.FromPtrOr(current.Spec.Source.ExportFormats, nil) {
		if prev, ok := published[string(format)]; ok {
			statuses = append(statuses, prev)
			continue
		}
		export, checkErr := c.resolveLatestCompletedExport(ctx, orgID, current.Spec.Source.ImageBuildRef, format)
		if checkErr != nil {
			return false, nil, fmt.Errorf("%w: failed to check export readiness for format %s: %w", errTransientExportLookup, format, checkErr)
		}
		ready := export != nil
		entry := domain.ArtifactPromotionStatus{Format: string(format), Ready: ready}
		if ready {
			entry.ResolvedExport = export.Metadata.Name
		} else {
			allUnpublishedReady = false
		}
		statuses = append(statuses, entry)
		unpublishedFormats = append(unpublishedFormats, format)
	}
	current.Status.ArtifactStatuses = &statuses
	return allUnpublishedReady, unpublishedFormats, nil
}

// resolveLatestCompletedExport returns the most recently completed ImageExport for the given format.
func (c *Consumer) resolveLatestCompletedExport(ctx context.Context, orgID uuid.UUID, imageBuildRef string, format domain.ExportFormatType) (*domain.ImageExport, error) {
	return c.imageBuilderService.ImageExport().ListCompletedForBuild(ctx, orgID, imageBuildRef, format)
}

// ---- state transition helpers ----

func (c *Consumer) transitionToCompleted(ctx context.Context, orgID uuid.UUID, promotion *domain.ImagePromotion, resolvedExports map[string]string) error {
	now := time.Now().UTC()
	if promotion.Status == nil {
		promotion.Status = &domain.ImagePromotionStatus{}
	}
	if promotion.Status.Conditions == nil {
		promotion.Status.Conditions = &[]domain.ImagePromotionCondition{}
	}
	domain.SetImagePromotionStatusCondition(promotion.Status.Conditions, domain.ImagePromotionCondition{
		Type:               domain.ImagePromotionConditionTypeReady,
		Status:             coredomain.ConditionStatusTrue,
		Reason:             string(domain.ImagePromotionConditionReasonCompleted),
		Message:            conditionMessageForReasonWorker(domain.ImagePromotionConditionReasonCompleted),
		LastTransitionTime: now,
	})
	promotion.Status.PublishedAt = lo.ToPtr(now)

	if promotion.Status.ArtifactStatuses != nil {
		for i := range *promotion.Status.ArtifactStatuses {
			entry := &(*promotion.Status.ArtifactStatuses)[i]
			entry.Ready = true
			entry.Published = true
			if exportName, ok := resolvedExports[entry.Format]; ok {
				entry.ResolvedExport = lo.ToPtr(exportName)
			}
		}
	}

	if _, err := c.imageBuilderService.ImagePromotion().UpdateStatus(ctx, orgID, promotion); err != nil {
		c.log.WithError(err).WithField("promotion", lo.FromPtr(promotion.Metadata.Name)).
			Error("failed to update promotion status to Completed")
		return fmt.Errorf("failed to update promotion %q status to Completed: %w", lo.FromPtr(promotion.Metadata.Name), err)
	}
	return nil
}

func (c *Consumer) transitionToFailed(ctx context.Context, orgID uuid.UUID, promotion *domain.ImagePromotion, reason domain.ImagePromotionConditionReason, message string) error {
	now := time.Now().UTC()
	if promotion.Status == nil {
		promotion.Status = &domain.ImagePromotionStatus{}
	}
	if promotion.Status.Conditions == nil {
		promotion.Status.Conditions = &[]domain.ImagePromotionCondition{}
	}
	domain.SetImagePromotionStatusCondition(promotion.Status.Conditions, domain.ImagePromotionCondition{
		Type:               domain.ImagePromotionConditionTypeReady,
		Status:             coredomain.ConditionStatusFalse,
		Reason:             string(reason),
		Message:            message,
		LastTransitionTime: now,
	})
	if _, err := c.imageBuilderService.ImagePromotion().UpdateStatus(ctx, orgID, promotion); err != nil {
		c.log.WithError(err).WithField("promotion", lo.FromPtr(promotion.Metadata.Name)).
			Error("failed to update promotion status to Failed")
		return fmt.Errorf("failed to update promotion %q status to Failed: %w", lo.FromPtr(promotion.Metadata.Name), err)
	}
	return nil
}

func (c *Consumer) transitionToAmendmentCompleted(ctx context.Context, orgID uuid.UUID, promotion *domain.ImagePromotion, resolvedExports map[string]string) error {
	now := time.Now().UTC()
	if promotion.Status == nil {
		promotion.Status = &domain.ImagePromotionStatus{}
	}
	if promotion.Status.Conditions == nil {
		promotion.Status.Conditions = &[]domain.ImagePromotionCondition{}
	}
	domain.SetImagePromotionStatusCondition(promotion.Status.Conditions, domain.ImagePromotionCondition{
		Type:               domain.ImagePromotionConditionTypeReady,
		Status:             coredomain.ConditionStatusTrue,
		Reason:             string(domain.ImagePromotionConditionReasonCompleted),
		Message:            conditionMessageForReasonWorker(domain.ImagePromotionConditionReasonCompleted),
		LastTransitionTime: now,
	})
	promotion.Status.LastAmendedAt = lo.ToPtr(now)

	if promotion.Status.ArtifactStatuses != nil {
		for i := range *promotion.Status.ArtifactStatuses {
			entry := &(*promotion.Status.ArtifactStatuses)[i]
			if exportName, ok := resolvedExports[entry.Format]; ok {
				entry.Ready = true
				entry.Published = true
				entry.ResolvedExport = lo.ToPtr(exportName)
			}
		}
	}

	if _, err := c.imageBuilderService.ImagePromotion().UpdateStatus(ctx, orgID, promotion); err != nil {
		c.log.WithError(err).WithField("promotion", lo.FromPtr(promotion.Metadata.Name)).
			Error("failed to update promotion status after amendment")
		return fmt.Errorf("failed to update promotion %q status after amendment: %w", lo.FromPtr(promotion.Metadata.Name), err)
	}
	return nil
}

func (c *Consumer) transitionToAmendmentFailed(ctx context.Context, orgID uuid.UUID, promotion *domain.ImagePromotion, message string) error {
	now := time.Now().UTC()
	if promotion.Status == nil {
		promotion.Status = &domain.ImagePromotionStatus{}
	}
	if promotion.Status.Conditions == nil {
		promotion.Status.Conditions = &[]domain.ImagePromotionCondition{}
	}
	domain.SetImagePromotionStatusCondition(promotion.Status.Conditions, domain.ImagePromotionCondition{
		Type:               domain.ImagePromotionConditionTypeReady,
		Status:             coredomain.ConditionStatusFalse,
		Reason:             string(domain.ImagePromotionConditionReasonAmendmentFailed),
		Message:            message,
		LastTransitionTime: now,
	})
	if _, err := c.imageBuilderService.ImagePromotion().UpdateStatus(ctx, orgID, promotion); err != nil {
		c.log.WithError(err).WithField("promotion", lo.FromPtr(promotion.Metadata.Name)).
			Error("failed to update promotion status to AmendmentFailed")
		return fmt.Errorf("failed to update promotion %q status to AmendmentFailed: %w", lo.FromPtr(promotion.Metadata.Name), err)
	}
	return nil
}

func getPromotionReadyReasonWorker(promotion *domain.ImagePromotion) string {
	if promotion == nil || promotion.Status == nil || promotion.Status.Conditions == nil {
		return ""
	}
	cond := domain.FindImagePromotionStatusCondition(*promotion.Status.Conditions, domain.ImagePromotionConditionTypeReady)
	if cond == nil {
		return ""
	}
	return cond.Reason
}

func conditionMessageForReasonWorker(reason domain.ImagePromotionConditionReason) string {
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

func exportFormatToCatalogItemArtifactTypeWorker(format domain.ExportFormatType) (coredomain.CatalogItemArtifactType, error) {
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

func resolveExportRefWorker(export *domain.ImageExport) string {
	if export == nil || export.Status == nil {
		return ""
	}
	if export.Status.ManifestDigest != nil && *export.Status.ManifestDigest != "" {
		return *export.Status.ManifestDigest
	}
	return ""
}

func referencesToArtifactsWorker(references map[string]string, baseURI string) []coredomain.CatalogItemArtifact {
	artifacts := make([]coredomain.CatalogItemArtifact, 0, len(references))
	for artifactType := range references {
		artifacts = append(artifacts, coredomain.CatalogItemArtifact{
			Type: coredomain.CatalogItemArtifactType(artifactType),
			Uri:  baseURI,
		})
	}
	return artifacts
}

// qualifyReferencesForURIMismatchWorker returns a copy of refs where, for any artifact type whose
// existing URI differs from baseURI, the short reference is expanded to a fully-qualified reference.
func qualifyReferencesForURIMismatchWorker(refs map[string]string, existingArtifacts []coredomain.CatalogItemArtifact, baseURI string) map[string]string {
	artifactURIs := make(map[string]string, len(existingArtifacts))
	for _, a := range existingArtifacts {
		artifactURIs[string(a.Type)] = a.Uri
	}

	result := make(map[string]string, len(refs))
	for artifactType, ref := range refs {
		existingURI, ok := artifactURIs[artifactType]
		if ok && existingURI != baseURI && existingURI != "" {
			// URI mismatch: qualify the reference so it is unambiguously resolvable.
			if !strings.HasPrefix(ref, existingURI) && !strings.HasPrefix(ref, baseURI) {
				// Digests (e.g. "sha256:abc123") contain ":" and must be joined with "@";
				// tags never contain ":" so they use the conventional ":" separator.
				if strings.Contains(ref, ":") {
					ref = baseURI + "@" + ref
				} else {
					ref = baseURI + ":" + ref
				}
			}
		}
		result[artifactType] = ref
	}
	return result
}

// referencesEqualWorker returns true when both maps contain identical key/value pairs.
func referencesEqualWorker(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// extractBaseURIWorker strips the tag or digest qualifier from an OCI image reference.
//
//	"quay.io/org/image:v1.0"           → "quay.io/org/image"
//	"quay.io/org/image@sha256:abc123"  → "quay.io/org/image"
//	"quay.io/org/image"               → "quay.io/org/image"
func extractBaseURIWorker(imageRef *string) string {
	if imageRef == nil || *imageRef == "" {
		return ""
	}
	ref := *imageRef
	if idx := strings.Index(ref, "@"); idx >= 0 {
		return ref[:idx]
	}
	if idx := strings.LastIndex(ref, ":"); idx >= 0 {
		if !strings.Contains(ref[idx+1:], "/") {
			return ref[:idx]
		}
	}
	return ref
}
