package tasks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	apiimagebuilder "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockImageBuilderService is a mock implementation of imagebuilderapi.Service for testing
type mockImageBuilderService struct {
	imageBuilds    []*apiimagebuilder.ImageBuild
	imageExports   []*apiimagebuilder.ImageExport
	updatedBuilds  map[string]*apiimagebuilder.ImageBuild  // tracks which builds were updated
	updatedExports map[string]*apiimagebuilder.ImageExport // tracks which exports were updated
}

func newMockImageBuilderService() *mockImageBuilderService {
	return &mockImageBuilderService{
		imageBuilds:    make([]*apiimagebuilder.ImageBuild, 0),
		imageExports:   make([]*apiimagebuilder.ImageExport, 0),
		updatedBuilds:  make(map[string]*apiimagebuilder.ImageBuild),
		updatedExports: make(map[string]*apiimagebuilder.ImageExport),
	}
}

func (m *mockImageBuilderService) ImageBuild() imagebuilderapi.ImageBuildService {
	return &mockImageBuildService{parent: m}
}

func (m *mockImageBuilderService) ImageExport() imagebuilderapi.ImageExportService {
	return &mockImageExportService{parent: m}
}

type mockImageBuildService struct {
	parent *mockImageBuilderService
}

func (m *mockImageBuildService) Create(ctx context.Context, orgId uuid.UUID, imageBuild apiimagebuilder.ImageBuild) (*apiimagebuilder.ImageBuild, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageBuildService) Get(ctx context.Context, orgId uuid.UUID, name string, withExports bool) (*apiimagebuilder.ImageBuild, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageBuildService) List(ctx context.Context, orgId uuid.UUID, params apiimagebuilder.ListImageBuildsParams) (*apiimagebuilder.ImageBuildList, v1beta1.Status) {
	// Filter based on field selector - simulate the actual field selector behavior
	var filtered []apiimagebuilder.ImageBuild
	var threshold time.Time

	// Parse threshold from field selector if present
	// Format: "status.conditions.ready.reason not in (Completed, Failed, Pending),status.lastSeen<{timestamp}"
	if params.FieldSelector != nil {
		parts := strings.Split(*params.FieldSelector, ",")
		for _, part := range parts {
			if strings.HasPrefix(part, "status.lastSeen<") {
				thresholdStr := strings.TrimPrefix(part, "status.lastSeen<")
				parsed, err := time.Parse(time.RFC3339, thresholdStr)
				if err == nil {
					threshold = parsed
				}
			}
		}
	}

	for _, ib := range m.parent.imageBuilds {
		if ib.Status == nil || ib.Status.Conditions == nil {
			continue
		}

		readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*ib.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
		if readyCondition == nil {
			continue
		}

		reason := readyCondition.Reason
		// Exclude Completed, Failed, Pending (as per field selector)
		if reason == string(apiimagebuilder.ImageBuildConditionReasonCompleted) ||
			reason == string(apiimagebuilder.ImageBuildConditionReasonFailed) ||
			reason == string(apiimagebuilder.ImageBuildConditionReasonPending) {
			continue
		}

		// If field selector is provided, check lastSeen against threshold
		if params.FieldSelector != nil {
			if ib.Status.LastSeen == nil {
				continue
			}
			if !ib.Status.LastSeen.Before(threshold) {
				continue // lastSeen is not older than threshold
			}
		}

		filtered = append(filtered, *ib)
	}

	return &apiimagebuilder.ImageBuildList{Items: filtered}, v1beta1.StatusOK()
}

func (m *mockImageBuildService) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *apiimagebuilder.ImageBuild) (*apiimagebuilder.ImageBuild, error) {
	name := lo.FromPtr(imageBuild.Metadata.Name)
	m.parent.updatedBuilds[name] = imageBuild
	return imageBuild, nil
}

func (m *mockImageBuildService) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return nil
}

func (m *mockImageBuildService) GetLogs(ctx context.Context, orgId uuid.UUID, name string, follow bool) (imagebuilderapi.LogStreamReader, string, v1beta1.Status) {
	return nil, "", v1beta1.StatusOK()
}

func (m *mockImageBuildService) UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error {
	return nil
}

func (m *mockImageBuildService) Delete(ctx context.Context, orgId uuid.UUID, name string) (*apiimagebuilder.ImageBuild, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageBuildService) Cancel(ctx context.Context, orgId uuid.UUID, name string) (*apiimagebuilder.ImageBuild, error) {
	return m.CancelWithReason(ctx, orgId, name, "Build cancellation requested")
}

func (m *mockImageBuildService) CancelWithReason(ctx context.Context, orgId uuid.UUID, name string, reason string) (*apiimagebuilder.ImageBuild, error) {
	// Find and update the build
	for _, build := range m.parent.imageBuilds {
		if lo.FromPtr(build.Metadata.Name) == name {
			// Set the Canceling condition
			now := time.Now().UTC()
			cancelingCondition := apiimagebuilder.ImageBuildCondition{
				Type:               apiimagebuilder.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(apiimagebuilder.ImageBuildConditionReasonCanceling),
				Message:            reason,
				LastTransitionTime: now,
			}
			apiimagebuilder.SetImageBuildStatusCondition(build.Status.Conditions, cancelingCondition)
			m.parent.updatedBuilds[name] = build
			return build, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

type mockImageExportService struct {
	parent *mockImageBuilderService
}

func (m *mockImageExportService) Create(ctx context.Context, orgId uuid.UUID, imageExport apiimagebuilder.ImageExport) (*apiimagebuilder.ImageExport, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageExportService) Get(ctx context.Context, orgId uuid.UUID, name string) (*apiimagebuilder.ImageExport, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageExportService) List(ctx context.Context, orgId uuid.UUID, params apiimagebuilder.ListImageExportsParams) (*apiimagebuilder.ImageExportList, v1beta1.Status) {
	// Filter based on field selector - simulate the actual field selector behavior
	var filtered []apiimagebuilder.ImageExport
	var threshold time.Time

	// Parse threshold from field selector if present
	if params.FieldSelector != nil {
		parts := strings.Split(*params.FieldSelector, ",")
		for _, part := range parts {
			if strings.HasPrefix(part, "status.lastSeen<") {
				thresholdStr := strings.TrimPrefix(part, "status.lastSeen<")
				parsed, err := time.Parse(time.RFC3339, thresholdStr)
				if err == nil {
					threshold = parsed
				}
			}
		}
	}

	for _, ie := range m.parent.imageExports {
		if ie.Status == nil || ie.Status.Conditions == nil {
			continue
		}

		readyCondition := apiimagebuilder.FindImageExportStatusCondition(*ie.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
		if readyCondition == nil {
			continue
		}

		reason := readyCondition.Reason
		// Exclude Completed, Failed, Pending (as per field selector)
		if reason == string(apiimagebuilder.ImageExportConditionReasonCompleted) ||
			reason == string(apiimagebuilder.ImageExportConditionReasonFailed) ||
			reason == string(apiimagebuilder.ImageExportConditionReasonPending) {
			continue
		}

		// If field selector is provided, check lastSeen against threshold
		if params.FieldSelector != nil {
			if ie.Status.LastSeen == nil {
				continue
			}
			if !ie.Status.LastSeen.Before(threshold) {
				continue // lastSeen is not older than threshold
			}
		}

		filtered = append(filtered, *ie)
	}

	return &apiimagebuilder.ImageExportList{Items: filtered}, v1beta1.StatusOK()
}

func (m *mockImageExportService) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *apiimagebuilder.ImageExport) (*apiimagebuilder.ImageExport, error) {
	name := lo.FromPtr(imageExport.Metadata.Name)
	m.parent.updatedExports[name] = imageExport
	return imageExport, nil
}

func (m *mockImageExportService) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return nil
}

func (m *mockImageExportService) Delete(ctx context.Context, orgId uuid.UUID, name string) (*apiimagebuilder.ImageExport, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageExportService) Download(ctx context.Context, orgId uuid.UUID, name string) (*imagebuilderapi.ImageExportDownload, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *mockImageExportService) GetLogs(ctx context.Context, orgId uuid.UUID, name string, follow bool) (imagebuilderapi.LogStreamReader, string, v1beta1.Status) {
	return nil, "", v1beta1.StatusOK()
}

func (m *mockImageExportService) UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error {
	return nil
}

func (m *mockImageExportService) Cancel(ctx context.Context, orgId uuid.UUID, name string) (*apiimagebuilder.ImageExport, error) {
	return m.CancelWithReason(ctx, orgId, name, "Export cancellation requested")
}

func (m *mockImageExportService) CancelWithReason(ctx context.Context, orgId uuid.UUID, name string, reason string) (*apiimagebuilder.ImageExport, error) {
	// Find and update the export
	for _, export := range m.parent.imageExports {
		if lo.FromPtr(export.Metadata.Name) == name {
			// Set the Canceling condition
			now := time.Now().UTC()
			cancelingCondition := apiimagebuilder.ImageExportCondition{
				Type:               apiimagebuilder.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(apiimagebuilder.ImageExportConditionReasonCanceling),
				Message:            reason,
				LastTransitionTime: now,
			}
			apiimagebuilder.SetImageExportStatusCondition(export.Status.Conditions, cancelingCondition)
			m.parent.updatedExports[name] = export
			return export, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func createTestImageBuild(name string, reason apiimagebuilder.ImageBuildConditionReason, lastSeen time.Time) *apiimagebuilder.ImageBuild {
	now := time.Now().UTC()
	conditions := []apiimagebuilder.ImageBuildCondition{
		{
			Type:               apiimagebuilder.ImageBuildConditionTypeReady,
			Status:             v1beta1.ConditionStatusFalse,
			Reason:             string(reason),
			Message:            "test",
			LastTransitionTime: now,
		},
	}

	return &apiimagebuilder.ImageBuild{
		ApiVersion: apiimagebuilder.ImageBuildAPIVersion,
		Kind:       string(apiimagebuilder.ResourceKindImageBuild),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Status: &apiimagebuilder.ImageBuildStatus{
			Conditions: &conditions,
			LastSeen:   &lastSeen,
		},
	}
}

func createTestImageExport(name string, reason apiimagebuilder.ImageExportConditionReason, lastSeen time.Time) *apiimagebuilder.ImageExport {
	now := time.Now().UTC()
	conditions := []apiimagebuilder.ImageExportCondition{
		{
			Type:               apiimagebuilder.ImageExportConditionTypeReady,
			Status:             v1beta1.ConditionStatusFalse,
			Reason:             string(reason),
			Message:            "test",
			LastTransitionTime: now,
		},
	}

	return &apiimagebuilder.ImageExport{
		ApiVersion: apiimagebuilder.ImageExportAPIVersion,
		Kind:       string(apiimagebuilder.ResourceKindImageExport),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Status: &apiimagebuilder.ImageExportStatus{
			Conditions: &conditions,
			LastSeen:   &lastSeen,
		},
	}
}

func TestCheckAndMarkTimeoutsForOrg(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	timeoutDuration := 5 * time.Minute
	logger := logrus.NewEntry(logrus.New())

	tests := []struct {
		name                   string
		setupBuilds            []*apiimagebuilder.ImageBuild
		setupExports           []*apiimagebuilder.ImageExport
		timeoutDuration        time.Duration
		expectedUpdatedBuilds  []string
		expectedUpdatedExports []string
	}{
		{
			name: "marks timed-out Building ImageBuild as Canceling",
			setupBuilds: []*apiimagebuilder.ImageBuild{
				createTestImageBuild("build-1", apiimagebuilder.ImageBuildConditionReasonBuilding, time.Now().UTC().Add(-10*time.Minute)),
			},
			setupExports:           []*apiimagebuilder.ImageExport{},
			timeoutDuration:        timeoutDuration,
			expectedUpdatedBuilds:  []string{"build-1"},
			expectedUpdatedExports: []string{},
		},
		{
			name:        "marks timed-out Converting ImageExport as Canceling",
			setupBuilds: []*apiimagebuilder.ImageBuild{},
			setupExports: []*apiimagebuilder.ImageExport{
				createTestImageExport("export-1", apiimagebuilder.ImageExportConditionReasonConverting, time.Now().UTC().Add(-10*time.Minute)),
			},
			timeoutDuration:        timeoutDuration,
			expectedUpdatedBuilds:  []string{},
			expectedUpdatedExports: []string{"export-1"},
		},
		{
			name: "marks timed-out Pushing ImageBuild as Canceling",
			setupBuilds: []*apiimagebuilder.ImageBuild{
				createTestImageBuild("build-2", apiimagebuilder.ImageBuildConditionReasonPushing, time.Now().UTC().Add(-10*time.Minute)),
			},
			setupExports:           []*apiimagebuilder.ImageExport{},
			timeoutDuration:        timeoutDuration,
			expectedUpdatedBuilds:  []string{"build-2"},
			expectedUpdatedExports: []string{},
		},
		{
			name: "does not mark recent Building ImageBuild",
			setupBuilds: []*apiimagebuilder.ImageBuild{
				createTestImageBuild("build-3", apiimagebuilder.ImageBuildConditionReasonBuilding, time.Now().UTC().Add(-1*time.Minute)),
			},
			setupExports:           []*apiimagebuilder.ImageExport{},
			timeoutDuration:        timeoutDuration,
			expectedUpdatedBuilds:  []string{},
			expectedUpdatedExports: []string{},
		},
		{
			name: "does not mark Pending ImageBuild (excluded by field selector)",
			setupBuilds: []*apiimagebuilder.ImageBuild{
				createTestImageBuild("build-4", apiimagebuilder.ImageBuildConditionReasonPending, time.Now().UTC().Add(-10*time.Minute)),
			},
			setupExports:           []*apiimagebuilder.ImageExport{},
			timeoutDuration:        timeoutDuration,
			expectedUpdatedBuilds:  []string{},
			expectedUpdatedExports: []string{},
		},
		{
			name: "does not mark Completed ImageBuild (excluded by field selector)",
			setupBuilds: []*apiimagebuilder.ImageBuild{
				createTestImageBuild("build-5", apiimagebuilder.ImageBuildConditionReasonCompleted, time.Now().UTC().Add(-10*time.Minute)),
			},
			setupExports:           []*apiimagebuilder.ImageExport{},
			timeoutDuration:        timeoutDuration,
			expectedUpdatedBuilds:  []string{},
			expectedUpdatedExports: []string{},
		},
		{
			name: "does not mark Failed ImageBuild (excluded by field selector)",
			setupBuilds: []*apiimagebuilder.ImageBuild{
				createTestImageBuild("build-6", apiimagebuilder.ImageBuildConditionReasonFailed, time.Now().UTC().Add(-10*time.Minute)),
			},
			setupExports:           []*apiimagebuilder.ImageExport{},
			timeoutDuration:        timeoutDuration,
			expectedUpdatedBuilds:  []string{},
			expectedUpdatedExports: []string{},
		},
		{
			name: "marks multiple timed-out resources",
			setupBuilds: []*apiimagebuilder.ImageBuild{
				createTestImageBuild("build-7", apiimagebuilder.ImageBuildConditionReasonBuilding, time.Now().UTC().Add(-10*time.Minute)),
				createTestImageBuild("build-8", apiimagebuilder.ImageBuildConditionReasonPushing, time.Now().UTC().Add(-10*time.Minute)),
			},
			setupExports: []*apiimagebuilder.ImageExport{
				createTestImageExport("export-2", apiimagebuilder.ImageExportConditionReasonConverting, time.Now().UTC().Add(-10*time.Minute)),
				createTestImageExport("export-3", apiimagebuilder.ImageExportConditionReasonPushing, time.Now().UTC().Add(-10*time.Minute)),
			},
			timeoutDuration:        timeoutDuration,
			expectedUpdatedBuilds:  []string{"build-7", "build-8"},
			expectedUpdatedExports: []string{"export-2", "export-3"},
		},
		{
			name: "handles mix of timed-out and recent resources",
			setupBuilds: []*apiimagebuilder.ImageBuild{
				createTestImageBuild("build-9", apiimagebuilder.ImageBuildConditionReasonBuilding, time.Now().UTC().Add(-10*time.Minute)),
				createTestImageBuild("build-10", apiimagebuilder.ImageBuildConditionReasonBuilding, time.Now().UTC().Add(-1*time.Minute)),
			},
			setupExports: []*apiimagebuilder.ImageExport{
				createTestImageExport("export-4", apiimagebuilder.ImageExportConditionReasonConverting, time.Now().UTC().Add(-10*time.Minute)),
				createTestImageExport("export-5", apiimagebuilder.ImageExportConditionReasonConverting, time.Now().UTC().Add(-1*time.Minute)),
			},
			timeoutDuration:        timeoutDuration,
			expectedUpdatedBuilds:  []string{"build-9"},
			expectedUpdatedExports: []string{"export-4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			mockService := newMockImageBuilderService()
			mockService.imageBuilds = tt.setupBuilds
			mockService.imageExports = tt.setupExports

			consumer := &Consumer{
				imageBuilderService: mockService,
				cfg:                 &config.Config{},
				log:                 logger,
			}

			// Execute
			failedCount, err := consumer.CheckAndMarkTimeoutsForOrg(ctx, orgID, tt.timeoutDuration, logger)
			require.NoError(t, err)
			expectedFailedCount := len(tt.expectedUpdatedBuilds) + len(tt.expectedUpdatedExports)
			assert.Equal(t, expectedFailedCount, failedCount, "failed count should match expected")

			// Verify updated builds
			assert.Equal(t, len(tt.expectedUpdatedBuilds), len(mockService.updatedBuilds), "number of updated builds should match")
			for _, expectedName := range tt.expectedUpdatedBuilds {
				updated, ok := mockService.updatedBuilds[expectedName]
				require.True(t, ok, "build %s should have been updated", expectedName)

				// Verify it was marked as Canceling (graceful cancellation)
				readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
				require.NotNil(t, readyCondition, "build %s should have Ready condition", expectedName)
				assert.Equal(t, string(apiimagebuilder.ImageBuildConditionReasonCanceling), readyCondition.Reason, "build %s should be marked as Canceling", expectedName)
				assert.Equal(t, v1beta1.ConditionStatusFalse, readyCondition.Status, "build %s should have Status False", expectedName)
				assert.Contains(t, readyCondition.Message, "timed out", "build %s should have timeout message", expectedName)
			}

			// Verify updated exports
			assert.Equal(t, len(tt.expectedUpdatedExports), len(mockService.updatedExports), "number of updated exports should match")
			for _, expectedName := range tt.expectedUpdatedExports {
				updated, ok := mockService.updatedExports[expectedName]
				require.True(t, ok, "export %s should have been updated", expectedName)

				// Verify it was marked as Canceling (graceful cancellation)
				readyCondition := apiimagebuilder.FindImageExportStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
				require.NotNil(t, readyCondition, "export %s should have Ready condition", expectedName)
				assert.Equal(t, string(apiimagebuilder.ImageExportConditionReasonCanceling), readyCondition.Reason, "export %s should be marked as Canceling", expectedName)
				assert.Equal(t, v1beta1.ConditionStatusFalse, readyCondition.Status, "export %s should have Status False", expectedName)
				assert.Contains(t, readyCondition.Message, "timed out", "export %s should have timeout message", expectedName)
			}
		})
	}
}
