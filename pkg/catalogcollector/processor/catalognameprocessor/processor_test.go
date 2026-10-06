package catalognameprocessor

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *logrus.Entry {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log.WithField("test", true)
}

// --- test helpers ---

func catalog(name string) apiv1alpha1.Catalog {
	return apiv1alpha1.Catalog{
		ApiVersion: "v1alpha1",
		Kind:       "Catalog",
		Metadata:   apiv1beta1.ObjectMeta{Name: lo.ToPtr(name)},
	}
}

func catalogItem(catalogName, itemName string) apiv1alpha1.CatalogItem {
	return apiv1alpha1.CatalogItem{
		ApiVersion: "v1alpha1",
		Kind:       "CatalogItem",
		Metadata: apiv1alpha1.CatalogItemMeta{
			Catalog: catalogName,
			Name:    lo.ToPtr(itemName),
		},
	}
}

func buildProcessor(t *testing.T, mappings map[string]string, next catalogcollector.Consumer) catalogcollector.Consumer {
	t.Helper()
	id, err := catalogcollector.ParseComponentID("catalogname/test")
	require.NoError(t, err)
	cfg := &Config{Mappings: mappings}
	require.NoError(t, cfg.Validate())
	return &processor{id: id, log: testLogger(), mappings: mappings, next: next}
}

type recordingConsumer struct {
	snapshots []*catalogcollector.CatalogSnapshot
}

func (r *recordingConsumer) Consume(_ context.Context, s *catalogcollector.CatalogSnapshot) error {
	r.snapshots = append(r.snapshots, s)
	return nil
}

type errorConsumer struct{ err error }

func (e *errorConsumer) Consume(_ context.Context, _ *catalogcollector.CatalogSnapshot) error {
	return e.err
}

// --- Factory tests ---

func TestFactory_Type(t *testing.T) {
	f := NewFactory()
	assert.Equal(t, catalogcollector.ComponentType("catalogname"), f.Type())
}

func TestFactory_CreateDefaultConfig_ReturnsTypedConfig(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig()
	require.NotNil(t, cfg)
	_, ok := cfg.(*Config)
	assert.True(t, ok, "CreateDefaultConfig must return *Config")
}

func TestFactory_CreateProcessor_TypeMismatch(t *testing.T) {
	f := NewFactory()
	id, _ := catalogcollector.ParseComponentID("catalogname")
	settings := catalogcollector.Settings{ID: id}
	_, err := f.CreateProcessor(
		context.Background(), settings, "not-a-config", &recordingConsumer{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected config type")
}

func TestFactory_CreateProcessor_ValidConfig(t *testing.T) {
	f := NewFactory()
	id, _ := catalogcollector.ParseComponentID("catalogname")
	settings := catalogcollector.Settings{ID: id}
	cfg := &Config{Mappings: map[string]string{"a": "b"}}
	consumer, err := f.CreateProcessor(
		context.Background(), settings, cfg, &recordingConsumer{},
	)
	require.NoError(t, err)
	assert.NotNil(t, consumer)
}

// --- Config validation tests ---

func TestConfig_Validate_EmptyMappings(t *testing.T) {
	cfg := &Config{Mappings: map[string]string{}}
	assert.ErrorContains(t, cfg.Validate(), "mappings must not be empty")
}

func TestConfig_Validate_NilMappings(t *testing.T) {
	cfg := &Config{}
	assert.ErrorContains(t, cfg.Validate(), "mappings must not be empty")
}

func TestConfig_Validate_EmptySourceName(t *testing.T) {
	cfg := &Config{Mappings: map[string]string{"": "target"}}
	assert.ErrorContains(t, cfg.Validate(), "source name must not be empty")
}

func TestConfig_Validate_EmptyDestinationName(t *testing.T) {
	cfg := &Config{Mappings: map[string]string{"source": ""}}
	assert.ErrorContains(t, cfg.Validate(), `destination for "source" must not be empty`)
}

func TestConfig_Validate_DuplicateTargets(t *testing.T) {
	cfg := &Config{Mappings: map[string]string{"a": "same", "b": "same"}}
	assert.ErrorContains(t, cfg.Validate(), `both target the same name "same"`)
}

func TestConfig_Validate_Valid(t *testing.T) {
	cfg := &Config{Mappings: map[string]string{"upstream": "downstream", "dev": "stage"}}
	assert.NoError(t, cfg.Validate())
}

// --- Transform: Catalog renaming ---

func TestTransform_CatalogRenamed(t *testing.T) {
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"upstream-models": "stage-models"}, rec)

	snap := &catalogcollector.CatalogSnapshot{
		Revision: "r1",
		Catalogs: []apiv1alpha1.Catalog{catalog("upstream-models")},
	}
	require.NoError(t, p.Consume(context.Background(), snap))

	require.Len(t, rec.snapshots, 1)
	require.Len(t, rec.snapshots[0].Catalogs, 1)
	assert.Equal(t, "stage-models", nameOf(rec.snapshots[0].Catalogs[0].Metadata.Name))
}

func TestTransform_UnmatchedCatalogUnchanged(t *testing.T) {
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"upstream-models": "stage-models"}, rec)

	snap := &catalogcollector.CatalogSnapshot{
		Revision: "r1",
		Catalogs: []apiv1alpha1.Catalog{catalog("other-catalog")},
	}
	require.NoError(t, p.Consume(context.Background(), snap))

	require.Len(t, rec.snapshots, 1)
	require.Len(t, rec.snapshots[0].Catalogs, 1)
	assert.Equal(t, "other-catalog", nameOf(rec.snapshots[0].Catalogs[0].Metadata.Name))
}

func TestTransform_MixedCatalogsRenamedAndUnchanged(t *testing.T) {
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"upstream-models": "stage-models"}, rec)

	snap := &catalogcollector.CatalogSnapshot{
		Revision: "r1",
		Catalogs: []apiv1alpha1.Catalog{
			catalog("upstream-models"),
			catalog("other-catalog"),
		},
	}
	require.NoError(t, p.Consume(context.Background(), snap))

	require.Len(t, rec.snapshots[0].Catalogs, 2)
	names := []string{
		nameOf(rec.snapshots[0].Catalogs[0].Metadata.Name),
		nameOf(rec.snapshots[0].Catalogs[1].Metadata.Name),
	}
	assert.Contains(t, names, "stage-models")
	assert.Contains(t, names, "other-catalog")
}

// --- Transform: CatalogItem renaming ---

func TestTransform_CatalogItemCatalogFieldRenamed(t *testing.T) {
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"upstream-models": "stage-models"}, rec)

	snap := &catalogcollector.CatalogSnapshot{
		Revision:     "r1",
		CatalogItems: []apiv1alpha1.CatalogItem{catalogItem("upstream-models", "item-a")},
	}
	require.NoError(t, p.Consume(context.Background(), snap))

	require.Len(t, rec.snapshots[0].CatalogItems, 1)
	assert.Equal(t, "stage-models", rec.snapshots[0].CatalogItems[0].Metadata.Catalog)
	assert.Equal(t, "item-a", nameOf(rec.snapshots[0].CatalogItems[0].Metadata.Name))
}

func TestTransform_UnmatchedCatalogItemUnchanged(t *testing.T) {
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"upstream-models": "stage-models"}, rec)

	snap := &catalogcollector.CatalogSnapshot{
		CatalogItems: []apiv1alpha1.CatalogItem{catalogItem("other-catalog", "item-b")},
	}
	require.NoError(t, p.Consume(context.Background(), snap))

	assert.Equal(t, "other-catalog", rec.snapshots[0].CatalogItems[0].Metadata.Catalog)
}

func TestTransform_RevisionPreserved(t *testing.T) {
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"a": "b"}, rec)

	snap := &catalogcollector.CatalogSnapshot{Revision: "rev-42"}
	require.NoError(t, p.Consume(context.Background(), snap))

	assert.Equal(t, "rev-42", rec.snapshots[0].Revision)
}

// --- Immutability ---

func TestTransform_InputSnapshotNotMutated(t *testing.T) {
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"upstream-models": "stage-models"}, rec)

	originalName := "upstream-models"
	snap := &catalogcollector.CatalogSnapshot{
		Catalogs:     []apiv1alpha1.Catalog{catalog(originalName)},
		CatalogItems: []apiv1alpha1.CatalogItem{catalogItem(originalName, "item-a")},
	}

	require.NoError(t, p.Consume(context.Background(), snap))

	// original slice and pointers must be untouched
	assert.Equal(t, "upstream-models", nameOf(snap.Catalogs[0].Metadata.Name))
	assert.Equal(t, "upstream-models", snap.CatalogItems[0].Metadata.Catalog)
}

func TestTransform_OutputIsNewSnapshot(t *testing.T) {
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"a": "b"}, rec)

	snap := &catalogcollector.CatalogSnapshot{
		Catalogs: []apiv1alpha1.Catalog{catalog("a")},
	}
	require.NoError(t, p.Consume(context.Background(), snap))

	assert.NotSame(t, snap, rec.snapshots[0], "output must be a new snapshot, not the input")
}

// --- Downstream interaction ---

func TestConsume_DownstreamReceivesExactlyOneSnapshot(t *testing.T) {
	var callCount atomic.Int32
	counter := &countingConsumer{count: &callCount}
	p := buildProcessor(t, map[string]string{"a": "b"}, counter)

	snap := &catalogcollector.CatalogSnapshot{}
	require.NoError(t, p.Consume(context.Background(), snap))
	assert.Equal(t, int32(1), callCount.Load())
}

type countingConsumer struct{ count *atomic.Int32 }

func (c *countingConsumer) Consume(_ context.Context, _ *catalogcollector.CatalogSnapshot) error {
	c.count.Add(1)
	return nil
}

func TestConsume_DownstreamErrorPropagates(t *testing.T) {
	sentinel := errors.New("downstream failure")
	p := buildProcessor(t, map[string]string{"a": "b"}, &errorConsumer{err: sentinel})

	err := p.Consume(context.Background(), &catalogcollector.CatalogSnapshot{})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

// --- Collision detection ---

func TestTransform_CatalogCollisionDetected(t *testing.T) {
	// "a" → "target" and "b" → "target" would produce two Catalogs named "target"
	// This is caught by Config.Validate, but let's also verify the runtime check.
	// We bypass Validate by constructing the processor directly.
	id, _ := catalogcollector.ParseComponentID("catalogname/test")
	p := &processor{
		id:       id,
		log:      testLogger(),
		mappings: map[string]string{"a": "target", "b": "target"},
		next:     &recordingConsumer{},
	}
	snap := &catalogcollector.CatalogSnapshot{
		Catalogs: []apiv1alpha1.Catalog{catalog("a"), catalog("b")},
	}
	err := p.Consume(context.Background(), snap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate Catalog")
}

func TestTransform_CatalogItemCollisionDetected(t *testing.T) {
	// Two items in "a" and "b" where both map to "target": same item name → collision
	id, _ := catalogcollector.ParseComponentID("catalogname/test")
	p := &processor{
		id:       id,
		log:      testLogger(),
		mappings: map[string]string{"a": "target", "b": "target"},
		next:     &recordingConsumer{},
	}
	snap := &catalogcollector.CatalogSnapshot{
		CatalogItems: []apiv1alpha1.CatalogItem{
			catalogItem("a", "item-x"),
			catalogItem("b", "item-x"),
		},
	}
	err := p.Consume(context.Background(), snap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate CatalogItem")
}

func TestTransform_CatalogItemsInDifferentCatalogsWithSameNameNoCollision(t *testing.T) {
	// "a/item" and "b/item" with "a"→"c" and "b"→"d": different target catalogs, no collision
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"a": "c", "b": "d"}, rec)

	snap := &catalogcollector.CatalogSnapshot{
		CatalogItems: []apiv1alpha1.CatalogItem{
			catalogItem("a", "item-x"),
			catalogItem("b", "item-x"),
		},
	}
	require.NoError(t, p.Consume(context.Background(), snap))
	require.Len(t, rec.snapshots[0].CatalogItems, 2)
}

// --- Determinism ---

func TestTransform_DeterministicForIdenticalInput(t *testing.T) {
	snap := &catalogcollector.CatalogSnapshot{
		Revision: "r1",
		Catalogs: []apiv1alpha1.Catalog{
			catalog("upstream-models"),
			catalog("other"),
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			catalogItem("upstream-models", "item-1"),
			catalogItem("other", "item-2"),
		},
	}

	var first, second *catalogcollector.CatalogSnapshot

	rec1 := &recordingConsumer{}
	p1 := buildProcessor(t, map[string]string{"upstream-models": "stage-models"}, rec1)
	require.NoError(t, p1.Consume(context.Background(), snap))
	first = rec1.snapshots[0]

	rec2 := &recordingConsumer{}
	p2 := buildProcessor(t, map[string]string{"upstream-models": "stage-models"}, rec2)
	require.NoError(t, p2.Consume(context.Background(), snap))
	second = rec2.snapshots[0]

	assert.Equal(t, first.Revision, second.Revision)
	require.Len(t, first.Catalogs, len(second.Catalogs))
	for i := range first.Catalogs {
		assert.Equal(t, nameOf(first.Catalogs[i].Metadata.Name), nameOf(second.Catalogs[i].Metadata.Name))
	}
	require.Len(t, first.CatalogItems, len(second.CatalogItems))
	for i := range first.CatalogItems {
		assert.Equal(t, first.CatalogItems[i].Metadata.Catalog, second.CatalogItems[i].Metadata.Catalog)
		assert.Equal(t, nameOf(first.CatalogItems[i].Metadata.Name), nameOf(second.CatalogItems[i].Metadata.Name))
	}
}

// --- Empty slices ---

func TestTransform_EmptyCatalogsAndItemsProducesEmptyOutput(t *testing.T) {
	rec := &recordingConsumer{}
	p := buildProcessor(t, map[string]string{"a": "b"}, rec)

	snap := &catalogcollector.CatalogSnapshot{Revision: "r0"}
	require.NoError(t, p.Consume(context.Background(), snap))

	assert.Empty(t, rec.snapshots[0].Catalogs)
	assert.Empty(t, rec.snapshots[0].CatalogItems)
}
