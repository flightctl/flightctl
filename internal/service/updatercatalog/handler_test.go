package updatercatalog_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/stretchr/testify/require"
)

func TestReleaseDescriptor(t *testing.T) {
	c, err := updatercatalog.ParseAdministratorConfig(encode(t, fixture(t)))
	require.NoError(t, err)
	r := c.Releases[0]
	descriptor, err := r.Descriptor()
	require.NoError(t, err)
	require.False(t, descriptor.Approved)
	require.Equal(t, r.PackageDigest, descriptor.BuildDigest)
	require.Equal(t, r.Schemas.Config.Digest, descriptor.SchemaDigest)
	require.Equal(t, r.Schemas.Observation.Digest, descriptor.ObservationSchemaDigest)
	require.Equal(t, r.Schemas.Config.Bytes, string(descriptor.ConfigSchema))
	var bound updatercatalog.ReleaseBinding
	require.NoError(t, json.Unmarshal(descriptor.DescriptorJSON, &bound))
	require.Equal(t, r, bound)
	descriptor.BudgetKeySet[0] = "changed"
	descriptor.ConfigSchema[0] = ' '
	again, err := r.Descriptor()
	require.NoError(t, err)
	require.NotEqual(t, descriptor.BudgetKeySet, again.BudgetKeySet)
	require.Equal(t, r.Schemas.Config.Bytes, string(again.ConfigSchema))
}

func TestReadAdministratorConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		ok   bool
	}{{"valid", encode(t, fixture(t)), true}, {"empty", nil, false}, {"malformed", []byte("[invalid"), false}, {"oversized", make([]byte, 4*1024*1024+1), false}} {
		t.Run("When "+tc.name+" input is read it should enforce whole-file validation", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "catalog.yaml")
			require.NoError(t, os.WriteFile(path, tc.data, 0o600))
			_, err := updatercatalog.ReadAdministratorConfig(path)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	t.Run("When file is missing it should report failure", func(t *testing.T) {
		_, err := updatercatalog.ReadAdministratorConfig(filepath.Join(t.TempDir(), "missing"))
		require.Error(t, err)
	})
	t.Run("When the store is missing it should reject construction", func(t *testing.T) { _, err := updatercatalog.NewService(nil); require.Error(t, err) })
}

func TestCatalogBootstrapFailures(t *testing.T) {
	t.Run("When the database is missing it should reject bootstrap", func(t *testing.T) {
		_, err := updatercatalog.Bootstrap(context.Background(), nil, "")
		require.Error(t, err)
	})
	t.Run("When the catalog path is a directory it should report a read failure", func(t *testing.T) {
		_, err := updatercatalog.ReadAdministratorConfig(t.TempDir())
		require.Error(t, err)
	})
}
