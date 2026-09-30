package flightctldestination

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/bearertokenauthextension"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func testLogger() *logrus.Entry {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log.WithField("test", true)
}

// captureHook records every log entry fired through the logger it is attached to.
type captureHook struct {
	mu      sync.Mutex
	entries []logrus.Fields
}

func (h *captureHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *captureHook) Fire(entry *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	fields := make(logrus.Fields, len(entry.Data))
	for k, v := range entry.Data {
		fields[k] = v
	}
	h.entries = append(h.entries, fields)
	return nil
}

func (h *captureHook) all() []logrus.Fields {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]logrus.Fields(nil), h.entries...)
}

func newTestDestinationWithLogger(t *testing.T, server *httptest.Server, logger *logrus.Entry) catalogcollector.Destination {
	t.Helper()
	f := NewFactory()
	c, err := f.CreateDestination(context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test-dest"},
			Logger: logger,
		},
		&Config{Server: server.URL, InsecureSkipVerify: true},
	)
	require.NoError(t, err)
	return c
}

func testCatalog(name string, displayName string) apiv1alpha1.Catalog {
	return apiv1alpha1.Catalog{
		ApiVersion: "v1alpha1",
		Kind:       "Catalog",
		Metadata:   apiv1beta1.ObjectMeta{Name: lo.ToPtr(name)},
		Spec:       apiv1alpha1.CatalogSpec{DisplayName: lo.ToPtr(displayName)},
	}
}

func testCatalogItem(catalog, name, displayName string) apiv1alpha1.CatalogItem {
	return apiv1alpha1.CatalogItem{
		ApiVersion: "v1alpha1",
		Kind:       "CatalogItem",
		Metadata: apiv1alpha1.CatalogItemMeta{
			Catalog: catalog,
			Name:    lo.ToPtr(name),
		},
		Spec: apiv1alpha1.CatalogItemSpec{
			DisplayName: lo.ToPtr(displayName),
			Type:        "application",
			Artifacts:   []apiv1alpha1.CatalogItemArtifact{},
			Versions:    []apiv1alpha1.CatalogItemVersion{},
		},
	}
}

type fakeServer struct {
	mu       sync.Mutex
	catalogs map[string]*apiv1alpha1.Catalog
	items    map[string]*apiv1alpha1.CatalogItem // key: "catalog/item"
	calls    []string
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		catalogs: make(map[string]*apiv1alpha1.Catalog),
		items:    make(map[string]*apiv1alpha1.CatalogItem),
	}
}

func (fs *fakeServer) recordCall(call string) {
	fs.calls = append(fs.calls, call)
}

func (fs *fakeServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		defer fs.mu.Unlock()

		path := r.URL.Path
		path = strings.TrimPrefix(path, "/api/v1")

		switch {
		// catalog items nested under a specific catalog
		case strings.HasPrefix(path, "/catalogs/") && strings.Contains(path, "/items/"):
			switch r.Method {
			case http.MethodGet:
				fs.handleGetCatalogItem(w, r, path)
			case http.MethodPut:
				fs.handleReplaceCatalogItem(w, r, path)
			case http.MethodDelete:
				fs.handleDeleteCatalogItem(w, r, path)
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		// individual catalog or catalog list
		case strings.HasPrefix(path, "/catalogs"):
			switch r.Method {
			case http.MethodGet:
				if path == "/catalogs" {
					fs.handleListCatalogs(w, r)
				} else {
					fs.handleGetCatalog(w, r, path)
				}
			case http.MethodPut:
				fs.handleReplaceCatalog(w, r, path)
			case http.MethodDelete:
				fs.handleDeleteCatalog(w, r, path)
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		// cross-catalog catalog item list
		case path == "/catalogitems" && r.Method == http.MethodGet:
			fs.handleListCatalogItems(w, r)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
}

func (fs *fakeServer) handleGetCatalog(w http.ResponseWriter, _ *http.Request, path string) {
	name := strings.TrimPrefix(path, "/catalogs/")
	fs.recordCall("GET catalog/" + name)
	cat, ok := fs.catalogs[name]
	if !ok {
		writeStatus(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, cat)
}

func (fs *fakeServer) handleReplaceCatalog(w http.ResponseWriter, r *http.Request, path string) {
	name := strings.TrimPrefix(path, "/catalogs/")
	fs.recordCall("PUT catalog/" + name)
	var catalog apiv1alpha1.Catalog
	if err := json.NewDecoder(r.Body).Decode(&catalog); err != nil {
		writeStatus(w, http.StatusBadRequest, err.Error())
		return
	}

	existing, existed := fs.catalogs[name]
	if existed && existing.Metadata.Owner != nil && *existing.Metadata.Owner != "" {
		writeStatus(w, http.StatusConflict, "resource is owned")
		return
	}

	fs.catalogs[name] = &catalog
	if existed {
		writeJSON(w, http.StatusOK, &catalog)
	} else {
		writeJSON(w, http.StatusCreated, &catalog)
	}
}

func (fs *fakeServer) handleGetCatalogItem(w http.ResponseWriter, _ *http.Request, path string) {
	parts := parseCatalogItemPath(path)
	if parts == nil {
		writeStatus(w, http.StatusBadRequest, "invalid path")
		return
	}
	key := parts[0] + "/" + parts[1]
	fs.recordCall("GET item/" + key)
	item, ok := fs.items[key]
	if !ok {
		writeStatus(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (fs *fakeServer) handleReplaceCatalogItem(w http.ResponseWriter, r *http.Request, path string) {
	parts := parseCatalogItemPath(path)
	if parts == nil {
		writeStatus(w, http.StatusBadRequest, "invalid path")
		return
	}
	key := parts[0] + "/" + parts[1]
	fs.recordCall("PUT item/" + key)
	var item apiv1alpha1.CatalogItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		writeStatus(w, http.StatusBadRequest, err.Error())
		return
	}

	existing, existed := fs.items[key]
	if existed && existing.Metadata.Owner != nil && *existing.Metadata.Owner != "" {
		writeStatus(w, http.StatusConflict, "resource is owned")
		return
	}

	fs.items[key] = &item
	if existed {
		writeJSON(w, http.StatusOK, &item)
	} else {
		writeJSON(w, http.StatusCreated, &item)
	}
}

func (fs *fakeServer) handleListCatalogs(w http.ResponseWriter, r *http.Request) {
	fs.recordCall("LIST catalogs")
	selector := parseLabelSelector(r.URL.Query().Get("labelSelector"))
	var items []apiv1alpha1.Catalog
	for _, cat := range fs.catalogs {
		if matchesLabels(labelsValue(cat.Metadata.Labels), selector) {
			items = append(items, *cat)
		}
	}
	if items == nil {
		items = []apiv1alpha1.Catalog{}
	}
	writeJSON(w, http.StatusOK, apiv1alpha1.CatalogList{
		ApiVersion: "v1alpha1",
		Kind:       "CatalogList",
		Items:      items,
		Metadata:   apiv1beta1.ListMeta{},
	})
}

func (fs *fakeServer) handleListCatalogItems(w http.ResponseWriter, r *http.Request) {
	fs.recordCall("LIST catalogitems")
	selector := parseLabelSelector(r.URL.Query().Get("labelSelector"))
	var items []apiv1alpha1.CatalogItem
	for _, item := range fs.items {
		if matchesLabels(labelsValue(item.Metadata.Labels), selector) {
			items = append(items, *item)
		}
	}
	if items == nil {
		items = []apiv1alpha1.CatalogItem{}
	}
	writeJSON(w, http.StatusOK, apiv1alpha1.CatalogItemList{
		ApiVersion: "v1alpha1",
		Kind:       "CatalogItemList",
		Items:      items,
		Metadata:   apiv1beta1.ListMeta{},
	})
}

func (fs *fakeServer) handleDeleteCatalog(w http.ResponseWriter, _ *http.Request, path string) {
	name := strings.TrimPrefix(path, "/catalogs/")
	fs.recordCall("DELETE catalog/" + name)
	cat, ok := fs.catalogs[name]
	if !ok {
		writeStatus(w, http.StatusNotFound, "not found")
		return
	}
	delete(fs.catalogs, name)
	writeJSON(w, http.StatusOK, cat)
}

func (fs *fakeServer) handleDeleteCatalogItem(w http.ResponseWriter, _ *http.Request, path string) {
	parts := parseCatalogItemPath(path)
	if parts == nil {
		writeStatus(w, http.StatusBadRequest, "invalid path")
		return
	}
	key := parts[0] + "/" + parts[1]
	fs.recordCall("DELETE item/" + key)
	item, ok := fs.items[key]
	if !ok {
		writeStatus(w, http.StatusNotFound, "not found")
		return
	}
	delete(fs.items, key)
	writeJSON(w, http.StatusOK, item)
}

// parseLabelSelector parses a simple "key=value,key=value" label selector.
func parseLabelSelector(selector string) map[string]string {
	result := make(map[string]string)
	if selector == "" {
		return result
	}
	for _, part := range strings.Split(selector, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			result[kv[0]] = kv[1]
		}
	}
	return result
}

// matchesLabels returns true when all selector key=value pairs are present in labels.
func matchesLabels(labels map[string]string, selector map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func parseCatalogItemPath(path string) []string {
	path = strings.TrimPrefix(path, "/catalogs/")
	parts := strings.SplitN(path, "/items/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil
	}
	return parts
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeStatus(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, apiv1alpha1.Status{
		ApiVersion: "v1alpha1",
		Kind:       "Status",
		Code:       int32(code),
		Message:    msg,
		Status:     "Failure",
	})
}

// managementLabels returns a pointer to a label map containing the two
// management labels that the reconciler writes on every resource it owns.
func managementLabels(pipelineID string) *map[string]string {
	m := map[string]string{
		managedByLabel: managedByValue,
		pipelineLabel:  pipelineID,
	}
	return &m
}

func newTestDestination(t *testing.T, server *httptest.Server) catalogcollector.Destination {
	t.Helper()
	f := NewFactory()
	c, err := f.CreateDestination(context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test-dest"},
			Logger: testLogger(),
		},
		&Config{Server: server.URL, InsecureSkipVerify: true})
	require.NoError(t, err)
	return c
}

// mapHost implements catalogcollector.Host for tests.
type mapHost struct {
	extensions map[catalogcollector.ComponentID]catalogcollector.Extension
}

func (h *mapHost) GetExtension(id catalogcollector.ComponentID) (catalogcollector.Extension, error) {
	ext, ok := h.extensions[id]
	if !ok {
		return nil, fmt.Errorf("no extension registered for ID %q", id)
	}
	return ext, nil
}

func newTestDestinationWithToken(t *testing.T, server *httptest.Server, token string) catalogcollector.Destination {
	t.Helper()
	return newTestDestinationWithBearerExt(t, server, &bearertokenauthextension.Config{Token: api.SecureString(token)})
}

func newTestDestinationWithTokenFile(t *testing.T, server *httptest.Server, tokenFile string) catalogcollector.Destination {
	t.Helper()
	return newTestDestinationWithBearerExt(t, server, &bearertokenauthextension.Config{TokenFile: tokenFile})
}

func newTestDestinationWithBearerExt(t *testing.T, server *httptest.Server, extCfg *bearertokenauthextension.Config) catalogcollector.Destination {
	t.Helper()
	extID := catalogcollector.ComponentID{Type: bearertokenauthextension.Type, Name: "test"}
	ext, err := bearertokenauthextension.NewFactory().CreateExtension(
		context.Background(),
		catalogcollector.Settings{ID: extID},
		extCfg,
	)
	require.NoError(t, err)

	host := &mapHost{
		extensions: map[catalogcollector.ComponentID]catalogcollector.Extension{extID: ext},
	}

	f := NewFactory()
	c, err := f.CreateDestination(
		context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test-dest"},
			Host:   host,
			Logger: testLogger(),
		},
		&Config{
			Server:             server.URL,
			InsecureSkipVerify: true,
			Auth:               &AuthConfig{Authenticator: string(bearertokenauthextension.Type) + "/test"},
		},
	)
	require.NoError(t, err)
	return c
}

// nopExtension implements catalogcollector.Extension but NOT extensionauth.HTTPClient.
type nopExtension struct{}

func (e *nopExtension) Start(_ context.Context, _ catalogcollector.Host) error { return nil }
func (e *nopExtension) Shutdown(_ context.Context) error                       { return nil }

func TestFactoryType(t *testing.T) {
	f := NewFactory()
	require.Equal(t, Type, f.Type())
	require.Equal(t, catalogcollector.ComponentType("flightctl"), f.Type())
}

func TestConsume_CreatesMissingResources(t *testing.T) {
	fs := newFakeServer()
	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)

	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("my-catalog", "My Catalog"),
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			testCatalogItem("my-catalog", "my-item", "My Item"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.Contains(t, fs.catalogs, "my-catalog")
	require.Contains(t, fs.items, "my-catalog/my-item")
}

func TestConsume_SkipsUnchangedResources(t *testing.T) {
	fs := newFakeServer()
	existing := testCatalog("my-catalog", "My Catalog")
	existing.Metadata.Labels = managementLabels("test")
	fs.catalogs["my-catalog"] = &existing
	existingItem := testCatalogItem("my-catalog", "my-item", "My Item")
	existingItem.Metadata.Labels = managementLabels("test")
	fs.items["my-catalog/my-item"] = &existingItem

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("my-catalog", "My Catalog"),
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			testCatalogItem("my-catalog", "my-item", "My Item"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, call := range fs.calls {
		require.NotContains(t, call, "PUT", "unchanged resources should not be written")
	}
}

func TestConsume_UpdatesChangedResources(t *testing.T) {
	fs := newFakeServer()
	existing := testCatalog("my-catalog", "Old Name")
	existing.Metadata.Labels = managementLabels("test")
	fs.catalogs["my-catalog"] = &existing

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-2",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("my-catalog", "New Name"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.Equal(t, "New Name", *fs.catalogs["my-catalog"].Spec.DisplayName)
	require.Contains(t, fs.calls, "PUT catalog/my-catalog")
}

func TestConsume_CatalogBeforeCatalogItem(t *testing.T) {
	fs := newFakeServer()
	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("cat-b", "B"),
			testCatalog("cat-a", "A"),
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			testCatalogItem("cat-a", "item-1", "Item 1"),
			testCatalogItem("cat-b", "item-2", "Item 2"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()

	firstCatalogPut := -1
	firstItemPut := -1
	for i, call := range fs.calls {
		if strings.HasPrefix(call, "PUT catalog/") && firstCatalogPut == -1 {
			firstCatalogPut = i
		}
		if strings.HasPrefix(call, "PUT item/") && firstItemPut == -1 {
			firstItemPut = i
		}
	}
	require.NotEqual(t, -1, firstCatalogPut, "expected at least one catalog PUT")
	require.NotEqual(t, -1, firstItemPut, "expected at least one item PUT")
	require.Less(t, firstCatalogPut, firstItemPut, "catalogs must be written before items")

	var catalogPuts []string
	for _, call := range fs.calls {
		if strings.HasPrefix(call, "PUT catalog/") {
			catalogPuts = append(catalogPuts, call)
		}
	}
	require.Equal(t, []string{"PUT catalog/cat-a", "PUT catalog/cat-b"}, catalogPuts)
}

func TestConsume_ReplayWithSameRevisionRepairsDrift(t *testing.T) {
	fs := newFakeServer()
	original := testCatalog("my-catalog", "Original")
	original.Metadata.Labels = managementLabels("test")
	fs.catalogs["my-catalog"] = &original

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("my-catalog", "Original"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)
	fs.mu.Lock()
	require.NotContains(t, fs.calls, "PUT catalog/my-catalog")
	fs.mu.Unlock()

	fs.mu.Lock()
	fs.catalogs["my-catalog"].Spec.DisplayName = lo.ToPtr("Drifted")
	fs.calls = nil
	fs.mu.Unlock()

	err = c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.Contains(t, fs.calls, "PUT catalog/my-catalog")
	require.Equal(t, "Original", *fs.catalogs["my-catalog"].Spec.DisplayName)
}

func TestConsume_ReplayWithSameRevisionRepairsDeleted(t *testing.T) {
	fs := newFakeServer()
	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("my-catalog", "My Catalog"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)
	fs.mu.Lock()
	require.Contains(t, fs.catalogs, "my-catalog")
	delete(fs.catalogs, "my-catalog")
	fs.calls = nil
	fs.mu.Unlock()

	err = c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.Contains(t, fs.catalogs, "my-catalog")
}

func TestConsume_OwnershipConflict(t *testing.T) {
	fs := newFakeServer()
	owned := testCatalog("owned-catalog", "Old")
	owned.Metadata.Owner = lo.ToPtr("ResourceSync/my-sync")
	fs.catalogs["owned-catalog"] = &owned

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("owned-catalog", "New"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.Error(t, err)
	require.ErrorContains(t, err, "owned-catalog")
	require.ErrorContains(t, err, "is owned by")
}

func TestConsume_OwnershipConflictCatalogItem(t *testing.T) {
	fs := newFakeServer()
	cat := testCatalog("my-catalog", "Cat")
	cat.Metadata.Labels = managementLabels("test")
	fs.catalogs["my-catalog"] = &cat
	owned := testCatalogItem("my-catalog", "owned-item", "Old")
	owned.Metadata.Owner = lo.ToPtr("ResourceSync/my-sync")
	fs.items["my-catalog/owned-item"] = &owned

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("my-catalog", "Cat"),
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			testCatalogItem("my-catalog", "owned-item", "New"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.Error(t, err)
	require.ErrorContains(t, err, "owned-item")
}

func TestConsume_UnmanagedCollision(t *testing.T) {
	fs := newFakeServer()
	// resource exists with no management labels
	unmanaged := testCatalog("my-catalog", "Unmanaged")
	fs.catalogs["my-catalog"] = &unmanaged

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	err := newTestDestination(t, server).Reconcile(context.Background(), "test", &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{testCatalog("my-catalog", "Desired")},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "my-catalog")
	require.ErrorContains(t, err, "outside pipeline")

	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, call := range fs.calls {
		require.NotContains(t, call, "PUT")
		require.NotContains(t, call, "DELETE")
	}
}

func TestConsume_OtherPipelineCollision(t *testing.T) {
	fs := newFakeServer()
	other := testCatalog("my-catalog", "Other's")
	other.Metadata.Labels = managementLabels("other-pipeline")
	fs.catalogs["my-catalog"] = &other

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	err := newTestDestination(t, server).Reconcile(context.Background(), "test", &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{testCatalog("my-catalog", "Desired")},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "my-catalog")
	require.ErrorContains(t, err, "outside pipeline")

	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, call := range fs.calls {
		require.NotContains(t, call, "PUT")
		require.NotContains(t, call, "DELETE")
	}
}

func TestConsume_LabelChangeTriggersPUT(t *testing.T) {
	fs := newFakeServer()
	// existing: only management labels, no source label
	existing := testCatalog("my-catalog", "My Catalog")
	existing.Metadata.Labels = managementLabels("test")
	fs.catalogs["my-catalog"] = &existing

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	// desired: same spec, but source provides an extra label
	desired := testCatalog("my-catalog", "My Catalog")
	sourceLabel := map[string]string{"app": "my-app"}
	desired.Metadata.Labels = &sourceLabel

	err := newTestDestination(t, server).Reconcile(context.Background(), "test", &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{desired},
	})
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.Contains(t, fs.calls, "PUT catalog/my-catalog", "label change must trigger PUT even when spec is unchanged")
	stored := fs.catalogs["my-catalog"]
	require.Equal(t, "my-app", (*stored.Metadata.Labels)["app"])
}

func TestConsume_HTTPError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusInternalServerError, "internal error")
	}))
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("my-catalog", "My Catalog"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.Error(t, err)
	require.ErrorContains(t, err, "500")
}

func TestConsume_UnauthenticatedAccess(t *testing.T) {
	var receivedAuth atomic.Value
	fs := newFakeServer()
	handler := fs.handler()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth.Store(r.Header.Get("Authorization"))
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	f := NewFactory()
	c, err := f.CreateDestination(context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test"},
			Logger: testLogger(),
		},
		&Config{Server: server.URL, InsecureSkipVerify: true})
	require.NoError(t, err)

	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{testCatalog("cat", "Cat")},
	}
	err = c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	auth, _ := receivedAuth.Load().(string)
	require.Empty(t, auth, "unauthenticated access should not send Authorization header")
}

func TestConsume_InlineToken(t *testing.T) {
	var receivedAuth atomic.Value
	fs := newFakeServer()
	handler := fs.handler()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth.Store(r.Header.Get("Authorization"))
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	c := newTestDestinationWithToken(t, server, "my-secret-token")
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{testCatalog("cat", "Cat")},
	}
	err := c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	auth, _ := receivedAuth.Load().(string)
	require.Equal(t, "Bearer my-secret-token", auth)
}

func TestConsume_RotatingTokenFile(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	require.NoError(t, os.WriteFile(tokenFile, []byte("token-v1"), 0600))

	var (
		firstAuthMu   sync.Mutex
		firstAuthSeen bool
		firstAuthVal  string
	)
	fs := newFakeServer()
	handler := fs.handler()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		firstAuthMu.Lock()
		if !firstAuthSeen {
			firstAuthVal = auth
			firstAuthSeen = true
		}
		firstAuthMu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	c := newTestDestinationWithTokenFile(t, server, tokenFile)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{testCatalog("cat", "Cat")},
	}

	require.NoError(t, c.Reconcile(context.Background(), "test", snapshot))

	firstAuthMu.Lock()
	got := firstAuthVal
	firstAuthSeen = false
	firstAuthMu.Unlock()
	require.Equal(t, "Bearer token-v1", got)

	// Rotate the token and force re-reconciliation by deleting the server-side resource.
	require.NoError(t, os.WriteFile(tokenFile, []byte("token-v2\n"), 0600))
	fs.mu.Lock()
	delete(fs.catalogs, "cat")
	fs.mu.Unlock()

	require.NoError(t, c.Reconcile(context.Background(), "test", snapshot))

	firstAuthMu.Lock()
	got2 := firstAuthVal
	firstAuthMu.Unlock()
	require.Equal(t, "Bearer token-v2", got2, "token must be re-read on each request")
}

func TestConsume_ConcurrentSafety(t *testing.T) {
	fs := newFakeServer()
	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)

	const n = 5
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Use distinct pipeline IDs to avoid concurrent reconcile operations
			// from deleting each other's resources via the prune step.
			pipelineID := fmt.Sprintf("pipeline-%d", idx)
			snapshot := &catalogcollector.CatalogSnapshot{
				Revision: fmt.Sprintf("rev-%d", idx),
				Catalogs: []apiv1alpha1.Catalog{
					testCatalog(fmt.Sprintf("catalog-%d", idx), fmt.Sprintf("Catalog %d", idx)),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					testCatalogItem(fmt.Sprintf("catalog-%d", idx), "item", "Item"),
				},
			}
			errs[idx] = c.Reconcile(context.Background(), pipelineID, snapshot)
		}(i)
	}
	wg.Wait()

	// Verify that no goroutine panicked and all calls completed without error.
	for i, err := range errs {
		require.NoError(t, err, "concurrent Reconcile call %d returned an error", i)
	}

	// Verify the server received resources from all concurrent goroutines.
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for i := 0; i < n; i++ {
		catalogName := fmt.Sprintf("catalog-%d", i)
		require.Contains(t, fs.catalogs, catalogName, "catalog-%d must be present on server", i)
	}
}

func TestConsume_CredentialsNeverInErrors(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	require.NoError(t, os.WriteFile(tokenFile, []byte("super-secret-token"), 0600))

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusInternalServerError, "server error")
	}))
	defer server.Close()

	c := newTestDestinationWithTokenFile(t, server, tokenFile)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{testCatalog("cat", "Cat")},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "super-secret-token")
}

func TestConsume_EmptySnapshot(t *testing.T) {
	fs := newFakeServer()

	// Pre-seed a catalog and item owned by pipeline "test" — should be pruned.
	managed := testCatalog("managed-catalog", "Managed")
	managed.Metadata.Labels = managementLabels("test")
	fs.catalogs["managed-catalog"] = &managed

	managedItem := testCatalogItem("managed-catalog", "managed-item", "Item")
	managedItem.Metadata.Labels = managementLabels("test")
	fs.items["managed-catalog/managed-item"] = &managedItem

	// Pre-seed an unmanaged catalog — must not be touched.
	unmanaged := testCatalog("unmanaged-catalog", "Unmanaged")
	fs.catalogs["unmanaged-catalog"] = &unmanaged

	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{Revision: "rev-1"}
	err := c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.NotContains(t, fs.catalogs, "managed-catalog", "managed catalog must be pruned by empty snapshot")
	require.NotContains(t, fs.items, "managed-catalog/managed-item", "managed item must be pruned by empty snapshot")
	require.Contains(t, fs.catalogs, "unmanaged-catalog", "unmanaged catalog must not be affected")
}

func TestConsume_ClearsServerManagedFields(t *testing.T) {
	fs := newFakeServer()
	handler := fs.handler()
	// Use a channel to capture validation errors from the HTTP handler goroutine
	// instead of calling require/assert directly (which would panic from a non-test goroutine).
	handlerErrs := make(chan string, 10)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			bodyStr := string(body)
			if strings.Contains(bodyStr, "resourceVersion") {
				handlerErrs <- "body contains resourceVersion"
			}
			if strings.Contains(bodyStr, "generation") {
				handlerErrs <- "body contains generation"
			}
			if strings.Contains(bodyStr, "owner") {
				handlerErrs <- "body contains owner"
			}
			r.Body = io.NopCloser(strings.NewReader(bodyStr))
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	c := newTestDestination(t, server)

	catalog := testCatalog("my-catalog", "My Catalog")
	catalog.Metadata.ResourceVersion = lo.ToPtr("should-be-cleared")
	catalog.Metadata.Generation = lo.ToPtr(int64(42))
	catalog.Metadata.Owner = lo.ToPtr("should-be-cleared")

	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{catalog},
	}
	err := c.Reconcile(context.Background(), "test", snapshot)
	require.NoError(t, err)

	// Drain and assert handler errors captured via channel.
	close(handlerErrs)
	for errMsg := range handlerErrs {
		t.Errorf("PUT body validation failed: %s", errMsg)
	}
}

func TestConsume_TokenFileMissing(t *testing.T) {
	fs := newFakeServer()
	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	c := newTestDestinationWithTokenFile(t, server, "/nonexistent/path/credentials")
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{testCatalog("cat", "Cat")},
	}
	err := c.Reconcile(context.Background(), "test", snapshot)
	require.Error(t, err)
	require.ErrorContains(t, err, "reading token file")
	require.ErrorContains(t, err, "no such file")
}

func TestConsume_ErrorMessagesNoAuthHeader(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	require.NoError(t, os.WriteFile(tokenFile, []byte("bearer-secret"), 0600))

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusUnauthorized, "unauthorized")
	}))
	defer server.Close()

	c := newTestDestinationWithTokenFile(t, server, tokenFile)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{testCatalog("cat", "Cat")},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "bearer-secret")
	require.NotContains(t, err.Error(), "Bearer")
	require.NotContains(t, err.Error(), "Authorization")
}

func TestConsume_MissingAuthenticatorExtension(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	host := &mapHost{extensions: map[catalogcollector.ComponentID]catalogcollector.Extension{}}
	f := NewFactory()
	_, err := f.CreateDestination(
		context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test"},
			Host:   host,
			Logger: testLogger(),
		},
		&Config{
			Server:             server.URL,
			InsecureSkipVerify: true,
			Auth:               &AuthConfig{Authenticator: "bearertokenauth/missing"},
		},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "bearertokenauth/missing")
}

func TestConsume_ErrorIncludesBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusBadRequest, "user belongs to multiple organizations")
	}))
	defer server.Close()

	c := newTestDestination(t, server)
	snapshot := &catalogcollector.CatalogSnapshot{
		Revision: "rev-1",
		Catalogs: []apiv1alpha1.Catalog{
			testCatalog("my-catalog", "My Catalog"),
		},
	}

	err := c.Reconcile(context.Background(), "test", snapshot)
	require.Error(t, err)
	require.ErrorContains(t, err, "user belongs to multiple organizations")
}

func TestConsume_WrongCapabilityExtension(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	extID := catalogcollector.ComponentID{Type: "nop", Name: "test"}
	host := &mapHost{
		extensions: map[catalogcollector.ComponentID]catalogcollector.Extension{
			extID: &nopExtension{},
		},
	}
	f := NewFactory()
	_, err := f.CreateDestination(
		context.Background(),
		catalogcollector.Settings{
			ID:     catalogcollector.ComponentID{Type: Type, Name: "test"},
			Host:   host,
			Logger: testLogger(),
		},
		&Config{
			Server:             server.URL,
			InsecureSkipVerify: true,
			Auth:               &AuthConfig{Authenticator: "nop/test"},
		},
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "extensionauth.HTTPClient")
}

// TestDestination_BaseLoggerHasNoPipelineID verifies that the reconciler's
// stored logger does not carry a pipeline_id field at construction time. A
// shared destination serves multiple pipelines, so pipeline_id must be added
// at reconciliation time rather than baked in during construction.
func TestDestination_BaseLoggerHasNoPipelineID(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	log := logrus.New()
	log.SetOutput(io.Discard)
	logger := log.WithFields(logrus.Fields{
		"component_kind": "destination",
		"component_id":   "flightctl/shared",
	})

	dest := newTestDestinationWithLogger(t, server, logger)
	r := dest.(*reconciler)

	_, hasPipelineID := r.log.Data["pipeline_id"]
	require.False(t, hasPipelineID, "destination base logger must not carry pipeline_id at construction time")
}

// TestReconcile_EnrichesLogWithPipelineID verifies that Reconcile derives an
// operation logger that includes the runtime pipeline_id field. The base logger
// must not be mutated; the enrichment lives only for the duration of the call.
func TestReconcile_EnrichesLogWithPipelineID(t *testing.T) {
	hook := &captureHook{}
	log := logrus.New()
	log.SetOutput(io.Discard)
	log.AddHook(hook)
	logger := log.WithFields(logrus.Fields{
		"component_kind": "destination",
		"component_id":   "flightctl/test-dest",
	})

	fs := newFakeServer()
	server := httptest.NewTLSServer(fs.handler())
	defer server.Close()

	dest := newTestDestinationWithLogger(t, server, logger)

	err := dest.Reconcile(context.Background(), "my-pipeline", &catalogcollector.CatalogSnapshot{
		Revision:     "r1",
		Catalogs:     []apiv1alpha1.Catalog{},
		CatalogItems: []apiv1alpha1.CatalogItem{},
	})
	require.NoError(t, err)

	entries := hook.all()
	require.NotEmpty(t, entries, "Reconcile must emit at least one log entry")

	found := false
	for _, fields := range entries {
		if fields["pipeline_id"] == "my-pipeline" {
			found = true
			break
		}
	}
	require.True(t, found, "at least one log entry from Reconcile must carry pipeline_id=%q", "my-pipeline")

	// Base logger must not have been mutated.
	_, hasPipelineID := logger.Data["pipeline_id"]
	require.False(t, hasPipelineID, "base logger must not be mutated by Reconcile")
}
