package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	ibstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// deepCopyJSON performs a deep copy via JSON round-trip.
func deepCopyJSON(src, dst interface{}) {
	data, err := json.Marshal(src)
	if err != nil {
		panic("deepCopyJSON marshal: " + err.Error())
	}
	if err = json.Unmarshal(data, dst); err != nil {
		panic("deepCopyJSON unmarshal: " + err.Error())
	}
}

// ---- in-memory test stores ----

type inMemoryPromotionStore struct {
	data           map[string]*domain.ImagePromotion
	orgID          uuid.UUID
	listPendingErr error // if set, ListPendingForBuild returns this error
}

func newInMemoryPromotionStore(orgID uuid.UUID) *inMemoryPromotionStore {
	return &inMemoryPromotionStore{data: make(map[string]*domain.ImagePromotion), orgID: orgID}
}

func (s *inMemoryPromotionStore) Create(ctx context.Context, orgId uuid.UUID, p *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	var cp domain.ImagePromotion
	deepCopyJSON(p, &cp)
	s.data[lo.FromPtr(p.Metadata.Name)] = &cp
	return &cp, nil
}
func (s *inMemoryPromotionStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, error) {
	p, ok := s.data[name]
	if !ok {
		return nil, nil
	}
	var cp domain.ImagePromotion
	deepCopyJSON(p, &cp)
	return &cp, nil
}
func (s *inMemoryPromotionStore) List(ctx context.Context, orgId uuid.UUID, lp flightctlstore.ListParams) (*domain.ImagePromotionList, error) {
	var items []domain.ImagePromotion
	for _, p := range s.data {
		var cp domain.ImagePromotion
		deepCopyJSON(p, &cp)
		items = append(items, cp)
	}
	return &domain.ImagePromotionList{Items: items}, nil
}
func (s *inMemoryPromotionStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, error) {
	p := s.data[name]
	delete(s.data, name)
	return p, nil
}
func (s *inMemoryPromotionStore) Update(ctx context.Context, orgId uuid.UUID, p *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	var cp domain.ImagePromotion
	deepCopyJSON(p, &cp)
	s.data[lo.FromPtr(p.Metadata.Name)] = &cp
	return &cp, nil
}
func (s *inMemoryPromotionStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, p *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	name := lo.FromPtr(p.Metadata.Name)
	existing := s.data[name]
	if existing != nil {
		existing.Status = p.Status
	} else {
		var cp domain.ImagePromotion
		deepCopyJSON(p, &cp)
		s.data[name] = &cp
		existing = s.data[name]
	}
	var result domain.ImagePromotion
	deepCopyJSON(existing, &result)
	return &result, nil
}
func (s *inMemoryPromotionStore) ListPendingForBuild(ctx context.Context, orgId uuid.UUID, imageBuildRef string) ([]domain.ImagePromotion, error) {
	if s.listPendingErr != nil {
		return nil, s.listPendingErr
	}
	var result []domain.ImagePromotion
	for _, p := range s.data {
		if p.Spec.Source.ImageBuildRef != imageBuildRef {
			continue
		}
		reason := getPromotionReadyReasonWorker(p)
		if reason == string(domain.ImagePromotionConditionReasonWaitingForArtifacts) ||
			reason == string(domain.ImagePromotionConditionReasonAmendmentFailed) {
			var cp domain.ImagePromotion
			deepCopyJSON(p, &cp)
			result = append(result, cp)
		}
	}
	return result, nil
}
func (s *inMemoryPromotionStore) InitialMigration(ctx context.Context) error { return nil }

type inMemoryBuildStore struct {
	data map[string]*domain.ImageBuild
}

func newInMemoryBuildStore() *inMemoryBuildStore {
	return &inMemoryBuildStore{data: make(map[string]*domain.ImageBuild)}
}

func (s *inMemoryBuildStore) Create(ctx context.Context, orgId uuid.UUID, b *domain.ImageBuild) (*domain.ImageBuild, error) {
	var cp domain.ImageBuild
	deepCopyJSON(b, &cp)
	s.data[lo.FromPtr(b.Metadata.Name)] = &cp
	return &cp, nil
}
func (s *inMemoryBuildStore) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...ibstore.GetOption) (*domain.ImageBuild, error) {
	b, ok := s.data[name]
	if !ok {
		return nil, nil
	}
	var cp domain.ImageBuild
	deepCopyJSON(b, &cp)
	return &cp, nil
}
func (s *inMemoryBuildStore) List(ctx context.Context, orgId uuid.UUID, lp flightctlstore.ListParams, opts ...ibstore.ListOption) (*domain.ImageBuildList, error) {
	return &domain.ImageBuildList{}, nil
}
func (s *inMemoryBuildStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImageBuild, error) {
	return nil, nil
}
func (s *inMemoryBuildStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, b *domain.ImageBuild) (*domain.ImageBuild, error) {
	return b, nil
}
func (s *inMemoryBuildStore) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, ts time.Time) error {
	return nil
}
func (s *inMemoryBuildStore) UpdateLogs(ctx context.Context, orgId uuid.UUID, name, logs string) error {
	return nil
}
func (s *inMemoryBuildStore) GetLogs(ctx context.Context, orgId uuid.UUID, name string) (string, error) {
	return "", nil
}
func (s *inMemoryBuildStore) InitialMigration(ctx context.Context) error { return nil }

type inMemoryExportStore struct {
	data    map[string]*domain.ImageExport
	getErr  error // if set, Get returns this error
	listErr error // if set, List returns this error
}

func newInMemoryExportStore() *inMemoryExportStore {
	return &inMemoryExportStore{data: make(map[string]*domain.ImageExport)}
}

func (s *inMemoryExportStore) Create(ctx context.Context, orgId uuid.UUID, e *domain.ImageExport) (*domain.ImageExport, error) {
	var cp domain.ImageExport
	deepCopyJSON(e, &cp)
	s.data[lo.FromPtr(e.Metadata.Name)] = &cp
	return &cp, nil
}
func (s *inMemoryExportStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImageExport, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	e, ok := s.data[name]
	if !ok {
		return nil, nil
	}
	var cp domain.ImageExport
	deepCopyJSON(e, &cp)
	return &cp, nil
}
func (s *inMemoryExportStore) List(ctx context.Context, orgId uuid.UUID, lp flightctlstore.ListParams) (*domain.ImageExportList, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	var items []domain.ImageExport
	for _, e := range s.data {
		var cp domain.ImageExport
		deepCopyJSON(e, &cp)
		items = append(items, cp)
	}
	return &domain.ImageExportList{Items: items}, nil
}
func (s *inMemoryExportStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImageExport, error) {
	return nil, nil
}
func (s *inMemoryExportStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, e *domain.ImageExport) (*domain.ImageExport, error) {
	return e, nil
}
func (s *inMemoryExportStore) UpdateNextRetryAt(ctx context.Context, orgId uuid.UUID, name string, ts time.Time) error {
	return nil
}
func (s *inMemoryExportStore) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, ts time.Time) error {
	return nil
}
func (s *inMemoryExportStore) ListPendingRetry(ctx context.Context, orgId uuid.UUID, before time.Time) (*domain.ImageExportList, error) {
	return &domain.ImageExportList{}, nil
}
func (s *inMemoryExportStore) UpdateLogs(ctx context.Context, orgId uuid.UUID, name, logs string) error {
	return nil
}
func (s *inMemoryExportStore) GetLogs(ctx context.Context, orgId uuid.UUID, name string) (string, error) {
	return "", nil
}
func (s *inMemoryExportStore) InitialMigration(ctx context.Context) error { return nil }

// ---- dummy catalog writer ----

type dummyCatalogItemWriter struct {
	catalogs map[string]bool
	items    map[string]*coredomain.CatalogItem // "catalog/item"
}

func newDummyCatalogItemWriter() *dummyCatalogItemWriter {
	return &dummyCatalogItemWriter{
		catalogs: make(map[string]bool),
		items:    make(map[string]*coredomain.CatalogItem),
	}
}

func (d *dummyCatalogItemWriter) AddCatalog(name string) { d.catalogs[name] = true }

func (d *dummyCatalogItemWriter) GetItem(catalogName, itemName string) *coredomain.CatalogItem {
	return d.items[catalogName+"/"+itemName]
}

// dummyCatalogStoreAdapter bridges dummyCatalogItemWriter to catalogstore.Store.
type dummyCatalogStoreAdapter struct {
	w *dummyCatalogItemWriter
}

func (a *dummyCatalogStoreAdapter) Get(ctx context.Context, orgId uuid.UUID, name string) (*coredomain.Catalog, error) {
	if !a.w.catalogs[name] {
		return nil, nil
	}
	return &coredomain.Catalog{Metadata: coredomain.ObjectMeta{Name: lo.ToPtr(name)}}, nil
}

func (a *dummyCatalogStoreAdapter) GetItem(ctx context.Context, orgId uuid.UUID, catalogName, itemName string) (*coredomain.CatalogItem, error) {
	item := a.w.items[catalogName+"/"+itemName]
	if item == nil {
		return nil, fmt.Errorf("catalog item %s/%s not found", catalogName, itemName)
	}
	var cp coredomain.CatalogItem
	deepCopyJSON(item, &cp)
	return &cp, nil
}

func (a *dummyCatalogStoreAdapter) CreateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *coredomain.CatalogItem) (*coredomain.CatalogItem, error) {
	key := catalogName + "/" + lo.FromPtr(item.Metadata.Name)
	if _, exists := a.w.items[key]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	var cp coredomain.CatalogItem
	deepCopyJSON(item, &cp)
	a.w.items[key] = &cp
	return &cp, nil
}
func (a *dummyCatalogStoreAdapter) UpdateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *coredomain.CatalogItem) (*coredomain.CatalogItem, error) {
	key := catalogName + "/" + lo.FromPtr(item.Metadata.Name)
	var cp coredomain.CatalogItem
	deepCopyJSON(item, &cp)
	a.w.items[key] = &cp
	return &cp, nil
}
func (a *dummyCatalogStoreAdapter) CreateOrUpdateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *coredomain.CatalogItem) (*coredomain.CatalogItem, bool, error) {
	key := catalogName + "/" + lo.FromPtr(item.Metadata.Name)
	_, existed := a.w.items[key]
	var cp coredomain.CatalogItem
	deepCopyJSON(item, &cp)
	a.w.items[key] = &cp
	return &cp, !existed, nil
}
func (a *dummyCatalogStoreAdapter) Create(ctx context.Context, orgId uuid.UUID, catalog *coredomain.Catalog, cb flightctlstore.EventCallback) (*coredomain.Catalog, error) {
	return nil, nil
}
func (a *dummyCatalogStoreAdapter) Update(ctx context.Context, orgId uuid.UUID, catalog *coredomain.Catalog, cb flightctlstore.EventCallback) (*coredomain.Catalog, error) {
	return nil, nil
}
func (a *dummyCatalogStoreAdapter) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, catalog *coredomain.Catalog, cb flightctlstore.EventCallback) (*coredomain.Catalog, bool, error) {
	return nil, false, nil
}
func (a *dummyCatalogStoreAdapter) List(ctx context.Context, orgId uuid.UUID, lp flightctlstore.ListParams) (*coredomain.CatalogList, error) {
	return &coredomain.CatalogList{}, nil
}
func (a *dummyCatalogStoreAdapter) Delete(ctx context.Context, orgId uuid.UUID, name string, rc flightctlstore.RemoveOwnerCallback, cb flightctlstore.EventCallback) error {
	return nil
}
func (a *dummyCatalogStoreAdapter) UpdateStatus(ctx context.Context, orgId uuid.UUID, catalog *coredomain.Catalog, cb flightctlstore.EventCallback) (*coredomain.Catalog, error) {
	return nil, nil
}
func (a *dummyCatalogStoreAdapter) Count(ctx context.Context, orgId uuid.UUID, lp flightctlstore.ListParams) (int64, error) {
	return 0, nil
}
func (a *dummyCatalogStoreAdapter) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	return nil
}
func (a *dummyCatalogStoreAdapter) UnsetItemOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	return nil
}
func (a *dummyCatalogStoreAdapter) ListAllItems(ctx context.Context, orgId uuid.UUID, lp flightctlstore.ListParams) (*coredomain.CatalogItemList, error) {
	return &coredomain.CatalogItemList{}, nil
}
func (a *dummyCatalogStoreAdapter) ListItems(ctx context.Context, orgId uuid.UUID, catalogName string, lp flightctlstore.ListParams) (*coredomain.CatalogItemList, error) {
	return &coredomain.CatalogItemList{}, nil
}
func (a *dummyCatalogStoreAdapter) DeleteItem(ctx context.Context, orgId uuid.UUID, catalogName, itemName string) error {
	return nil
}
func (a *dummyCatalogStoreAdapter) InitialMigration(ctx context.Context) error { return nil }

// ---- helpers ----

func testImageRef(name string) string { return "quay.io/test-org/" + name + ":v1.0" }

func makeCompletedBuild(name, digest string) *domain.ImageBuild {
	ref := testImageRef(name)
	now := time.Now()
	return &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr(name), CreationTimestamp: &now},
		Spec: api.ImageBuildSpec{
			Source:      api.ImageBuildSource{Repository: "src", ImageName: name, ImageTag: "v1.0"},
			Destination: api.ImageBuildDestination{Repository: "dst", ImageName: name, ImageTag: "v1.0"},
		},
		Status: &api.ImageBuildStatus{
			ImageReference: lo.ToPtr(ref),
			ManifestDigest: lo.ToPtr(digest),
			Conditions: &[]api.ImageBuildCondition{
				{Type: api.ImageBuildConditionTypeReady, Status: coredomain.ConditionStatusTrue, Reason: string(api.ImageBuildConditionReasonCompleted)},
			},
		},
	}
}

func makeCompletedExport(name, buildRef string, format domain.ExportFormatType, digest string) *domain.ImageExport {
	now := time.Now()
	src := api.ImageExportSource{}
	_ = src.FromImageBuildRefSource(api.ImageBuildRefSource{Type: api.ImageBuildRefSourceTypeImageBuild, ImageBuildRef: buildRef})
	return &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr(name), CreationTimestamp: &now},
		Spec:     api.ImageExportSpec{Source: src, Format: format},
		Status: &api.ImageExportStatus{
			ManifestDigest: lo.ToPtr(digest),
			Conditions: &[]api.ImageExportCondition{
				{Type: api.ImageExportConditionTypeReady, Status: coredomain.ConditionStatusTrue, Reason: string(api.ImageExportConditionReasonCompleted)},
			},
		},
	}
}

func makeWaitingPromotion(name, buildRef, catalogName, itemName, version string) *domain.ImagePromotion {
	target := api.ImagePromotionTarget{}
	_ = target.FromNewCatalogItemTarget(api.NewCatalogItemTarget{
		Type:            api.NewCatalogItem,
		CatalogName:     catalogName,
		CatalogItemName: itemName,
		Version:         version,
	})
	p := &api.ImagePromotion{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr(name)},
		Spec: api.ImagePromotionSpec{
			Source: api.ImagePromotionSource{ImageBuildRef: buildRef},
			Target: target,
		},
		Status: &api.ImagePromotionStatus{},
	}
	setPromotionReadyCondition(p, domain.ImagePromotionConditionReasonWaitingForArtifacts,
		conditionMessageForReasonWorker(domain.ImagePromotionConditionReasonWaitingForArtifacts))
	return p
}

func setPromotionReadyCondition(p *domain.ImagePromotion, reason domain.ImagePromotionConditionReason, message string) {
	if p.Status == nil {
		p.Status = &domain.ImagePromotionStatus{}
	}
	if p.Status.Conditions == nil {
		p.Status.Conditions = &[]domain.ImagePromotionCondition{}
	}
	status := coredomain.ConditionStatusFalse
	if reason == domain.ImagePromotionConditionReasonCompleted {
		status = coredomain.ConditionStatusTrue
	}
	domain.SetImagePromotionStatusCondition(p.Status.Conditions, domain.ImagePromotionCondition{
		Type:               domain.ImagePromotionConditionTypeReady,
		Status:             status,
		Reason:             string(reason),
		Message:            message,
		LastTransitionTime: time.Now().UTC(),
	})
}

// testIBService wraps the three in-memory stores behind the imagebuilderapi.Service interface.
type testIBService struct {
	promotions *inMemoryPromotionStore
	builds     *inMemoryBuildStore
	exports    *inMemoryExportStore
}

func newTestIBService(orgID uuid.UUID) *testIBService {
	return &testIBService{
		promotions: newInMemoryPromotionStore(orgID),
		builds:     newInMemoryBuildStore(),
		exports:    newInMemoryExportStore(),
	}
}

func (s *testIBService) ImagePromotion() imagebuilderapi.ImagePromotionService {
	return &testPromotionSvc{s.promotions}
}
func (s *testIBService) ImageBuild() imagebuilderapi.ImageBuildService {
	return &testBuildSvc{s.builds}
}
func (s *testIBService) ImageExport() imagebuilderapi.ImageExportService {
	return &testExportSvc{s.exports}
}

// testPromotionSvc implements imagebuilderapi.ImagePromotionService backed by inMemoryPromotionStore.
type testPromotionSvc struct{ store *inMemoryPromotionStore }

func (s *testPromotionSvc) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, domain.Status) {
	p, err := s.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, domain.Status{Code: 500, Message: err.Error()}
	}
	if p == nil {
		return nil, domain.Status{Code: 404, Message: name + " not found"}
	}
	return p, domain.Status{Code: 200}
}
func (s *testPromotionSvc) Create(_ context.Context, _ uuid.UUID, p domain.ImagePromotion) (*domain.ImagePromotion, domain.Status) {
	panic("not used in tests")
}
func (s *testPromotionSvc) List(_ context.Context, _ uuid.UUID, _ domain.ListImagePromotionsParams) (*domain.ImagePromotionList, domain.Status) {
	panic("not used in tests")
}
func (s *testPromotionSvc) Delete(_ context.Context, _ uuid.UUID, _ string) domain.Status {
	panic("not used in tests")
}
func (s *testPromotionSvc) Replace(_ context.Context, _ uuid.UUID, _ string, _ domain.ImagePromotion) (*domain.ImagePromotion, domain.Status) {
	panic("not used in tests")
}
func (s *testPromotionSvc) Patch(_ context.Context, _ uuid.UUID, _ string, _ domain.PatchRequest) (*domain.ImagePromotion, domain.Status) {
	panic("not used in tests")
}
func (s *testPromotionSvc) UpdateStatus(ctx context.Context, orgId uuid.UUID, p *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	return s.store.UpdateStatus(ctx, orgId, p)
}
func (s *testPromotionSvc) ListPendingForBuild(ctx context.Context, orgId uuid.UUID, ref string) ([]domain.ImagePromotion, error) {
	return s.store.ListPendingForBuild(ctx, orgId, ref)
}

// testBuildSvc implements imagebuilderapi.ImageBuildService backed by inMemoryBuildStore.
type testBuildSvc struct{ store *inMemoryBuildStore }

func (s *testBuildSvc) Get(ctx context.Context, orgId uuid.UUID, name string, withExports bool) (*domain.ImageBuild, domain.Status) {
	b, err := s.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, domain.Status{Code: 500, Message: err.Error()}
	}
	if b == nil {
		return nil, domain.Status{Code: 404, Message: name + " not found"}
	}
	return b, domain.Status{Code: 200}
}
func (s *testBuildSvc) Create(_ context.Context, _ uuid.UUID, _ domain.ImageBuild) (*domain.ImageBuild, domain.Status) {
	panic("not used in tests")
}
func (s *testBuildSvc) List(_ context.Context, _ uuid.UUID, _ domain.ListImageBuildsParams) (*domain.ImageBuildList, domain.Status) {
	panic("not used in tests")
}
func (s *testBuildSvc) Delete(_ context.Context, _ uuid.UUID, _ string) domain.Status {
	panic("not used in tests")
}
func (s *testBuildSvc) NewVersion(_ context.Context, _ uuid.UUID, _ string, _ domain.ImageBuildNewVersionRequest) (*domain.ImageBuild, domain.Status) {
	panic("not used in tests")
}
func (s *testBuildSvc) Cancel(_ context.Context, _ uuid.UUID, _ string) (*domain.ImageBuild, error) {
	panic("not used in tests")
}
func (s *testBuildSvc) CancelWithReason(_ context.Context, _ uuid.UUID, _ string, _ string) (*domain.ImageBuild, error) {
	panic("not used in tests")
}
func (s *testBuildSvc) GetLogs(_ context.Context, _ uuid.UUID, _ string, _ bool) (imagebuilderapi.LogStreamReader, string, domain.Status) {
	panic("not used in tests")
}
func (s *testBuildSvc) UpdateStatus(ctx context.Context, orgId uuid.UUID, b *domain.ImageBuild) (*domain.ImageBuild, error) {
	return s.store.UpdateStatus(ctx, orgId, b)
}
func (s *testBuildSvc) UpdateLastSeen(_ context.Context, _ uuid.UUID, _ string, _ time.Time) error {
	return nil
}
func (s *testBuildSvc) UpdateLogs(_ context.Context, _ uuid.UUID, _ string, _ string) error {
	return nil
}

// testExportSvc implements imagebuilderapi.ImageExportService backed by inMemoryExportStore.
type testExportSvc struct {
	store *inMemoryExportStore
}

func (s *testExportSvc) ListCompletedForBuild(ctx context.Context, orgId uuid.UUID, imageBuildRef string, format domain.ExportFormatType) (*domain.ImageExport, error) {
	for _, e := range s.store.data {
		srcType, err := e.Spec.Source.Discriminator()
		if err != nil || srcType != string(domain.ImageExportSourceTypeImageBuild) {
			continue
		}
		src, err := e.Spec.Source.AsImageBuildRefSource()
		if err != nil || src.ImageBuildRef != imageBuildRef {
			continue
		}
		if string(e.Spec.Format) != string(format) {
			continue
		}
		if e.Status == nil || e.Status.Conditions == nil {
			continue
		}
		ready := domain.FindImageExportStatusCondition(*e.Status.Conditions, domain.ImageExportConditionTypeReady)
		if ready != nil && ready.Reason == string(domain.ImageExportConditionReasonCompleted) {
			var cp domain.ImageExport
			deepCopyJSON(e, &cp)
			return &cp, nil
		}
	}
	return nil, nil
}
func (s *testExportSvc) Create(_ context.Context, _ uuid.UUID, _ domain.ImageExport) (*domain.ImageExport, domain.Status) {
	panic("not used in tests")
}
func (s *testExportSvc) Get(_ context.Context, _ uuid.UUID, _ string) (*domain.ImageExport, domain.Status) {
	panic("not used in tests")
}
func (s *testExportSvc) List(ctx context.Context, orgId uuid.UUID, _ domain.ListImageExportsParams) (*domain.ImageExportList, domain.Status) {
	result, err := s.store.List(ctx, orgId, flightctlstore.ListParams{})
	if err != nil {
		return nil, domain.Status{Code: 500, Message: err.Error()}
	}
	return result, domain.Status{Code: 200}
}
func (s *testExportSvc) Delete(_ context.Context, _ uuid.UUID, _ string) domain.Status {
	panic("not used in tests")
}
func (s *testExportSvc) Cancel(_ context.Context, _ uuid.UUID, _ string) (*domain.ImageExport, error) {
	panic("not used in tests")
}
func (s *testExportSvc) CancelWithReason(_ context.Context, _ uuid.UUID, _ string, _ string) (*domain.ImageExport, error) {
	panic("not used in tests")
}
func (s *testExportSvc) Download(_ context.Context, _ uuid.UUID, _ string) (*imagebuilderapi.ImageExportDownload, error) {
	panic("not used in tests")
}
func (s *testExportSvc) GetLogs(_ context.Context, _ uuid.UUID, _ string, _ bool) (imagebuilderapi.LogStreamReader, string, domain.Status) {
	panic("not used in tests")
}
func (s *testExportSvc) UpdateStatus(ctx context.Context, orgId uuid.UUID, e *domain.ImageExport) (*domain.ImageExport, error) {
	return s.store.UpdateStatus(ctx, orgId, e)
}
func (s *testExportSvc) UpdateLastSeen(_ context.Context, _ uuid.UUID, _ string, _ time.Time) error {
	return nil
}
func (s *testExportSvc) UpdateLogs(_ context.Context, _ uuid.UUID, _ string, _ string) error {
	return nil
}

// testIBStore is a minimal imagebuilderstore.Store backed by the in-memory stores.
type testIBStore struct {
	builds     *inMemoryBuildStore
	exports    *inMemoryExportStore
	promotions *inMemoryPromotionStore
}

func (s *testIBStore) ImageBuild() ibstore.ImageBuildStore         { return s.builds }
func (s *testIBStore) ImageExport() ibstore.ImageExportStore       { return s.exports }
func (s *testIBStore) ImagePromotion() ibstore.ImagePromotionStore { return s.promotions }
func (s *testIBStore) RunMigrations(_ context.Context) error       { return nil }
func (s *testIBStore) Ping() error                                 { return nil }
func (s *testIBStore) Close() error                                { return nil }

func newTestConsumer(
	svc *testIBService,
	catalogStore catalogstore.Store,
) *Consumer {
	ibStore := &testIBStore{
		builds:     svc.builds,
		exports:    svc.exports,
		promotions: svc.promotions,
	}
	return &Consumer{
		store:               ibStore,
		imageBuilderService: svc,
		catalogStore:        catalogStore,
		log:                 log.InitLogs(),
	}
}

// requirePromotionReasonWorker reads the Ready condition reason from the promotion store.
func requirePromotionReasonWorker(t *testing.T, store *inMemoryPromotionStore, name string, expected domain.ImagePromotionConditionReason) {
	t.Helper()
	p, err := store.Get(context.Background(), store.orgID, name)
	require.NoError(t, err)
	require.NotNil(t, p, "promotion %s not found", name)
	reason := getPromotionReadyReasonWorker(p)
	require.Equal(t, string(expected), reason, "promotion %s: expected reason %s got %s", name, expected, reason)
}

// ---- tests ----

// TestEvaluator_NewCatalogItem verifies that a WaitingForArtifacts promotion transitions to
// Completed when a completed ImageBuild is available.
func TestEvaluator_NewCatalogItem(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	require := require.New(t)

	svc := newTestIBService(orgID)
	catalogWriter := newDummyCatalogItemWriter()
	catalogWriter.AddCatalog("my-catalog")
	catalogStore := &dummyCatalogStoreAdapter{catalogWriter}

	build := makeCompletedBuild("build-1", "sha256:aabb")
	_, _ = svc.builds.Create(ctx, orgID, build)

	promotion := makeWaitingPromotion("promo-1", "build-1", "my-catalog", "my-app", "1.0.0")
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	consumer := newTestConsumer(svc, catalogStore)
	require.NoError(consumer.evaluateAndTransition(ctx, orgID, promotion, build))

	requirePromotionReasonWorker(t, svc.promotions, "promo-1", domain.ImagePromotionConditionReasonCompleted)

	item := catalogWriter.GetItem("my-catalog", "my-app")
	require.NotNil(item, "CatalogItem should be created")
	require.Len(item.Spec.Versions, 1)
	require.Equal("1.0.0", item.Spec.Versions[0].Version)
	require.Equal([]string{"testing"}, item.Spec.Versions[0].Channels)
	require.Equal("v1.0", item.Spec.Versions[0].References[string(coredomain.CatalogItemArtifactTypeContainer)])
}

// TestEvaluator_NewCatalogItemWithDisplayName verifies that displayName from the promotion target
// is persisted on the created CatalogItem spec.
func TestEvaluator_NewCatalogItemWithDisplayName(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	require := require.New(t)

	svc := newTestIBService(orgID)
	catalogWriter := newDummyCatalogItemWriter()
	catalogWriter.AddCatalog("my-catalog")
	catalogStore := &dummyCatalogStoreAdapter{catalogWriter}

	build := makeCompletedBuild("build-1", "sha256:aabb")
	_, _ = svc.builds.Create(ctx, orgID, build)

	target := api.ImagePromotionTarget{}
	_ = target.FromNewCatalogItemTarget(api.NewCatalogItemTarget{
		Type:            api.NewCatalogItem,
		CatalogName:     "my-catalog",
		CatalogItemName: "my-app",
		Version:         "1.0.0",
		DisplayName:     lo.ToPtr("My Pretty Name"),
	})
	promotion := &api.ImagePromotion{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("promo-display-name")},
		Spec: api.ImagePromotionSpec{
			Source: api.ImagePromotionSource{ImageBuildRef: "build-1"},
			Target: target,
		},
		Status: &api.ImagePromotionStatus{},
	}
	setPromotionReadyCondition(promotion, domain.ImagePromotionConditionReasonWaitingForArtifacts,
		conditionMessageForReasonWorker(domain.ImagePromotionConditionReasonWaitingForArtifacts))
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	consumer := newTestConsumer(svc, catalogStore)
	require.NoError(consumer.evaluateAndTransition(ctx, orgID, promotion, build))

	item := catalogWriter.GetItem("my-catalog", "my-app")
	require.NotNil(item, "CatalogItem should be created")
	require.NotNil(item.Spec.DisplayName)
	require.Equal("My Pretty Name", *item.Spec.DisplayName)
}

// TestEvaluator_ExportsPending verifies that a promotion with export formats stays in
// WaitingForArtifacts when the exports are not yet complete.
func TestEvaluator_ExportsPending(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	require := require.New(t)

	svc := newTestIBService(orgID)
	catalogWriter := newDummyCatalogItemWriter()
	catalogWriter.AddCatalog("my-catalog")
	catalogStore := &dummyCatalogStoreAdapter{catalogWriter}

	build := makeCompletedBuild("build-1", "sha256:aabb")
	_, _ = svc.builds.Create(ctx, orgID, build)

	target := api.ImagePromotionTarget{}
	_ = target.FromNewCatalogItemTarget(api.NewCatalogItemTarget{
		Type: api.NewCatalogItem, CatalogName: "my-catalog", CatalogItemName: "my-app", Version: "1.0.0",
	})
	promotion := &api.ImagePromotion{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("promo-pending")},
		Spec: api.ImagePromotionSpec{
			Source: api.ImagePromotionSource{
				ImageBuildRef: "build-1",
				ExportFormats: lo.ToPtr([]api.ExportFormatType{api.ExportFormatTypeQCOW2}),
			},
			Target: target,
		},
		Status: &api.ImagePromotionStatus{},
	}
	setPromotionReadyCondition(promotion, domain.ImagePromotionConditionReasonWaitingForArtifacts,
		conditionMessageForReasonWorker(domain.ImagePromotionConditionReasonWaitingForArtifacts))
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	consumer := newTestConsumer(svc, catalogStore)
	require.NoError(consumer.evaluateAndTransition(ctx, orgID, promotion, build))

	// Still waiting for export.
	requirePromotionReasonWorker(t, svc.promotions, "promo-pending", domain.ImagePromotionConditionReasonWaitingForArtifacts)

	// CatalogItem must NOT be created yet.
	require.Nil(catalogWriter.GetItem("my-catalog", "my-app"), "CatalogItem must not be created while exports are pending")
}

// TestEvaluator_ExportsReady verifies that when all exports complete, the promotion transitions
// to Completed and the CatalogItem is created with export references.
func TestEvaluator_ExportsReady(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	require := require.New(t)

	svc := newTestIBService(orgID)
	catalogWriter := newDummyCatalogItemWriter()
	catalogWriter.AddCatalog("my-catalog")
	catalogStore := &dummyCatalogStoreAdapter{catalogWriter}

	build := makeCompletedBuild("build-2", "sha256:ccdd")
	_, _ = svc.builds.Create(ctx, orgID, build)

	export := makeCompletedExport("exp-qcow2", "build-2", domain.ExportFormatTypeQCOW2, "sha256:qcow2digest")
	_, _ = svc.exports.Create(ctx, orgID, export)

	target := api.ImagePromotionTarget{}
	_ = target.FromNewCatalogItemTarget(api.NewCatalogItemTarget{
		Type: api.NewCatalogItem, CatalogName: "my-catalog", CatalogItemName: "my-app", Version: "1.0.0",
	})
	promotion := &api.ImagePromotion{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("promo-ready")},
		Spec: api.ImagePromotionSpec{
			Source: api.ImagePromotionSource{
				ImageBuildRef: "build-2",
				ExportFormats: lo.ToPtr([]api.ExportFormatType{api.ExportFormatTypeQCOW2}),
			},
			Target: target,
		},
		Status: &api.ImagePromotionStatus{},
	}
	setPromotionReadyCondition(promotion, domain.ImagePromotionConditionReasonWaitingForArtifacts,
		conditionMessageForReasonWorker(domain.ImagePromotionConditionReasonWaitingForArtifacts))
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	consumer := newTestConsumer(svc, catalogStore)
	require.NoError(consumer.evaluateAndTransition(ctx, orgID, promotion, build))

	requirePromotionReasonWorker(t, svc.promotions, "promo-ready", domain.ImagePromotionConditionReasonCompleted)

	item := catalogWriter.GetItem("my-catalog", "my-app")
	require.NotNil(item, "CatalogItem should be created")
	require.Len(item.Spec.Versions, 1)
	refs := item.Spec.Versions[0].References
	require.Equal("v1.0", refs[string(coredomain.CatalogItemArtifactTypeContainer)], "container reference must be the image tag")
	require.Equal("sha256:qcow2digest", refs[string(coredomain.CatalogItemArtifactTypeQcow2)], "qcow2 reference must be the export digest")
}

// TestEvaluator_FailPromotionsForBuild verifies that failPromotionsForBuild transitions
// all waiting promotions to Failed.
func TestEvaluator_FailPromotionsForBuild(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()

	svc := newTestIBService(orgID)
	catalogWriter := newDummyCatalogItemWriter()
	catalogStore := &dummyCatalogStoreAdapter{catalogWriter}

	for i, name := range []string{"promo-a", "promo-b"} {
		p := makeWaitingPromotion(name, "build-fail", "cat", "item"+string(rune('a'+i)), "1.0.0")
		_, _ = svc.promotions.Create(ctx, orgID, p)
	}

	consumer := newTestConsumer(svc, catalogStore)
	err := consumer.failPromotionsForBuild(ctx, orgID, "build-fail",
		domain.ImagePromotionConditionReasonBuildFailed, "build failed")
	require.NoError(t, err)

	requirePromotionReasonWorker(t, svc.promotions, "promo-a", domain.ImagePromotionConditionReasonBuildFailed)
	requirePromotionReasonWorker(t, svc.promotions, "promo-b", domain.ImagePromotionConditionReasonBuildFailed)
}

// TestEvaluator_AppendVersion verifies that an ExistingCatalogItem target appends a new version.
func TestEvaluator_AppendVersion(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	require := require.New(t)

	svc := newTestIBService(orgID)
	catalogWriter := newDummyCatalogItemWriter()
	catalogWriter.AddCatalog("cat")
	catalogStore := &dummyCatalogStoreAdapter{catalogWriter}

	// Pre-populate the CatalogItem with version 1.0.0.
	systemCategory := coredomain.CatalogItemCategorySystem
	existingItem := &coredomain.CatalogItem{
		Metadata: coredomain.CatalogItemMeta{Name: lo.ToPtr("my-app")},
		Spec: coredomain.CatalogItemSpec{
			Type:     coredomain.CatalogItemTypeOS,
			Category: &systemCategory,
			Artifacts: []coredomain.CatalogItemArtifact{
				{Type: coredomain.CatalogItemArtifactTypeContainer, Uri: "quay.io/test-org/build-3"},
			},
			Versions: []coredomain.CatalogItemVersion{
				{Version: "1.0.0", Channels: []string{"stable"}, References: map[string]string{string(coredomain.CatalogItemArtifactTypeContainer): "v0.9"}},
			},
		},
	}
	_, _ = catalogStore.CreateItem(ctx, orgID, "cat", existingItem)

	build := makeCompletedBuild("build-3", "sha256:eeff")
	_, _ = svc.builds.Create(ctx, orgID, build)

	target := api.ImagePromotionTarget{}
	_ = target.FromExistingCatalogItemTarget(api.ExistingCatalogItemTarget{
		Type: api.ExistingCatalogItem, CatalogName: "cat", CatalogItemName: "my-app", Version: "2.0.0",
	})
	promotion := &api.ImagePromotion{
		Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr("promo-append")},
		Spec:     api.ImagePromotionSpec{Source: api.ImagePromotionSource{ImageBuildRef: "build-3"}, Target: target},
		Status:   &api.ImagePromotionStatus{},
	}
	setPromotionReadyCondition(promotion, domain.ImagePromotionConditionReasonWaitingForArtifacts, "")
	_, _ = svc.promotions.Create(ctx, orgID, promotion)

	consumer := newTestConsumer(svc, catalogStore)
	require.NoError(consumer.evaluateAndTransition(ctx, orgID, promotion, build))

	requirePromotionReasonWorker(t, svc.promotions, "promo-append", domain.ImagePromotionConditionReasonCompleted)

	item := catalogWriter.GetItem("cat", "my-app")
	require.NotNil(item)
	require.Len(item.Spec.Versions, 2, "should have both old and new version")

	versions := map[string]bool{}
	for _, v := range item.Spec.Versions {
		versions[v.Version] = true
	}
	require.True(versions["1.0.0"], "original version should still be present")
	require.True(versions["2.0.0"], "new version should be appended")
}

// TestEvaluator_PublishingRetry tests that a promotion stuck in Publishing (worker crashed
// after UpdateStatus but before completing catalog writes) is correctly recovered.
// References are deterministic from immutable build/export digests, so matching
// version+references in the catalog unambiguously identifies our own previous write.
func TestEvaluator_PublishingRetry(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()

	systemCategory := coredomain.CatalogItemCategorySystem

	makePublishingPromotion := func(name, buildRef, catalogName, itemName, version string) *api.ImagePromotion {
		target := api.ImagePromotionTarget{}
		_ = target.FromNewCatalogItemTarget(api.NewCatalogItemTarget{
			Type: api.NewCatalogItem, CatalogName: catalogName, CatalogItemName: itemName, Version: version,
		})
		p := &api.ImagePromotion{
			Metadata: v1beta1.ObjectMeta{Name: lo.ToPtr(name)},
			Spec:     api.ImagePromotionSpec{Source: api.ImagePromotionSource{ImageBuildRef: buildRef}, Target: target},
			Status:   &api.ImagePromotionStatus{},
		}
		setPromotionReadyCondition(p, domain.ImagePromotionConditionReasonPublishing, "")
		return p
	}

	t.Run("When catalog item not yet written it should complete successfully", func(t *testing.T) {
		svc := newTestIBService(orgID)
		catalogWriter := newDummyCatalogItemWriter()
		catalogWriter.AddCatalog("cat")
		catalogStore := &dummyCatalogStoreAdapter{catalogWriter}

		build := makeCompletedBuild("build-1", "sha256:aabb")
		_, _ = svc.builds.Create(ctx, orgID, build)

		promotion := makePublishingPromotion("promo", "build-1", "cat", "my-app", "1.0.0")
		_, _ = svc.promotions.Create(ctx, orgID, promotion)

		require.NoError(t, newTestConsumer(svc, catalogStore).evaluateAndTransition(ctx, orgID, promotion, build))

		requirePromotionReasonWorker(t, svc.promotions, "promo", domain.ImagePromotionConditionReasonCompleted)
		require.NotNil(t, catalogWriter.GetItem("cat", "my-app"))
	})

	t.Run("When catalog item already written with matching references it should complete idempotently", func(t *testing.T) {
		svc := newTestIBService(orgID)
		catalogWriter := newDummyCatalogItemWriter()
		catalogWriter.AddCatalog("cat")
		catalogStore := &dummyCatalogStoreAdapter{catalogWriter}

		build := makeCompletedBuild("build-1", "sha256:aabb")
		_, _ = svc.builds.Create(ctx, orgID, build)

		// Simulate a previous attempt that wrote the catalog item but crashed before Completed.
		preExisting := &coredomain.CatalogItem{
			Metadata: coredomain.CatalogItemMeta{Name: lo.ToPtr("my-app")},
			Spec: coredomain.CatalogItemSpec{
				Type:     coredomain.CatalogItemTypeOS,
				Category: &systemCategory,
				Artifacts: []coredomain.CatalogItemArtifact{
					{Type: coredomain.CatalogItemArtifactTypeContainer, Uri: "quay.io/test-org/build-1"},
				},
				Versions: []coredomain.CatalogItemVersion{
					{Version: "1.0.0", Channels: []string{"testing"}, References: map[string]string{
						string(coredomain.CatalogItemArtifactTypeContainer): build.Spec.Destination.ImageTag,
					}},
				},
			},
		}
		catalogWriter.items["cat/my-app"] = preExisting

		promotion := makePublishingPromotion("promo", "build-1", "cat", "my-app", "1.0.0")
		_, _ = svc.promotions.Create(ctx, orgID, promotion)

		require.NoError(t, newTestConsumer(svc, catalogStore).evaluateAndTransition(ctx, orgID, promotion, build))

		requirePromotionReasonWorker(t, svc.promotions, "promo", domain.ImagePromotionConditionReasonCompleted)
	})

	t.Run("When catalog item already exists with different references it should fail", func(t *testing.T) {
		svc := newTestIBService(orgID)
		catalogWriter := newDummyCatalogItemWriter()
		catalogWriter.AddCatalog("cat")
		catalogStore := &dummyCatalogStoreAdapter{catalogWriter}

		build := makeCompletedBuild("build-1", "sha256:aabb")
		_, _ = svc.builds.Create(ctx, orgID, build)

		// Catalog item exists but with different content (written by someone else).
		conflicting := &coredomain.CatalogItem{
			Metadata: coredomain.CatalogItemMeta{Name: lo.ToPtr("my-app")},
			Spec: coredomain.CatalogItemSpec{
				Type:     coredomain.CatalogItemTypeOS,
				Category: &systemCategory,
				Artifacts: []coredomain.CatalogItemArtifact{
					{Type: coredomain.CatalogItemArtifactTypeContainer, Uri: "quay.io/other-org/other-image"},
				},
				Versions: []coredomain.CatalogItemVersion{
					{Version: "1.0.0", Channels: []string{"stable"}, References: map[string]string{
						string(coredomain.CatalogItemArtifactTypeContainer): "other-tag",
					}},
				},
			},
		}
		catalogWriter.items["cat/my-app"] = conflicting

		promotion := makePublishingPromotion("promo", "build-1", "cat", "my-app", "1.0.0")
		_, _ = svc.promotions.Create(ctx, orgID, promotion)

		require.NoError(t, newTestConsumer(svc, catalogStore).evaluateAndTransition(ctx, orgID, promotion, build))

		requirePromotionReasonWorker(t, svc.promotions, "promo", domain.ImagePromotionConditionReasonFailed)
	})
}
