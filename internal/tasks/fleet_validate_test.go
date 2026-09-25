package tasks

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestFleetValidateLogic_CreateNewTemplateVersionIfFleetValid_EmitsPrepareDeltas(t *testing.T) {
	tests := []struct {
		name          string
		rolloutPolicy *domain.RolloutPolicy
	}{
		{
			name:          "When there is no rollout policy it should emit PrepareDeltas",
			rolloutPolicy: nil,
		},
		{
			name: "When DeviceSelection is set it should emit PrepareDeltas",
			rolloutPolicy: &domain.RolloutPolicy{
				DeviceSelection: &domain.RolloutDeviceSelection{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			fleetName := "test-fleet"
			fleet := createTestFleet(fleetName, tt.rolloutPolicy)
			fleet.Metadata.ResourceVersion = lo.ToPtr("1")
			event := createTestEvent(domain.FleetKind, "some-reason", fleetName)
			orgId := uuid.New()
			log := logrus.New()
			emit := &prepareDeltasEmitter{}

			mockFleetSvc := fleetservice.NewMockService(ctrl)
			mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
			mockDeviceSvc := deviceservice.NewMockService(ctrl)
			mockRepositorySvc := repositoryservice.NewMockService(ctrl)
			mockK8SClient := k8sclient.NewMockK8SClient(ctrl)

			mockFleetSvc.EXPECT().GetFleet(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(fleet, domain.Status{Code: http.StatusOK})
			mockFleetSvc.EXPECT().OverwriteFleetRepositoryRefs(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(domain.Status{Code: http.StatusOK})
			mockTemplateVersionSvc.EXPECT().CreateTemplateVersion(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, orgId uuid.UUID, tv domain.TemplateVersion, immediateRollout bool) (*domain.TemplateVersion, domain.Status) {
					return &domain.TemplateVersion{
						Metadata: domain.ObjectMeta{
							Name: lo.ToPtr("test-tv"),
						},
					}, domain.Status{Code: http.StatusCreated}
				})
			mockFleetSvc.EXPECT().SetDeltaPrepareIdentity(gomock.Any(), gomock.Any(), fleetName, int64(1), int64(1)).Return(true, domain.StatusOK())
			mockFleetSvc.EXPECT().UpdateFleetConditions(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(domain.Status{Code: http.StatusOK})

			logic := NewFleetValidateLogic(log, mockFleetSvc, mockTemplateVersionSvc, mockDeviceSvc, mockRepositorySvc, mockK8SClient, orgId, event)
			logic.WorkerClient = emit

			err := logic.CreateNewTemplateVersionIfFleetValid(context.Background())
			require.NoError(t, err)
			require.Len(t, emit.events, 1)
			assert.Equal(t, domain.EventReasonPrepareDeltas, emit.events[0].Reason)
			assert.Equal(t, domain.FleetKind, emit.events[0].InvolvedObject.Kind)
			assert.Equal(t, fleetName, emit.events[0].InvolvedObject.Name)
			details, err := emit.events[0].Details.AsPrepareDeltasDetails()
			require.NoError(t, err)
			assert.Equal(t, "test-tv", lo.FromPtr(details.TemplateVersion))
			assert.Equal(t, "1", lo.FromPtr(details.ResourceVersion))
		})
	}
}

func TestFleetValidateLogic_WhenTemplateVersionAlreadyExistsItRecoversPrepareDeltas(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetName := "test-fleet"
	fleet := createTestFleet(fleetName, nil)
	fleet.Metadata.ResourceVersion = lo.ToPtr("1")
	event := createTestEvent(domain.FleetKind, "some-reason", fleetName)
	orgID := uuid.New()
	emit := &prepareDeltasEmitter{}

	mockFleetSvc := fleetservice.NewMockService(ctrl)
	mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
	mockDeviceSvc := deviceservice.NewMockService(ctrl)
	mockRepositorySvc := repositoryservice.NewMockService(ctrl)
	mockK8SClient := k8sclient.NewMockK8SClient(ctrl)

	mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, fleetName, gomock.Any()).Return(fleet, domain.Status{Code: http.StatusOK})
	mockFleetSvc.EXPECT().OverwriteFleetRepositoryRefs(gomock.Any(), orgID, fleetName, gomock.Any()).Return(domain.Status{Code: http.StatusOK})
	mockTemplateVersionSvc.EXPECT().CreateTemplateVersion(gomock.Any(), orgID, gomock.Any(), gomock.Any()).Return(nil, domain.Status{Code: http.StatusConflict})
	mockTemplateVersionSvc.EXPECT().GetTemplateVersion(gomock.Any(), orgID, fleetName, gomock.Any()).Return(&domain.TemplateVersion{Metadata: domain.ObjectMeta{Name: lo.ToPtr("test-tv")}}, domain.Status{Code: http.StatusOK})
	mockFleetSvc.EXPECT().SetDeltaPrepareIdentity(gomock.Any(), orgID, fleetName, int64(1), int64(1)).Return(true, domain.StatusOK())
	mockFleetSvc.EXPECT().UpdateFleetConditions(gomock.Any(), orgID, fleetName, gomock.Any()).Return(domain.Status{Code: http.StatusOK})

	logic := NewFleetValidateLogic(logrus.New(), mockFleetSvc, mockTemplateVersionSvc, mockDeviceSvc, mockRepositorySvc, mockK8SClient, orgID, event)
	logic.WorkerClient = emit

	require.NoError(t, logic.CreateNewTemplateVersionIfFleetValid(context.Background()))
	require.Len(t, emit.events, 1)
	details, err := emit.events[0].Details.AsPrepareDeltasDetails()
	require.NoError(t, err)
	assert.Equal(t, "test-tv", lo.FromPtr(details.TemplateVersion))
}

func TestFleetValidateLogic_WhenPrepareIdentityWasSupersededItShouldNotEmit(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	fleetName := "test-fleet"
	fleetSvc.EXPECT().SetDeltaPrepareIdentity(gomock.Any(), gomock.Any(), fleetName, int64(1), int64(1)).Return(false, domain.StatusOK())
	emit := &prepareDeltasEmitter{}
	logic := NewFleetValidateLogic(logrus.New(), fleetSvc, nil, nil, nil, nil, uuid.New(), domain.Event{})
	logic.WorkerClient = emit

	err := logic.prepareFleetRollout(context.Background(), &domain.Fleet{Metadata: domain.ObjectMeta{
		Name:            lo.ToPtr(fleetName),
		ResourceVersion: lo.ToPtr("1"),
		Generation:      lo.ToPtr(int64(1)),
	}}, "tv-stale")
	require.NoError(t, err)
	assert.Empty(t, emit.events)
}

func TestFleetValidateLogic_WhenPrepareDeltasPublicationFailsItReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetName := "test-fleet"
	fleet := createTestFleet(fleetName, nil)
	fleet.Metadata.ResourceVersion = lo.ToPtr("1")
	event := createTestEvent(domain.FleetKind, "some-reason", fleetName)
	orgID := uuid.New()
	publishErr := errors.New("queue unavailable")
	emit := &prepareDeltasEmitter{err: publishErr}

	mockFleetSvc := fleetservice.NewMockService(ctrl)
	mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
	mockDeviceSvc := deviceservice.NewMockService(ctrl)
	mockRepositorySvc := repositoryservice.NewMockService(ctrl)
	mockK8SClient := k8sclient.NewMockK8SClient(ctrl)

	mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, fleetName, gomock.Any()).Return(fleet, domain.StatusOK())
	mockFleetSvc.EXPECT().OverwriteFleetRepositoryRefs(gomock.Any(), orgID, fleetName, gomock.Any()).Return(domain.StatusOK())
	mockTemplateVersionSvc.EXPECT().CreateTemplateVersion(gomock.Any(), orgID, gomock.Any(), gomock.Any()).Return(
		&domain.TemplateVersion{Metadata: domain.ObjectMeta{Name: lo.ToPtr("test-tv")}}, domain.StatusCreated())
	mockFleetSvc.EXPECT().SetDeltaPrepareIdentity(gomock.Any(), orgID, fleetName, int64(1), int64(1)).Return(true, domain.StatusOK())
	mockFleetSvc.EXPECT().UpdateFleetConditions(gomock.Any(), orgID, fleetName, gomock.Any()).DoAndReturn(
		func(_ context.Context, _ uuid.UUID, _ string, conditions []domain.Condition) domain.Status {
			require.Len(t, conditions, 1)
			assert.Equal(t, domain.ConditionStatusTrue, conditions[0].Status)
			return domain.StatusOK()
		})

	logic := NewFleetValidateLogic(logrus.New(), mockFleetSvc, mockTemplateVersionSvc, mockDeviceSvc, mockRepositorySvc, mockK8SClient, orgID, event)
	logic.WorkerClient = emit

	err := logic.CreateNewTemplateVersionIfFleetValid(context.Background())
	require.ErrorIs(t, err, publishErr)
	assert.False(t, isInvalidFleetConfigError(err))
	assert.Empty(t, emit.events)
}

func TestFleetValidateLogic_InvalidConfigErrorsArePermanentOnlyWhenStatusIsUpdated(t *testing.T) {
	tests := []struct {
		name               string
		conditionStatus    domain.Status
		wantPermanentError bool
	}{
		{
			name:               "When invalid Fleet condition is updated it should return a permanent validation error",
			conditionStatus:    domain.StatusOK(),
			wantPermanentError: true,
		},
		{
			name:               "When invalid Fleet condition update fails it should return a retryable error",
			conditionStatus:    domain.StatusInternalServerError("status store unavailable"),
			wantPermanentError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			fleetName := "test-fleet"
			fleet := createTestFleet(fleetName, nil)
			invalidConfig := []domain.ConfigProviderSpec{{}}
			fleet.Spec.Template.Spec.Config = &invalidConfig
			event := createTestEvent(domain.FleetKind, "some-reason", fleetName)
			orgID := uuid.New()

			mockFleetSvc := fleetservice.NewMockService(ctrl)
			mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
			mockDeviceSvc := deviceservice.NewMockService(ctrl)
			mockRepositorySvc := repositoryservice.NewMockService(ctrl)
			mockK8SClient := k8sclient.NewMockK8SClient(ctrl)

			mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, fleetName, gomock.Any()).Return(fleet, domain.StatusOK())
			mockFleetSvc.EXPECT().OverwriteFleetRepositoryRefs(gomock.Any(), orgID, fleetName, gomock.Any()).Return(domain.StatusOK())
			mockFleetSvc.EXPECT().UpdateFleetConditions(gomock.Any(), orgID, fleetName, gomock.Any()).DoAndReturn(
				func(_ context.Context, _ uuid.UUID, _ string, conditions []domain.Condition) domain.Status {
					require.Len(t, conditions, 1)
					assert.Equal(t, domain.ConditionStatusFalse, conditions[0].Status)
					return tt.conditionStatus
				})

			logic := NewFleetValidateLogic(logrus.New(), mockFleetSvc, mockTemplateVersionSvc, mockDeviceSvc, mockRepositorySvc, mockK8SClient, orgID, event)
			err := logic.CreateNewTemplateVersionIfFleetValid(context.Background())
			require.Error(t, err)
			assert.Equal(t, tt.wantPermanentError, isInvalidFleetConfigError(err))
		})
	}
}

func TestFleetValidateLogic_WhenRepositoryLookupFailsItReturnsRetryableValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetName := "test-fleet"
	fleet := createTestFleet(fleetName, nil)
	configs := []domain.ConfigProviderSpec{makeGitConfigItem(t, "repo-config", "repo-1", "main")}
	fleet.Spec.Template.Spec.Config = &configs
	event := createTestEvent(domain.FleetKind, "some-reason", fleetName)
	orgID := uuid.New()

	mockFleetSvc := fleetservice.NewMockService(ctrl)
	mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
	mockDeviceSvc := deviceservice.NewMockService(ctrl)
	mockRepositorySvc := repositoryservice.NewMockService(ctrl)
	mockK8SClient := k8sclient.NewMockK8SClient(ctrl)

	mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, fleetName, gomock.Any()).Return(fleet, domain.StatusOK())
	mockRepositorySvc.EXPECT().GetRepository(gomock.Any(), orgID, "repo-1").Return(nil, domain.StatusInternalServerError("repository store unavailable"))
	mockFleetSvc.EXPECT().OverwriteFleetRepositoryRefs(gomock.Any(), orgID, fleetName, gomock.Any()).Return(domain.StatusOK())
	mockFleetSvc.EXPECT().UpdateFleetConditions(gomock.Any(), orgID, fleetName, gomock.Any()).Return(domain.StatusOK())

	logic := NewFleetValidateLogic(logrus.New(), mockFleetSvc, mockTemplateVersionSvc, mockDeviceSvc, mockRepositorySvc, mockK8SClient, orgID, event)
	err := logic.CreateNewTemplateVersionIfFleetValid(context.Background())
	require.Error(t, err)
	assert.False(t, isInvalidFleetConfigError(err))
	assert.ErrorContains(t, err, "repository store unavailable")
}

func TestFleetValidateLogic_WhenSecretIsMissingItReturnsPermanentValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetName := "test-fleet"
	fleet := createTestFleet(fleetName, nil)
	configs := []domain.ConfigProviderSpec{makeSecretConfigItem(t, "secret-config", "default", "missing-secret")}
	fleet.Spec.Template.Spec.Config = &configs
	event := createTestEvent(domain.FleetKind, "some-reason", fleetName)
	orgID := uuid.New()

	mockFleetSvc := fleetservice.NewMockService(ctrl)
	mockTemplateVersionSvc := templateversionservice.NewMockService(ctrl)
	mockDeviceSvc := deviceservice.NewMockService(ctrl)
	mockRepositorySvc := repositoryservice.NewMockService(ctrl)
	mockK8SClient := k8sclient.NewMockK8SClient(ctrl)

	mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, fleetName, gomock.Any()).Return(fleet, domain.StatusOK())
	mockK8SClient.EXPECT().GetSecret(gomock.Any(), "default", "missing-secret").Return(nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "missing-secret"))
	mockFleetSvc.EXPECT().OverwriteFleetRepositoryRefs(gomock.Any(), orgID, fleetName, gomock.Any()).Return(domain.StatusOK())
	mockFleetSvc.EXPECT().UpdateFleetConditions(gomock.Any(), orgID, fleetName, gomock.Any()).Return(domain.StatusOK())

	logic := NewFleetValidateLogic(logrus.New(), mockFleetSvc, mockTemplateVersionSvc, mockDeviceSvc, mockRepositorySvc, mockK8SClient, orgID, event)
	err := logic.CreateNewTemplateVersionIfFleetValid(context.Background())
	require.Error(t, err)
	assert.True(t, isInvalidFleetConfigError(err))
}

type prepareDeltasEmitter struct {
	events []*domain.Event
	err    error
}

func (e *prepareDeltasEmitter) EmitEvent(ctx context.Context, orgID uuid.UUID, event *domain.Event) {
	_ = e.EmitEventWithError(ctx, orgID, event)
}

func (e *prepareDeltasEmitter) EmitEventWithError(_ context.Context, _ uuid.UUID, event *domain.Event) error {
	if e.err != nil {
		return e.err
	}
	if event == nil {
		return nil
	}
	cp := *event
	e.events = append(e.events, &cp)
	return nil
}

func TestGenerateTemplateVersionName(t *testing.T) {
	require := require.New(t)

	makeFleet := func(name string, generation int64) *domain.Fleet {
		return &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:       &name,
				Generation: &generation,
			},
		}
	}

	tests := []struct {
		name        string
		fleetName   string
		generation  int64
		fingerprint string
		expected    string
	}{
		{
			name:        "When fingerprint is empty it should return v{generation}",
			fleetName:   "my-fleet",
			generation:  1,
			fingerprint: "",
			expected:    "v1",
		},
		{
			name:        "When fingerprint is empty with large generation it should return v{generation}",
			fleetName:   "my-fleet",
			generation:  9999999999,
			fingerprint: "",
			expected:    "v9999999999",
		},
		{
			name:        "When fingerprint is empty with 253-char fleet name it should return v{generation}",
			fleetName:   strings.Repeat("a", 253),
			generation:  42,
			fingerprint: "",
			expected:    "v42",
		},
		{
			name:        "When fingerprint is a git SHA it should return v{generation}-{hash}",
			fleetName:   "my-fleet",
			generation:  3,
			fingerprint: "abc123def456789",
			expected:    "v3-eafa0cba",
		},
		{
			name:        "When fingerprint is short it should still hash it",
			fleetName:   "my-fleet",
			generation:  1,
			fingerprint: "abc",
			expected:    "v1-ba7816bf",
		},
		{
			name:        "When fingerprint is an HTTP ETag with quotes it should produce valid name",
			fleetName:   "my-fleet",
			generation:  1,
			fingerprint: `"etag-v1"`,
			expected:    "v1-16da6c0f",
		},
		{
			name:        "When fingerprint is a Last-Modified date it should produce valid name",
			fleetName:   "my-fleet",
			generation:  2,
			fingerprint: "Mon, 25 May 2026 13:30:47 GMT",
			expected:    "v2-308b62ef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateTemplateVersionName(makeFleet(tt.fleetName, tt.generation), tt.fingerprint)
			require.Equal(tt.expected, result)
		})
	}
}

func TestFleetValidateLogic_GetFingerprint(t *testing.T) {
	tests := []struct {
		name     string
		event    domain.Event
		expected string
	}{
		{
			name: "When event is DependencyChangeDetected it should return the fingerprint",
			event: func() domain.Event {
				details := domain.EventDetails{}
				_ = details.FromDependencyChangeDetectedDetails(domain.DependencyChangeDetectedDetails{
					DetailType:  domain.DependencyChangeDetected,
					ResourceKey: "git:my-repo/main",
					Fingerprint: "abc123def456",
				})
				return domain.Event{
					Reason:  domain.EventReasonDependencyChangeDetected,
					Details: &details,
				}
			}(),
			expected: "abc123def456",
		},
		{
			name: "When event is ResourceUpdated it should return empty string",
			event: domain.Event{
				Reason: domain.EventReasonResourceUpdated,
			},
			expected: "",
		},
		{
			name: "When event is DependencyChangeDetected with nil details it should return empty string",
			event: domain.Event{
				Reason: domain.EventReasonDependencyChangeDetected,
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logic := FleetValidateLogic{
				log:   logrus.New(),
				event: tt.event,
			}
			result := logic.getFingerprint()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func makeGitConfigItem(t *testing.T, name, repo, revision string) domain.ConfigProviderSpec {
	t.Helper()
	gitSpec := &domain.GitConfigProviderSpec{Name: name}
	gitSpec.GitRef.Repository = repo
	gitSpec.GitRef.TargetRevision = revision
	gitSpec.GitRef.Path = "/etc/config"
	item := domain.ConfigProviderSpec{}
	require.NoError(t, item.FromGitConfigProviderSpec(*gitSpec))
	return item
}

func makeHttpConfigItem(t *testing.T, name, repo string, suffix *string) domain.ConfigProviderSpec {
	t.Helper()
	httpSpec := &domain.HttpConfigProviderSpec{Name: name}
	httpSpec.HttpRef.Repository = repo
	httpSpec.HttpRef.FilePath = "/etc/http-config"
	httpSpec.HttpRef.Suffix = suffix
	item := domain.ConfigProviderSpec{}
	require.NoError(t, item.FromHttpConfigProviderSpec(*httpSpec))
	return item
}

func makeSecretConfigItem(t *testing.T, name, namespace, secretName string) domain.ConfigProviderSpec {
	t.Helper()
	secretSpec := &domain.KubernetesSecretProviderSpec{Name: name}
	secretSpec.SecretRef.Namespace = namespace
	secretSpec.SecretRef.Name = secretName
	secretSpec.SecretRef.MountPath = "/etc/secrets"
	item := domain.ConfigProviderSpec{}
	require.NoError(t, item.FromKubernetesSecretProviderSpec(*secretSpec))
	return item
}
