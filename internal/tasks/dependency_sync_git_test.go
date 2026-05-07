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

func makeGitDepRef(fleetName, repoName, revision string) model.DependencyRef {
	return model.DependencyRef{
		FleetName:      lo.ToPtr(fleetName),
		DeviceName:     lo.ToPtr(""),
		RefType:        "git",
		RepositoryName: lo.ToPtr(repoName),
		Revision:       lo.ToPtr(revision),
	}
}

func makeDeviceDepRef(deviceName, repoName, revision string) model.DependencyRef {
	return model.DependencyRef{
		FleetName:      lo.ToPtr(""),
		DeviceName:     lo.ToPtr(deviceName),
		RefType:        "git",
		RepositoryName: lo.ToPtr(repoName),
		Revision:       lo.ToPtr(revision),
	}
}

type emittedEvent struct {
	kind string
	name string
}

func makeGitRepo(t *testing.T, url string) *domain.Repository {
	t.Helper()
	spec := api.RepositorySpec{}
	err := spec.FromGitRepoSpec(api.GitRepoSpec{
		Type: api.GitRepoSpecTypeGit,
		Url:  url,
	})
	require.NoError(t, err)
	return &domain.Repository{Spec: spec}
}

var statusOK = domain.Status{Code: http.StatusOK}

func TestDependencySyncGit_Poll(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()

	t.Run("When a change is detected it should update sync_state and emit events", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		refs := []model.DependencyRef{makeGitDepRef("fleet-1", "my-repo", "main")}
		mockService.EXPECT().ListDependencyRefsByRefType(gomock.Any(), orgId, "git").Return(refs, statusOK)

		existingState := &model.SyncState{
			OrgID: orgId, ResourceKey: "git:my-repo/main", Fingerprint: "oldsha999",
			LastCheckedAt: time.Now().Add(-30 * time.Minute),
		}
		mockService.EXPECT().GetSyncState(gomock.Any(), orgId, "git:my-repo/main").Return(existingState, statusOK)

		mockService.EXPECT().GetRepository(gomock.Any(), orgId, "my-repo").Return(
			makeGitRepo(t, "https://example.com/repo.git"), statusOK)

		mockService.EXPECT().SetSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, state *model.SyncState) domain.Status {
				assert.Equal(t, "newsha123456789", state.Fingerprint)
				return statusOK
			})

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		lsRemote := func(_ context.Context, _ string, _ []string, _ transport.AuthMethod) (map[string]string, error) {
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

	t.Run("When no change is detected it should update last_checked_at only", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		refs := []model.DependencyRef{makeGitDepRef("fleet-1", "my-repo", "main")}
		mockService.EXPECT().ListDependencyRefsByRefType(gomock.Any(), orgId, "git").Return(refs, statusOK)

		existingState := &model.SyncState{
			OrgID: orgId, ResourceKey: "git:my-repo/main", Fingerprint: "samesha123",
			LastCheckedAt: time.Now().Add(-30 * time.Minute),
		}
		mockService.EXPECT().GetSyncState(gomock.Any(), orgId, "git:my-repo/main").Return(existingState, statusOK)

		mockService.EXPECT().GetRepository(gomock.Any(), orgId, "my-repo").Return(
			makeGitRepo(t, "https://example.com/repo.git"), statusOK)

		mockService.EXPECT().SetSyncStateLastCheckedAt(gomock.Any(), orgId, "git:my-repo/main", gomock.Any()).Return(statusOK)

		lsRemote := func(_ context.Context, _ string, _ []string, _ transport.AuthMethod) (map[string]string, error) {
			return map[string]string{"main": "samesha123"}, nil
		}

		d := &DependencySyncGit{
			log: logrus.New(), serviceHandler: mockService,
			cfg: &config.Config{}, lsRemote: lsRemote, maxConcurrent: 10,
		}
		d.Poll(ctx, orgId)
	})

	t.Run("When probe errors it should continue without emitting events", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		refs := []model.DependencyRef{makeGitDepRef("fleet-1", "my-repo", "main")}
		mockService.EXPECT().ListDependencyRefsByRefType(gomock.Any(), orgId, "git").Return(refs, statusOK)

		mockService.EXPECT().GetSyncState(gomock.Any(), orgId, "git:my-repo/main").Return(nil, statusOK)

		mockService.EXPECT().GetRepository(gomock.Any(), orgId, "my-repo").Return(
			makeGitRepo(t, "https://example.com/repo.git"), statusOK)

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

		mockService.EXPECT().ListDependencyRefsByRefType(gomock.Any(), orgId, "git").Return([]model.DependencyRef{}, statusOK)

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

		refs := []model.DependencyRef{
			makeGitDepRef("fleet-a", "shared-repo", "main"),
			makeGitDepRef("fleet-b", "shared-repo", "main"),
		}
		mockService.EXPECT().ListDependencyRefsByRefType(gomock.Any(), orgId, "git").Return(refs, statusOK)

		existingState := &model.SyncState{
			OrgID: orgId, ResourceKey: "git:shared-repo/main", Fingerprint: "oldsha",
			LastCheckedAt: time.Now().Add(-30 * time.Minute),
		}
		mockService.EXPECT().GetSyncState(gomock.Any(), orgId, "git:shared-repo/main").Return(existingState, statusOK)

		mockService.EXPECT().GetRepository(gomock.Any(), orgId, "shared-repo").Return(
			makeGitRepo(t, "https://example.com/repo.git"), statusOK)

		mockService.EXPECT().SetSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)

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

		refs := []model.DependencyRef{makeDeviceDepRef("device-standalone", "my-repo", "main")}
		mockService.EXPECT().ListDependencyRefsByRefType(gomock.Any(), orgId, "git").Return(refs, statusOK)

		existingState := &model.SyncState{
			OrgID: orgId, ResourceKey: "git:my-repo/main", Fingerprint: "oldsha",
			LastCheckedAt: time.Now().Add(-30 * time.Minute),
		}
		mockService.EXPECT().GetSyncState(gomock.Any(), orgId, "git:my-repo/main").Return(existingState, statusOK)

		mockService.EXPECT().GetRepository(gomock.Any(), orgId, "my-repo").Return(
			makeGitRepo(t, "https://example.com/repo.git"), statusOK)

		mockService.EXPECT().SetSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)

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

		refs := []model.DependencyRef{makeGitDepRef("fleet-1", "new-repo", "main")}
		mockService.EXPECT().ListDependencyRefsByRefType(gomock.Any(), orgId, "git").Return(refs, statusOK)

		mockService.EXPECT().GetSyncState(gomock.Any(), orgId, "git:new-repo/main").Return(nil, statusOK)

		mockService.EXPECT().GetRepository(gomock.Any(), orgId, "new-repo").Return(
			makeGitRepo(t, "https://example.com/repo.git"), statusOK)

		mockService.EXPECT().SetSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, state *model.SyncState) domain.Status {
				assert.Equal(t, "initialsha123", state.Fingerprint)
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
}

func TestNewDependencySyncGit(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockService := service.NewMockService(ctrl)

	d := NewDependencySyncGit(logrus.New(), mockService, &config.Config{})
	require.NotNil(t, d)
	assert.Equal(t, 10, d.maxConcurrent)
	assert.NotNil(t, d.lsRemote)
}
