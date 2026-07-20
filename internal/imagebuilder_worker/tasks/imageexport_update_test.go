package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

// makeExportUpdateEvent builds a minimal worker_client.EventWithOrgId for an
// ImageExport ResourceUpdated event.
func makeExportUpdateEvent(orgID uuid.UUID, exportName string) worker_client.EventWithOrgId {
	return worker_client.EventWithOrgId{
		OrgId: orgID,
		Event: coredomain.Event{
			InvolvedObject: coredomain.ObjectReference{
				Kind: string(domain.ResourceKindImageExport),
				Name: exportName,
			},
		},
	}
}

// makeExportWithCondition creates an ImageExport with a single Ready condition.
func makeExportWithCondition(name, buildRef string, format domain.ExportFormatType, reason domain.ImageExportConditionReason) *domain.ImageExport {
	now := time.Now()
	src := api.ImageExportSource{}
	_ = src.FromImageBuildRefSource(api.ImageBuildRefSource{
		Type:          api.ImageBuildRefSourceTypeImageBuild,
		ImageBuildRef: buildRef,
	})
	condStatus := coredomain.ConditionStatusFalse
	if reason == domain.ImageExportConditionReasonCompleted {
		condStatus = coredomain.ConditionStatusTrue
	}
	return &domain.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr(name), CreationTimestamp: &now},
		Spec:     api.ImageExportSpec{Source: src, Format: format},
		Status: &api.ImageExportStatus{
			Conditions: &[]api.ImageExportCondition{
				{
					Type:   domain.ImageExportConditionTypeReady,
					Status: condStatus,
					Reason: string(reason),
				},
			},
		},
	}
}

// TestHandleImageExportUpdated_StoreError verifies that when the store fails to
// retrieve the ImageExport the handler propagates the error so the queue retries.
func TestHandleImageExportUpdated_StoreError(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	svc.exports.getErr = errors.New("db unavailable")
	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.handleImageExportUpdated(ctx, makeExportUpdateEvent(orgID, "export-1"), consumer.log)
	require.Error(err)
	require.Contains(err.Error(), "db unavailable")
}

// TestHandleImageExportUpdated_NoStatus verifies that when the ImageExport has
// no status the handler skips promotion evaluation and returns nil.
func TestHandleImageExportUpdated_NoStatus(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	now := time.Now()
	export := &domain.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("export-no-status"), CreationTimestamp: &now},
		Spec:     api.ImageExportSpec{Format: api.ExportFormatTypeQCOW2},
	}
	_, _ = svc.exports.Create(ctx, orgID, export)
	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.handleImageExportUpdated(ctx, makeExportUpdateEvent(orgID, "export-no-status"), consumer.log)
	require.NoError(err)
}

// TestHandleImageExportUpdated_PendingExport verifies that a Pending export
// does not trigger promotion enqueueing.
func TestHandleImageExportUpdated_PendingExport(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	export := makeExportWithCondition("export-pending", "build-1", api.ExportFormatTypeQCOW2, domain.ImageExportConditionReasonPending)
	_, _ = svc.exports.Create(ctx, orgID, export)

	promotion := makeWaitingPromotion("promo-1", "build-1", "my-catalog", "my-app", "1.0.0")
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	producer := &recordingQueueProducer{}
	consumer := newTestConsumerWithProducer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()}, producer)

	err := consumer.handleImageExportUpdated(ctx, makeExportUpdateEvent(orgID, "export-pending"), consumer.log)
	require.NoError(err)
	require.Empty(producer.enqueued, "no promotion events should be enqueued for a non-completed export")
}

// TestHandleImageExportUpdated_ConvertingExport verifies that an export still
// in the Converting state does not trigger promotion enqueueing.
func TestHandleImageExportUpdated_ConvertingExport(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	export := makeExportWithCondition("export-converting", "build-1", api.ExportFormatTypeQCOW2, domain.ImageExportConditionReasonConverting)
	_, _ = svc.exports.Create(ctx, orgID, export)

	promotion := makeWaitingPromotion("promo-1", "build-1", "my-catalog", "my-app", "1.0.0")
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	producer := &recordingQueueProducer{}
	consumer := newTestConsumerWithProducer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()}, producer)

	err := consumer.handleImageExportUpdated(ctx, makeExportUpdateEvent(orgID, "export-converting"), consumer.log)
	require.NoError(err)
	require.Empty(producer.enqueued, "no promotion events should be enqueued for a non-completed export")
}

// TestHandleImageExportUpdated_InvalidSource verifies that a completed export
// whose source union is malformed (Discriminator fails) is skipped gracefully.
func TestHandleImageExportUpdated_InvalidSource(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)

	// Export with an empty (zero-value) source — Discriminator() will fail on
	// json.Unmarshal of a nil union, returning an error.
	now := time.Now()
	export := &domain.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("export-invalid-src"), CreationTimestamp: &now},
		Spec:     api.ImageExportSpec{Format: api.ExportFormatTypeQCOW2}, // Source left as zero value
		Status: &api.ImageExportStatus{
			Conditions: &[]api.ImageExportCondition{
				{
					Type:   domain.ImageExportConditionTypeReady,
					Status: coredomain.ConditionStatusTrue,
					Reason: string(domain.ImageExportConditionReasonCompleted),
				},
			},
		},
	}
	_, _ = svc.exports.Create(ctx, orgID, export)
	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.handleImageExportUpdated(ctx, makeExportUpdateEvent(orgID, "export-invalid-src"), consumer.log)
	require.NoError(err)
}

// TestHandleImageExportUpdated_CompletedNoPendingPromotions verifies that a
// completed ImageBuild-sourced export with no pending promotions returns nil.
func TestHandleImageExportUpdated_CompletedNoPendingPromotions(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	export := makeExportWithCondition("export-done", "build-1", api.ExportFormatTypeQCOW2, domain.ImageExportConditionReasonCompleted)
	_, _ = svc.exports.Create(ctx, orgID, export)

	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.handleImageExportUpdated(ctx, makeExportUpdateEvent(orgID, "export-done"), consumer.log)
	require.NoError(err)
}

// TestHandleImageExportUpdated_CompletedTriggersPromotion verifies that when an export
// completes, a waiting promotion for the same build gets a ResourceUpdated event enqueued
// so that processImagePromotion is the sole handler of promotion evaluation logic.
func TestHandleImageExportUpdated_CompletedTriggersPromotion(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)

	// Create the build and its completed export.
	build := makeCompletedBuild("build-1", "sha256:aabb")
	_, _ = svc.builds.Create(ctx, orgID, build)
	export := makeCompletedExport("export-done", "build-1", api.ExportFormatTypeQCOW2, "sha256:ccdd")
	_, _ = svc.exports.Create(ctx, orgID, export)

	// Create a promotion waiting for QCOW2 artifacts.
	promotion := makeWaitingPromotion("promo-1", "build-1", "my-catalog", "my-app", "1.0.0")
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	producer := &recordingQueueProducer{}
	consumer := newTestConsumerWithProducer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()}, producer)

	err := consumer.handleImageExportUpdated(ctx, makeExportUpdateEvent(orgID, "export-done"), consumer.log)
	require.NoError(err)

	// The promotion should have been enqueued for evaluation, not evaluated inline.
	require.Len(producer.enqueued, 1, "expected one ImagePromotion event to be enqueued")
	// Promotion state must not have changed — evaluation happens when the event is consumed.
	requirePromotionReasonWorker(t, svc.promotions, "promo-1", domain.ImagePromotionConditionReasonWaitingForArtifacts)
}

// TestHandleImageExportUpdated_EvaluationError verifies that when ListPendingForBuild
// fails inside enqueuePromotionsForBuild the error is propagated (so the queue retries).
func TestHandleImageExportUpdated_EvaluationError(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	export := makeExportWithCondition("export-done", "build-1", api.ExportFormatTypeQCOW2, domain.ImageExportConditionReasonCompleted)
	_, _ = svc.exports.Create(ctx, orgID, export)

	// Inject an error into ListPendingForBuild so enqueuePromotionsForBuild fails.
	svc.promotions.listPendingErr = errors.New("db error")

	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.handleImageExportUpdated(ctx, makeExportUpdateEvent(orgID, "export-done"), consumer.log)
	require.Error(err)
	require.Contains(err.Error(), "db error")
}
