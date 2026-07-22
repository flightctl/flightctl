package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fmt"

	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// dbAppSecret creates a fake flightctl-db-app-secret for use in tests.
func dbAppSecret(ns, user, password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dbAppSecretName,
			Namespace: ns,
		},
		Data: map[string][]byte{
			dbUserKey:     []byte(user),
			dbPasswordKey: []byte(password),
		},
	}
}

func TestKubernetesDeployer_BackupDatabase_ExternalDB(t *testing.T) {
	log, _ := test.NewNullLogger()

	// No flightctl-db-app-secret in the fake cluster → external DB
	fakeClient := fake.NewSimpleClientset()
	deployer := NewKubernetesDeployer(log,
		WithInternalNamespace("flightctl-internal"),
		WithClientset(fakeClient),
	)
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupDatabase(ctx, outputDir)

	require.ErrorIs(t, err, ErrExternalDatabase)

	dbDir := filepath.Join(outputDir, "db")
	_, err = os.Stat(dbDir)
	require.True(t, os.IsNotExist(err), "db directory should not be created for external DB")
}

func TestKubernetesDeployer_BackupDatabase_ExternalDB_WithSecret(t *testing.T) {
	log, _ := test.NewNullLogger()

	// Secret exists but no Deployment → external DB (detected before creating directories)
	secret := dbAppSecret("flightctl-internal", "flightctl_app", "secret")

	fakeClient := fake.NewSimpleClientset(secret)
	deployer := NewKubernetesDeployer(log,
		WithInternalNamespace("flightctl-internal"),
		WithClientset(fakeClient),
	)
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupDatabase(ctx, outputDir)

	require.ErrorIs(t, err, ErrExternalDatabase, "should return ErrExternalDatabase when no Deployment exists")

	// No directory should be created - external DB detected early
	dbDir := filepath.Join(outputDir, "db")
	_, err = os.Stat(dbDir)
	require.True(t, os.IsNotExist(err), "db directory should not be created for external DB")
}

func TestKubernetesDeployer_BackupDatabase_BuiltinDB_PodUnavailable(t *testing.T) {
	log, _ := test.NewNullLogger()

	// Helm-managed Deployment exists but no DB pod → builtin DB failure (pod crashed)
	secret := dbAppSecret("flightctl-internal", "flightctl_app", "secret")

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flightctl-db",
			Namespace: "flightctl-internal",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "Helm", // Helm-managed builtin DB
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(secret, deployment)
	deployer := NewKubernetesDeployer(log,
		WithInternalNamespace("flightctl-internal"),
		WithClientset(fakeClient),
	)
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupDatabase(ctx, outputDir)

	require.Error(t, err, "should return hard error when builtin DB pod is unavailable")
	require.NotErrorIs(t, err, ErrExternalDatabase, "should NOT return ErrExternalDatabase for unavailable builtin DB")
	require.Contains(t, err.Error(), "database pod not found", "error should indicate pod not found")
	require.Contains(t, err.Error(), "builtin database configured", "error should indicate builtin DB")
	require.Contains(t, err.Error(), "incomplete backup would result", "error should warn about incomplete backup")
}

func TestKubernetesDeployer_BackupDatabase_InternalDB_DirectoryCreation(t *testing.T) {
	log, _ := test.NewNullLogger()

	// Secret and Helm-managed Deployment present → builtin DB; DB pod absent → error before directory creation
	secret := dbAppSecret("flightctl-internal", "flightctl_app", "secret")
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flightctl-db",
			Namespace: "flightctl-internal",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "Helm",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(secret, deployment)
	deployer := NewKubernetesDeployer(log,
		WithInternalNamespace("flightctl-internal"),
		WithClientset(fakeClient),
	)
	ctx := context.Background()
	outputDir := t.TempDir()

	// Error is expected (no DB pod); test validates directory creation happens before pod check
	err := deployer.BackupDatabase(ctx, outputDir)
	require.Error(t, err, "should return error when builtin DB pod is unavailable")

	// Directory should be created - builtin DB detected, directory created, then pod check fails
	dbDir := filepath.Join(outputDir, "db")
	stat, statErr := os.Stat(dbDir)
	require.NoError(t, statErr, "db directory should be created for builtin DB before pod check")
	require.True(t, stat.IsDir(), "db should be a directory")
}

func TestKubernetesDeployer_BackupDatabase_CredentialExtraction(t *testing.T) {
	tests := []struct {
		name      string
		user      string
		namespace string
	}{
		{
			name:      "When Helm-managed Deployment exists with credentials it should proceed past credential extraction",
			user:      "flightctl_app",
			namespace: "flightctl-internal",
		},
		{
			name:      "When Helm-managed Deployment exists with custom user it should proceed past credential extraction",
			user:      "custom_user",
			namespace: "flightctl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, _ := test.NewNullLogger()
			secret := dbAppSecret(tt.namespace, tt.user, "testpass")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "flightctl-db",
					Namespace: tt.namespace,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "Helm",
					},
				},
			}

			fakeClient := fake.NewSimpleClientset(secret, deployment)
			deployer := NewKubernetesDeployer(log,
				WithInternalNamespace(tt.namespace),
				WithClientset(fakeClient),
			)
			ctx := context.Background()
			outputDir := t.TempDir()

			err := deployer.BackupDatabase(ctx, outputDir)

			// dbDir should be created - builtin DB detected, credentials extracted, directory created
			dbDir := filepath.Join(outputDir, "db")
			stat, statErr := os.Stat(dbDir)
			require.NoError(t, statErr, "db directory should be created")
			require.True(t, stat.IsDir())

			// Error expected since no DB pod exists (but it should NOT be ErrExternalDatabase)
			require.Error(t, err, "should return error when DB pod is not found")
			require.NotErrorIs(t, err, ErrExternalDatabase, "should NOT return ErrExternalDatabase for builtin DB")
		})
	}
}

func TestKubernetesDeployer_BackupPKI_NoDirectoryOnValidationFailure(t *testing.T) {
	log, _ := test.NewNullLogger()

	fakeClient := fake.NewSimpleClientset()
	deployer := NewKubernetesDeployer(log, WithNamespace("nonexistent-namespace"), WithClientset(fakeClient))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupPKI(ctx, outputDir)

	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to verify PKI secrets exist")

	pkiDir := filepath.Join(outputDir, "pki")
	_, statErr := os.Stat(pkiDir)
	require.True(t, os.IsNotExist(statErr), "PKI directory should not be created when validation fails")
}

func TestKubernetesDeployer_BackupConfig_NoHelmSecrets(t *testing.T) {
	log, _ := test.NewNullLogger()

	fakeClient := fake.NewSimpleClientset()
	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupConfig(ctx, outputDir)

	require.Error(t, err)
	require.Contains(t, err.Error(), "no deployed Helm release found")
}

func TestKubernetesDeployer_BackupConfig_DirectoryPermissions(t *testing.T) {
	log, _ := test.NewNullLogger()

	fakeClient := fake.NewSimpleClientset()
	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient))
	ctx := context.Background()
	outputDir := t.TempDir()

	// Error is expected (no Helm secrets); test validates directory creation despite failure
	_ = deployer.BackupConfig(ctx, outputDir)

	configDir := filepath.Join(outputDir, "config")
	stat, err := os.Stat(configDir)
	require.NoError(t, err, "config directory should be created")
	require.True(t, stat.IsDir(), "config should be a directory")
	require.Equal(t, os.FileMode(0700), stat.Mode().Perm(), "config directory should have 0700 permissions")
}

func TestKubernetesDeployer_BackupConfig_Success(t *testing.T) {
	log, _ := test.NewNullLogger()

	helmSecret1 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v1",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "1",
				"status":  "superseded",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("base64+gzip+json encoded release data"),
		},
	}

	helmSecret2 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v3",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "3",
				"status":  "deployed",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("base64+gzip+json with chart+config+manifest"),
		},
	}

	fakeClient := fake.NewSimpleClientset(helmSecret1, helmSecret2)
	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outputDir := t.TempDir()

	err := deployer.BackupConfig(ctx, outputDir)
	require.NoError(t, err)

	helmSecretPath := filepath.Join(outputDir, "config", "helm-release-flightctl.yaml")
	require.FileExists(t, helmSecretPath)

	stat, err := os.Stat(helmSecretPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), stat.Mode().Perm(), "Helm Secret YAML should have 0600 permissions")

	content, err := os.ReadFile(helmSecretPath)
	require.NoError(t, err)
	require.Contains(t, string(content), "sh.helm.release.v1.flightctl.v3")
	require.Contains(t, string(content), "helm.sh/release.v1")
}

func TestKubernetesDeployer_BackupConfig_DeployedRevision(t *testing.T) {
	log, _ := test.NewNullLogger()

	helmSecret1 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v1",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "1",
				"status":  "superseded",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{"release": []byte("old revision")},
	}

	helmSecret5 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v5",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "5",
				"status":  "deployed",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{"release": []byte("deployed revision")},
	}

	helmSecret3 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v3",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "3",
				"status":  "superseded",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{"release": []byte("middle revision")},
	}

	fakeClient := fake.NewSimpleClientset(helmSecret1, helmSecret5, helmSecret3)
	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupConfig(ctx, outputDir)
	require.NoError(t, err)

	deployedPath := filepath.Join(outputDir, "config", "helm-release-flightctl.yaml")
	require.FileExists(t, deployedPath)

	content, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Contains(t, string(content), "sh.helm.release.v1.flightctl.v5")
	require.Contains(t, string(content), "status: deployed")
}

func TestKubernetesDeployer_BackupConfig_LabelFiltering(t *testing.T) {
	log, _ := test.NewNullLogger()

	flightctlSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v2",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "2",
				"status":  "deployed",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{"release": []byte("flightctl release data")},
	}

	otherSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.other-app.v1",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "other-app",
				"version": "1",
				"status":  "deployed",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{"release": []byte("other app release data")},
	}

	fakeClient := fake.NewSimpleClientset(flightctlSecret, otherSecret)
	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient), WithHelmReleaseName("flightctl"))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupConfig(ctx, outputDir)
	require.NoError(t, err)

	flightctlPath := filepath.Join(outputDir, "config", "helm-release-flightctl.yaml")
	require.FileExists(t, flightctlPath)

	otherPath := filepath.Join(outputDir, "config", "helm-release-other-app.yaml")
	require.NoFileExists(t, otherPath)
}

func TestKubernetesDeployer_BackupEncryptionKeys_MissingSecret(t *testing.T) {
	log, _ := test.NewNullLogger()

	fakeClient := fake.NewSimpleClientset()
	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupEncryptionKeys(ctx, outputDir)

	require.NoError(t, err, "missing encryption key should not be an error")

	encDir := filepath.Join(outputDir, "encryption")
	_, statErr := os.Stat(encDir)
	require.True(t, os.IsNotExist(statErr), "encryption directory should not be created when secret is missing")
}

func TestKubernetesDeployer_BackupEncryptionKeys_Success(t *testing.T) {
	log, _ := test.NewNullLogger()

	encSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      encryptionKeySecretName,
			Namespace: "flightctl",
		},
		Data: map[string][]byte{
			"key": []byte("supersecretkey"),
		},
	}

	fakeClient := fake.NewSimpleClientset(encSecret)
	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupEncryptionKeys(ctx, outputDir)
	require.NoError(t, err)

	yamlPath := filepath.Join(outputDir, "encryption", encryptionKeySecretName+".yaml")
	require.FileExists(t, yamlPath)

	stat, err := os.Stat(yamlPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), stat.Mode().Perm(), "encryption key YAML should have 0600 permissions")

	content, err := os.ReadFile(yamlPath)
	require.NoError(t, err)
	require.Contains(t, string(content), encryptionKeySecretName)
}

func TestKubernetesDeployer_BackupEncryptionKeys_DirectoryPermissions(t *testing.T) {
	log, _ := test.NewNullLogger()

	encSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      encryptionKeySecretName,
			Namespace: "flightctl",
		},
		Data: map[string][]byte{
			"key": []byte("key"),
		},
	}

	fakeClient := fake.NewSimpleClientset(encSecret)
	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient))
	ctx := context.Background()
	outputDir := t.TempDir()

	require.NoError(t, deployer.BackupEncryptionKeys(ctx, outputDir))

	encDir := filepath.Join(outputDir, "encryption")
	stat, err := os.Stat(encDir)
	require.NoError(t, err)
	require.True(t, stat.IsDir())
	require.Equal(t, os.FileMode(0700), stat.Mode().Perm(), "encryption directory should have 0700 permissions")
}

func TestKubernetesDeployer_BackupEncryptionKeys_CleanupOnError(t *testing.T) {
	log, _ := test.NewNullLogger()

	encSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      encryptionKeySecretName,
			Namespace: "flightctl",
		},
		Data: map[string][]byte{
			"key": []byte("key"),
		},
	}

	fakeClient := fake.NewSimpleClientset(encSecret)
	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient))
	ctx := context.Background()

	outputDir := t.TempDir()
	encDir := filepath.Join(outputDir, "encryption")
	require.NoError(t, os.MkdirAll(encDir, 0700))
	yamlPath := filepath.Join(encDir, encryptionKeySecretName+".yaml")
	require.NoError(t, os.MkdirAll(yamlPath, 0700))

	err := deployer.BackupEncryptionKeys(ctx, outputDir)
	require.Error(t, err, "should fail when YAML path is a directory")

	_, statErr := os.Stat(encDir)
	require.True(t, os.IsNotExist(statErr), "encryption directory should be cleaned up on error")
}

func TestKubernetesDeployer_BackupEncryptionKeys_APIErrorReturned(t *testing.T) {
	log, _ := test.NewNullLogger()

	fakeClient := fake.NewSimpleClientset()
	fakeClient.PrependReactor("get", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("API server unavailable")
	})

	deployer := NewKubernetesDeployer(log, WithNamespace("flightctl"), WithClientset(fakeClient))
	outputDir := t.TempDir()

	err := deployer.BackupEncryptionKeys(context.Background(), outputDir)
	require.Error(t, err, "non-NotFound API errors must be returned, not swallowed")
	require.ErrorContains(t, err, "API server unavailable")

	encDir := filepath.Join(outputDir, "encryption")
	_, statErr := os.Stat(encDir)
	require.True(t, os.IsNotExist(statErr), "encryption directory should not be created on API error")
}
