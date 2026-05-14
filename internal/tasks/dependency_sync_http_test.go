package tasks

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func httpRepoSpec(t *testing.T, url string) *model.JSONField[api.RepositorySpec] {
	t.Helper()
	spec := api.RepositorySpec{}
	err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Type: api.HttpRepoSpecTypeHttp,
		Url:  url,
	})
	require.NoError(t, err)
	return model.MakeJSONField(spec)
}

func makeHttpProbe(repoName, suffix string, fingerprint *string, fleetNames, deviceNames model.StringArray, repoSpec *model.JSONField[api.RepositorySpec]) model.HttpDependencyProbe {
	return model.HttpDependencyProbe{
		RepositoryName: repoName,
		HTTPSuffix:     suffix,
		Fingerprint:    fingerprint,
		FleetNames:     fleetNames,
		DeviceNames:    deviceNames,
		RepoSpec:       repoSpec,
	}
}

func TestDependencySyncHttp_Poll(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()
	pollInterval := 15 * time.Minute

	t.Run("When a change is detected it should bulk upsert sync state and emit events", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := httpRepoSpec(t, "https://example.com")
		probes := []model.HttpDependencyProbe{
			makeHttpProbe("http-repo", "/config.json", lo.ToPtr(`"old-etag"`), model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 1)
				assert.Equal(t, `"new-etag"`, states[0].Fingerprint)
				assert.Equal(t, "http:http-repo/config.json", states[0].ResourceKey)
				return statusOK
			})

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		conditionalHead := func(_ context.Context, _ *http.Client, _ string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
			return `"new-etag"`, http.StatusOK, nil
		}

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: conditionalHead, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)

		require.Len(t, events, 1)
		assert.Equal(t, string(domain.FleetKind), events[0].kind)
		assert.Equal(t, "fleet-1", events[0].name)
	})

	// gomock strict mode: any unexpected call (e.g. BulkUpsertSyncState, CreateEvent) fails the test.
	t.Run("When no change is detected (304) it should bulk update last_checked_at only", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := httpRepoSpec(t, "https://example.com")
		probes := []model.HttpDependencyProbe{
			makeHttpProbe("http-repo", "/config.json", lo.ToPtr(`"same-etag"`), model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpdateSyncStateLastCheckedAt(gomock.Any(), orgId, gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, keys []string, _ time.Time) domain.Status {
				require.Len(t, keys, 1)
				assert.Equal(t, "http:http-repo/config.json", keys[0])
				return statusOK
			})

		conditionalHead := func(_ context.Context, _ *http.Client, _ string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
			return "", http.StatusNotModified, nil
		}

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: conditionalHead, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When endpoint lacks ETag and Last-Modified it should log warning and still update last_checked_at", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := httpRepoSpec(t, "https://example.com")
		probes := []model.HttpDependencyProbe{
			makeHttpProbe("http-repo", "/config.json", lo.ToPtr(`"old-etag"`), model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpdateSyncStateLastCheckedAt(gomock.Any(), orgId, gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, keys []string, _ time.Time) domain.Status {
				require.Len(t, keys, 1)
				assert.Equal(t, "http:http-repo/config.json", keys[0])
				return statusOK
			})

		conditionalHead := func(_ context.Context, _ *http.Client, _ string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
			return "", http.StatusOK, nil
		}

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: conditionalHead, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When HTTP request errors it should emit probe failure event and record ProbeFailed status", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := httpRepoSpec(t, "https://example.com")
		probes := []model.HttpDependencyProbe{
			makeHttpProbe("failing-repo", "/fail", lo.ToPtr(`"etag1"`), model.StringArray{"fleet-1"}, nil, repoSpec),
			makeHttpProbe("ok-repo", "/ok", lo.ToPtr(`"old-etag"`), model.StringArray{"fleet-2"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 2)
				failState := states[0]
				okState := states[1]
				if failState.ProbeStatus != "ProbeFailed" {
					failState, okState = okState, failState
				}
				assert.Equal(t, "ProbeFailed", failState.ProbeStatus)
				assert.Contains(t, failState.ProbeMessage, "connection refused")
				assert.Equal(t, `"new-etag"`, okState.Fingerprint)
				assert.Equal(t, "Synced", okState.ProbeStatus)
				return statusOK
			})

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Times(2).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		conditionalHead := func(_ context.Context, _ *http.Client, url string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
			if url == "https://example.com/fail" {
				return "", 0, fmt.Errorf("connection refused")
			}
			return `"new-etag"`, http.StatusOK, nil
		}

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: conditionalHead, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)

		require.Len(t, events, 2)
	})

	t.Run("When work list is empty it should be a no-op", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return([]model.HttpDependencyProbe{}, statusOK)

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: func(_ context.Context, _ *http.Client, _ string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
				t.Fatal("conditionalHead should not be called with empty work list")
				return "", 0, nil
			}, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When multiple fleets reference the same repo+suffix it should fan out events to each", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := httpRepoSpec(t, "https://example.com")
		probes := []model.HttpDependencyProbe{
			makeHttpProbe("shared-repo", "/config.json", lo.ToPtr(`"old-etag"`), model.StringArray{"fleet-a", "fleet-b"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Times(2).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		conditionalHead := func(_ context.Context, _ *http.Client, _ string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
			return `"new-etag"`, http.StatusOK, nil
		}

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: conditionalHead, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
		assert.Len(t, events, 2)
	})

	t.Run("When standalone device has a dependency it should emit device event", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := httpRepoSpec(t, "https://example.com")
		probes := []model.HttpDependencyProbe{
			makeHttpProbe("http-repo", "/config.json", lo.ToPtr(`"old-etag"`), nil, model.StringArray{"device-standalone"}, repoSpec),
		}
		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		conditionalHead := func(_ context.Context, _ *http.Client, _ string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
			return `"new-etag"`, http.StatusOK, nil
		}

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: conditionalHead, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)

		require.Len(t, events, 1)
		assert.Equal(t, string(domain.DeviceKind), events[0].kind)
		assert.Equal(t, "device-standalone", events[0].name)
	})

	t.Run("When first seen it should store fingerprint without emitting events", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := httpRepoSpec(t, "https://example.com")
		probes := []model.HttpDependencyProbe{
			makeHttpProbe("http-repo", "/config.json", nil, model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 1)
				assert.Equal(t, `"initial-etag"`, states[0].Fingerprint)
				return statusOK
			})

		conditionalHead := func(_ context.Context, _ *http.Client, _ string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
			return `"initial-etag"`, http.StatusOK, nil
		}

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: conditionalHead, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When repository spec is nil it should skip the probe entirely", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		probes := []model.HttpDependencyProbe{
			makeHttpProbe("orphan-repo", "/config.json", nil, model.StringArray{"fleet-1"}, nil, nil),
		}
		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)
		// gomock strict mode: no BulkUpsert/BulkUpdate/CreateEvent calls expected.

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: func(_ context.Context, _ *http.Client, _ string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
				t.Fatal("conditionalHead should not be called when RepoSpec is nil")
				return "", 0, nil
			}, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When multiple probes exist each should get its own HTTP request", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := httpRepoSpec(t, "https://example.com")
		probes := []model.HttpDependencyProbe{
			makeHttpProbe("repo-1", "/a.json", lo.ToPtr(`"etag-a"`), model.StringArray{"fleet-1"}, nil, repoSpec),
			makeHttpProbe("repo-1", "/b.json", lo.ToPtr(`"etag-b"`), model.StringArray{"fleet-2"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueHttpDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		requestedURLs := make(map[string]bool)
		var mu sync.Mutex
		conditionalHead := func(_ context.Context, _ *http.Client, url string, _ domain.HttpRepoSpec, _ string) (string, int, error) {
			mu.Lock()
			requestedURLs[url] = true
			mu.Unlock()
			return `"new-etag"`, http.StatusOK, nil
		}

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Times(2)

		d := &DependencySyncHttp{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, conditionalHead: conditionalHead, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)

		assert.True(t, requestedURLs["https://example.com/a.json"])
		assert.True(t, requestedURLs["https://example.com/b.json"])
	})
}

func TestNewDependencySyncHttp(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockService := service.NewMockService(ctrl)

	d := NewDependencySyncHttp(logrus.New(), mockService, &config.Config{}, nil, nil)
	require.NotNil(t, d)
	assert.Equal(t, 10, d.maxConcurrent)
	assert.NotNil(t, d.conditionalHead)
}

func plainHttpSpec() domain.HttpRepoSpec {
	return domain.HttpRepoSpec{
		Type: domain.HttpRepoSpecTypeHttp,
		Url:  "http://ignored",
	}
}

func TestHttpConditionalHead(t *testing.T) {
	ctx := context.Background()
	spec := plainHttpSpec()
	client := &http.Client{Timeout: 5 * time.Second}

	t.Run("When endpoint returns ETag and supports conditional HEAD it should return 304 on match", func(t *testing.T) {
		etag := `"abc123"`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodHead, r.Method)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
		}))
		defer srv.Close()

		fp, status, err := httpConditionalHead(ctx, client, srv.URL, spec, "")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, etag, fp)

		fp, status, err = httpConditionalHead(ctx, client, srv.URL, spec, etag)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotModified, status)
		assert.Empty(t, fp)
	})

	t.Run("When endpoint returns ETag but ignores conditional HEAD it should return new ETag", func(t *testing.T) {
		etag := `"v2"`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodHead, r.Method)
			w.Header().Set("ETag", etag)
		}))
		defer srv.Close()

		fp, status, err := httpConditionalHead(ctx, client, srv.URL, spec, `"v1"`)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, etag, fp)
	})

	t.Run("When endpoint returns Last-Modified and supports conditional HEAD it should return 304 on match", func(t *testing.T) {
		lastMod := "Wed, 14 May 2026 08:00:00 GMT"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodHead, r.Method)
			if r.Header.Get("If-Modified-Since") == lastMod {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Last-Modified", lastMod)
		}))
		defer srv.Close()

		fp, status, err := httpConditionalHead(ctx, client, srv.URL, spec, "")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, lastMod, fp)

		fp, status, err = httpConditionalHead(ctx, client, srv.URL, spec, lastMod)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotModified, status)
		assert.Empty(t, fp)
	})

	t.Run("When endpoint returns Last-Modified but ignores conditional HEAD it should return new timestamp", func(t *testing.T) {
		lastMod := "Wed, 14 May 2026 09:00:00 GMT"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodHead, r.Method)
			w.Header().Set("Last-Modified", lastMod)
		}))
		defer srv.Close()

		fp, status, err := httpConditionalHead(ctx, client, srv.URL, spec, "Tue, 13 May 2026 08:00:00 GMT")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, lastMod, fp)
	})

	t.Run("When endpoint does not support HEAD it should return error with 405", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer srv.Close()

		_, status, err := httpConditionalHead(ctx, client, srv.URL, spec, "")
		require.Error(t, err)
		assert.Equal(t, http.StatusMethodNotAllowed, status)
		assert.Contains(t, err.Error(), "405")
	})

	t.Run("When endpoint returns 4xx or 5xx it should return error with status code", func(t *testing.T) {
		codes := []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError, http.StatusBadGateway}
		for _, code := range codes {
			t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(code)
				}))
				defer srv.Close()

				_, status, err := httpConditionalHead(ctx, client, srv.URL, spec, "")
				require.Error(t, err)
				assert.Equal(t, code, status)
			})
		}
	})
}
