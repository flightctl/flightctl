package debugdestination

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// captureHook records every log entry fired through the logger it is attached to.
type captureHook struct {
	mu      sync.Mutex
	entries []*logrus.Entry
}

func (h *captureHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *captureHook) Fire(entry *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	clone := *entry
	clone.Data = make(logrus.Fields, len(entry.Data))
	for k, v := range entry.Data {
		clone.Data[k] = v
	}
	h.entries = append(h.entries, &clone)
	return nil
}

func (h *captureHook) all() []*logrus.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*logrus.Entry(nil), h.entries...)
}

func testLogger() *logrus.Entry {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log.WithField("test", true)
}

func testLoggerWithHook() (*logrus.Entry, *captureHook) {
	hook := &captureHook{}
	log := logrus.New()
	log.SetOutput(io.Discard)
	log.SetLevel(logrus.TraceLevel)
	log.AddHook(hook)
	return log.WithField("test", true), hook
}

func testCatalog(name string) apiv1alpha1.Catalog {
	return apiv1alpha1.Catalog{
		ApiVersion: "v1alpha1",
		Kind:       "Catalog",
		Metadata:   apiv1beta1.ObjectMeta{Name: lo.ToPtr(name)},
		Spec:       apiv1alpha1.CatalogSpec{DisplayName: lo.ToPtr(name)},
	}
}

func testCatalogItem(catalog, name string) apiv1alpha1.CatalogItem {
	return apiv1alpha1.CatalogItem{
		ApiVersion: "v1alpha1",
		Kind:       "CatalogItem",
		Metadata: apiv1alpha1.CatalogItemMeta{
			Catalog: catalog,
			Name:    lo.ToPtr(name),
		},
		Spec: apiv1alpha1.CatalogItemSpec{
			DisplayName: lo.ToPtr(name),
			Type:        "application",
			Artifacts:   []apiv1alpha1.CatalogItemArtifact{},
			Versions:    []apiv1alpha1.CatalogItemVersion{},
		},
	}
}

func newTestDestination(t *testing.T, verbosity Verbosity, logger *logrus.Entry) catalogcollector.Destination {
	t.Helper()
	f := NewFactory()
	dest, err := f.CreateDestination(context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test"},
			Logger: logger,
		},
		&Config{Verbosity: verbosity},
	)
	require.NoError(t, err)
	return dest
}

// --- Factory tests ---

func TestFactoryType(t *testing.T) {
	f := NewFactory()
	require.Equal(t, Type, f.Type())
	require.Equal(t, catalogcollector.ComponentType("debug"), f.Type())
}

func TestFactory_EmptyConfigDefaultsToBasic(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig()
	c, ok := cfg.(*Config)
	require.True(t, ok)
	require.Equal(t, VerbosityBasic, c.Verbosity)
}

func TestFactory_UnknownConfigFieldsRejected(t *testing.T) {
	f := NewFactory()
	defaults := f.CreateDefaultConfig()
	raw := json.RawMessage(`{"verbosity":"basic","unknown_field":"bad"}`)
	err := config.DecodeComponent(raw, defaults, "destinations.debug/test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "destinations.debug/test")
}

func TestFactory_NilLoggerRejected(t *testing.T) {
	f := NewFactory()
	_, err := f.CreateDestination(context.Background(),
		catalogcollector.Settings{
			ID: catalogcollector.ComponentID{Type: Type, Name: "test"},
		},
		&Config{Verbosity: VerbosityBasic},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "logger must not be nil")
}

func TestFactory_WrongConfigType(t *testing.T) {
	f := NewFactory()
	_, err := f.CreateDestination(context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test"},
			Logger: testLogger(),
		},
		"not-a-config-struct",
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "unexpected config type")
}

func TestFactory_UnsupportedVerbosityRejected(t *testing.T) {
	f := NewFactory()
	_, err := f.CreateDestination(context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test"},
			Logger: testLogger(),
		},
		&Config{Verbosity: "verbose"},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported verbosity")
}

// --- Reconcile output tests ---

func TestReconcile_BasicOutputContainsSummaryFields(t *testing.T) {
	logger, hook := testLoggerWithHook()
	dest := newTestDestination(t, VerbosityBasic, logger)

	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "abc123",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("cat-a"),
			testCatalog("cat-b"),
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			testCatalogItem("cat-a", "item-1"),
			testCatalogItem("cat-b", "item-2"),
			testCatalogItem("cat-b", "item-3"),
		},
	}

	err := dest.Reconcile(context.Background(), "my-pipeline", snapshot)
	require.NoError(t, err)

	entries := hook.all()
	require.NotEmpty(t, entries)

	var found *logrus.Entry
	for _, e := range entries {
		if e.Data["pipeline_id"] == "my-pipeline" {
			found = e
			break
		}
	}
	require.NotNil(t, found, "expected at least one entry with pipeline_id")
	require.Equal(t, logrus.InfoLevel, found.Level)
	require.Equal(t, "snapshot received", found.Message)
	require.Equal(t, "my-pipeline", found.Data["pipeline_id"])
	require.Equal(t, "abc123", found.Data["revision"])
	require.Equal(t, 2, found.Data["catalog_count"])
	require.Equal(t, 3, found.Data["catalog_item_count"])
}

func TestReconcile_NormalOutputContainsSortedIdentities(t *testing.T) {
	logger, hook := testLoggerWithHook()
	dest := newTestDestination(t, VerbosityNormal, logger)

	// Supply catalogs and items in unsorted order to verify sorting.
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("zulu"),
			testCatalog("alpha"),
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			testCatalogItem("zulu", "item-b"),
			testCatalogItem("alpha", "item-a"),
			testCatalogItem("alpha", "item-c"),
		},
	}

	err := dest.Reconcile(context.Background(), "pipe", snapshot)
	require.NoError(t, err)

	entries := hook.all()
	var found *logrus.Entry
	for _, e := range entries {
		if e.Data["pipeline_id"] == "pipe" {
			found = e
			break
		}
	}
	require.NotNil(t, found)
	require.Equal(t, logrus.InfoLevel, found.Level)

	// Catalogs must be sorted alphabetically.
	catalogs, ok := found.Data["catalogs"].([]string)
	require.True(t, ok, "catalogs field must be []string")
	require.Equal(t, []string{"alpha", "zulu"}, catalogs)

	// Items must be sorted as catalog/name.
	items, ok := found.Data["catalog_items"].([]string)
	require.True(t, ok, "catalog_items field must be []string")
	require.Equal(t, []string{"alpha/item-a", "alpha/item-c", "zulu/item-b"}, items)

	// Summary fields are present too.
	require.Equal(t, 2, found.Data["catalog_count"])
	require.Equal(t, 3, found.Data["catalog_item_count"])
}

func TestReconcile_DetailedOutputLogsPerResource(t *testing.T) {
	logger, hook := testLoggerWithHook()
	dest := newTestDestination(t, VerbosityDetailed, logger)

	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-42",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("cat-a"),
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			testCatalogItem("cat-a", "item-1"),
		},
	}

	err := dest.Reconcile(context.Background(), "pipe", snapshot)
	require.NoError(t, err)

	entries := hook.all()

	// First entry is the summary.
	var summary *logrus.Entry
	for _, e := range entries {
		if e.Data["pipeline_id"] == "pipe" {
			summary = e
			break
		}
	}
	require.NotNil(t, summary)
	require.Equal(t, 1, summary.Data["catalog_count"])
	require.Equal(t, 1, summary.Data["catalog_item_count"])

	// Subsequent entries log individual resources.
	var catalogEntry, itemEntry *logrus.Entry
	for _, e := range entries {
		switch e.Data["resource_kind"] {
		case "Catalog":
			catalogEntry = e
		case "CatalogItem":
			itemEntry = e
		}
	}

	require.NotNil(t, catalogEntry, "expected a Catalog resource entry")
	require.Equal(t, "cat-a", catalogEntry.Data["resource_name"])
	require.NotEmpty(t, catalogEntry.Data["resource_json"], "resource_json must be populated")

	require.NotNil(t, itemEntry, "expected a CatalogItem resource entry")
	require.Equal(t, "item-1", itemEntry.Data["resource_name"])
	require.Equal(t, "cat-a", itemEntry.Data["catalog_name"])
	require.NotEmpty(t, itemEntry.Data["resource_json"], "resource_json must be populated")
}

func TestReconcile_EmptySnapshotSucceeds(t *testing.T) {
	logger, hook := testLoggerWithHook()
	dest := newTestDestination(t, VerbosityBasic, logger)

	snapshot := &catalogcollector.CatalogSnapshot{Revision: "empty"}

	err := dest.Reconcile(context.Background(), "pipe", snapshot)
	require.NoError(t, err)

	entries := hook.all()
	var found *logrus.Entry
	for _, e := range entries {
		if e.Data["pipeline_id"] == "pipe" {
			found = e
			break
		}
	}
	require.NotNil(t, found)
	require.Equal(t, 0, found.Data["catalog_count"])
	require.Equal(t, 0, found.Data["catalog_item_count"])
}

func TestReconcile_DoesNotMutateSnapshot(t *testing.T) {
	logger, _ := testLoggerWithHook()

	catalogs := []apiv1alpha1.Catalog{
		testCatalog("zulu"),
		testCatalog("alpha"),
	}
	items := []apiv1alpha1.CatalogItem{
		testCatalogItem("zulu", "item-b"),
		testCatalogItem("alpha", "item-a"),
	}

	snapshot := &catalogcollector.CatalogSnapshot{
		Revision:     "rev-1",
		Catalogs:     catalogs,
		CatalogItems: items,
	}

	// Deep-copy for comparison by serializing.
	beforeJSON, err := json.Marshal(snapshot)
	require.NoError(t, err)

	// Run with normal (which sorts internally) to verify no mutation.
	dest := newTestDestination(t, VerbosityNormal, logger)
	err = dest.Reconcile(context.Background(), "pipe", snapshot)
	require.NoError(t, err)

	afterJSON, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.JSONEq(t, string(beforeJSON), string(afterJSON), "snapshot must not be mutated")

	// Verify original slice order is preserved.
	require.Equal(t, "zulu", *snapshot.Catalogs[0].Metadata.Name)
	require.Equal(t, "alpha", *snapshot.Catalogs[1].Metadata.Name)
}

func TestReconcile_ReturnsNil(t *testing.T) {
	for _, v := range []Verbosity{VerbosityBasic, VerbosityNormal, VerbosityDetailed} {
		t.Run(string(v), func(t *testing.T) {
			dest := newTestDestination(t, v, testLogger())
			err := dest.Reconcile(context.Background(), "pipe", &catalogcollector.CatalogSnapshot{
				Revision: "r1",
				Catalogs: []apiv1alpha1.Catalog{testCatalog("c")},
			})
			require.NoError(t, err)
		})
	}
}

func TestReconcile_ConcurrentSafety(t *testing.T) {
	dest := newTestDestination(t, VerbosityNormal, testLogger())

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			snapshot := &catalogcollector.CatalogSnapshot{
				Revision: fmt.Sprintf("rev-%d", idx),
				Catalogs: []apiv1alpha1.Catalog{
					testCatalog(fmt.Sprintf("catalog-%d", idx)),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					testCatalogItem(fmt.Sprintf("catalog-%d", idx), "item"),
				},
			}
			_ = dest.Reconcile(context.Background(), fmt.Sprintf("pipe-%d", idx), snapshot)
		}(i)
	}
	wg.Wait()
}

// TestFactory_RegisteredInComponents is covered by
// TestComponents_DebugDestinationRegistered in main_test.go which
// verifies the factory is actually registered in the binary's
// component set. A type-constant equality check here adds no value.

func TestReconcile_NormalEmptySnapshotYieldsEmptyLists(t *testing.T) {
	logger, hook := testLoggerWithHook()
	dest := newTestDestination(t, VerbosityNormal, logger)

	err := dest.Reconcile(context.Background(), "pipe", &catalogcollector.CatalogSnapshot{Revision: "empty"})
	require.NoError(t, err)

	entries := hook.all()
	var found *logrus.Entry
	for _, e := range entries {
		if e.Data["pipeline_id"] == "pipe" {
			found = e
			break
		}
	}
	require.NotNil(t, found)

	catalogs, ok := found.Data["catalogs"].([]string)
	require.True(t, ok)
	require.Empty(t, catalogs)

	items, ok := found.Data["catalog_items"].([]string)
	require.True(t, ok)
	require.Empty(t, items)
}

func TestReconcile_DetailedEmptySnapshotLogsSummaryOnly(t *testing.T) {
	logger, hook := testLoggerWithHook()
	dest := newTestDestination(t, VerbosityDetailed, logger)

	snapshot := &catalogcollector.CatalogSnapshot{Revision: "empty"}
	err := dest.Reconcile(context.Background(), "pipe", snapshot)
	require.NoError(t, err)

	entries := hook.all()
	var summary *logrus.Entry
	for _, e := range entries {
		if e.Data["pipeline_id"] == "pipe" {
			summary = e
			break
		}
	}
	require.NotNil(t, summary)
	require.Equal(t, 0, summary.Data["catalog_count"])
	require.Equal(t, 0, summary.Data["catalog_item_count"])

	// No per-resource entries for an empty snapshot.
	for _, e := range entries {
		require.Nil(t, e.Data["resource_kind"],
			"expected no resource entries for an empty snapshot")
	}
}
