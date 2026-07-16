package restore

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
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

// buildExtractDirWithPKI creates an extract directory containing a pki/ subdirectory
// with the given files (filename → mode). Returns the extract directory path.
func buildExtractDirWithPKI(t *testing.T, files map[string]os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	pkiDir := filepath.Join(dir, "pki")
	require.NoError(t, os.MkdirAll(pkiDir, 0700))
	for name, mode := range files {
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, name), []byte("mock-pki-content"), mode))
	}
	return dir
}

func TestCopyDirSecure(t *testing.T) {
	log, _ := test.NewNullLogger()

	t.Run("When source has regular files it should copy them to destination", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("aaa"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(src, "b.txt"), []byte("bbb"), 0600))

		count, err := copyDirSecure(context.Background(), src, dst, log)
		require.NoError(t, err)
		require.Equal(t, 2, count)

		data, err := os.ReadFile(filepath.Join(dst, "a.txt"))
		require.NoError(t, err)
		require.Equal(t, "aaa", string(data))

		data, err = os.ReadFile(filepath.Join(dst, "b.txt"))
		require.NoError(t, err)
		require.Equal(t, "bbb", string(data))
	})

	t.Run("When source has files it should preserve their permissions", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(src, "secret.key"), []byte("key"), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(src, "public.crt"), []byte("cert"), 0644))

		_, err := copyDirSecure(context.Background(), src, dst, log)
		require.NoError(t, err)

		info, err := os.Stat(filepath.Join(dst, "secret.key"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())

		info, err = os.Stat(filepath.Join(dst, "public.crt"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0644), info.Mode().Perm())
	})

	t.Run("When source has subdirectories it should recreate them with contents", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		subDir := filepath.Join(src, "sub", "nested")
		require.NoError(t, os.MkdirAll(subDir, 0750))
		require.NoError(t, os.WriteFile(filepath.Join(subDir, "deep.txt"), []byte("deep"), 0600))

		count, err := copyDirSecure(context.Background(), src, dst, log)
		require.NoError(t, err)
		require.Equal(t, 1, count)

		data, err := os.ReadFile(filepath.Join(dst, "sub", "nested", "deep.txt"))
		require.NoError(t, err)
		require.Equal(t, "deep", string(data))
	})

	t.Run("When source contains a symlink it should return an error", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(src, "real.txt"), []byte("real"), 0600))
		require.NoError(t, os.Symlink(filepath.Join(src, "real.txt"), filepath.Join(src, "link.txt")))

		_, err := copyDirSecure(context.Background(), src, dst, log)
		require.Error(t, err)
		require.ErrorContains(t, err, "non-regular")
	})

	t.Run("When context is cancelled it should return early", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("data"), 0600))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := copyDirSecure(ctx, src, dst, log)
		require.Error(t, err)
	})

	t.Run("When source is empty it should return zero count", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()

		count, err := copyDirSecure(context.Background(), src, dst, log)
		require.NoError(t, err)
		require.Equal(t, 0, count)
	})
}

// TestPodmanRestoreDeployer_RestorePKI validates the behavioral contracts
// of PodmanRestoreDeployer.RestorePKI.
func TestPodmanRestoreDeployer_RestorePKI(t *testing.T) {
	tests := []struct {
		name        string
		setupDir    func(t *testing.T) string
		wantErr     bool
		errContains string
		verify      func(t *testing.T, destDir string)
	}{
		{
			name: "When pki dir is absent from archive it should return error",
			setupDir: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr:     true,
			errContains: "pki",
		},
		{
			name: "When pki dir exists it should copy all files to destination",
			setupDir: func(t *testing.T) string {
				return buildExtractDirWithPKI(t, map[string]os.FileMode{
					"ca.crt":     0600,
					"server.key": 0600,
				})
			},
			verify: func(t *testing.T, destDir string) {
				require.FileExists(t, filepath.Join(destDir, "ca.crt"))
				require.FileExists(t, filepath.Join(destDir, "server.key"))
				data, err := os.ReadFile(filepath.Join(destDir, "ca.crt"))
				require.NoError(t, err)
				require.Equal(t, "mock-pki-content", string(data))
			},
		},
		{
			name: "When pki dir exists it should preserve file permissions",
			setupDir: func(t *testing.T) string {
				return buildExtractDirWithPKI(t, map[string]os.FileMode{
					"ca.crt": 0600,
				})
			},
			verify: func(t *testing.T, destDir string) {
				info, err := os.Stat(filepath.Join(destDir, "ca.crt"))
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0600), info.Mode().Perm())
			},
		},
		{
			name: "When pki dir contains a symlink it should return an error",
			setupDir: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				pkiDir := filepath.Join(dir, "pki")
				require.NoError(t, os.MkdirAll(pkiDir, 0700))
				// Create a real file and a symlink pointing at it.
				target := filepath.Join(pkiDir, "ca.crt")
				require.NoError(t, os.WriteFile(target, []byte("cert"), 0600))
				require.NoError(t, os.Symlink(target, filepath.Join(pkiDir, "ca-link.crt")))
				return dir
			},
			wantErr:     true,
			errContains: "non-regular",
		},
		{
			name: "When pki dir contains subdirectories it should create them and copy nested files",
			setupDir: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				subDir := filepath.Join(dir, "pki", "sub")
				require.NoError(t, os.MkdirAll(subDir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(subDir, "nested.crt"), []byte("nested-content"), 0600))
				return dir
			},
			verify: func(t *testing.T, destDir string) {
				data, err := os.ReadFile(filepath.Join(destDir, "sub", "nested.crt"))
				require.NoError(t, err)
				require.Equal(t, "nested-content", string(data))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, _ := test.NewNullLogger()
			destDir := t.TempDir()
			d := NewPodmanRestoreDeployer(log, WithPKIDestPath(destDir))
			err := d.RestorePKI(context.Background(), tt.setupDir(t))

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.ErrorContains(t, err, tt.errContains)
				}
				return
			}
			require.NoError(t, err)
			if tt.verify != nil {
				tt.verify(t, destDir)
			}
		})
	}
}

// buildExtractDirWithPKISecrets creates an extract directory with a pki/
// subdirectory containing one YAML file per Secret (serialised as JSON,
// which is valid YAML). Returns the extract directory and the fake clientset
// that can be used to verify post-restore cluster state.
func buildExtractDirWithPKISecrets(t *testing.T, secrets []*corev1.Secret) (string, kubernetes.Interface) {
	t.Helper()
	dir := t.TempDir()
	pkiDir := filepath.Join(dir, "pki")
	require.NoError(t, os.MkdirAll(pkiDir, 0700))
	for _, s := range secrets {
		data, err := json.Marshal(s)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, s.Name+".yaml"), data, 0600))
	}
	return dir, fake.NewSimpleClientset()
}

// TestKubernetesRestoreDeployer_RestorePKI validates the behavioral contracts
// of KubernetesRestoreDeployer.RestorePKI.
func TestKubernetesRestoreDeployer_RestorePKI(t *testing.T) {
	const ns = "flightctl"

	t.Run("When pki dir is absent from archive it should return error", func(t *testing.T) {
		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err := d.RestorePKI(context.Background(), t.TempDir())
		require.Error(t, err)
		require.ErrorContains(t, err, "pki")
	})

	t.Run("When pki dir exists with no yaml files it should return error", func(t *testing.T) {
		log, _ := test.NewNullLogger()
		extractDir := buildExtractDirWithPKI(t, map[string]os.FileMode{})
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err := d.RestorePKI(context.Background(), extractDir)
		require.Error(t, err)
		require.ErrorContains(t, err, "no Secret YAML files")
	})

	t.Run("When pki dir exists with yaml files it should create each Secret in the cluster", func(t *testing.T) {
		secrets := []*corev1.Secret{
			{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{Name: "flightctl-ca", Namespace: ns},
				Data:       map[string][]byte{"ca.crt": []byte("mock-cert")},
			},
			{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{Name: "flightctl-api-server-tls", Namespace: ns},
				Data:       map[string][]byte{"tls.crt": []byte("mock-tls")},
			},
		}
		extractDir, cs := buildExtractDirWithPKISecrets(t, secrets)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		require.NoError(t, d.RestorePKI(context.Background(), extractDir))

		for _, s := range secrets {
			got, err := cs.CoreV1().Secrets(ns).Get(context.Background(), s.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, s.Name, got.Name)
		}
	})

	t.Run("When yaml file contains invalid content it should return an error", func(t *testing.T) {
		dir := t.TempDir()
		pkiDir := filepath.Join(dir, "pki")
		require.NoError(t, os.MkdirAll(pkiDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, "bad.yaml"), []byte("{not: valid: yaml: ["), 0600))

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err := d.RestorePKI(context.Background(), dir)
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to apply PKI Secret")
	})

	t.Run("When secret yaml has no namespace it should use the deployer namespace", func(t *testing.T) {
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-ca"},
			Data:       map[string][]byte{"ca.crt": []byte("cert")},
		}
		extractDir, cs := buildExtractDirWithPKISecrets(t, []*corev1.Secret{secret})

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		require.NoError(t, d.RestorePKI(context.Background(), extractDir))

		got, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "flightctl-ca", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "flightctl-ca", got.Name)
	})

	t.Run("When yaml file contains an existing secret it should update the cluster secret", func(t *testing.T) {
		existing := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-ca", Namespace: ns},
			Data:       map[string][]byte{"ca.crt": []byte("old-cert")},
		}
		updated := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-ca", Namespace: ns},
			Data:       map[string][]byte{"ca.crt": []byte("new-cert")},
		}
		extractDir, cs := buildExtractDirWithPKISecrets(t, []*corev1.Secret{updated})
		_, err := cs.CoreV1().Secrets(ns).Create(context.Background(), existing, metav1.CreateOptions{})
		require.NoError(t, err)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		require.NoError(t, d.RestorePKI(context.Background(), extractDir))

		got, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "flightctl-ca", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("new-cert"), got.Data["ca.crt"])
	})
}

// buildExtractDirWithConfig creates an extract directory with a config/service-config.yaml
// containing the given YAML content.
func buildExtractDirWithConfig(t *testing.T, cfgContent string) string {
	t.Helper()
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(cfgDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "service-config.yaml"), []byte(cfgContent), 0600))
	return dir
}

// encodeHelmRelease encodes a helmReleaseJSON as base64(gzip(json)) — the format
// Helm 3 uses when storing release data in a Kubernetes Secret.
func encodeHelmRelease(t *testing.T, release helmReleaseJSON) []byte {
	t.Helper()
	releaseJSON, err := json.Marshal(release)
	require.NoError(t, err)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err = gw.Write(releaseJSON)
	require.NoError(t, err)
	require.NoError(t, gw.Close())

	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

// buildExtractDirWithHelmSecret creates an extract directory with a
// config/helm-release-<name>.yaml file containing a properly encoded Helm release Secret.
func buildExtractDirWithHelmSecret(t *testing.T, release helmReleaseJSON, ns string) (string, kubernetes.Interface) {
	t.Helper()
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(cfgDir, 0700))

	secret := corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1." + release.Name + ".v1",
			Namespace: ns,
			Labels:    map[string]string{"owner": "helm", "name": release.Name},
		},
		Data: map[string][]byte{"release": encodeHelmRelease(t, release)},
		Type: "helm.sh/release.v1",
	}
	secretYAML, err := yaml.Marshal(secret)
	require.NoError(t, err)
	filename := "helm-release-" + release.Name + ".yaml"
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, filename), secretYAML, 0600))

	return dir, fake.NewSimpleClientset()
}

// TestPodmanRestoreDeployer_RestoreConfig validates the behavioral contracts of
// PodmanRestoreDeployer.RestoreConfig.
func TestPodmanRestoreDeployer_RestoreConfig(t *testing.T) {
	const cfgContent = "database:\n  hostname: testhost\n"

	t.Run("When service-config.yaml is in the archive it should write it to the destination", func(t *testing.T) {
		extractDir := buildExtractDirWithConfig(t, cfgContent)
		destPath := filepath.Join(t.TempDir(), "service-config.yaml")

		log, _ := test.NewNullLogger()
		ctrl := gomock.NewController(t)
		d := NewPodmanRestoreDeployer(log,
			WithServiceHandler(NewMockServiceHandler(ctrl)),
			WithServiceConfigPath(destPath),
			WithContainerCLI("echo"),
		)
		require.NoError(t, d.RestoreConfig(context.Background(), extractDir))

		content, err := os.ReadFile(destPath)
		require.NoError(t, err)
		require.Contains(t, string(content), "testhost")
	})

	t.Run("When service-config.yaml is absent from the archive it should return an error", func(t *testing.T) {
		extractDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(extractDir, "config"), 0700))
		destPath := filepath.Join(t.TempDir(), "service-config.yaml")

		log, _ := test.NewNullLogger()
		ctrl := gomock.NewController(t)
		d := NewPodmanRestoreDeployer(log,
			WithServiceHandler(NewMockServiceHandler(ctrl)),
			WithServiceConfigPath(destPath),
		)
		err := d.RestoreConfig(context.Background(), extractDir)
		require.Error(t, err)
		require.ErrorContains(t, err, "service configuration not found in archive")
	})

	t.Run("When PAM Issuer volume archive is absent it should succeed", func(t *testing.T) {
		extractDir := buildExtractDirWithConfig(t, cfgContent)
		destPath := filepath.Join(t.TempDir(), "service-config.yaml")

		log, _ := test.NewNullLogger()
		ctrl := gomock.NewController(t)
		d := NewPodmanRestoreDeployer(log,
			WithServiceHandler(NewMockServiceHandler(ctrl)),
			WithServiceConfigPath(destPath),
		)
		require.NoError(t, d.RestoreConfig(context.Background(), extractDir))
	})

	t.Run("When PAM Issuer volume import fails it should succeed with a warning", func(t *testing.T) {
		extractDir := buildExtractDirWithConfig(t, cfgContent)
		volumesDir := filepath.Join(extractDir, "volumes")
		require.NoError(t, os.MkdirAll(volumesDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(volumesDir, "pam-issuer-etc.tar"), []byte("fake-tar"), 0600))
		destPath := filepath.Join(t.TempDir(), "service-config.yaml")

		log, _ := test.NewNullLogger()
		ctrl := gomock.NewController(t)
		d := NewPodmanRestoreDeployer(log,
			WithServiceHandler(NewMockServiceHandler(ctrl)),
			WithServiceConfigPath(destPath),
			WithContainerCLI("/nonexistent-cli-for-testing"),
		)
		require.NoError(t, d.RestoreConfig(context.Background(), extractDir))
	})

	t.Run("When PAM Issuer volume import succeeds it should report success", func(t *testing.T) {
		extractDir := buildExtractDirWithConfig(t, cfgContent)
		volumesDir := filepath.Join(extractDir, "volumes")
		require.NoError(t, os.MkdirAll(volumesDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(volumesDir, "pam-issuer-etc.tar"), []byte("fake-tar"), 0600))
		destPath := filepath.Join(t.TempDir(), "service-config.yaml")

		log, _ := test.NewNullLogger()
		ctrl := gomock.NewController(t)
		d := NewPodmanRestoreDeployer(log,
			WithServiceHandler(NewMockServiceHandler(ctrl)),
			WithServiceConfigPath(destPath),
			WithContainerCLI("echo"),
		)
		require.NoError(t, d.RestoreConfig(context.Background(), extractDir))
	})
}

// TestKubernetesRestoreDeployer_RestoreConfig validates the behavioral contracts of
// KubernetesRestoreDeployer.RestoreConfig.
func TestKubernetesRestoreDeployer_RestoreConfig(t *testing.T) {
	const ns = "flightctl"

	minimalRelease := helmReleaseJSON{
		Name:      "flightctl",
		Namespace: ns,
		Chart: &helmChartJSON{
			Metadata:  map[string]interface{}{"name": "flightctl", "version": "0.1.0"},
			Templates: []*helmFileJSON{{Name: "templates/deploy.yaml", Data: []byte("---")}},
		},
		Config: map[string]interface{}{"replicaCount": 2},
	}

	t.Run("When config dir is absent it should return an error", func(t *testing.T) {
		extractDir := t.TempDir()

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err := d.RestoreConfig(context.Background(), extractDir)
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to list config directory")
	})

	t.Run("When a Helm release template has a path-traversal name it should return an error", func(t *testing.T) {
		traversalRelease := helmReleaseJSON{
			Name:      "flightctl",
			Namespace: ns,
			Chart: &helmChartJSON{
				Metadata:  map[string]interface{}{"name": "flightctl", "version": "0.1.0"},
				Templates: []*helmFileJSON{{Name: "../../../etc/cron.d/evil", Data: []byte("evil")}},
			},
		}
		extractDir, cs := buildExtractDirWithHelmSecret(t, traversalRelease, ns)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
			WithHelmUpgradeFunc(func(_ context.Context, _, _, _, _ string) error { return nil }),
		)
		err := d.RestoreConfig(context.Background(), extractDir)
		require.Error(t, err)
		require.ErrorContains(t, err, "unsafe template path")
	})

	t.Run("When config dir is empty it should succeed without calling helm upgrade", func(t *testing.T) {
		extractDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(extractDir, "config"), 0700))

		upgradeCalled := false
		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
			WithHelmUpgradeFunc(func(_ context.Context, _, _, _, _ string) error {
				upgradeCalled = true
				return nil
			}),
		)
		require.NoError(t, d.RestoreConfig(context.Background(), extractDir))
		require.False(t, upgradeCalled)
	})

	t.Run("When a Helm release Secret is present it should apply the Secret and run helm upgrade", func(t *testing.T) {
		extractDir, cs := buildExtractDirWithHelmSecret(t, minimalRelease, ns)

		var capturedRelease, capturedChartDir, capturedNS, capturedValues string
		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
			WithHelmUpgradeFunc(func(_ context.Context, releaseName, chartDir, namespace, valuesFile string) error {
				capturedRelease = releaseName
				capturedChartDir = chartDir
				capturedNS = namespace
				capturedValues = valuesFile
				return nil
			}),
		)
		require.NoError(t, d.RestoreConfig(context.Background(), extractDir))

		require.Equal(t, "flightctl", capturedRelease)
		require.Equal(t, ns, capturedNS)
		require.NotEmpty(t, capturedChartDir)
		require.NotEmpty(t, capturedValues)

		// Verify the Secret was applied to the cluster.
		secrets, err := cs.CoreV1().Secrets(ns).List(context.Background(), metav1.ListOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, secrets.Items)
	})

	t.Run("When helm upgrade fails it should return an error", func(t *testing.T) {
		extractDir, cs := buildExtractDirWithHelmSecret(t, minimalRelease, ns)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
			WithHelmUpgradeFunc(func(_ context.Context, _, _, _, _ string) error {
				return fmt.Errorf("helm upgrade: connection refused")
			}),
		)
		err := d.RestoreConfig(context.Background(), extractDir)
		require.Error(t, err)
		require.ErrorContains(t, err, "helm upgrade")
	})

	t.Run("When Helm release chart and user values are present they should be written to the chart directory", func(t *testing.T) {
		extractDir, cs := buildExtractDirWithHelmSecret(t, minimalRelease, ns)

		// Inspect the chart dir and values file inside helmUpgradeFunc while they are
		// still on disk (deferred cleanup runs after helmUpgradeFunc returns).
		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
			WithHelmUpgradeFunc(func(_ context.Context, _, chartDir, _, valuesFile string) error {
				_, err := os.Stat(filepath.Join(chartDir, "Chart.yaml"))
				require.NoError(t, err)
				_, err = os.Stat(filepath.Join(chartDir, "templates", "deploy.yaml"))
				require.NoError(t, err)

				valuesContent, err := os.ReadFile(valuesFile)
				require.NoError(t, err)
				require.Contains(t, string(valuesContent), "replicaCount")
				return nil
			}),
		)
		require.NoError(t, d.RestoreConfig(context.Background(), extractDir))
	})
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
	apiConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flightctl-api-config",
			Namespace: "flightctl",
		},
		Data: map[string]string{
			"config.yaml": `database:
  hostname: flightctl-db
  name: flightctl
  port: 5432
  user: placeholder
  password: placeholder
kv:
  hostname: flightctl-kv
  port: 6379
  password: placeholder
`,
		},
	}

	log, _ := test.NewNullLogger()
	d := NewKubernetesRestoreDeployer(log,
		WithRestoreNamespace("flightctl"),
		WithRestoreClientset(fake.NewSimpleClientset(dbSecret, kvSecret, apiConfigMap)),
		WithRestoreRestConfig(&rest.Config{}),
	)

	cfg, err := d.GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "flightctl_app", cfg.Database.User)
	require.Equal(t, "dbsecret", string(cfg.Database.Password))
	require.Equal(t, "kvsecret", string(cfg.KV.Password))
	require.Equal(t, "flightctl-db", cfg.Database.Hostname)
	require.Equal(t, "flightctl", cfg.Database.Name)
	require.Equal(t, uint(dbInternalPort), cfg.Database.Port)
	require.Equal(t, "flightctl-kv", cfg.KV.Hostname)
	require.Equal(t, uint(kvInternalPort), cfg.KV.Port)
}

// buildExtractDirWithEncryption creates an extract directory containing an encryption/
// subdirectory with the given files (filename → content). Returns the extract directory path.
func buildExtractDirWithEncryption(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	encDir := filepath.Join(dir, "encryption")
	require.NoError(t, os.MkdirAll(encDir, 0700))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(encDir, name), []byte(content), 0600))
	}
	return dir
}

// buildExtractDirWithEncryptionSecret creates an extract directory with an encryption/
// subdirectory containing a Secret YAML file. Returns the extract directory and a fresh
// fake clientset for verifying post-restore cluster state.
func buildExtractDirWithEncryptionSecret(t *testing.T, secret *corev1.Secret) (string, kubernetes.Interface) {
	t.Helper()
	dir := t.TempDir()
	encDir := filepath.Join(dir, "encryption")
	require.NoError(t, os.MkdirAll(encDir, 0700))
	data, err := json.Marshal(secret)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(encDir, secret.Name+".yaml"), data, 0600))
	return dir, fake.NewSimpleClientset()
}

// TestPodmanRestoreDeployer_RestoreEncryptionKeys validates the behavioral contracts
// of PodmanRestoreDeployer.RestoreEncryptionKeys.
func TestPodmanRestoreDeployer_RestoreEncryptionKeys(t *testing.T) {
	tests := []struct {
		name        string
		setupDir    func(t *testing.T) string
		setupDest   func(t *testing.T, destDir string)
		wantErr     bool
		errContains string
		verify      func(t *testing.T, destDir string)
	}{
		{
			name: "When encryption dir is absent from archive it should skip silently",
			setupDir: func(t *testing.T) string {
				return t.TempDir()
			},
			verify: func(t *testing.T, destDir string) {
				entries, err := os.ReadDir(destDir)
				require.NoError(t, err)
				require.Empty(t, entries, "destination should remain empty")
			},
		},
		{
			name: "When encryption dir exists it should copy all files to destination",
			setupDir: func(t *testing.T) string {
				return buildExtractDirWithEncryption(t, map[string]string{
					"encryption.key": "secret-key-data",
				})
			},
			verify: func(t *testing.T, destDir string) {
				require.FileExists(t, filepath.Join(destDir, "encryption.key"))
				data, err := os.ReadFile(filepath.Join(destDir, "encryption.key"))
				require.NoError(t, err)
				require.Equal(t, "secret-key-data", string(data))
			},
		},
		{
			name: "When encryption dir exists it should preserve file permissions",
			setupDir: func(t *testing.T) string {
				return buildExtractDirWithEncryption(t, map[string]string{
					"encryption.key": "key-data",
				})
			},
			verify: func(t *testing.T, destDir string) {
				info, err := os.Stat(filepath.Join(destDir, "encryption.key"))
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0600), info.Mode().Perm())
			},
		},
		{
			name: "When encryption dir contains a symlink it should return an error and leave destination unchanged",
			setupDir: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				encDir := filepath.Join(dir, "encryption")
				require.NoError(t, os.MkdirAll(encDir, 0700))
				target := filepath.Join(encDir, "encryption.key")
				require.NoError(t, os.WriteFile(target, []byte("key"), 0600))
				require.NoError(t, os.Symlink(target, filepath.Join(encDir, "key-link")))
				return dir
			},
			wantErr:     true,
			errContains: "non-regular",
			verify: func(t *testing.T, destDir string) {
				entries, err := os.ReadDir(destDir)
				require.NoError(t, err)
				require.Empty(t, entries, "destination should remain unchanged after failed restore")
			},
		},
		{
			name: "When encryption dir contains a symlink it should preserve existing destination files",
			setupDir: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				encDir := filepath.Join(dir, "encryption")
				require.NoError(t, os.MkdirAll(encDir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(encDir, "good.key"), []byte("new-key"), 0600))
				require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(encDir, "evil-link")))
				return dir
			},
			setupDest: func(t *testing.T, destDir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(destDir, "existing.key"), []byte("original-key"), 0600))
			},
			wantErr:     true,
			errContains: "non-regular",
			verify: func(t *testing.T, destDir string) {
				data, err := os.ReadFile(filepath.Join(destDir, "existing.key"))
				require.NoError(t, err, "pre-existing file should survive failed restore")
				require.Equal(t, "original-key", string(data))
			},
		},
		{
			name: "When encryption dir is empty it should return an error and leave destination unchanged",
			setupDir: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "encryption"), 0700))
				return dir
			},
			setupDest: func(t *testing.T, destDir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(destDir, "existing.key"), []byte("live-key"), 0600))
			},
			wantErr:     true,
			errContains: "empty",
			verify: func(t *testing.T, destDir string) {
				data, err := os.ReadFile(filepath.Join(destDir, "existing.key"))
				require.NoError(t, err, "pre-existing key should survive failed restore of empty archive")
				require.Equal(t, "live-key", string(data))
			},
		},
		{
			name: "When existing destination has files it should replace them and leave no backup behind",
			setupDir: func(t *testing.T) string {
				return buildExtractDirWithEncryption(t, map[string]string{
					"new.key": "new-key-data",
				})
			},
			setupDest: func(t *testing.T, destDir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(destDir, "old.key"), []byte("old-key-data"), 0600))
			},
			verify: func(t *testing.T, destDir string) {
				data, err := os.ReadFile(filepath.Join(destDir, "new.key"))
				require.NoError(t, err)
				require.Equal(t, "new-key-data", string(data))
				require.NoFileExists(t, filepath.Join(destDir, "old.key"), "old files should not survive the swap")
				_, err = os.Stat(destDir + ".bak")
				require.True(t, os.IsNotExist(err), ".bak directory should be cleaned up after successful swap")
			},
		},
		{
			name: "When encryption dir contains subdirectories it should create them and copy nested files",
			setupDir: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				subDir := filepath.Join(dir, "encryption", "sub")
				require.NoError(t, os.MkdirAll(subDir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(subDir, "nested.key"), []byte("nested-key"), 0600))
				return dir
			},
			verify: func(t *testing.T, destDir string) {
				data, err := os.ReadFile(filepath.Join(destDir, "sub", "nested.key"))
				require.NoError(t, err)
				require.Equal(t, "nested-key", string(data))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, _ := test.NewNullLogger()
			destDir := t.TempDir()
			if tt.setupDest != nil {
				tt.setupDest(t, destDir)
			}
			d := NewPodmanRestoreDeployer(log, WithEncryptionDestPath(destDir))
			err := d.RestoreEncryptionKeys(context.Background(), tt.setupDir(t))

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.ErrorContains(t, err, tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
			if tt.verify != nil {
				tt.verify(t, destDir)
			}
		})
	}
}

// TestKubernetesRestoreDeployer_RestoreEncryptionKeys validates the behavioral contracts
// of KubernetesRestoreDeployer.RestoreEncryptionKeys.
func TestKubernetesRestoreDeployer_RestoreEncryptionKeys(t *testing.T) {
	const ns = "flightctl"

	t.Run("When encryption dir is absent from archive it should skip silently", func(t *testing.T) {
		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err := d.RestoreEncryptionKeys(context.Background(), t.TempDir())
		require.NoError(t, err)
	})

	t.Run("When encryption key yaml exists it should create the Secret in the cluster", func(t *testing.T) {
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-encryption-key", Namespace: ns},
			Data:       map[string][]byte{"key": []byte("supersecret")},
		}
		extractDir, cs := buildExtractDirWithEncryptionSecret(t, secret)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		require.NoError(t, d.RestoreEncryptionKeys(context.Background(), extractDir))

		got, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "flightctl-encryption-key", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("supersecret"), got.Data["key"])
	})

	t.Run("When internal namespace differs it should duplicate the Secret", func(t *testing.T) {
		const internalNS = "flightctl-internal"
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-encryption-key", Namespace: ns},
			Data:       map[string][]byte{"key": []byte("dualns")},
		}
		extractDir, cs := buildExtractDirWithEncryptionSecret(t, secret)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreInternalNamespace(internalNS),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		require.NoError(t, d.RestoreEncryptionKeys(context.Background(), extractDir))

		gotRelease, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "flightctl-encryption-key", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("dualns"), gotRelease.Data["key"])

		gotInternal, err := cs.CoreV1().Secrets(internalNS).Get(context.Background(), "flightctl-encryption-key", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("dualns"), gotInternal.Data["key"])
	})

	t.Run("When internal namespace apply fails it should return an error after release namespace succeeds", func(t *testing.T) {
		const internalNS = "flightctl-internal"
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-encryption-key", Namespace: ns},
			Data:       map[string][]byte{"key": []byte("partial-fail")},
		}

		dir := t.TempDir()
		encDir := filepath.Join(dir, "encryption")
		require.NoError(t, os.MkdirAll(encDir, 0700))
		data, err := json.Marshal(secret)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(encDir, "flightctl-encryption-key.yaml"), data, 0600))

		cs := fake.NewSimpleClientset()
		cs.PrependReactor("create", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetNamespace() == internalNS {
				return true, nil, fmt.Errorf("simulated internal namespace failure")
			}
			return false, nil, nil
		})
		cs.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetNamespace() == internalNS {
				return true, nil, fmt.Errorf("simulated internal namespace failure")
			}
			return false, nil, nil
		})

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreInternalNamespace(internalNS),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err = d.RestoreEncryptionKeys(context.Background(), dir)
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to apply encryption key Secret to internal namespace")

		gotRelease, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "flightctl-encryption-key", metav1.GetOptions{})
		require.NoError(t, err, "Release namespace Secret should have been created before the failure")
		require.Equal(t, []byte("partial-fail"), gotRelease.Data["key"])
	})

	t.Run("When internal namespace equals release namespace it should not duplicate", func(t *testing.T) {
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-encryption-key", Namespace: ns},
			Data:       map[string][]byte{"key": []byte("singlelns")},
		}
		extractDir, cs := buildExtractDirWithEncryptionSecret(t, secret)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreInternalNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		require.NoError(t, d.RestoreEncryptionKeys(context.Background(), extractDir))

		got, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "flightctl-encryption-key", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("singlelns"), got.Data["key"])
	})

	t.Run("When encryption yaml has no namespace it should use the deployer namespace", func(t *testing.T) {
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-encryption-key"},
			Data:       map[string][]byte{"key": []byte("no-ns-key")},
		}
		extractDir, cs := buildExtractDirWithEncryptionSecret(t, secret)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		require.NoError(t, d.RestoreEncryptionKeys(context.Background(), extractDir))

		got, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "flightctl-encryption-key", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "flightctl-encryption-key", got.Name)
	})

	t.Run("When encryption yaml contains invalid content it should return an error", func(t *testing.T) {
		dir := t.TempDir()
		encDir := filepath.Join(dir, "encryption")
		require.NoError(t, os.MkdirAll(encDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(encDir, "flightctl-encryption-key.yaml"), []byte("{not: valid: yaml: ["), 0600))

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err := d.RestoreEncryptionKeys(context.Background(), dir)
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to unmarshal encryption key Secret")
	})

	t.Run("When encryption yaml has unexpected secret name it should return an error", func(t *testing.T) {
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "wrong-secret-name", Namespace: ns},
			Data:       map[string][]byte{"key": []byte("some-key")},
		}
		dir := t.TempDir()
		encDir := filepath.Join(dir, "encryption")
		require.NoError(t, os.MkdirAll(encDir, 0700))
		data, err := yaml.Marshal(secret)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(encDir, "flightctl-encryption-key.yaml"), data, 0600))

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err = d.RestoreEncryptionKeys(context.Background(), dir)
		require.Error(t, err)
		require.ErrorContains(t, err, "unexpected Secret name")
	})

	t.Run("When encryption yaml has no data it should return an error", func(t *testing.T) {
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-encryption-key", Namespace: ns},
		}
		dir := t.TempDir()
		encDir := filepath.Join(dir, "encryption")
		require.NoError(t, os.MkdirAll(encDir, 0700))
		data, err := yaml.Marshal(secret)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(encDir, "flightctl-encryption-key.yaml"), data, 0600))

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(fake.NewSimpleClientset()),
			WithRestoreRestConfig(&rest.Config{}),
		)
		err = d.RestoreEncryptionKeys(context.Background(), dir)
		require.Error(t, err)
		require.ErrorContains(t, err, "has no data")
	})

	t.Run("When encryption yaml has a different namespace it should apply to the deployer namespace", func(t *testing.T) {
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-encryption-key", Namespace: "other-namespace"},
			Data:       map[string][]byte{"key": []byte("forced-ns-key")},
		}
		extractDir, cs := buildExtractDirWithEncryptionSecret(t, secret)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		require.NoError(t, d.RestoreEncryptionKeys(context.Background(), extractDir))

		got, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "flightctl-encryption-key", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("forced-ns-key"), got.Data["key"])

		_, err = cs.CoreV1().Secrets("other-namespace").Get(context.Background(), "flightctl-encryption-key", metav1.GetOptions{})
		require.Error(t, err, "Secret should NOT be created in the archived namespace")
	})

	t.Run("When existing secret exists it should update it", func(t *testing.T) {
		existing := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-encryption-key", Namespace: ns},
			Data:       map[string][]byte{"key": []byte("old-key")},
		}
		updated := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: "flightctl-encryption-key", Namespace: ns},
			Data:       map[string][]byte{"key": []byte("new-key")},
		}
		extractDir, cs := buildExtractDirWithEncryptionSecret(t, updated)
		_, err := cs.CoreV1().Secrets(ns).Create(context.Background(), existing, metav1.CreateOptions{})
		require.NoError(t, err)

		log, _ := test.NewNullLogger()
		d := NewKubernetesRestoreDeployer(log,
			WithRestoreNamespace(ns),
			WithRestoreClientset(cs),
			WithRestoreRestConfig(&rest.Config{}),
		)
		require.NoError(t, d.RestoreEncryptionKeys(context.Background(), extractDir))

		got, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "flightctl-encryption-key", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("new-key"), got.Data["key"])
	})
}
