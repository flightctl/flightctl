package tasks

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDependencySyncSecret_Reconcile(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	t.Run("When fingerprint changes it should update sync_state and emit events", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		refs := []model.SecretDependencyRef{
			{OrgID: orgId, FleetName: "fleet-a", DeviceName: "", Fingerprint: lo.ToPtr("sha256:oldfingerprint")},
		}
		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "sha256:newfingerprint").Return(refs, statusOK)

		mockService.EXPECT().SetSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, state *model.SyncState) domain.Status {
				assert.Equal(t, "secret:prod/db-creds", state.ResourceKey)
				assert.Equal(t, "sha256:newfingerprint", state.Fingerprint)
				return statusOK
			})

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "db-creds", "sha256:newfingerprint")

		require.Len(t, events, 1)
		assert.Equal(t, string(domain.FleetKind), events[0].kind)
		assert.Equal(t, "fleet-a", events[0].name)
	})

	t.Run("When fingerprint is unchanged it should not emit events or update sync_state", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		// SQL filters out unchanged rows — query returns empty
		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "sha256:samefingerprint").Return([]model.SecretDependencyRef{}, statusOK)

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "db-creds", "sha256:samefingerprint")
	})

	t.Run("When first seen it should store fingerprint without emitting events", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		refs := []model.SecretDependencyRef{
			{OrgID: orgId, FleetName: "fleet-a", DeviceName: "", Fingerprint: nil},
		}
		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "sha256:initialfingerprint").Return(refs, statusOK)

		mockService.EXPECT().SetSyncState(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, state *model.SyncState) domain.Status {
				assert.Equal(t, "sha256:initialfingerprint", state.Fingerprint)
				return statusOK
			})

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "db-creds", "sha256:initialfingerprint")
	})

	t.Run("When multiple orgs reference the same secret it should emit events and update sync_state per org", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		org1 := uuid.New()
		org2 := uuid.New()
		// SQL already filtered out unchanged — both orgs returned have stale fingerprints
		refs := []model.SecretDependencyRef{
			{OrgID: org1, FleetName: "fleet-a", DeviceName: "", Fingerprint: lo.ToPtr("sha256:old1")},
			{OrgID: org2, FleetName: "fleet-b", DeviceName: "", Fingerprint: lo.ToPtr("sha256:old2")},
		}
		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "sha256:newfingerprint").Return(refs, statusOK)

		mockService.EXPECT().SetSyncState(gomock.Any(), org1, gomock.Any()).Return(statusOK)
		mockService.EXPECT().SetSyncState(gomock.Any(), org2, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any(), gomock.Any()).Times(2).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "db-creds", "sha256:newfingerprint")

		require.Len(t, events, 2)
	})

	t.Run("When fan-out includes fleet and device rows it should emit correct event kinds", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		refs := []model.SecretDependencyRef{
			{OrgID: orgId, FleetName: "fleet-a", DeviceName: "", Fingerprint: lo.ToPtr("sha256:old")},
			{OrgID: orgId, FleetName: "fleet-a", DeviceName: "device-x", Fingerprint: lo.ToPtr("sha256:old")},
		}
		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "sha256:new").Return(refs, statusOK)
		mockService.EXPECT().SetSyncState(gomock.Any(), orgId, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Times(2).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "db-creds", "sha256:new")

		require.Len(t, events, 2)
		assert.Equal(t, string(domain.FleetKind), events[0].kind)
		assert.Equal(t, "fleet-a", events[0].name)
		assert.Equal(t, string(domain.DeviceKind), events[1].kind)
		assert.Equal(t, "device-x", events[1].name)
	})

	t.Run("When no refs match it should be a no-op", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "unknown", "sha256:anything").Return([]model.SecretDependencyRef{}, statusOK)

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "unknown", "sha256:anything")
	})
}

func TestDependencySyncSecret_ContextCancellation(t *testing.T) {
	t.Run("When context is cancelled handleSecretEvent should return early without DB calls", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
			hashFunc:       hashSecretData,
		}
		d.handleSecretEvent(cancelledCtx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "db-creds"},
			Data:       map[string][]byte{"key": []byte("value")},
		})
	})
}

func TestHashSecretData(t *testing.T) {
	t.Run("When same data is provided it should produce the same hash", func(t *testing.T) {
		data := map[string][]byte{
			"password": []byte("secret123"),
			"username": []byte("admin"),
		}
		h1 := hashSecretData(data)
		h2 := hashSecretData(data)
		assert.Equal(t, h1, h2)
		assert.Contains(t, h1, "sha256:")
	})

	t.Run("When keys are in different iteration order it should produce the same hash", func(t *testing.T) {
		data1 := map[string][]byte{
			"a": []byte("1"),
			"b": []byte("2"),
			"c": []byte("3"),
		}
		data2 := map[string][]byte{
			"c": []byte("3"),
			"a": []byte("1"),
			"b": []byte("2"),
		}
		assert.Equal(t, hashSecretData(data1), hashSecretData(data2))
	})

	t.Run("When data differs it should produce different hashes", func(t *testing.T) {
		data1 := map[string][]byte{"key": []byte("value1")}
		data2 := map[string][]byte{"key": []byte("value2")}
		assert.NotEqual(t, hashSecretData(data1), hashSecretData(data2))
	})

	t.Run("When data is nil it should produce a consistent hash", func(t *testing.T) {
		h1 := hashSecretData(nil)
		h2 := hashSecretData(nil)
		assert.Equal(t, h1, h2)
	})
}
