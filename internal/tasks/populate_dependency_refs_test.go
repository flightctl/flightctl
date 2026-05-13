package tasks

import (
	"context"
	"net/http"
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
)

func TestPopulateDependencyRefs_Fleet(t *testing.T) {
	okStatus := domain.Status{Code: http.StatusOK}
	orgId := uuid.New()
	fleetName := "test-fleet"

	t.Run("When fleet has non-parameterized git and HTTP config it should create fleet-level refs", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		repo := "my-repo"
		revision := "main"
		httpRepo := "http-repo"
		suffix := "/config.yaml"

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{
						Config: &[]domain.ConfigProviderSpec{
							makeGitConfigItem(t, "git-cfg", repo, revision),
							makeHttpConfigItem(t, "http-cfg", httpRepo, &suffix),
						},
					},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)

		require.NoError(t, err)
		require.Len(t, capturedRefs, 2)

		assert.Equal(t, "git", capturedRefs[0].RefType)
		assert.Equal(t, "git:my-repo/main", capturedRefs[0].ResourceKey)
		assert.Equal(t, &fleetName, capturedRefs[0].FleetName)

		assert.Equal(t, "http", capturedRefs[1].RefType)
		assert.Equal(t, "http:http-repo//config.yaml", capturedRefs[1].ResourceKey)
	})

	t.Run("When fleet has multiple git configs it should produce refs with independent pointers", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{
						Config: &[]domain.ConfigProviderSpec{
							makeGitConfigItem(t, "git-cfg-1", "repo-a", "main"),
							makeGitConfigItem(t, "git-cfg-2", "repo-b", "develop"),
						},
					},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)

		require.NoError(t, err)
		require.Len(t, capturedRefs, 2)

		// Each ref must have its own DeviceName pointer, not shared.
		require.NotSame(t, capturedRefs[0].DeviceName, capturedRefs[1].DeviceName)
		require.NotSame(t, capturedRefs[0].FleetName, capturedRefs[1].FleetName)
	})

	t.Run("When fleet has parameterized git revision it should skip that ref", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{
						Config: &[]domain.ConfigProviderSpec{
							makeGitConfigItem(t, "git-cfg", "my-repo", "{{ .metadata.labels.branch }}"),
						},
					},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)

		require.NoError(t, err)
		assert.Empty(t, capturedRefs)
	})

	t.Run("When fleet has non-parameterized secret config it should create fleet-level ref", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{
						Config: &[]domain.ConfigProviderSpec{
							makeSecretConfigItem(t, "secret-cfg", "prod", "db-creds"),
						},
					},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)

		require.NoError(t, err)
		require.Len(t, capturedRefs, 1)
		assert.Equal(t, "secret", capturedRefs[0].RefType)
		assert.Equal(t, "secret:prod/db-creds", capturedRefs[0].ResourceKey)
	})

	t.Run("When fleet has parameterized secret namespace it should skip that ref", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{
						Config: &[]domain.ConfigProviderSpec{
							makeSecretConfigItem(t, "secret-cfg", "{{ .metadata.labels.ns }}", "db-creds"),
						},
					},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)

		require.NoError(t, err)
		assert.Empty(t, capturedRefs)
	})

	t.Run("When fleet has parameterized secret name it should skip that ref", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{
						Config: &[]domain.ConfigProviderSpec{
							makeSecretConfigItem(t, "secret-cfg", "prod", "{{ .metadata.labels.secret }}"),
						},
					},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)

		require.NoError(t, err)
		assert.Empty(t, capturedRefs)
	})

	t.Run("When fleet has parameterized HTTP suffix it should skip that ref", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		paramSuffix := "/{{ .metadata.labels.env }}/config.json"
		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{
						Config: &[]domain.ConfigProviderSpec{
							makeHttpConfigItem(t, "http-cfg", "http-repo", &paramSuffix),
						},
					},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)

		require.NoError(t, err)
		assert.Empty(t, capturedRefs)
	})

	t.Run("When fleet has static HTTP suffix it should create fleet-level ref", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		suffix := "/config.json"
		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{
						Config: &[]domain.ConfigProviderSpec{
							makeHttpConfigItem(t, "http-cfg", "http-repo", &suffix),
						},
					},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)

		require.NoError(t, err)
		require.Len(t, capturedRefs, 1)
		assert.Equal(t, "http", capturedRefs[0].RefType)
		assert.Equal(t, "http:http-repo//config.json", capturedRefs[0].ResourceKey)
	})

	t.Run("When fleet has nil HTTP suffix it should create fleet-level ref with empty suffix", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{
						Config: &[]domain.ConfigProviderSpec{
							makeHttpConfigItem(t, "http-cfg", "http-repo", nil),
						},
					},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)

		require.NoError(t, err)
		require.Len(t, capturedRefs, 1)
		assert.Equal(t, "http", capturedRefs[0].RefType)
		assert.Equal(t, "http:http-repo/", capturedRefs[0].ResourceKey)
	})

	t.Run("When fleet has no config it should replace with empty refs", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: &fleetName},
			Spec: domain.FleetSpec{
				Template: struct {
					Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
					Spec     domain.DeviceSpec  "json:\"spec\""
				}{
					Spec: domain.DeviceSpec{Config: nil},
				},
			},
		}

		mockSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, okStatus)
		mockSvc.EXPECT().ReplaceDependencyRefsByFleet(gomock.Any(), orgId, fleetName, gomock.Len(0)).Return(okStatus)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForFleet(context.Background(), fleetName)
		require.NoError(t, err)
	})
}

func TestPopulateDependencyRefs_StandaloneDevice(t *testing.T) {
	okStatus := domain.Status{Code: http.StatusOK}
	orgId := uuid.New()
	deviceName := "standalone-device"

	t.Run("When standalone device has git config it should create device-level refs", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		repo := "my-repo"
		revision := "main"
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:  &deviceName,
				Owner: nil,
			},
			Spec: &domain.DeviceSpec{
				Config: &[]domain.ConfigProviderSpec{
					makeGitConfigItem(t, "git-cfg", repo, revision),
				},
			},
		}

		mockSvc.EXPECT().GetDevice(gomock.Any(), orgId, deviceName).Return(device, okStatus)

		var capturedRefs []model.DependencyRef
		mockSvc.EXPECT().ReplaceStandaloneDeviceDependencyRefs(gomock.Any(), orgId, deviceName, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, _ string, refs []model.DependencyRef) domain.Status {
				capturedRefs = refs
				return okStatus
			},
		)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForDevice(context.Background(), deviceName)

		require.NoError(t, err)
		require.Len(t, capturedRefs, 1)
		assert.Equal(t, "git", capturedRefs[0].RefType)
		assert.Equal(t, "git:my-repo/main", capturedRefs[0].ResourceKey)
		assert.Equal(t, lo.ToPtr(""), capturedRefs[0].FleetName)
		assert.Equal(t, &deviceName, capturedRefs[0].DeviceName)
	})

	t.Run("When device is fleet-owned it should clean up standalone refs", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		owner := "Fleet/my-fleet"
		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:  &deviceName,
				Owner: &owner,
			},
			Spec: &domain.DeviceSpec{
				Config: &[]domain.ConfigProviderSpec{
					makeGitConfigItem(t, "git-cfg", "my-repo", "main"),
				},
			},
		}

		mockSvc.EXPECT().GetDevice(gomock.Any(), orgId, deviceName).Return(device, okStatus)
		mockSvc.EXPECT().ReplaceStandaloneDeviceDependencyRefs(gomock.Any(), orgId, deviceName, gomock.Nil()).Return(okStatus)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForDevice(context.Background(), deviceName)
		require.NoError(t, err)
	})

	t.Run("When standalone device has no config it should replace with empty refs", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		device := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:  &deviceName,
				Owner: nil,
			},
			Spec: &domain.DeviceSpec{Config: nil},
		}

		mockSvc.EXPECT().GetDevice(gomock.Any(), orgId, deviceName).Return(device, okStatus)
		mockSvc.EXPECT().ReplaceStandaloneDeviceDependencyRefs(gomock.Any(), orgId, deviceName, gomock.Len(0)).Return(okStatus)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.PopulateForDevice(context.Background(), deviceName)
		require.NoError(t, err)
	})
}

func TestPopulateDependencyRefs_Deletion(t *testing.T) {
	okStatus := domain.Status{Code: http.StatusOK}
	orgId := uuid.New()

	t.Run("When fleet is deleted it should delete all fleet refs", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		fleetName := "deleted-fleet"
		event := domain.Event{
			Reason:         domain.EventReasonResourceDeleted,
			InvolvedObject: domain.ObjectReference{Kind: domain.FleetKind, Name: fleetName},
		}

		mockSvc.EXPECT().DeleteDependencyRefsByFleet(gomock.Any(), orgId, fleetName).Return(okStatus)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.HandleDeletion(context.Background(), event)
		require.NoError(t, err)
	})

	t.Run("When device is deleted it should delete all refs for that device", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockSvc := service.NewMockService(ctrl)

		deviceName := "deleted-device"
		event := domain.Event{
			Reason:         domain.EventReasonResourceDeleted,
			InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind, Name: deviceName},
		}

		mockSvc.EXPECT().DeleteDependencyRefsByDevice(gomock.Any(), orgId, deviceName).Return(okStatus)

		logic := NewPopulateDependencyRefsLogic(logrus.New(), mockSvc, orgId)
		err := logic.HandleDeletion(context.Background(), event)
		require.NoError(t, err)
	})
}

func TestShouldPopulateDependencyRefs(t *testing.T) {
	log := logrus.New()

	t.Run("When fleet template is updated it should return true", func(t *testing.T) {
		event := domain.Event{
			Reason:         domain.EventReasonResourceUpdated,
			InvolvedObject: domain.ObjectReference{Kind: domain.FleetKind, Name: "f"},
			Details:        makeUpdateDetails(t, domain.SpecTemplate),
		}
		assert.True(t, shouldPopulateDependencyRefs(context.Background(), event, log))
	})

	t.Run("When fleet is created it should return true", func(t *testing.T) {
		event := domain.Event{
			Reason:         domain.EventReasonResourceCreated,
			InvolvedObject: domain.ObjectReference{Kind: domain.FleetKind, Name: "f"},
		}
		assert.True(t, shouldPopulateDependencyRefs(context.Background(), event, log))
	})

	t.Run("When fleet is deleted it should return true", func(t *testing.T) {
		event := domain.Event{
			Reason:         domain.EventReasonResourceDeleted,
			InvolvedObject: domain.ObjectReference{Kind: domain.FleetKind, Name: "f"},
		}
		assert.True(t, shouldPopulateDependencyRefs(context.Background(), event, log))
	})

	t.Run("When device spec is updated it should return true", func(t *testing.T) {
		event := domain.Event{
			Reason:         domain.EventReasonResourceUpdated,
			InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind, Name: "d"},
			Details:        makeUpdateDetails(t, domain.Spec),
		}
		assert.True(t, shouldPopulateDependencyRefs(context.Background(), event, log))
	})

	t.Run("When device is created it should return true", func(t *testing.T) {
		event := domain.Event{
			Reason:         domain.EventReasonResourceCreated,
			InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind, Name: "d"},
		}
		assert.True(t, shouldPopulateDependencyRefs(context.Background(), event, log))
	})

	t.Run("When device is deleted it should return true", func(t *testing.T) {
		event := domain.Event{
			Reason:         domain.EventReasonResourceDeleted,
			InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind, Name: "d"},
		}
		assert.True(t, shouldPopulateDependencyRefs(context.Background(), event, log))
	})

	t.Run("When device labels are updated it should return false", func(t *testing.T) {
		event := domain.Event{
			Reason:         domain.EventReasonResourceUpdated,
			InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind, Name: "d"},
			Details:        makeUpdateDetails(t, domain.Labels),
		}
		assert.False(t, shouldPopulateDependencyRefs(context.Background(), event, log))
	})

	t.Run("When repository is updated it should return false", func(t *testing.T) {
		event := domain.Event{
			Reason:         domain.EventReasonResourceUpdated,
			InvolvedObject: domain.ObjectReference{Kind: domain.RepositoryKind, Name: "r"},
		}
		assert.False(t, shouldPopulateDependencyRefs(context.Background(), event, log))
	})
}

func makeUpdateDetails(t *testing.T, fields ...domain.ResourceUpdatedDetailsUpdatedFields) *domain.EventDetails {
	t.Helper()
	details := domain.ResourceUpdatedDetails{
		DetailType:    domain.ResourceUpdated,
		UpdatedFields: fields,
	}
	ed := domain.EventDetails{}
	require.NoError(t, ed.FromResourceUpdatedDetails(details))
	return &ed
}
