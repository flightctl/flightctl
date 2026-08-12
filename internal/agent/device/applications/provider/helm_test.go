package provider

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

var testBackoffConfig = poll.Config{
	BaseDelay: 10 * time.Millisecond,
	Factor:    2.0,
	MaxDelay:  100 * time.Millisecond,
	MaxSteps:  1,
}

func TestHelmEnsureDependencies(t *testing.T) {
	tests := []struct {
		name           string
		appName        string
		commandChecker commandChecker
		setupMocks     func(*executer.MockExecuter)
		kubeBinary     string
		wantErr        error
	}{
		{
			name:           "missing helm binary returns ErrAppDependency",
			appName:        "app",
			commandChecker: func(cmd string) bool { return false },
			kubeBinary:     "kubectl",
			wantErr:        errors.ErrAppDependency,
		},
		{
			name:           "missing kubectl/oc binary returns ErrAppDependency",
			appName:        "app",
			commandChecker: func(cmd string) bool { return cmd == "helm" || cmd == "crictl" },
			kubeBinary:     "kubectl",
			wantErr:        errors.ErrAppDependency,
		},
		{
			name:           "missing crictl binary returns ErrAppDependency",
			appName:        "app",
			commandChecker: func(cmd string) bool { return cmd == "helm" || cmd == "kubectl" },
			kubeBinary:     "kubectl",
			wantErr:        errors.ErrAppDependency,
		},
		{
			name:           "helm version below minimum",
			appName:        "app",
			commandChecker: func(cmd string) bool { return cmd == "helm" || cmd == "kubectl" || cmd == "crictl" },
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{"version", "--short"}).
					Return("v3.7.0+gc4e7498\n", "", 0)
			},
			kubeBinary: "kubectl",
			wantErr:    errors.ErrAppDependency,
		},
		{
			name:           "helm and kubectl available with valid version",
			appName:        "app",
			commandChecker: func(cmd string) bool { return cmd == "helm" || cmd == "kubectl" || cmd == "crictl" },
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{"version", "--short"}).
					Return("v3.8.0+gc4e7498\n", "", 0)
			},
			kubeBinary: "kubectl",
			wantErr:    nil,
		},
		{
			name:           "helm and oc available with valid version",
			appName:        "app",
			commandChecker: func(cmd string) bool { return cmd == "helm" || cmd == "oc" || cmd == "crictl" },
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{"version", "--short"}).
					Return("v3.15.0+gc4e7498\n", "", 0)
			},
			kubeBinary: "oc",
			wantErr:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := log.NewPrefixLogger("test")

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)

			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)

			if tt.setupMocks != nil {
				tt.setupMocks(mockExec)
			}

			helmClient := client.NewHelm(log, mockExec, rw, tmpDir, testBackoffConfig)
			kubeClient := client.NewKube(log, mockExec, rw, client.WithBinary(tt.kubeBinary))
			cliClients := client.NewCLIClients(
				client.WithHelmClient(helmClient),
				client.WithKubeClient(kubeClient),
			)

			helmApp := v1beta1.HelmApplication{
				AppType: v1beta1.AppTypeHelm,
				Name:    lo.ToPtr(tt.appName),
			}
			err := helmApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "oci://registry.example.com/charts/myapp:1.0.0"})
			require.NoError(err)

			var appSpec v1beta1.ApplicationProviderSpec
			err = appSpec.FromHelmApplication(helmApp)
			require.NoError(err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var rwFactory fileio.ReadWriterFactory = func(user v1beta1.Username) (fileio.ReadWriter, error) {
				return rw, nil
			}

			helmProvider, err := newHelmProvider(ctx, log, cliClients, &appSpec, rwFactory)
			require.NoError(err)

			if tt.commandChecker != nil {
				helmProvider.commandChecker = tt.commandChecker
			}

			err = helmProvider.EnsureDependencies(ctx)
			if tt.wantErr != nil {
				require.Error(err)
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
		})
	}
}

// TestHelmVerify_WhenRenderedLifecycleFieldsPresent_ItShouldNotRejectAsReadOnly
// guards the QE regression where stop overlays desiredState into RenderedApplications
// and Helm Verify reused API apply validation, rejecting the update before stop ran.
func TestHelmVerify_WhenRenderedLifecycleFieldsPresent_ItShouldNotRejectAsReadOnly(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)

	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{"version", "--short"}).
		Return("v3.15.0+gc4e7498\n", "", 0)

	helmClient := client.NewHelm(log, mockExec, rw, tmpDir, testBackoffConfig)
	kubeClient := client.NewKube(log, mockExec, rw, client.WithBinary("kubectl"))
	cliClients := client.NewCLIClients(
		client.WithHelmClient(helmClient),
		client.WithKubeClient(kubeClient),
	)

	helmApp := v1beta1.HelmApplication{
		AppType:           v1beta1.AppTypeHelm,
		Name:              lo.ToPtr("helm-demo"),
		DesiredState:      lo.ToPtr(v1beta1.ApplicationDesiredStateStopped),
		RestartGeneration: lo.ToPtr(1),
	}
	err := helmApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{
		Image: "registry.example.com/charts/myapp:1.0.0",
	})
	require.NoError(err)

	var appSpec v1beta1.ApplicationProviderSpec
	require.NoError(appSpec.FromHelmApplication(helmApp))

	// API apply validation still rejects these fields — that guard must remain.
	errs := v1beta1.ValidateHelmApplication(appSpec, "helm-demo", false)
	require.NotEmpty(errs)
	require.Contains(errs[0].Error(), "desiredState is read-only")
	require.Empty(v1beta1.ValidateHelmApplicationForAgent(appSpec, "helm-demo", false))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var rwFactory fileio.ReadWriterFactory = func(user v1beta1.Username) (fileio.ReadWriter, error) {
		return rw, nil
	}

	helmProvider, err := newHelmProvider(ctx, log, cliClients, &appSpec, rwFactory)
	require.NoError(err)
	helmProvider.commandChecker = func(cmd string) bool {
		return cmd == "helm" || cmd == "kubectl" || cmd == "crictl"
	}

	// Chart is intentionally unresolved so Verify fails after validation. Without
	// ForAgent validation, Verify would fail earlier with the read-only desiredState error.
	err = helmProvider.Verify(ctx)
	require.Error(err)
	require.NotContains(err.Error(), "desiredState is read-only")
	require.NotContains(err.Error(), "restartGeneration is read-only")
	require.Contains(err.Error(), "not resolved")

	require.Equal(v1beta1.ApplicationDesiredStateStopped, helmProvider.Spec().DesiredState)
	require.Equal(1, helmProvider.Spec().RestartGeneration)
}
