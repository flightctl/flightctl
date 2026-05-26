package restore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// buildExtractDirWithDump creates an extract directory containing db/dump.sql
// with the given SQL content. Returns the extract directory path.
func buildExtractDirWithDump(t *testing.T, sqlContent string) string {
	t.Helper()
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "db")
	require.NoError(t, os.MkdirAll(dbDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dbDir, "dump.sql"), []byte(sqlContent), 0600))
	return dir
}

// buildExtractDirNoDump creates an extract directory without db/dump.sql (external DB scenario).
func buildExtractDirNoDump(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// writeServiceConfig saves cfg to a temporary service-config.yaml and returns its path.
func writeServiceConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service-config.yaml")
	require.NoError(t, config.Save(cfg, path))
	return path
}

// defaultServiceConfig returns a minimal valid config with internal DB credentials.
func defaultServiceConfig() *config.Config {
	cfg := config.NewDefault()
	cfg.Database.Hostname = "localhost"
	cfg.Database.Port = 5432
	cfg.Database.User = "flightctl_app"
	cfg.Database.Name = "flightctl"
	cfg.Database.Password = "testpass"
	return cfg
}

// TestPodmanRestoreDeployer_Type validates the Type() method.
func TestPodmanRestoreDeployer_Type(t *testing.T) {
	log, _ := test.NewNullLogger()
	ctrl := gomock.NewController(t)
	d := NewPodmanRestoreDeployer(log, WithServiceHandler(NewMockServiceHandler(ctrl)))
	require.Equal(t, backup.DeploymentTypePodman, d.Type())
}

// TestPodmanRestoreDeployer_StopStartServices validates that StopServices and
// StartServices delegate to the ServiceHandler.
func TestPodmanRestoreDeployer_StopStartServices(t *testing.T) {
	log, _ := test.NewNullLogger()
	ctrl := gomock.NewController(t)
	handler := NewMockServiceHandler(ctrl)
	handler.EXPECT().Stop(gomock.Any()).Return(nil)
	handler.EXPECT().Start(gomock.Any()).Return(nil)

	d := NewPodmanRestoreDeployer(log, WithServiceHandler(handler))
	require.NoError(t, d.StopServices(context.Background()))
	require.NoError(t, d.StartServices(context.Background()))
}

// TestPodmanRestoreDeployer_RestoreDatabase validates the behavioral contracts
// of PodmanRestoreDeployer.RestoreDatabase.
func TestPodmanRestoreDeployer_RestoreDatabase(t *testing.T) {
	tests := []struct {
		name        string
		setupDir    func(t *testing.T) string
		wantErr     bool
		errContains string
	}{
		{
			name:     "When db/dump.sql is absent it should return nil and log external-DB instructions",
			setupDir: buildExtractDirNoDump,
		},
		{
			name: "When db/dump.sql exists and container is not found it should return error mentioning the container",
			setupDir: func(t *testing.T) string {
				return buildExtractDirWithDump(t, "SELECT 1;")
			},
			wantErr:     true,
			errContains: "flightctl-db-nonexistent-test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, _ := test.NewNullLogger()
			ctrl := gomock.NewController(t)
			cfgPath := writeServiceConfig(t, defaultServiceConfig())
			d := NewPodmanRestoreDeployer(log,
				WithDBContainerName("flightctl-db-nonexistent-test"),
				WithServiceHandler(NewMockServiceHandler(ctrl)),
				WithServiceConfigPath(cfgPath),
			)
			err := d.RestoreDatabase(context.Background(), tt.setupDir(t))

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.ErrorContains(t, err, tt.errContains)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestPodmanRestoreDeployer_GetConfig_MissingFile validates that GetConfig returns
// an error when the service config file is absent.
func TestPodmanRestoreDeployer_GetConfig_MissingFile(t *testing.T) {
	log, _ := test.NewNullLogger()
	nonExistent := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	d := NewPodmanRestoreDeployer(log, WithServiceConfigPath(nonExistent))
	_, err := d.GetConfig(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to load service configuration")
}

// TestPodmanRestoreDeployer_GetConfig_ValidFile validates credential extraction from
// a valid service config file.
func TestPodmanRestoreDeployer_GetConfig_ValidFile(t *testing.T) {
	log, _ := test.NewNullLogger()
	cfgPath := writeServiceConfig(t, defaultServiceConfig())
	d := NewPodmanRestoreDeployer(log, WithServiceConfigPath(cfgPath))
	cfg, err := d.GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "flightctl_app", cfg.Database.User)
	require.Equal(t, "flightctl", cfg.Database.Name)
}

// TestKubernetesRestoreDeployer_Type validates the Type() method.
func TestKubernetesRestoreDeployer_Type(t *testing.T) {
	log, _ := test.NewNullLogger()
	d := NewKubernetesRestoreDeployer(log)
	require.Equal(t, backup.DeploymentTypeKubernetes, d.Type())
}

// TestKubernetesRestoreDeployer_StopStartServices validates that StopServices
// returns an error when a deployment in the default registry is not found.
func TestKubernetesRestoreDeployer_StopStartServices(t *testing.T) {
	log, _ := test.NewNullLogger()

	t.Run("When registry deployments do not exist StopServices should return error", func(t *testing.T) {
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace("flightctl"),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err := d.StopServices(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "flightctl-api")
	})
}

// TestKubernetesRestoreDeployer_RestoreDatabase validates the behavioral contracts
// of KubernetesRestoreDeployer.RestoreDatabase.
func TestKubernetesRestoreDeployer_RestoreDatabase(t *testing.T) {
	tests := []struct {
		name        string
		setupDir    func(t *testing.T) string
		wantErr     bool
		errContains string
	}{
		{
			name:     "When db/dump.sql is absent it should return nil and log external-DB instructions",
			setupDir: buildExtractDirNoDump,
		},
		{
			name: "When db/dump.sql exists and kubectl exec fails it should return error",
			setupDir: func(t *testing.T) string {
				return buildExtractDirWithDump(t, "SELECT 1;")
			},
			wantErr: true,
		},
	}

	// Build a fake clientset that has the required DB credential Secrets so
	// GetConfig succeeds. The new RestoreDatabase uses kubectl exec directly
	// (no pod lookup via clientset), so missing pods no longer trigger an error
	// from the clientset — instead kubectl exec will fail in the test environment.
	dbSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flightctl-db-app-secret",
			Namespace: "flightctl",
		},
		Data: map[string][]byte{
			"user":         []byte("flightctl_app"),
			"userPassword": []byte("secret"),
		},
	}
	kvSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flightctl-kv-secret",
			Namespace: "flightctl",
		},
		Data: map[string][]byte{
			"password": []byte("kvpass"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, _ := test.NewNullLogger()
			d := NewKubernetesRestoreDeployer(log,
				WithRestoreNamespace("flightctl"),
				WithRestoreClientset(fake.NewSimpleClientset(dbSecret, kvSecret)),
				WithRestoreRestConfig(&rest.Config{}),
			)
			err := d.RestoreDatabase(context.Background(), tt.setupDir(t))

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.ErrorContains(t, err, tt.errContains)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestKubernetesRestoreDeployer_GetConfig_MissingSecret validates that GetConfig
// returns an appropriate error when the DB Secret is absent.
func TestKubernetesRestoreDeployer_GetConfig_MissingSecret(t *testing.T) {
	log, _ := test.NewNullLogger()
	d := NewKubernetesRestoreDeployer(log,
		WithRestoreNamespace("flightctl"),
		WithRestoreClientset(fake.NewSimpleClientset()),
		WithRestoreRestConfig(&rest.Config{}),
	)
	_, err := d.GetConfig(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "flightctl-db-app-secret")
}

// TestKubernetesRestoreDeployer_GetConfig_Success validates credential extraction
// from Kubernetes Secrets.
func TestKubernetesRestoreDeployer_GetConfig_Success(t *testing.T) {
	dbSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flightctl-db-app-secret",
			Namespace: "flightctl",
		},
		Data: map[string][]byte{
			"user":         []byte("flightctl_app"),
			"userPassword": []byte("dbsecret"),
		},
	}
	kvSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flightctl-kv-secret",
			Namespace: "flightctl",
		},
		Data: map[string][]byte{
			"password": []byte("kvsecret"),
		},
	}

	log, _ := test.NewNullLogger()
	d := NewKubernetesRestoreDeployer(log,
		WithRestoreNamespace("flightctl"),
		WithRestoreClientset(fake.NewSimpleClientset(dbSecret, kvSecret)),
		WithRestoreRestConfig(&rest.Config{}),
	)

	cfg, err := d.GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "flightctl_app", cfg.Database.User)
	require.Equal(t, "dbsecret", string(cfg.Database.Password))
	require.Equal(t, "kvsecret", string(cfg.KV.Password))
	require.Equal(t, "flightctl", cfg.Database.Name)
	require.Equal(t, uint(dbInternalPort), cfg.Database.Port)
	require.Equal(t, uint(kvInternalPort), cfg.KV.Port)
}
