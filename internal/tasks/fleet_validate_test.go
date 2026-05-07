package tasks

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestFleetValidateLogic_CreateNewTemplateVersionIfFleetValid_ImmediateRollout(t *testing.T) {
	tests := []struct {
		name              string
		rolloutPolicy     *domain.RolloutPolicy
		expectedImmediate bool
		description       string
	}{
		{
			name:              "NoRolloutPolicy",
			rolloutPolicy:     nil,
			expectedImmediate: true,
			description:       "immediateRollout should be true when RolloutPolicy is nil",
		},
		{
			name: "RolloutPolicyWithoutDeviceSelection",
			rolloutPolicy: &domain.RolloutPolicy{
				DeviceSelection: nil,
			},
			expectedImmediate: true,
			description:       "immediateRollout should be true when RolloutPolicy exists but DeviceSelection is nil",
		},
		{
			name: "RolloutPolicyWithDeviceSelection",
			rolloutPolicy: &domain.RolloutPolicy{
				DeviceSelection: &domain.RolloutDeviceSelection{},
			},
			expectedImmediate: false,
			description:       "immediateRollout should be false when RolloutPolicy.DeviceSelection exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			fleetName := "test-fleet"
			fleet := createTestFleet(fleetName, tt.rolloutPolicy)
			event := createTestEvent(domain.FleetKind, "some-reason", fleetName)
			orgId := uuid.New()
			log := logrus.New()

			mockService := service.NewMockService(ctrl)
			mockK8SClient := k8sclient.NewMockK8SClient(ctrl)

			// Mock GetFleet to return our test fleet
			mockService.EXPECT().GetFleet(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(fleet, domain.Status{Code: http.StatusOK})

			// Mock OverwriteFleetRepositoryRefs to succeed
			mockService.EXPECT().OverwriteFleetRepositoryRefs(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(domain.Status{Code: http.StatusOK})

			// Mock DependencyRef operations to succeed
			mockService.EXPECT().DeleteDependencyRefsByFleet(gomock.Any(), gomock.Any(), fleetName).Return(domain.Status{Code: http.StatusOK})

			// Mock CreateTemplateVersion to capture the immediateRollout parameter
			var capturedImmediateRollout bool
			mockService.EXPECT().CreateTemplateVersion(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, orgId uuid.UUID, tv domain.TemplateVersion, immediateRollout bool) (*domain.TemplateVersion, domain.Status) {
					capturedImmediateRollout = immediateRollout
					return &domain.TemplateVersion{
						Metadata: domain.ObjectMeta{
							Name: &[]string{"test-tv"}[0],
						},
					}, domain.Status{Code: http.StatusCreated}
				})

			// Mock UpdateFleetAnnotations to succeed
			mockService.EXPECT().UpdateFleetAnnotations(gomock.Any(), gomock.Any(), fleetName, gomock.Any(), gomock.Any()).Return(domain.Status{Code: http.StatusOK})

			// Mock UpdateToOutOfDateByOwner to succeed
			mockService.EXPECT().SetOutOfDate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

			// Mock UpdateFleetConditions to succeed
			mockService.EXPECT().UpdateFleetConditions(gomock.Any(), gomock.Any(), fleetName, gomock.Any()).Return(domain.Status{Code: http.StatusOK})

			// Create FleetValidateLogic instance
			logic := NewFleetValidateLogic(log, mockService, mockK8SClient, orgId, event)

			// Execute
			err := logic.CreateNewTemplateVersionIfFleetValid(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.expectedImmediate, capturedImmediateRollout, tt.description)
		})
	}
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
			name:        "When fingerprint is provided it should return v{generation}-{short-hash}",
			fleetName:   "my-fleet",
			generation:  3,
			fingerprint: "abc123def456789",
			expected:    "v3-abc123de",
		},
		{
			name:        "When fingerprint is shorter than 8 chars it should use full fingerprint",
			fleetName:   "my-fleet",
			generation:  1,
			fingerprint: "abc",
			expected:    "v1-abc",
		},
		{
			name:        "When fingerprint is exactly 8 chars it should use full fingerprint",
			fleetName:   "my-fleet",
			generation:  1,
			fingerprint: "abcd1234",
			expected:    "v1-abcd1234",
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

func makeK8sSecretConfigItem(t *testing.T, name, secretName, secretNamespace string) domain.ConfigProviderSpec {
	t.Helper()
	k8sSpec := &domain.KubernetesSecretProviderSpec{Name: name}
	k8sSpec.SecretRef.Name = secretName
	k8sSpec.SecretRef.Namespace = secretNamespace
	k8sSpec.SecretRef.MountPath = "/etc/secret"
	item := domain.ConfigProviderSpec{}
	require.NoError(t, item.FromKubernetesSecretProviderSpec(*k8sSpec))
	return item
}

func makeInlineConfigItem(t *testing.T, name string) domain.ConfigProviderSpec {
	t.Helper()
	inlineSpec := &domain.InlineConfigProviderSpec{Name: name}
	item := domain.ConfigProviderSpec{}
	require.NoError(t, item.FromInlineConfigProviderSpec(*inlineSpec))
	return item
}

func TestCollectDependencyRefs(t *testing.T) {
	tests := []struct {
		name      string
		config    *[]domain.ConfigProviderSpec
		fleetName string
		expected  []model.DependencyRef
	}{
		{
			name:      "When config is nil it should return nil",
			config:    nil,
			fleetName: "my-fleet",
			expected:  nil,
		},
		{
			name:      "When config is empty it should return no refs",
			config:    &[]domain.ConfigProviderSpec{},
			fleetName: "my-fleet",
			expected:  nil,
		},
		{
			name:      "When config has only inline items it should return no refs",
			fleetName: "my-fleet",
		},
		{
			name:      "When config has a git item it should return a git ref",
			fleetName: "my-fleet",
		},
		{
			name:      "When config has an HTTP item it should return an HTTP ref",
			fleetName: "my-fleet",
		},
		{
			name:      "When config has a K8s secret item it should return a secret ref",
			fleetName: "my-fleet",
		},
		{
			name:      "When config has mixed items it should return refs for all external types",
			fleetName: "my-fleet",
		},
	}

	// Build configs dynamically inside each test case
	tests[2].config = &[]domain.ConfigProviderSpec{}
	tests[3].config = &[]domain.ConfigProviderSpec{}
	tests[4].config = &[]domain.ConfigProviderSpec{}
	tests[5].config = &[]domain.ConfigProviderSpec{}
	tests[6].config = &[]domain.ConfigProviderSpec{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switch tt.name {
			case "When config has only inline items it should return no refs":
				*tt.config = []domain.ConfigProviderSpec{makeInlineConfigItem(t, "my-inline")}
				tt.expected = nil

			case "When config has a git item it should return a git ref":
				*tt.config = []domain.ConfigProviderSpec{makeGitConfigItem(t, "my-git", "my-repo", "main")}
				tt.expected = []model.DependencyRef{{
					FleetName:      lo.ToPtr("my-fleet"),
					RefType:        "git",
					RepositoryName: lo.ToPtr("my-repo"),
					Revision:       lo.ToPtr("main"),
				}}

			case "When config has an HTTP item it should return an HTTP ref":
				suffix := "/api/config"
				*tt.config = []domain.ConfigProviderSpec{makeHttpConfigItem(t, "my-http", "http-repo", &suffix)}
				tt.expected = []model.DependencyRef{{
					FleetName:      lo.ToPtr("my-fleet"),
					RefType:        "http",
					RepositoryName: lo.ToPtr("http-repo"),
					HTTPSuffix:     lo.ToPtr("/api/config"),
				}}

			case "When config has a K8s secret item it should return a secret ref":
				*tt.config = []domain.ConfigProviderSpec{makeK8sSecretConfigItem(t, "my-secret", "db-creds", "prod")}
				tt.expected = []model.DependencyRef{{
					FleetName:       lo.ToPtr("my-fleet"),
					RefType:         "secret",
					SecretName:      lo.ToPtr("db-creds"),
					SecretNamespace: lo.ToPtr("prod"),
				}}

			case "When config has mixed items it should return refs for all external types":
				*tt.config = []domain.ConfigProviderSpec{
					makeGitConfigItem(t, "git-cfg", "repo-a", "v1.0"),
					makeInlineConfigItem(t, "inline-cfg"),
					makeHttpConfigItem(t, "http-cfg", "repo-b", nil),
					makeK8sSecretConfigItem(t, "secret-cfg", "api-key", "default"),
				}
				tt.expected = []model.DependencyRef{
					{FleetName: lo.ToPtr("my-fleet"), RefType: "git", RepositoryName: lo.ToPtr("repo-a"), Revision: lo.ToPtr("v1.0")},
					{FleetName: lo.ToPtr("my-fleet"), RefType: "http", RepositoryName: lo.ToPtr("repo-b"), HTTPSuffix: nil},
					{FleetName: lo.ToPtr("my-fleet"), RefType: "secret", SecretName: lo.ToPtr("api-key"), SecretNamespace: lo.ToPtr("default")},
				}
			}

			logic := FleetValidateLogic{
				log:            logrus.New(),
				templateConfig: tt.config,
			}
			refs := logic.collectDependencyRefs(tt.fleetName)
			assert.Equal(t, tt.expected, refs)
		})
	}
}

func TestPopulateDependencyRefs(t *testing.T) {
	orgId := uuid.New()
	okStatus := domain.Status{Code: http.StatusOK}
	failStatus := domain.Status{Code: http.StatusInternalServerError, Message: "db error"}

	t.Run("When config has refs it should delete then upsert each", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockSvc := service.NewMockService(ctrl)

		var upsertedRefs []model.DependencyRef
		mockSvc.EXPECT().DeleteDependencyRefsByFleet(gomock.Any(), orgId, "my-fleet").Return(okStatus)
		mockSvc.EXPECT().UpsertDependencyRef(gomock.Any(), orgId, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, ref *model.DependencyRef) domain.Status {
				upsertedRefs = append(upsertedRefs, *ref)
				return okStatus
			}).Times(2)

		logic := FleetValidateLogic{
			log:            logrus.New(),
			serviceHandler: mockSvc,
			orgId:          orgId,
			templateConfig: &[]domain.ConfigProviderSpec{
				makeGitConfigItem(t, "git-cfg", "my-repo", "main"),
				makeK8sSecretConfigItem(t, "secret-cfg", "my-secret", "ns"),
			},
		}
		err := logic.populateDependencyRefs(context.Background(), "my-fleet")
		require.NoError(t, err)

		require.Len(t, upsertedRefs, 2)
		assert.Equal(t, "git", upsertedRefs[0].RefType)
		assert.Equal(t, "my-repo", *upsertedRefs[0].RepositoryName)
		assert.Equal(t, "secret", upsertedRefs[1].RefType)
		assert.Equal(t, "my-secret", *upsertedRefs[1].SecretName)
	})

	t.Run("When config is empty it should delete then upsert nothing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockSvc := service.NewMockService(ctrl)

		mockSvc.EXPECT().DeleteDependencyRefsByFleet(gomock.Any(), orgId, "my-fleet").Return(okStatus)

		logic := FleetValidateLogic{
			log:            logrus.New(),
			serviceHandler: mockSvc,
			orgId:          orgId,
			templateConfig: &[]domain.ConfigProviderSpec{},
		}
		err := logic.populateDependencyRefs(context.Background(), "my-fleet")
		require.NoError(t, err)
	})

	t.Run("When DeleteDependencyRefsByFleet fails it should return error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockSvc := service.NewMockService(ctrl)

		mockSvc.EXPECT().DeleteDependencyRefsByFleet(gomock.Any(), orgId, "my-fleet").Return(failStatus)

		logic := FleetValidateLogic{
			log:            logrus.New(),
			serviceHandler: mockSvc,
			orgId:          orgId,
			templateConfig: &[]domain.ConfigProviderSpec{
				makeGitConfigItem(t, "cfg", "repo", "main"),
			},
		}
		err := logic.populateDependencyRefs(context.Background(), "my-fleet")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "deleting stale dependency refs")
	})

	t.Run("When UpsertDependencyRef fails it should return error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockSvc := service.NewMockService(ctrl)

		mockSvc.EXPECT().DeleteDependencyRefsByFleet(gomock.Any(), orgId, "my-fleet").Return(okStatus)
		mockSvc.EXPECT().UpsertDependencyRef(gomock.Any(), orgId, gomock.Any()).Return(failStatus)

		logic := FleetValidateLogic{
			log:            logrus.New(),
			serviceHandler: mockSvc,
			orgId:          orgId,
			templateConfig: &[]domain.ConfigProviderSpec{
				makeGitConfigItem(t, "cfg", "repo", "main"),
			},
		}
		err := logic.populateDependencyRefs(context.Background(), "my-fleet")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "upserting dependency ref")
	})
}
