package labelsyncmapping

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestInitialLabelSyncMappingProvisioner(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()

	t.Run("When the manifest contains mappings it should create them for the organization", func(t *testing.T) {
		manifest := writeInitialMappingsManifest(t, `
- apiVersion: flightctl.io/v1beta1
  kind: LabelSyncMapping
  metadata:
    name: first
  spec:
    resourceType: Device
    expression: '{}'
- apiVersion: flightctl.io/v1beta1
  kind: LabelSyncMapping
  metadata:
    name: second
  spec:
    resourceType: Device
    expression: '{}'
`)
		ctrl := gomock.NewController(t)
		mappingService := NewMockService(ctrl)
		mappingService.EXPECT().CreateLabelSyncMapping(gomock.Any(), orgID, gomock.Any()).Times(2).DoAndReturn(
			func(_ context.Context, gotOrgID uuid.UUID, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
				require.Equal(t, orgID, gotOrgID)
				require.Contains(t, []string{"first", "second"}, *mapping.Metadata.Name)
				return &mapping, domain.StatusOK()
			},
		)

		provisioner, err := NewInitialLabelSyncMappingProvisioner(manifest, mappingService, logrus.New())
		require.NoError(t, err)
		provisioner.Provision(ctx, orgID)
	})

	t.Run("When organizations share a mapping name it should create the mapping independently for each organization", func(t *testing.T) {
		manifest := writeInitialMappingsManifest(t, `
- apiVersion: flightctl.io/v1beta1
  kind: LabelSyncMapping
  metadata:
    name: shared-name
  spec:
    resourceType: Device
    expression: '{}'
`)
		org1ID := uuid.New()
		org2ID := uuid.New()
		ctrl := gomock.NewController(t)
		mappingService := NewMockService(ctrl)
		for _, id := range []uuid.UUID{org1ID, org2ID} {
			mappingService.EXPECT().CreateLabelSyncMapping(gomock.Any(), id, gomock.Any()).DoAndReturn(
				func(_ context.Context, gotOrgID uuid.UUID, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
					require.Equal(t, id, gotOrgID)
					require.Equal(t, "shared-name", *mapping.Metadata.Name)
					return &mapping, domain.StatusOK()
				},
			)
		}

		provisioner, err := NewInitialLabelSyncMappingProvisioner(manifest, mappingService, logrus.New())
		require.NoError(t, err)
		provisioner.ProvisionForOrganizations(ctx, []uuid.UUID{org1ID, org2ID})
	})

	t.Run("When the manifest is an empty sequence it should create no mappings", func(t *testing.T) {
		manifest := writeInitialMappingsManifest(t, "[]\n")
		ctrl := gomock.NewController(t)
		mappingService := NewMockService(ctrl)

		provisioner, err := NewInitialLabelSyncMappingProvisioner(manifest, mappingService, logrus.New())
		require.NoError(t, err)
		provisioner.Provision(ctx, orgID)
	})

	t.Run("When a mapping conflicts it should log the failure and continue with later entries", func(t *testing.T) {
		manifest := writeInitialMappingsManifest(t, `
- apiVersion: flightctl.io/v1beta1
  kind: LabelSyncMapping
  metadata:
    name: existing
  spec:
    resourceType: Device
    expression: '{}'
- apiVersion: flightctl.io/v1beta1
  kind: LabelSyncMapping
  metadata:
    name: later
  spec:
    resourceType: Device
    expression: '{}'
`)
		var output bytes.Buffer
		logger := logrus.New()
		logger.SetOutput(&output)
		logger.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true})

		ctrl := gomock.NewController(t)
		mappingService := NewMockService(ctrl)
		mappingService.EXPECT().CreateLabelSyncMapping(gomock.Any(), orgID, gomock.Any()).Times(2).DoAndReturn(
			func(_ context.Context, gotOrgID uuid.UUID, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
				require.Equal(t, orgID, gotOrgID)
				switch *mapping.Metadata.Name {
				case "existing":
					return nil, domain.StatusConflict("already exists")
				case "later":
					return &mapping, domain.StatusOK()
				default:
					t.Fatalf("unexpected mapping %q", *mapping.Metadata.Name)
					return nil, domain.StatusInternalServerError("unexpected mapping")
				}
			},
		)

		provisioner, err := NewInitialLabelSyncMappingProvisioner(manifest, mappingService, logger)
		require.NoError(t, err)
		provisioner.Provision(ctx, orgID)

		require.Contains(t, output.String(), "existing")
		require.Contains(t, output.String(), "already exists")
		require.Contains(t, output.String(), "level=info")
		require.NotContains(t, output.String(), "level=error")
	})

	t.Run("When the manifest cannot be read or decoded it should log the issue without creating mappings", func(t *testing.T) {
		var output bytes.Buffer
		logger := logrus.New()
		logger.SetOutput(&output)
		logger.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true})

		ctrl := gomock.NewController(t)
		mappingService := NewMockService(ctrl)

		missing := filepath.Join(t.TempDir(), "missing.yaml")
		missingProvisioner, err := NewInitialLabelSyncMappingProvisioner(missing, mappingService, logger)
		require.NoError(t, err)
		missingProvisioner.Provision(ctx, orgID)
		missingProvisioner.Provision(ctx, uuid.New())
		malformed := writeInitialMappingsManifest(t, "mappings: []\n")
		malformedProvisioner, err := NewInitialLabelSyncMappingProvisioner(malformed, mappingService, logger)
		require.NoError(t, err)
		malformedProvisioner.Provision(ctx, orgID)

		require.Contains(t, output.String(), missing)
		require.Contains(t, output.String(), malformed)
		require.Equal(t, 1, strings.Count(output.String(), "Initial label-sync mappings file is not available"))
	})

	t.Run("When the manifest is blank or null it should log the invalid sequence without creating mappings", func(t *testing.T) {
		var output bytes.Buffer
		logger := logrus.New()
		logger.SetOutput(&output)
		logger.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true})

		ctrl := gomock.NewController(t)
		mappingService := NewMockService(ctrl)
		for _, contents := range []string{"", "null\n"} {
			manifest := writeInitialMappingsManifest(t, contents)
			provisioner, err := NewInitialLabelSyncMappingProvisioner(manifest, mappingService, logger)
			require.NoError(t, err)
			provisioner.Provision(ctx, orgID)
		}

		require.Contains(t, output.String(), "must be a YAML sequence")
	})

	t.Run("When an entry cannot be decoded it should continue with later entries", func(t *testing.T) {
		manifest := writeInitialMappingsManifest(t, `
- apiVersion: flightctl.io/v1beta1
  kind: LabelSyncMapping
  metadata:
    name: malformed
  spec: not-an-object
- apiVersion: flightctl.io/v1beta1
  kind: LabelSyncMapping
  metadata:
    name: later
  spec:
    resourceType: Device
    expression: '{}'
`)
		var output bytes.Buffer
		logger := logrus.New()
		logger.SetOutput(&output)
		logger.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true})

		ctrl := gomock.NewController(t)
		mappingService := NewMockService(ctrl)
		mappingService.EXPECT().CreateLabelSyncMapping(gomock.Any(), orgID, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
				require.Equal(t, "later", *mapping.Metadata.Name)
				return &mapping, domain.StatusOK()
			},
		)

		provisioner, err := NewInitialLabelSyncMappingProvisioner(manifest, mappingService, logger)
		require.NoError(t, err)
		provisioner.Provision(ctx, orgID)

		require.Contains(t, output.String(), "entry_index=0")
	})

	t.Run("When an entry contains managed fields it should clear them and preserve annotations", func(t *testing.T) {
		manifest := writeInitialMappingsManifest(t, `
- apiVersion: flightctl.io/v1beta1
  kind: LabelSyncMapping
  metadata:
    name: managed
    owner: system
    generation: 12
    annotations:
      operator.example/retained: value
  status:
    conditions: []
  spec:
    resourceType: Device
    expression: '{}'
`)
		ctrl := gomock.NewController(t)
		mappingService := NewMockService(ctrl)
		mappingService.EXPECT().CreateLabelSyncMapping(gomock.Any(), orgID, gomock.Any()).DoAndReturn(
			func(_ context.Context, _ uuid.UUID, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
				require.Nil(t, mapping.Metadata.Owner)
				require.Nil(t, mapping.Metadata.Generation)
				require.NotNil(t, mapping.Metadata.Annotations)
				require.Equal(t, map[string]string{"operator.example/retained": "value"}, *mapping.Metadata.Annotations)
				require.Nil(t, mapping.Status)
				return &mapping, domain.StatusOK()
			},
		)

		provisioner, err := NewInitialLabelSyncMappingProvisioner(manifest, mappingService, logrus.New())
		require.NoError(t, err)
		provisioner.Provision(ctx, orgID)
	})
}

func writeInitialMappingsManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mappings.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	return path
}
