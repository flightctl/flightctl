package tasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// testKVStore is a controllable in-memory KVStore for unit tests.
// It records which keys are deleted so tests can assert on cache-invalidation behaviour.
type testKVStore struct {
	mu      sync.Mutex
	store   map[string][]byte
	deleted []string
}

func newTestKVStore() *testKVStore {
	return &testKVStore{store: make(map[string][]byte)}
}

func (s *testKVStore) seed(key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[key] = value
}

func (s *testKVStore) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.store[key]
	return ok
}

func (s *testKVStore) wasDeleted(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.deleted {
		if k == key {
			return true
		}
	}
	return false
}

func (s *testKVStore) Close() {}

func (s *testKVStore) SetNX(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.store[key]; ok {
		return false, nil
	}
	s.store[key] = value
	return true, nil
}

func (s *testKVStore) SetIfGreater(_ context.Context, _ string, _ int64) (bool, error) {
	return false, nil
}

func (s *testKVStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.store[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (s *testKVStore) GetOrSetNX(_ context.Context, key string, value []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.store[key]; ok {
		return v, nil
	}
	s.store[key] = value
	return value, nil
}

func (s *testKVStore) DeleteKeysForTemplateVersion(_ context.Context, _ string) error { return nil }
func (s *testKVStore) DeleteAllKeys(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = make(map[string][]byte)
	return nil
}
func (s *testKVStore) PrintAllKeys(_ context.Context) {}

func (s *testKVStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, key)
	delete(s.store, key)
	return nil
}

func (s *testKVStore) StreamAdd(_ context.Context, _ string, _ []byte) (string, error) {
	return "", nil
}
func (s *testKVStore) StreamRange(_ context.Context, _, _, _ string) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (s *testKVStore) StreamRead(_ context.Context, _ string, _ string, _ time.Duration, _ int64) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (s *testKVStore) SetExpire(_ context.Context, _ string, _ time.Duration) error { return nil }

var _ kvstore.KVStore = (*testKVStore)(nil)

// newDepChangeEvent constructs a DependencyChangeDetected event for testing.
func newDepChangeEvent(deviceName, resourceKey, fingerprint string) domain.Event {
	details := domain.DependencyChangeDetectedDetails{
		DetailType:  domain.DependencyChangeDetected,
		ResourceKey: resourceKey,
		Fingerprint: fingerprint,
	}
	eventDetails := domain.EventDetails{}
	_ = eventDetails.FromDependencyChangeDetectedDetails(details)
	return domain.Event{
		InvolvedObject: domain.ObjectReference{
			Kind: string(domain.DeviceKind),
			Name: deviceName,
		},
		Reason:  domain.EventReasonDependencyChangeDetected,
		Details: &eventDetails,
	}
}

// newFleetOwnedLogic builds a DeviceRenderLogic that simulates a fleet-owned device.
func newFleetOwnedLogic(
	svc service.Service,
	k8s k8sclient.K8SClient,
	kv kvstore.KVStore,
	orgId uuid.UUID,
	event domain.Event,
	fleet, templateVersion string,
) DeviceRenderLogic {
	l := NewDeviceRenderLogic(logrus.New(), svc, k8s, kv, &config.Config{}, orgId, event)
	l.ownerFleet = &fleet
	l.templateVersion = &templateVersion
	return l
}

// emptyIgnitionConfig returns an empty ignition config suitable as the initial
// accumulator for renderConfigItem calls.
func emptyIgnitionConfig() config_latest_types.Config {
	return config_latest_types.Config{}
}

// TestGetDepChangeDetails verifies that the getDepChangeDetails helper correctly
// extracts the fingerprint and resource key from DependencyChangeDetected events
// and returns empty strings for other event types.
func TestGetDepChangeDetails(t *testing.T) {
	orgId := uuid.New()

	tests := []struct {
		name                string
		event               domain.Event
		expectedFingerprint string
		expectedResourceKey string
	}{
		{
			name:                "When event is DependencyChangeDetected it should return fingerprint and resource key",
			event:               newDepChangeEvent("device1", "git:my-repo/main", "sha-abc123"),
			expectedFingerprint: "sha-abc123",
			expectedResourceKey: "git:my-repo/main",
		},
		{
			name:                "When event reason is ResourceUpdated it should return empty strings",
			event:               createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1"),
			expectedFingerprint: "",
			expectedResourceKey: "",
		},
		{
			name: "When event is DependencyChangeDetected but has no details it should return empty strings",
			event: func() domain.Event {
				e := createTestEvent(domain.DeviceKind, domain.EventReasonDependencyChangeDetected, "device1")
				e.Details = nil
				return e
			}(),
			expectedFingerprint: "",
			expectedResourceKey: "",
		},
		{
			name:                "When event is DependencyChangeDetected for HTTP it should return etag fingerprint",
			event:               newDepChangeEvent("device1", "http:my-repo/config.json", `"etag-v2"`),
			expectedFingerprint: `"etag-v2"`,
			expectedResourceKey: "http:my-repo/config.json",
		},
		{
			name:                "When event is DependencyChangeDetected for K8s secret it should return resource version",
			event:               newDepChangeEvent("device1", "secret:default/my-secret", "rv-42"),
			expectedFingerprint: "rv-42",
			expectedResourceKey: "secret:default/my-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := DeviceRenderLogic{log: logrus.New(), orgId: orgId, event: tt.event}
			fp, rk := l.getDepChangeDetails()
			assert.Equal(t, tt.expectedFingerprint, fp)
			assert.Equal(t, tt.expectedResourceKey, rk)
		})
	}
}

// TestCloneCachedGitRepoToIgnition_CacheHit tests that when the git revision cache
// contains data and the fingerprint matches (or is absent), the function returns
// the cached ignition config without any Delete calls.
func TestCloneCachedGitRepoToIgnition_CacheHit(t *testing.T) {
	const (
		fleet           = "my-fleet"
		templateVersion = "tv-1"
		repoName        = "my-repo"
		targetRevision  = "main"
		cachedSHA       = "sha-abc123"
		configPath      = "/config"
		repoURL         = "https://example.com/repo.git"
	)

	orgId := uuid.New()
	minimalIgnJSON := []byte(`{"ignition":{"version":"3.4.0"}}`)

	repo := makeGitRepository(repoName, repoURL)

	gitRevKey := kvstore.GitRevisionKey{OrgID: orgId, Fleet: fleet, TemplateVersion: templateVersion, Repository: repoName, TargetRevision: targetRevision}
	gitDataKey := kvstore.GitContentsKey{OrgID: orgId, Fleet: fleet, TemplateVersion: templateVersion, Repository: repoName, TargetRevision: targetRevision, Path: configPath}
	repoURLKey := kvstore.RepositoryUrlKey{OrgID: orgId, Fleet: fleet, TemplateVersion: templateVersion, Repository: repoName}

	tests := []struct {
		name           string
		newFingerprint string
	}{
		{
			name:           "When fingerprint matches cached SHA it should serve from cache without deletion",
			newFingerprint: cachedSHA,
		},
		{
			name:           "When fingerprint is empty (non-dep-change event) it should serve from cache without deletion",
			newFingerprint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kv := newTestKVStore()
			kv.seed(repoURLKey.ComposeKey(), []byte(repoURL))
			kv.seed(gitRevKey.ComposeKey(), []byte(cachedSHA))
			kv.seed(gitDataKey.ComposeKey(), minimalIgnJSON)

			event := createTestEvent(domain.DeviceKind, domain.EventReasonDependencyChangeDetected, "device1")
			l := newFleetOwnedLogic(nil, nil, kv, orgId, event, fleet, templateVersion)

			ign, hash, err := l.cloneCachedGitRepoToIgnition(context.Background(), repo, targetRevision, configPath, tt.newFingerprint)

			require.NoError(t, err)
			assert.Equal(t, cachedSHA, hash)
			assert.NotNil(t, ign)
			assert.False(t, kv.wasDeleted(gitRevKey.ComposeKey()), "GitRevisionKey must not be deleted for matching/empty fingerprint")
			assert.False(t, kv.wasDeleted(gitDataKey.ComposeKey()), "GitContentsKey must not be deleted for matching/empty fingerprint")
		})
	}
}

// TestCloneCachedGitRepoToIgnition_StaleFingerprint verifies that when a
// DependencyChangeDetected event carries a newer commit SHA the stale cache
// entries are deleted before a re-fetch is attempted.
func TestCloneCachedGitRepoToIgnition_StaleFingerprint(t *testing.T) {
	const (
		fleet           = "my-fleet"
		templateVersion = "tv-1"
		repoName        = "my-repo"
		targetRevision  = "main"
		oldSHA          = "sha-old"
		newSHA          = "sha-new"
		configPath      = "/config"
	)

	orgId := uuid.New()
	// Use a loopback address with a refused port so the clone fails instantly
	// (no DNS lookup, immediate connection refused) rather than waiting for a
	// DNS timeout on an invalid domain.
	const unreachableURL = "http://127.0.0.1:1/repo.git"
	repo := makeGitRepository(repoName, unreachableURL)

	gitRevKey := kvstore.GitRevisionKey{OrgID: orgId, Fleet: fleet, TemplateVersion: templateVersion, Repository: repoName, TargetRevision: targetRevision}
	gitDataKey := kvstore.GitContentsKey{OrgID: orgId, Fleet: fleet, TemplateVersion: templateVersion, Repository: repoName, TargetRevision: targetRevision, Path: configPath}
	repoURLKey := kvstore.RepositoryUrlKey{OrgID: orgId, Fleet: fleet, TemplateVersion: templateVersion, Repository: repoName}

	kv := newTestKVStore()
	kv.seed(repoURLKey.ComposeKey(), []byte(unreachableURL))
	kv.seed(gitRevKey.ComposeKey(), []byte(oldSHA))
	kv.seed(gitDataKey.ComposeKey(), []byte(`{"ignition":{"version":"3.4.0"}}`))

	event := newDepChangeEvent("device1", fmt.Sprintf("git:%s/%s", repoName, targetRevision), newSHA)
	l := newFleetOwnedLogic(nil, nil, kv, orgId, event, fleet, templateVersion)

	// The clone will fail; that is expected. What matters is that the stale
	// cache entries are deleted BEFORE the clone attempt.
	_, _, err := l.cloneCachedGitRepoToIgnition(context.Background(), repo, targetRevision, configPath, newSHA)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed cloning git", "error should originate from the clone step, not from cache invalidation")

	assert.True(t, kv.wasDeleted(gitRevKey.ComposeKey()), "GitRevisionKey must be deleted to bust stale cache")
	assert.True(t, kv.wasDeleted(gitDataKey.ComposeKey()), "GitContentsKey must be deleted to bust stale cache")
	assert.False(t, kv.has(gitRevKey.ComposeKey()), "GitRevisionKey must be absent after deletion")
	assert.False(t, kv.has(gitDataKey.ComposeKey()), "GitContentsKey must be absent after deletion")
}

// TestCloneCachedGitRepoToIgnition_ResourceKeyMismatch verifies that a
// DependencyChangeDetected event for a different git resource does not trigger
// cache invalidation for the current config item.
func TestCloneCachedGitRepoToIgnition_ResourceKeyMismatch(t *testing.T) {
	const (
		fleet           = "my-fleet"
		templateVersion = "tv-1"
		repoName        = "my-repo"
		targetRevision  = "main"
		cachedSHA       = "sha-old"
		configPath      = "/config"
		repoURL         = "https://example.com/repo.git"
	)

	orgId := uuid.New()
	repo := makeGitRepository(repoName, repoURL)

	gitRevKey := kvstore.GitRevisionKey{OrgID: orgId, Fleet: fleet, TemplateVersion: templateVersion, Repository: repoName, TargetRevision: targetRevision}
	gitDataKey := kvstore.GitContentsKey{OrgID: orgId, Fleet: fleet, TemplateVersion: templateVersion, Repository: repoName, TargetRevision: targetRevision, Path: configPath}
	repoURLKey := kvstore.RepositoryUrlKey{OrgID: orgId, Fleet: fleet, TemplateVersion: templateVersion, Repository: repoName}

	kv := newTestKVStore()
	kv.seed(repoURLKey.ComposeKey(), []byte(repoURL))
	kv.seed(gitRevKey.ComposeKey(), []byte(cachedSHA))
	kv.seed(gitDataKey.ComposeKey(), []byte(`{"ignition":{"version":"3.4.0"}}`))

	// Event references a completely different repository.
	event := newDepChangeEvent("device1", "git:other-repo/other-branch", "sha-new")
	l := newFleetOwnedLogic(nil, nil, kv, orgId, event, fleet, templateVersion)

	// depResourceKey mismatch → fingerprint cleared → should serve from cache.
	_, hash, err := l.cloneCachedGitRepoToIgnition(context.Background(), repo, targetRevision, configPath, "")
	require.NoError(t, err)
	assert.Equal(t, cachedSHA, hash)
	assert.False(t, kv.wasDeleted(gitRevKey.ComposeKey()))
	assert.False(t, kv.wasDeleted(gitDataKey.ComposeKey()))
}

// TestRenderHttpProviderConfig_CacheInvalidation verifies the HTTP fingerprint-based
// cache invalidation: stale fingerprints cause both the body and fingerprint keys to
// be deleted and the endpoint to be re-fetched; matching fingerprints are served
// from cache without a remote call.
func TestRenderHttpProviderConfig_CacheInvalidation(t *testing.T) {
	const (
		repoName    = "http-repo"
		suffix      = "/config.json"
		filePath    = "/etc/config.json"
		fleet       = "my-fleet"
		tmplVersion = "tv-1"
		cachedBody  = `{"key":"old-value"}`
		freshBody   = `{"key":"new-value"}`
		oldETag     = `"etag-old"`
		newETag     = `"etag-new"`
	)

	orgId := uuid.New()

	tests := []struct {
		name              string
		cachedFingerprint string
		eventFingerprint  string
		eventResourceKey  string
		expectDelete      bool
		expectedBody      string
	}{
		{
			name:              "When cached fingerprint matches event fingerprint it should serve from cache",
			cachedFingerprint: newETag,
			eventFingerprint:  newETag,
			eventResourceKey:  fmt.Sprintf("http:%s%s", repoName, suffix),
			expectDelete:      false,
			expectedBody:      cachedBody,
		},
		{
			name:              "When cached fingerprint is stale it should delete both keys and re-fetch",
			cachedFingerprint: oldETag,
			eventFingerprint:  newETag,
			eventResourceKey:  fmt.Sprintf("http:%s%s", repoName, suffix),
			expectDelete:      true,
			expectedBody:      freshBody,
		},
		{
			name:              "When fingerprint is empty (non-dep-change event) it should serve from cache",
			cachedFingerprint: oldETag,
			eventFingerprint:  "",
			eventResourceKey:  "",
			expectDelete:      false,
			expectedBody:      cachedBody,
		},
		{
			name:              "When resource key does not match it should serve from cache",
			cachedFingerprint: oldETag,
			eventFingerprint:  newETag,
			eventResourceKey:  "http:other-repo/other-path",
			expectDelete:      false,
			expectedBody:      cachedBody,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(freshBody))
			}))
			defer srv.Close()

			repoURL := srv.URL
			fullURL := repoURL + suffix

			repo := makeHTTPRepository(repoName, repoURL)
			mockSvc := service.NewMockService(ctrl)
			mockSvc.EXPECT().GetRepository(gomock.Any(), orgId, repoName).Return(repo, statusOK)

			kv := newTestKVStore()
			httpKey := kvstore.HttpKey{OrgID: orgId, Fleet: fleet, TemplateVersion: tmplVersion, URL: fullURL}
			httpFPKey := kvstore.HttpFingerprintKey{OrgID: orgId, Fleet: fleet, TemplateVersion: tmplVersion, URL: fullURL}
			repoURLKey := kvstore.RepositoryUrlKey{OrgID: orgId, Fleet: fleet, TemplateVersion: tmplVersion, Repository: repoName}

			kv.seed(repoURLKey.ComposeKey(), []byte(repoURL))
			kv.seed(httpKey.ComposeKey(), []byte(cachedBody))
			kv.seed(httpFPKey.ComposeKey(), []byte(tt.cachedFingerprint))

			var event domain.Event
			if tt.eventFingerprint != "" {
				event = newDepChangeEvent("device1", tt.eventResourceKey, tt.eventFingerprint)
			} else {
				event = createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1")
			}

			l := newFleetOwnedLogic(mockSvc, nil, kv, orgId, event, fleet, tmplVersion)

			configItem := makeHTTPConfigItem("http-config", repoName, suffix, filePath)
			empty := emptyIgnitionConfig()
			ignCfg := &empty

			_, _, _, err := l.renderHttpProviderConfig(context.Background(), &configItem, &ignCfg)
			require.NoError(t, err)

			if tt.expectDelete {
				assert.True(t, kv.wasDeleted(httpKey.ComposeKey()), "HttpKey should be deleted for stale fingerprint")
				assert.True(t, kv.wasDeleted(httpFPKey.ComposeKey()), "HttpFingerprintKey should be deleted for stale fingerprint")
			} else {
				assert.False(t, kv.wasDeleted(httpKey.ComposeKey()), "HttpKey must not be deleted")
				assert.False(t, kv.wasDeleted(httpFPKey.ComposeKey()), "HttpFingerprintKey must not be deleted")
			}

			require.NotNil(t, ignCfg)
			require.Len(t, ignCfg.Storage.Files, 1)
			rawSource := lo.FromPtr(ignCfg.Storage.Files[0].Contents.Source)
			// Ignition stores file content as a data URL (data:text/plain;base64,<b64>).
			fileContent := decodeIgnitionFileContent(t, rawSource)
			assert.Equal(t, tt.expectedBody, fileContent)
		})
	}
}

// TestRenderK8sConfig_CacheInvalidation verifies the K8s secret ResourceVersion-based
// cache invalidation: a differing ResourceVersion causes the cache entry to be deleted
// and the secret to be re-fetched; a matching one is served directly from cache.
func TestRenderK8sConfig_CacheInvalidation(t *testing.T) {
	const (
		fleet       = "my-fleet"
		tmplVersion = "tv-1"
		namespace   = "default"
		secretName  = "my-secret"
		mountPath   = "/etc/secret"
		oldRV       = "rv-1"
		newRV       = "rv-2"
	)

	orgId := uuid.New()
	secretData := map[string][]byte{"token": []byte("supersecret")}

	tests := []struct {
		name             string
		cachedRV         string
		eventFingerprint string
		eventResourceKey string
		expectDelete     bool
		expectK8sFetch   bool
	}{
		{
			name:             "When cached ResourceVersion matches event fingerprint it should serve from cache",
			cachedRV:         newRV,
			eventFingerprint: newRV,
			eventResourceKey: fmt.Sprintf("secret:%s/%s", namespace, secretName),
			expectDelete:     false,
			expectK8sFetch:   false,
		},
		{
			name:             "When cached ResourceVersion is stale it should delete cache and re-fetch",
			cachedRV:         oldRV,
			eventFingerprint: newRV,
			eventResourceKey: fmt.Sprintf("secret:%s/%s", namespace, secretName),
			expectDelete:     true,
			expectK8sFetch:   true,
		},
		{
			name:             "When fingerprint is empty (non-dep-change event) it should serve from cache",
			cachedRV:         oldRV,
			eventFingerprint: "",
			eventResourceKey: "",
			expectDelete:     false,
			expectK8sFetch:   false,
		},
		{
			name:             "When resource key does not match it should serve from cache",
			cachedRV:         oldRV,
			eventFingerprint: newRV,
			eventResourceKey: "secret:other-ns/other-secret",
			expectDelete:     false,
			expectK8sFetch:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			kv := newTestKVStore()
			k8sKey := kvstore.K8sSecretKey{OrgID: orgId, Fleet: fleet, TemplateVersion: tmplVersion, Namespace: namespace, Name: secretName}

			cachedEntry := cachedSecretData{Data: secretData, ResourceVersion: tt.cachedRV}
			cachedBytes, err := json.Marshal(cachedEntry)
			require.NoError(t, err)
			kv.seed(k8sKey.ComposeKey(), cachedBytes)

			mockK8S := k8sclient.NewMockK8SClient(ctrl)
			if tt.expectK8sFetch {
				mockK8S.EXPECT().GetSecret(gomock.Any(), namespace, secretName).Return(
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{ResourceVersion: newRV},
						Data:       secretData,
					}, nil,
				)
			}

			var event domain.Event
			if tt.eventFingerprint != "" {
				event = newDepChangeEvent("device1", tt.eventResourceKey, tt.eventFingerprint)
			} else {
				event = createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, "device1")
			}

			l := newFleetOwnedLogic(nil, mockK8S, kv, orgId, event, fleet, tmplVersion)

			configItem := makeK8sSecretConfigItem("k8s-config", namespace, secretName, mountPath)
			empty := emptyIgnitionConfig()
			ignCfg := &empty

			_, _, _, err = l.renderK8sConfig(context.Background(), &configItem, &ignCfg)
			require.NoError(t, err)

			if tt.expectDelete {
				assert.True(t, kv.wasDeleted(k8sKey.ComposeKey()), "K8sSecretKey must be deleted for stale ResourceVersion")
			} else {
				assert.False(t, kv.wasDeleted(k8sKey.ComposeKey()), "K8sSecretKey must not be deleted")
			}
		})
	}
}

// decodeIgnitionFileContent decodes the data URL (data:...;base64,<b64>) that ignition
// uses to embed file contents and returns the raw bytes as a string.
func decodeIgnitionFileContent(t *testing.T, source string) string {
	t.Helper()
	const prefix = "base64,"
	idx := strings.Index(source, prefix)
	if idx == -1 {
		return source
	}
	decoded, err := base64.StdEncoding.DecodeString(source[idx+len(prefix):])
	require.NoError(t, err, "failed to decode ignition file content")
	return string(decoded)
}

// --- domain object helpers ---

func makeGitRepository(name, url string) *domain.Repository {
	spec := api.RepositorySpec{}
	_ = spec.FromGitRepoSpec(api.GitRepoSpec{
		Type: api.GitRepoSpecTypeGit,
		Url:  url,
	})
	return &api.Repository{
		Metadata: api.ObjectMeta{Name: &name},
		Spec:     spec,
	}
}

func makeHTTPRepository(name, url string) *domain.Repository {
	spec := api.RepositorySpec{}
	_ = spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Type: api.HttpRepoSpecTypeHttp,
		Url:  url,
	})
	return &api.Repository{
		Metadata: api.ObjectMeta{Name: &name},
		Spec:     spec,
	}
}

func makeHTTPConfigItem(configName, repoName, suffix, filePath string) domain.ConfigProviderSpec {
	item := domain.ConfigProviderSpec{}
	_ = item.FromHttpConfigProviderSpec(api.HttpConfigProviderSpec{
		Name: configName,
		HttpRef: struct {
			FilePath   string  `json:"filePath"`
			Repository string  `json:"repository"`
			Suffix     *string `json:"suffix,omitempty"`
		}{
			FilePath:   filePath,
			Repository: repoName,
			Suffix:     lo.ToPtr(suffix),
		},
	})
	return item
}

func makeK8sSecretConfigItem(configName, namespace, name, mountPath string) domain.ConfigProviderSpec {
	item := domain.ConfigProviderSpec{}
	_ = item.FromKubernetesSecretProviderSpec(api.KubernetesSecretProviderSpec{
		Name: configName,
		SecretRef: struct {
			Group     string       `json:"group,omitempty"`
			MountPath string       `json:"mountPath"`
			Name      string       `json:"name"`
			Namespace string       `json:"namespace"`
			User      api.Username `json:"user,omitempty"`
		}{
			Namespace: namespace,
			Name:      name,
			MountPath: mountPath,
		},
	})
	return item
}
