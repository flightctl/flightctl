package tasks

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

type emittedEvent struct {
	kind string
	name string
}

func gitRepoSpec(t *testing.T, url string) *model.JSONField[api.RepositorySpec] {
	t.Helper()
	spec := api.RepositorySpec{}
	err := spec.FromGitRepoSpec(api.GitRepoSpec{
		Type: api.GitRepoSpecTypeGit,
		Url:  url,
	})
	require.NoError(t, err)
	return model.MakeJSONField(spec)
}

func makeProbe(repoName, revision string, fingerprint *string, fleetNames, deviceNames model.StringArray, repoSpec *model.JSONField[api.RepositorySpec]) model.GitDependencyProbe {
	return model.GitDependencyProbe{
		RepositoryName: repoName,
		Revision:       revision,
		Fingerprint:    fingerprint,
		FleetNames:     fleetNames,
		DeviceNames:    deviceNames,
		RepoSpec:       repoSpec,
	}
}

var statusOK = domain.Status{Code: http.StatusOK}

func TestDependencySyncGit_Poll(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()
	pollInterval := 15 * time.Minute

	t.Run("When a change is detected it should bulk upsert sync state and emit events", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", lo.ToPtr("oldsha999"), model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 1)
				assert.Equal(t, "newsha123456789", states[0].Fingerprint)
				assert.Equal(t, "git:my-repo/main", states[0].ResourceKey)
				return statusOK
			})

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		lsRemote := func(_ context.Context, _ string, refs []string, _ transport.AuthMethod) (map[string]string, error) {
			return map[string]string{"main": "newsha123456789"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)

		require.Len(t, events, 1)
		assert.Equal(t, string(domain.FleetKind), events[0].kind)
		assert.Equal(t, "fleet-1", events[0].name)
	})

	t.Run("When no change is detected it should update last_checked_at and clear any stale ProbeFailed", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", lo.ToPtr("samesha123"), model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpdateSyncStateLastCheckedAt(gomock.Any(), orgId, gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, keys []string, _ time.Time) domain.Status {
				require.Len(t, keys, 1)
				assert.Equal(t, "git:my-repo/main", keys[0])
				return statusOK
			})

		lsRemote := func(_ context.Context, _ string, _ []string, _ transport.AuthMethod) (map[string]string, error) {
			return map[string]string{"main": "samesha123"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When probe errors it should emit probe failure events and set ProbeFailed status", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", nil, model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			assert.Equal(t, domain.EventReasonDependencySyncProbeFailed, event.Reason)
			assert.Equal(t, domain.FleetKind, event.InvolvedObject.Kind)
			assert.Equal(t, "fleet-1", event.InvolvedObject.Name)
		})

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 1)
				assert.Equal(t, "ProbeFailed", states[0].ProbeStatus)
				assert.Contains(t, states[0].ProbeMessage, "connection refused")
				return statusOK
			},
		)

		lsRemote := func(_ context.Context, _ string, _ []string, _ transport.AuthMethod) (map[string]string, error) {
			return nil, fmt.Errorf("connection refused")
		}

		d := &DependencySyncGit{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When work list is empty it should be a no-op", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		mockService.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return([]model.GitDependencyProbe{}, statusOK)

		d := &DependencySyncGit{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, lsRemote: func(_ context.Context, _ string, _ []string, _ transport.AuthMethod) (map[string]string, error) {
				t.Fatal("ls-remote should not be called with empty work list")
				return nil, nil
			}, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When multiple fleets reference the same repo it should fan out events to each", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("shared-repo", "main", lo.ToPtr("oldsha"), model.StringArray{"fleet-a", "fleet-b"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Times(2).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		lsRemote := func(_ context.Context, _ string, _ []string, _ transport.AuthMethod) (map[string]string, error) {
			return map[string]string{"main": "newsha456"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
		assert.Len(t, events, 2)
	})

	t.Run("When standalone device has a dependency it should emit device event", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", lo.ToPtr("oldsha"), nil, model.StringArray{"device-standalone"}, repoSpec),
		}
		mockService.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		lsRemote := func(_ context.Context, _ string, _ []string, _ transport.AuthMethod) (map[string]string, error) {
			return map[string]string{"main": "devicenewsha"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
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

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("new-repo", "main", nil, model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 1)
				assert.Equal(t, "initialsha123", states[0].Fingerprint)
				return statusOK
			})

		lsRemote := func(_ context.Context, _ string, _ []string, _ transport.AuthMethod) (map[string]string, error) {
			return map[string]string{"main": "initialsha123"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When multiple revisions exist for the same repo it should call ls-remote once", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", lo.ToPtr("oldsha1"), model.StringArray{"fleet-1"}, nil, repoSpec),
			makeProbe("my-repo", "v1.0", lo.ToPtr("oldsha2"), model.StringArray{"fleet-2"}, nil, repoSpec),
		}
		mockService.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		lsRemoteCalls := 0
		lsRemote := func(_ context.Context, _ string, refs []string, _ transport.AuthMethod) (map[string]string, error) {
			lsRemoteCalls++
			assert.Len(t, refs, 2)
			return map[string]string{
				"main": "newsha1",
				"v1.0": "newsha2",
			}, nil
		}

		mockService.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				assert.Len(t, states, 2)
				return statusOK
			})

		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Times(2)

		d := &DependencySyncGit{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)

		assert.Equal(t, 1, lsRemoteCalls, "ls-remote should be called once per repo, not per revision")
	})
}

func TestNewDependencySyncGit(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockService := service.NewMockService(ctrl)

	d := NewDependencySyncGit(logrus.New(), mockService, &config.Config{}, nil, nil)
	require.NotNil(t, d)
	assert.Equal(t, 10, d.maxConcurrent)
	assert.NotNil(t, d.lsRemote)
}
