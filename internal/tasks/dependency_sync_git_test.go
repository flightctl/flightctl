package tasks

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/periodic"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	syncstateservice "github.com/flightctl/flightctl/internal/service/syncstate"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/go-git/go-git/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
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

func TestDependencySyncGitPollInvalidTLSOptions(t *testing.T) {
	cases := []struct {
		name      string
		caBundle  string
		wantError string
	}{
		{name: "When the CA encoding is invalid it should record failures without connecting", caBundle: "invalid-base64", wantError: "decode CA bundle"},
		{name: "When the CA bundle is empty it should record failures without connecting", caBundle: "", wantError: "parse CA bundle: no valid PEM certificates"},
		{name: "When the CA bundle is not PEM it should record failures without connecting", caBundle: base64.StdEncoding.EncodeToString([]byte("not a certificate")), wantError: "parse CA bundle: no valid PEM certificates"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
			mockEventSvc := eventservice.NewMockService(ctrl)
			mockSyncStateSvc := syncstateservice.NewMockService(ctrl)
			orgID := uuid.New()
			ctx := context.Background()
			pollInterval := 15 * time.Minute
			spec := api.RepositorySpec{}
			require.NoError(t, spec.FromGitRepoSpec(api.GitRepoSpec{
				Type: api.GitRepoSpecTypeGit, Url: "https://example.com/repo.git", HttpConfig: &api.HttpConfig{CaCrt: lo.ToPtr(tc.caBundle)},
			}))
			repoSpec := model.MakeJSONField(spec)
			probes := []model.GitDependencyProbe{
				makeProbe("my-repo", "main", lo.ToPtr("previous-main-sha"), model.StringArray{"fleet-1"}, nil, repoSpec),
				makeProbe("my-repo", "stable", lo.ToPtr("previous-stable-sha"), nil, model.StringArray{"device-1"}, repoSpec),
			}
			mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgID, pollInterval).Return(probes, statusOK)
			var involvedObjects []string
			mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgID, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
				involvedObjects = append(involvedObjects, event.InvolvedObject.Kind+"/"+event.InvolvedObject.Name)
				require.Equal(t, domain.EventReasonDependencySyncProbeFailed, event.Reason)
				details, err := event.Details.AsDependencySyncProbeFailedDetails()
				require.NoError(t, err)
				require.Contains(t, details.Error, tc.wantError)
				switch event.InvolvedObject.Kind {
				case string(domain.FleetKind):
					require.Equal(t, "fleet-1", event.InvolvedObject.Name)
					require.Equal(t, "git:my-repo/main", details.ResourceKey)
				case string(domain.DeviceKind):
					require.Equal(t, "device-1", event.InvolvedObject.Name)
					require.Equal(t, "git:my-repo/stable", details.ResourceKey)
				default:
					t.Errorf("unexpected involved object kind: %s", event.InvolvedObject.Kind)
				}
			}).Times(2)
			mockSyncStateSvc.EXPECT().BulkUpsertSyncState(gomock.Any(), orgID, gomock.Any()).DoAndReturn(func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 2)
				keys := make([]string, 0, len(states))
				for _, state := range states {
					require.Equal(t, "ProbeFailed", state.ProbeStatus)
					require.Contains(t, state.ProbeMessage, tc.wantError)
					// BulkUpsert preserves the previously stored fingerprint on failures.
					require.Empty(t, state.Fingerprint)
					require.False(t, state.LastCheckedAt.IsZero())
					keys = append(keys, state.ResourceKey)
				}
				require.ElementsMatch(t, []string{"git:my-repo/main", "git:my-repo/stable"}, keys)
				return statusOK
			})
			metrics := periodic.NewDependencySyncCollector()
			registry := prometheus.NewRegistry()
			require.NoError(t, registry.Register(metrics))
			d := &DependencySyncGit{
				log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
				cfg: &config.Config{}, maxConcurrent: 10, metrics: metrics,
				lsRemote: func(context.Context, string, []string, *git.ListOptions) (map[string]string, error) {
					t.Error("invalid TLS options must not call ls-remote")
					return nil, fmt.Errorf("unexpected ls-remote call")
				},
			}
			d.Poll(ctx, orgID)
			require.ElementsMatch(t, []string{string(domain.FleetKind) + "/fleet-1", string(domain.DeviceKind) + "/device-1"}, involvedObjects)
			families, err := registry.Gather()
			require.NoError(t, err)
			found := false
			for _, family := range families {
				if family.GetName() == "flightctl_dependency_sync_probe_errors_total" {
					found = true
					require.Len(t, family.Metric, 1)
					require.Len(t, family.Metric[0].Label, 1)
					require.Equal(t, "ref_type", family.Metric[0].Label[0].GetName())
					require.Equal(t, periodic.RefTypeGit, family.Metric[0].Label[0].GetValue())
					require.Equal(t, float64(1), family.Metric[0].GetCounter().GetValue(), "one error per repository, regardless of revision count")
				}
			}
			require.True(t, found, "Git probe error metric must be emitted")
		})
	}
}

func TestDependencySyncGit_Poll(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()
	pollInterval := 15 * time.Minute

	t.Run("When a change is detected it should bulk upsert sync state and emit events", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockSyncStateSvc := syncstateservice.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", lo.ToPtr("oldsha999"), model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockSyncStateSvc.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 1)
				assert.Equal(t, "newsha123456789", states[0].Fingerprint)
				assert.Equal(t, "git:my-repo/main", states[0].ResourceKey)
				return statusOK
			})

		var events []emittedEvent
		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		lsRemote := func(_ context.Context, _ string, refs []string, _ *git.ListOptions) (map[string]string, error) {
			return map[string]string{"main": "newsha123456789"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
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
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockSyncStateSvc := syncstateservice.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", lo.ToPtr("samesha123"), model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockSyncStateSvc.EXPECT().BulkUpdateSyncStateLastCheckedAt(gomock.Any(), orgId, gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, keys []string, _ time.Time) domain.Status {
				require.Len(t, keys, 1)
				assert.Equal(t, "git:my-repo/main", keys[0])
				return statusOK
			})

		lsRemote := func(_ context.Context, _ string, _ []string, _ *git.ListOptions) (map[string]string, error) {
			return map[string]string{"main": "samesha123"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When probe errors it should emit probe failure events and set ProbeFailed status", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockSyncStateSvc := syncstateservice.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", nil, model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			assert.Equal(t, domain.EventReasonDependencySyncProbeFailed, event.Reason)
			assert.Equal(t, domain.FleetKind, event.InvolvedObject.Kind)
			assert.Equal(t, "fleet-1", event.InvolvedObject.Name)
		})

		mockSyncStateSvc.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 1)
				assert.Equal(t, "ProbeFailed", states[0].ProbeStatus)
				assert.Contains(t, states[0].ProbeMessage, "connection refused")
				return statusOK
			},
		)

		lsRemote := func(_ context.Context, _ string, _ []string, _ *git.ListOptions) (map[string]string, error) {
			return nil, fmt.Errorf("connection refused")
		}

		d := &DependencySyncGit{
			log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When work list is empty it should be a no-op", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockSyncStateSvc := syncstateservice.NewMockService(ctrl)

		mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return([]model.GitDependencyProbe{}, statusOK)

		d := &DependencySyncGit{
			log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
			cfg: &config.Config{}, lsRemote: func(_ context.Context, _ string, _ []string, _ *git.ListOptions) (map[string]string, error) {
				t.Fatal("ls-remote should not be called with empty work list")
				return nil, nil
			}, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When multiple fleets reference the same repo it should fan out events to each", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockSyncStateSvc := syncstateservice.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("shared-repo", "main", lo.ToPtr("oldsha"), model.StringArray{"fleet-a", "fleet-b"}, nil, repoSpec),
		}
		mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockSyncStateSvc.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Times(2).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		lsRemote := func(_ context.Context, _ string, _ []string, _ *git.ListOptions) (map[string]string, error) {
			return map[string]string{"main": "newsha456"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
		assert.Len(t, events, 2)
	})

	t.Run("When standalone device has a dependency it should emit device event", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockSyncStateSvc := syncstateservice.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", lo.ToPtr("oldsha"), nil, model.StringArray{"device-standalone"}, repoSpec),
		}
		mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockSyncStateSvc.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		lsRemote := func(_ context.Context, _ string, _ []string, _ *git.ListOptions) (map[string]string, error) {
			return map[string]string{"main": "devicenewsha"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
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
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockSyncStateSvc := syncstateservice.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("new-repo", "main", nil, model.StringArray{"fleet-1"}, nil, repoSpec),
		}
		mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		mockSyncStateSvc.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				require.Len(t, states, 1)
				assert.Equal(t, "initialsha123", states[0].Fingerprint)
				return statusOK
			})

		lsRemote := func(_ context.Context, _ string, _ []string, _ *git.ListOptions) (map[string]string, error) {
			return map[string]string{"main": "initialsha123"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When multiple revisions exist for the same repo it should call ls-remote once", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockSyncStateSvc := syncstateservice.NewMockService(ctrl)

		repoSpec := gitRepoSpec(t, "https://example.com/repo.git")
		probes := []model.GitDependencyProbe{
			makeProbe("my-repo", "main", lo.ToPtr("oldsha1"), model.StringArray{"fleet-1"}, nil, repoSpec),
			makeProbe("my-repo", "v1.0", lo.ToPtr("oldsha2"), model.StringArray{"fleet-2"}, nil, repoSpec),
		}
		mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgId, pollInterval).Return(probes, statusOK)

		lsRemoteCalls := 0
		lsRemote := func(_ context.Context, _ string, refs []string, _ *git.ListOptions) (map[string]string, error) {
			lsRemoteCalls++
			assert.Len(t, refs, 2)
			return map[string]string{
				"main": "newsha1",
				"v1.0": "newsha2",
			}, nil
		}

		mockSyncStateSvc.EXPECT().BulkUpsertSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
				assert.Len(t, states, 2)
				return statusOK
			})

		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Times(2)

		d := &DependencySyncGit{
			log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)

		assert.Equal(t, 1, lsRemoteCalls, "ls-remote should be called once per repo, not per revision")
	})
}

func TestDependencySyncGitRecoveryWithoutChange(t *testing.T) {
	const sha = "previous-sha"
	cases := []struct {
		name        string
		fingerprint *string
		firstSeen   bool
	}{
		{name: "When a known fingerprint is unchanged after recovery it should clear the failure without a rollout", fingerprint: lo.ToPtr(sha)},
		{name: "When the first failed probe has an empty fingerprint it should record the initial SHA without a rollout", fingerprint: lo.ToPtr(""), firstSeen: true},
		{name: "When a dependency has never been probed it should record the initial SHA without a rollout", firstSeen: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
			mockEventSvc := eventservice.NewMockService(ctrl)
			mockSyncStateSvc := syncstateservice.NewMockService(ctrl)
			orgID := uuid.New()
			probes := []model.GitDependencyProbe{makeProbe("my-repo", "main", tc.fingerprint, model.StringArray{"fleet-1"}, model.StringArray{"device-1"}, gitRepoSpec(t, "https://example.com/repo.git"))}
			mockDependencyRefSvc.EXPECT().ListDueGitDependencies(gomock.Any(), orgID, 15*time.Minute).Return(probes, statusOK)
			if tc.firstSeen {
				mockSyncStateSvc.EXPECT().BulkUpsertSyncState(gomock.Any(), orgID, gomock.Any()).DoAndReturn(func(_ context.Context, _ uuid.UUID, states []model.SyncState) domain.Status {
					require.Len(t, states, 1)
					require.Equal(t, sha, states[0].Fingerprint)
					require.Equal(t, "Synced", states[0].ProbeStatus)
					require.Empty(t, states[0].ProbeMessage)
					return statusOK
				})
			} else {
				mockSyncStateSvc.EXPECT().BulkUpdateSyncStateLastCheckedAt(gomock.Any(), orgID, []string{"git:my-repo/main"}, gomock.Any()).Return(statusOK)
			}
			d := &DependencySyncGit{
				log: logrus.New(), dependencyrefSvc: mockDependencyRefSvc, eventSvc: mockEventSvc, syncstateSvc: mockSyncStateSvc,
				cfg: &config.Config{}, maxConcurrent: 10,
				lsRemote: func(context.Context, string, []string, *git.ListOptions) (map[string]string, error) {
					return map[string]string{"main": sha}, nil
				},
			}
			d.Poll(context.Background(), orgID)
		})
	}
}

func TestNewDependencySyncGit(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDependencyRefSvc := dependencyrefservice.NewMockService(ctrl)
	mockEventSvc := eventservice.NewMockService(ctrl)
	mockSyncStateSvc := syncstateservice.NewMockService(ctrl)

	d := NewDependencySyncGit(logrus.New(), mockDependencyRefSvc, mockEventSvc, mockSyncStateSvc, &config.Config{}, nil)
	require.NotNil(t, d)
	assert.Equal(t, 10, d.maxConcurrent)
	assert.NotNil(t, d.lsRemote)
}
