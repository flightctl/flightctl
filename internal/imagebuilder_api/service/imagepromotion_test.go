package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

// newTestImagePromotionService creates a thin ImagePromotionService backed by in-memory stores.
// No queueProducer is supplied; the service skips enqueueing silently.
func newTestImagePromotionService(
	promotionStore *DummyImagePromotionStore,
	imageBuildStore *DummyImageBuildStore,
	catalogStore *DummyCatalogStore,
) ImagePromotionService {
	return NewImagePromotionService(
		promotionStore,
		imageBuildStore,
		catalogStore,
		nil, // no queue: enqueue is a no-op when queueProducer is nil
		log.InitLogs(),
	)
}

// imageRef returns a realistic full OCI image reference for the named build.
func imageRef(name string) string {
	return "quay.io/test-org/" + name + ":v1.0"
}

// newCompletedImageBuild builds an ImageBuild in the Completed state.
func newCompletedImageBuild(name, manifestDigest string) api.ImageBuild {
	now := time.Now()
	ref := imageRef(name)
	return api.ImageBuild{
		ApiVersion: api.ImageBuildAPIVersion,
		Kind:       string(api.ResourceKindImageBuild),
		Metadata: v1beta1.ObjectMeta{
			Name:              lo.ToPtr(name),
			CreationTimestamp: &now,
		},
		Spec: api.ImageBuildSpec{
			Source: api.ImageBuildSource{
				Repository: "input-registry",
				ImageName:  "input-image",
				ImageTag:   "v1.0",
			},
			Destination: api.ImageBuildDestination{
				Repository: "output-registry",
				ImageName:  name,
				ImageTag:   "v1.0",
			},
		},
		Status: &api.ImageBuildStatus{
			ImageReference: lo.ToPtr(ref),
			ManifestDigest: lo.ToPtr(manifestDigest),
			Conditions: &[]api.ImageBuildCondition{
				{
					Type:   api.ImageBuildConditionTypeReady,
					Status: coredomain.ConditionStatusTrue,
					Reason: string(api.ImageBuildConditionReasonCompleted),
				},
			},
		},
	}
}

// makeNewCatalogItemTarget builds an ImagePromotionTarget for creating a brand-new CatalogItem.
func makeNewCatalogItemTarget(catalogName, itemName, version string) api.ImagePromotionTarget {
	target := api.ImagePromotionTarget{}
	_ = target.FromNewCatalogItemTarget(api.NewCatalogItemTarget{
		Type:            api.NewCatalogItem,
		CatalogName:     catalogName,
		CatalogItemName: itemName,
		Version:         version,
	})
	return target
}

// makeExistingCatalogItemTarget builds an ImagePromotionTarget for adding a version to an existing CatalogItem.
func makeExistingCatalogItemTarget(catalogName, itemName, version string) api.ImagePromotionTarget {
	target := api.ImagePromotionTarget{}
	_ = target.FromExistingCatalogItemTarget(api.ExistingCatalogItemTarget{
		Type:            api.ExistingCatalogItem,
		CatalogName:     catalogName,
		CatalogItemName: itemName,
		Version:         version,
	})
	return target
}

// requirePromotionReason retrieves the most-recent promotion by name and asserts its Ready condition reason.
func requirePromotionReason(t *testing.T, store *DummyImagePromotionStore, name string, expected api.ImagePromotionConditionReason) {
	t.Helper()
	p, err := store.Get(context.Background(), uuid.Nil, name)
	require.NoError(t, err)
	require.NotNil(t, p.Status, "promotion should have status")
	require.NotNil(t, p.Status.Conditions, "promotion should have conditions")
	reason := getPromotionReadyReason(p)
	require.Equal(t, string(expected), reason,
		"expected Ready condition reason to be %s, got %s", expected, reason)
}

// TestImagePromotionCreate verifies that Create always returns 201 and leaves the promotion in
// WaitingForArtifacts state — evaluation is handled asynchronously by the worker.
func TestImagePromotionCreate(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	imageBuildStore := NewDummyImageBuildStore()
	promotionStore := NewDummyImagePromotionStore()
	catalogStore := NewDummyCatalogStore()

	build := newCompletedImageBuild("my-build", "sha256:aaaa0000")
	_, err := imageBuildStore.Create(ctx, orgId, &build)
	require.NoError(err)
	catalogStore.AddCatalog("my-catalog")

	svc := newTestImagePromotionService(promotionStore, imageBuildStore, catalogStore)

	promotion := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-1")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "my-build"},
			Target: makeNewCatalogItemTarget("my-catalog", "my-app", "1.0.0"),
		},
	}

	result, status := svc.Create(ctx, orgId, promotion)
	require.Equal(int32(http.StatusCreated), status.Code, "expected 201 Created, got: %s", status.Message)
	require.NotNil(result)
	require.NotNil(result.Metadata.Generation)
	require.Equal(int64(1), lo.FromPtr(result.Metadata.Generation))

	// The service must immediately set WaitingForArtifacts; evaluation is deferred to the worker.
	requirePromotionReason(t, promotionStore, "promo-1", domain.ImagePromotionConditionReasonWaitingForArtifacts)
}

// TestImagePromotionCreateDuplicateVersion verifies that creating an ImagePromotion whose target
// version already exists in the CatalogItem is rejected at the service level with 400.
func TestImagePromotionCreateDuplicateVersion(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	imageBuildStore := NewDummyImageBuildStore()
	promotionStore := NewDummyImagePromotionStore()
	catalogStore := NewDummyCatalogStore()

	build := newCompletedImageBuild("my-build-conflict", "sha256:conflict0000")
	_, err := imageBuildStore.Create(ctx, orgId, &build)
	require.NoError(err)

	existingBaseURI := "quay.io/test-org/my-build-conflict"
	systemCategory := coredomain.CatalogItemCategorySystem
	catalogStore.AddCatalog("my-catalog")
	catalogStore.AddItem("my-catalog", &coredomain.CatalogItem{
		Metadata: coredomain.CatalogItemMeta{Name: lo.ToPtr("my-app")},
		Spec: coredomain.CatalogItemSpec{
			Type:     coredomain.CatalogItemTypeOS,
			Category: &systemCategory,
			Artifacts: []coredomain.CatalogItemArtifact{
				{Type: coredomain.CatalogItemArtifactTypeContainer, Uri: existingBaseURI},
			},
			Versions: []coredomain.CatalogItemVersion{
				{Version: "1.0.0", Channels: []string{"stable"}},
			},
		},
	})

	svc := newTestImagePromotionService(promotionStore, imageBuildStore, catalogStore)

	promotion := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-conflict")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "my-build-conflict"},
			Target: makeExistingCatalogItemTarget("my-catalog", "my-app", "1.0.0"),
		},
	}

	result, status := svc.Create(ctx, orgId, promotion)
	require.Equal(int32(http.StatusBadRequest), status.Code, "expected 400 for duplicate version, got: %s", status.Message)
	require.Nil(result)
}

// TestImagePromotionCreateWithExportFormats verifies that a promotion with additional export
// formats can be created; it starts in WaitingForArtifacts regardless of build state.
func TestImagePromotionCreateWithExportFormats(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	imageBuildStore := NewDummyImageBuildStore()
	promotionStore := NewDummyImagePromotionStore()
	catalogStore := NewDummyCatalogStore()

	build := newCompletedImageBuild("my-build-exports", "sha256:container0000")
	_, err := imageBuildStore.Create(ctx, orgId, &build)
	require.NoError(err)
	catalogStore.AddCatalog("my-catalog")

	svc := newTestImagePromotionService(promotionStore, imageBuildStore, catalogStore)

	promotion := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-exports-pending")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{
				ImageBuildRef: "my-build-exports",
				ExportFormats: lo.ToPtr([]domain.ExportFormatType{domain.ExportFormatTypeQCOW2}),
			},
			Target: makeNewCatalogItemTarget("my-catalog", "my-app", "1.0.0"),
		},
	}

	result, status := svc.Create(ctx, orgId, promotion)
	require.Equal(int32(http.StatusCreated), status.Code, "expected 201 Created, got: %s", status.Message)
	require.NotNil(result)
	requirePromotionReason(t, promotionStore, "promo-exports-pending", domain.ImagePromotionConditionReasonWaitingForArtifacts)
}

// TestImagePromotionValidation_MissingBuild verifies that creating a promotion against a
// non-existent ImageBuild is rejected with 400.
func TestImagePromotionValidation_MissingBuild(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	catalogStore := NewDummyCatalogStore()
	catalogStore.AddCatalog("cat")
	svc := newTestImagePromotionService(
		NewDummyImagePromotionStore(),
		NewDummyImageBuildStore(),
		catalogStore,
	)

	promotion := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-missing-build")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "nonexistent-build"},
			Target: makeNewCatalogItemTarget("cat", "item", "1.0.0"),
		},
	}

	_, status := svc.Create(ctx, orgId, promotion)
	require.Equal(int32(http.StatusBadRequest), status.Code)
}

// TestImagePromotionReplace_AppendOnlyFormats verifies that Replace accepts only append-only
// changes to exportFormats.
func TestImagePromotionReplace_AppendOnlyFormats(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	imageBuildStore := NewDummyImageBuildStore()
	promotionStore := NewDummyImagePromotionStore()
	catalogStore := NewDummyCatalogStore()

	build := newCompletedImageBuild("my-build", "sha256:1234")
	_, err := imageBuildStore.Create(ctx, orgId, &build)
	require.NoError(err)
	catalogStore.AddCatalog("my-catalog")

	svc := newTestImagePromotionService(promotionStore, imageBuildStore, catalogStore)

	// Create initial promotion without export formats.
	promotion := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-replace")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "my-build"},
			Target: makeNewCatalogItemTarget("my-catalog", "my-app", "1.0.0"),
		},
	}
	created, status := svc.Create(ctx, orgId, promotion)
	require.Equal(int32(http.StatusCreated), status.Code, "create failed: %s", status.Message)
	require.Equal(int64(1), lo.FromPtr(created.Metadata.Generation))

	// Replace: adding qcow2 format is allowed.
	updated := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-replace")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{
				ImageBuildRef: "my-build",
				ExportFormats: lo.ToPtr([]domain.ExportFormatType{domain.ExportFormatTypeQCOW2}),
			},
			Target: makeNewCatalogItemTarget("my-catalog", "my-app", "1.0.0"),
		},
	}
	replaced, replaceStatus := svc.Replace(ctx, orgId, "promo-replace", updated)
	require.Equal(int32(http.StatusOK), replaceStatus.Code, "replace with new format should succeed: %s", replaceStatus.Message)
	require.Equal(int64(2), lo.FromPtr(replaced.Metadata.Generation), "adding an export format is a spec change and should increment generation")

	// Replace: removing an existing format must be rejected.
	removed := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-replace")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{
				ImageBuildRef: "my-build",
				ExportFormats: nil, // removes qcow2
			},
			Target: makeNewCatalogItemTarget("my-catalog", "my-app", "1.0.0"),
		},
	}
	_, removedStatus := svc.Replace(ctx, orgId, "promo-replace", removed)
	require.Equal(int32(http.StatusBadRequest), removedStatus.Code, "removing a format should be rejected")
}

// TestImagePromotionReplace_LabelsOnly_GenerationUnchanged verifies that a Replace which only
// changes metadata.labels (no export format change) leaves generation untouched.
func TestImagePromotionReplace_LabelsOnly_GenerationUnchanged(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	imageBuildStore := NewDummyImageBuildStore()
	promotionStore := NewDummyImagePromotionStore()
	catalogStore := NewDummyCatalogStore()

	build := newCompletedImageBuild("my-build", "sha256:5678")
	_, err := imageBuildStore.Create(ctx, orgId, &build)
	require.NoError(err)
	catalogStore.AddCatalog("my-catalog")

	svc := newTestImagePromotionService(promotionStore, imageBuildStore, catalogStore)

	promotion := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-labels-only")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "my-build"},
			Target: makeNewCatalogItemTarget("my-catalog", "my-app", "1.0.0"),
		},
	}
	created, status := svc.Create(ctx, orgId, promotion)
	require.Equal(int32(http.StatusCreated), status.Code, "create failed: %s", status.Message)
	require.Equal(int64(1), lo.FromPtr(created.Metadata.Generation))

	// Replace with the same export formats (none) but new labels — not a spec change.
	relabeled := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr("promo-labels-only"),
			Labels: lo.ToPtr(map[string]string{"env": "prod"}),
		},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "my-build"},
			Target: makeNewCatalogItemTarget("my-catalog", "my-app", "1.0.0"),
		},
	}
	replaced, replaceStatus := svc.Replace(ctx, orgId, "promo-labels-only", relabeled)
	require.Equal(int32(http.StatusOK), replaceStatus.Code, "labels-only replace should succeed: %s", replaceStatus.Message)
	require.Equal(int64(1), lo.FromPtr(replaced.Metadata.Generation), "labels-only change should not increment generation")
}

// TestImagePromotionReplace_ImmutableFields verifies that immutable spec fields (imageBuildRef,
// target) cannot be changed via Replace.
func TestImagePromotionReplace_ImmutableFields(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	imageBuildStore := NewDummyImageBuildStore()
	promotionStore := NewDummyImagePromotionStore()
	catalogStore := NewDummyCatalogStore()

	build1 := newCompletedImageBuild("build-1", "sha256:build1")
	build2 := newCompletedImageBuild("build-2", "sha256:build2")
	_, _ = imageBuildStore.Create(ctx, orgId, &build1)
	_, _ = imageBuildStore.Create(ctx, orgId, &build2)
	catalogStore.AddCatalog("my-catalog")

	svc := newTestImagePromotionService(promotionStore, imageBuildStore, catalogStore)

	promotion := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-immutable")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "build-1"},
			Target: makeNewCatalogItemTarget("my-catalog", "item-a", "1.0.0"),
		},
	}
	_, createStatus := svc.Create(ctx, orgId, promotion)
	require.Equal(int32(http.StatusCreated), createStatus.Code, "create failed: %s", createStatus.Message)

	// Attempt to change imageBuildRef → must be rejected.
	changedBuild := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-immutable")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "build-2"},
			Target: makeNewCatalogItemTarget("my-catalog", "item-a", "1.0.0"),
		},
	}
	_, replaceStatus := svc.Replace(ctx, orgId, "promo-immutable", changedBuild)
	require.Equal(int32(http.StatusBadRequest), replaceStatus.Code, "changing imageBuildRef should be rejected")

	// Attempt to change target version → must be rejected.
	changedTarget := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-immutable")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "build-1"},
			Target: makeNewCatalogItemTarget("my-catalog", "item-a", "2.0.0"),
		},
	}
	_, targetStatus := svc.Replace(ctx, orgId, "promo-immutable", changedTarget)
	require.Equal(int32(http.StatusBadRequest), targetStatus.Code, "changing target should be rejected")
}

// TestImagePromotionDelete verifies that Delete removes the promotion and is idempotent.
func TestImagePromotionDelete(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	orgId := uuid.New()

	imageBuildStore := NewDummyImageBuildStore()
	promotionStore := NewDummyImagePromotionStore()
	catalogStore := NewDummyCatalogStore()

	build := newCompletedImageBuild("my-build", "sha256:del")
	_, _ = imageBuildStore.Create(ctx, orgId, &build)
	catalogStore.AddCatalog("my-catalog")

	svc := newTestImagePromotionService(promotionStore, imageBuildStore, catalogStore)

	promotion := domain.ImagePromotion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("promo-delete")},
		Spec: domain.ImagePromotionSpec{
			Source: domain.ImagePromotionSource{ImageBuildRef: "my-build"},
			Target: makeNewCatalogItemTarget("my-catalog", "my-app", "1.0.0"),
		},
	}
	_, createStatus := svc.Create(ctx, orgId, promotion)
	require.Equal(int32(http.StatusCreated), createStatus.Code)

	deleteStatus := svc.Delete(ctx, orgId, "promo-delete")
	require.Equal(int32(http.StatusOK), deleteStatus.Code)

	_, getStatus := svc.Get(ctx, orgId, "promo-delete")
	require.Equal(int32(http.StatusNotFound), getStatus.Code)
}
