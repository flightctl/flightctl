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

// recordingQueueProducer is a minimal QueueProducer that records enqueued payloads.
type recordingQueueProducer struct {
	enqueued [][]byte
	err      error // if set, Enqueue returns this error
}

func (r *recordingQueueProducer) Enqueue(_ context.Context, payload []byte, _ int64) error {
	if r.err != nil {
		return r.err
	}
	r.enqueued = append(r.enqueued, payload)
	return nil
}

func (r *recordingQueueProducer) Close() {}

// makeBuildUpdateEvent returns a worker event for an ImageBuild ResourceUpdated.
func makeBuildUpdateEvent(orgID uuid.UUID, buildName string) worker_client.EventWithOrgId {
	return worker_client.EventWithOrgId{
		OrgId: orgID,
		Event: coredomain.Event{
			InvolvedObject: coredomain.ObjectReference{
				Kind: string(domain.ResourceKindImageBuild),
				Name: buildName,
			},
		},
	}
}

// makeBuildWithReason creates an ImageBuild that has a single Ready condition with the given reason.
func makeBuildWithReason(name string, reason domain.ImageBuildConditionReason) *domain.ImageBuild {
	now := time.Now()
	return &domain.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr(name), CreationTimestamp: &now},
		Spec: api.ImageBuildSpec{
			Source:      api.ImageBuildSource{Repository: "src", ImageName: name, ImageTag: "v1"},
			Destination: api.ImageBuildDestination{Repository: "dst", ImageName: name, ImageTag: "v1"},
		},
		Status: &api.ImageBuildStatus{
			ImageReference: lo.ToPtr("quay.io/test/" + name + ":v1"),
			ManifestDigest: lo.ToPtr("sha256:aabb"),
			Conditions: &[]api.ImageBuildCondition{
				{Type: api.ImageBuildConditionTypeReady, Status: coredomain.ConditionStatusTrue, Reason: string(reason)},
			},
		},
	}
}

// makeExportForBuild creates an ImageExport linked to the given build with the given
// Ready condition reason. Pass an empty reason to create an export with no status.
func makeExportForBuild(name, buildRef string, reason domain.ImageExportConditionReason) *domain.ImageExport {
	now := time.Now()
	src := api.ImageExportSource{}
	_ = src.FromImageBuildRefSource(api.ImageBuildRefSource{
		Type:          api.ImageBuildRefSourceTypeImageBuild,
		ImageBuildRef: buildRef,
	})
	export := &domain.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr(name), CreationTimestamp: &now},
		Spec:     api.ImageExportSpec{Source: src, Format: api.ExportFormatTypeQCOW2},
	}
	if reason != "" {
		condStatus := coredomain.ConditionStatusFalse
		if reason == domain.ImageExportConditionReasonCompleted {
			condStatus = coredomain.ConditionStatusTrue
		}
		export.Status = &api.ImageExportStatus{
			Conditions: &[]api.ImageExportCondition{
				{Type: domain.ImageExportConditionTypeReady, Status: condStatus, Reason: string(reason)},
			},
		}
	}
	return export
}

// newTestConsumerWithProducer is a convenience wrapper that wires a custom queue producer.
func newTestConsumerWithProducer(svc *testIBService, catalogStore *dummyCatalogStoreAdapter, producer *recordingQueueProducer) *Consumer {
	c := newTestConsumer(svc, catalogStore)
	c.queueProducer = producer
	return c
}

// ---- HandleImageBuildUpdate tests ----

// TestHandleImageBuildUpdate_BuildNotFound verifies that when the ImageBuild cannot be
// loaded the handler returns an error (so the queue retries the event).
func TestHandleImageBuildUpdate_BuildNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "missing-build"), consumer.log)
	require.Error(err)
	require.Contains(err.Error(), "failed to load ImageBuild")
}

// TestHandleImageBuildUpdate_NoStatus verifies that a build with no status is a no-op.
func TestHandleImageBuildUpdate_NoStatus(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	now := time.Now()
	build := &domain.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("build-no-status"), CreationTimestamp: &now},
		Spec:     api.ImageBuildSpec{Source: api.ImageBuildSource{Repository: "src"}, Destination: api.ImageBuildDestination{Repository: "dst"}},
	}
	_, _ = svc.builds.Create(ctx, orgID, build)
	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-no-status"), consumer.log)
	require.NoError(err)
}

// TestHandleImageBuildUpdate_NoReadyCondition verifies that a build without a Ready
// condition is a no-op.
func TestHandleImageBuildUpdate_NoReadyCondition(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	now := time.Now()
	build := &domain.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("build-no-ready"), CreationTimestamp: &now},
		Spec:     api.ImageBuildSpec{Source: api.ImageBuildSource{Repository: "src"}, Destination: api.ImageBuildDestination{Repository: "dst"}},
		Status: &api.ImageBuildStatus{
			Conditions: &[]api.ImageBuildCondition{
				{Type: "SomeOtherCondition", Status: coredomain.ConditionStatusTrue, Reason: "SomeReason"},
			},
		},
	}
	_, _ = svc.builds.Create(ctx, orgID, build)
	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-no-ready"), consumer.log)
	require.NoError(err)
}

// TestHandleImageBuildUpdate_UnknownReason verifies that an unrecognised Ready condition
// reason is a no-op.
func TestHandleImageBuildUpdate_UnknownReason(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	build := makeBuildWithReason("build-building", domain.ImageBuildConditionReasonBuilding)
	_, _ = svc.builds.Create(ctx, orgID, build)
	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-building"), consumer.log)
	require.NoError(err)
}

// TestHandleImageBuildUpdate_Completed_NoPendingWork verifies that a completed build
// with no pending exports and no pending promotions returns nil without side effects.
func TestHandleImageBuildUpdate_Completed_NoPendingWork(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	build := makeCompletedBuild("build-1", "sha256:aabb")
	_, _ = svc.builds.Create(ctx, orgID, build)
	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-1"), consumer.log)
	require.NoError(err)
}

// TestHandleImageBuildUpdate_Completed_RequeuePendingExports verifies that only exports
// that need requeueing (no status, Pending condition, no Ready condition) are enqueued,
// while already-active exports (Converting, Completed) are skipped.
func TestHandleImageBuildUpdate_Completed_RequeuePendingExports(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	build := makeCompletedBuild("build-1", "sha256:aabb")
	_, _ = svc.builds.Create(ctx, orgID, build)

	// Should be requeued.
	noStatus := makeExportForBuild("export-no-status", "build-1", "")
	_, _ = svc.exports.Create(ctx, orgID, noStatus)

	pending := makeExportForBuild("export-pending", "build-1", domain.ImageExportConditionReasonPending)
	_, _ = svc.exports.Create(ctx, orgID, pending)

	// Should NOT be requeued.
	converting := makeExportForBuild("export-converting", "build-1", domain.ImageExportConditionReasonConverting)
	_, _ = svc.exports.Create(ctx, orgID, converting)

	completed := makeExportForBuild("export-completed", "build-1", domain.ImageExportConditionReasonCompleted)
	_, _ = svc.exports.Create(ctx, orgID, completed)

	producer := &recordingQueueProducer{}
	consumer := newTestConsumerWithProducer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()}, producer)

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-1"), consumer.log)
	require.NoError(err)
	require.Len(producer.enqueued, 2, "only the no-status and pending exports should be requeued")
}

// TestHandleImageBuildUpdate_Completed_EvaluatesPromotions verifies that a completed build
// enqueues a ResourceUpdated event for each waiting promotion rather than evaluating inline.
func TestHandleImageBuildUpdate_Completed_EvaluatesPromotions(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	build := makeCompletedBuild("build-1", "sha256:aabb")
	_, _ = svc.builds.Create(ctx, orgID, build)

	promotion := makeWaitingPromotion("promo-1", "build-1", "my-catalog", "my-app", "1.0.0")
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	producer := &recordingQueueProducer{}
	consumer := newTestConsumerWithProducer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()}, producer)

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-1"), consumer.log)
	require.NoError(err)
	require.Len(producer.enqueued, 1, "expected one ImagePromotion event to be enqueued")
	// Promotion state must not have changed — evaluation happens when the event is consumed.
	requirePromotionReasonWorker(t, svc.promotions, "promo-1", domain.ImagePromotionConditionReasonWaitingForArtifacts)
}

// TestHandleImageBuildUpdate_Completed_RequeueListError verifies that an export List failure
// is returned, but promotion enqueueing still runs (errors.Join, both ops attempted).
func TestHandleImageBuildUpdate_Completed_RequeueListError(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	svc.exports.listErr = errors.New("db failure")

	build := makeCompletedBuild("build-1", "sha256:aabb")
	_, _ = svc.builds.Create(ctx, orgID, build)

	promotion := makeWaitingPromotion("promo-1", "build-1", "my-catalog", "my-app", "1.0.0")
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	producer := &recordingQueueProducer{}
	consumer := newTestConsumerWithProducer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()}, producer)

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-1"), consumer.log)
	require.Error(err)
	require.Contains(err.Error(), "db failure")
	// Promotion enqueueing still ran despite the export requeue error.
	require.Len(producer.enqueued, 1, "expected one ImagePromotion event to be enqueued despite export list error")
}

// TestHandleImageBuildUpdate_Completed_BothErrors verifies that when both requeue and
// promotion-eval fail, errors.Join returns both error messages.
func TestHandleImageBuildUpdate_Completed_BothErrors(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	svc.exports.listErr = errors.New("export list failure")
	svc.promotions.listPendingErr = errors.New("promotion list failure")

	build := makeCompletedBuild("build-1", "sha256:aabb")
	_, _ = svc.builds.Create(ctx, orgID, build)

	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-1"), consumer.log)
	require.Error(err)
	require.Contains(err.Error(), "export list failure")
	require.Contains(err.Error(), "promotion list failure")
}

// TestHandleImageBuildUpdate_Failed_FailsPromotions verifies that a failed build
// transitions all waiting promotions to BuildFailed.
func TestHandleImageBuildUpdate_Failed_FailsPromotions(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	build := makeBuildWithReason("build-1", domain.ImageBuildConditionReasonFailed)
	_, _ = svc.builds.Create(ctx, orgID, build)

	promotion := makeWaitingPromotion("promo-1", "build-1", "my-catalog", "my-app", "1.0.0")
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-1"), consumer.log)
	require.NoError(err)
	requirePromotionReasonWorker(t, svc.promotions, "promo-1", domain.ImagePromotionConditionReasonBuildFailed)
}

// TestHandleImageBuildUpdate_Canceled_FailsPromotions verifies that a canceled build
// transitions all waiting promotions to BuildCanceled.
func TestHandleImageBuildUpdate_Canceled_FailsPromotions(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	build := makeBuildWithReason("build-1", domain.ImageBuildConditionReasonCanceled)
	_, _ = svc.builds.Create(ctx, orgID, build)

	promotion := makeWaitingPromotion("promo-1", "build-1", "my-catalog", "my-app", "1.0.0")
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-1"), consumer.log)
	require.NoError(err)
	requirePromotionReasonWorker(t, svc.promotions, "promo-1", domain.ImagePromotionConditionReasonBuildCanceled)
}

// TestHandleImageBuildUpdate_Failed_PromoListError verifies that a store failure inside
// failPromotionsForBuild is propagated to the caller.
func TestHandleImageBuildUpdate_Failed_PromoListError(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	svc.promotions.listPendingErr = errors.New("db failure")

	build := makeBuildWithReason("build-1", domain.ImageBuildConditionReasonFailed)
	_, _ = svc.builds.Create(ctx, orgID, build)

	consumer := newTestConsumer(svc, &dummyCatalogStoreAdapter{w: newDummyCatalogItemWriter()})

	err := consumer.HandleImageBuildUpdate(ctx, makeBuildUpdateEvent(orgID, "build-1"), consumer.log)
	require.Error(err)
	require.Contains(err.Error(), "db failure")
}
