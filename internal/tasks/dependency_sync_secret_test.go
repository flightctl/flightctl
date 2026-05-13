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
			{OrgID: orgId, FleetName: "fleet-a", DeviceName: "", Fingerprint: lo.ToPtr("1000")},
		}
		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "1001").Return(refs, statusOK)

		mockService.EXPECT().SetSyncState(gomock.Any(), uuid.Nil, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, state *model.SyncState) domain.Status {
				assert.Equal(t, "secret:prod/db-creds", state.ResourceKey)
				assert.Equal(t, "1001", state.Fingerprint)
				assert.Equal(t, uuid.Nil, state.OrgID)
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
		d.reconcile(ctx, "prod", "db-creds", "1001")

		require.Len(t, events, 1)
		assert.Equal(t, string(domain.FleetKind), events[0].kind)
		assert.Equal(t, "fleet-a", events[0].name)
	})

	t.Run("When fingerprint is unchanged it should not emit events or update sync_state", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "1000").Return([]model.SecretDependencyRef{}, statusOK)

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "db-creds", "1000")
	})

	t.Run("When first seen it should store fingerprint and emit events", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		refs := []model.SecretDependencyRef{
			{OrgID: orgId, FleetName: "fleet-a", DeviceName: "", Fingerprint: nil},
		}
		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "1000").Return(refs, statusOK)

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		mockService.EXPECT().SetSyncState(gomock.Any(), uuid.Nil, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, state *model.SyncState) domain.Status {
				assert.Equal(t, "1000", state.Fingerprint)
				assert.Equal(t, uuid.Nil, state.OrgID)
				return statusOK
			})

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "db-creds", "1000")

		require.Len(t, events, 1)
		assert.Equal(t, string(domain.FleetKind), events[0].kind)
		assert.Equal(t, "fleet-a", events[0].name)
	})

	t.Run("When multiple orgs reference the same secret it should emit events per org but write sync_state once with uuid.Nil", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		org1 := uuid.New()
		org2 := uuid.New()
		refs := []model.SecretDependencyRef{
			{OrgID: org1, FleetName: "fleet-a", DeviceName: "", Fingerprint: lo.ToPtr("999")},
			{OrgID: org2, FleetName: "fleet-b", DeviceName: "", Fingerprint: lo.ToPtr("998")},
		}
		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "1001").Return(refs, statusOK)

		mockService.EXPECT().SetSyncState(gomock.Any(), uuid.Nil, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any(), gomock.Any()).Times(2).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "db-creds", "1001")

		require.Len(t, events, 2)
	})

	t.Run("When fan-out includes fleet and device rows it should emit correct event kinds", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockService := service.NewMockService(ctrl)

		refs := []model.SecretDependencyRef{
			{OrgID: orgId, FleetName: "fleet-a", DeviceName: "", Fingerprint: lo.ToPtr("500")},
			{OrgID: orgId, FleetName: "fleet-a", DeviceName: "device-x", Fingerprint: lo.ToPtr("500")},
		}
		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "db-creds", "501").Return(refs, statusOK)
		mockService.EXPECT().SetSyncState(gomock.Any(), uuid.Nil, gomock.Any()).Return(statusOK)

		var events []emittedEvent
		mockService.EXPECT().CreateEvent(gomock.Any(), orgId, gomock.Any()).Times(2).Do(func(_ context.Context, _ uuid.UUID, event *domain.Event) {
			events = append(events, emittedEvent{kind: event.InvolvedObject.Kind, name: event.InvolvedObject.Name})
		})

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "db-creds", "501")

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

		mockService.EXPECT().ListSecretDependencyTargets(gomock.Any(), "prod", "unknown", "100").Return([]model.SecretDependencyRef{}, statusOK)

		d := &DependencySyncSecret{
			log:            logrus.New(),
			serviceHandler: mockService,
		}
		d.reconcile(ctx, "prod", "unknown", "100")
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
		}
		d.handleSecretEvent(cancelledCtx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "db-creds", ResourceVersion: "1000"},
		})
	})
}
