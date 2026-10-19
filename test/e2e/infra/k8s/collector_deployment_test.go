package k8s

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/stretchr/testify/require"
)

// fakeCollectorRuntime is a collectorRuntime that records every cluster
// operation it was asked to perform, in order, and fails whichever ones a test
// wants to fail. Tests assert on the recorded sequence, so an operation the
// code under test should not have performed shows up as an extra entry rather
// than passing unnoticed.
type fakeCollectorRuntime struct {
	// present is the set of images the host runtime reports.
	present map[string]bool
	// lookupErr, when set, is returned by every localImageExists call.
	lookupErr error
	// lookups records the images that were queried, in order.
	lookups []string

	// Failures for each step of starting and stopping.
	ensureNamespaceErr error
	applySecretErr     error
	applyConfigMapErr  error
	loadImageErr       error
	installErr         error

	uninstallErr          error
	releaseStillInstalled bool
	releaseInstalledErr   error
	workloadGoneErr       error
	workloadReadyErr      error
	deleteNamespaceErr    error

	// logs and logsErr are what workloadLogs reports.
	logs    string
	logsErr error
	// diagnostics is what workloadDiagnostics reports.
	diagnostics string

	// calls records the operations performed, in order.
	calls []string
	// installedRelease records the release installChart was given, so a test
	// can assert on the values file and image overrides without a cluster.
	installedRelease *chartRelease
}

func (f *fakeCollectorRuntime) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeCollectorRuntime) ensureNamespace(_ context.Context, namespace string) error {
	f.record("ensure-namespace %s", namespace)
	return f.ensureNamespaceErr
}

func (f *fakeCollectorRuntime) deleteNamespace(_ context.Context, namespace string, _ time.Duration) error {
	f.record("delete-namespace %s", namespace)
	return f.deleteNamespaceErr
}

func (f *fakeCollectorRuntime) applySecret(_ context.Context, namespace, name string, _ map[string]string) error {
	f.record("apply-secret %s/%s", namespace, name)
	return f.applySecretErr
}

func (f *fakeCollectorRuntime) applyConfigMap(_ context.Context, namespace, name string, _ map[string]string) error {
	f.record("apply-configmap %s/%s", namespace, name)
	return f.applyConfigMapErr
}

func (f *fakeCollectorRuntime) localImageExists(_ context.Context, image string) (bool, error) {
	f.lookups = append(f.lookups, image)
	if f.lookupErr != nil {
		return false, f.lookupErr
	}
	return f.present[image], nil
}

func (f *fakeCollectorRuntime) loadLocalImage(_ context.Context, image string) error {
	f.record("load-image %s", image)
	return f.loadImageErr
}

func (f *fakeCollectorRuntime) installChart(_ context.Context, release chartRelease) error {
	f.record("install %s/%s", release.namespace, release.name)
	f.installedRelease = &release
	return f.installErr
}

func (f *fakeCollectorRuntime) uninstallChart(_ context.Context, name, namespace string, _ time.Duration) error {
	f.record("uninstall %s/%s", namespace, name)
	return f.uninstallErr
}

func (f *fakeCollectorRuntime) releaseInstalled(_ context.Context, name, namespace string) (bool, error) {
	f.record("release-installed %s/%s", namespace, name)
	return f.releaseStillInstalled, f.releaseInstalledErr
}

func (f *fakeCollectorRuntime) waitForWorkloadReady(_ context.Context, namespace, _ string, _ time.Duration) error {
	f.record("wait-workload-ready %s", namespace)
	return f.workloadReadyErr
}

func (f *fakeCollectorRuntime) waitForWorkloadGone(_ context.Context, namespace, _ string, _ time.Duration) error {
	f.record("wait-workload-gone %s", namespace)
	return f.workloadGoneErr
}

func (f *fakeCollectorRuntime) workloadDiagnostics(_ context.Context, namespace, selector string) string {
	f.record("diagnostics %s %s", namespace, selector)
	return f.diagnostics
}

func (f *fakeCollectorRuntime) workloadLogs(_ context.Context, namespace, selector string, tailLines int64) (string, error) {
	f.record("logs %s %s tail=%d", namespace, selector, tailLines)
	return f.logs, f.logsErr
}

// fakeEnvironment is the surrounding Flight Control deployment.
type fakeEnvironment struct {
	namespace   string
	host        string
	port        int
	endpointErr error
}

func (f *fakeEnvironment) GetExternalNamespace() string { return f.namespace }

func (f *fakeEnvironment) GetServiceEndpoint(infra.ServiceName) (string, int, error) {
	if f.endpointErr != nil {
		return "", 0, f.endpointErr
	}
	return f.host, f.port, nil
}

// fakeSecrets is the CA bundle source.
type fakeSecrets struct {
	data map[string][]byte
	err  error
}

func (f *fakeSecrets) GetSecretData(_ context.Context, namespace, name, key string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.data[fmt.Sprintf("%s/%s/%s", namespace, name, key)], nil
}

// clearCollectorEnv removes the settings that select the collector image, so a
// developer's own environment cannot change the outcome of these tests.
func clearCollectorEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{infra.EnvCollectorImage, infra.EnvCollectorImagePreloaded, infra.EnvCollectorRequired} {
		t.Setenv(name, "")
	}
}

// TestCollectorDeployerAvailable covers the skip-or-fail decision the suite
// delegates here: an unsupported deployment type and a missing image are
// settled answers, and everything else is an error that must not degrade into
// one.
func TestCollectorDeployerAvailable(t *testing.T) {
	const ciImage = "quay.io/flightctl/flightctl-catalog-collector-el9:v0.9.0-123"

	tests := []struct {
		name    string
		envType string
		env     map[string]string
		runtime func() *fakeCollectorRuntime

		wantAvailable bool
		wantRequired  bool
		// wantReasonContains is checked against the reason an unavailable
		// deployment reports.
		wantReasonContains string
		// wantLookups is the host runtime queries that must have happened.
		wantLookups []string
		wantErr     bool
	}{
		{
			name:    "When CI passes a preloaded image on kind it should report the collector can be deployed",
			envType: infra.EnvironmentKind,
			env: map[string]string{
				infra.EnvCollectorImage:          ciImage,
				infra.EnvCollectorImagePreloaded: "true",
				infra.EnvCollectorRequired:       "true",
			},
			runtime:       func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantAvailable: true,
			wantRequired:  true,
			// The CI job loads the image bundle straight into the cluster, so
			// the host runtime never has it and must not be consulted.
			wantLookups: nil,
		},
		{
			name:    "When a local build is present on kind it should report the collector can be deployed",
			envType: infra.EnvironmentKind,
			runtime: func() *fakeCollectorRuntime {
				return &fakeCollectorRuntime{present: map[string]bool{collectorImageEL9: true}}
			},
			wantAvailable: true,
			wantLookups:   []string{collectorImageEL9},
		},
		{
			name:    "When only the un-suffixed local build is present it should fall back to it",
			envType: infra.EnvironmentKind,
			runtime: func() *fakeCollectorRuntime {
				return &fakeCollectorRuntime{present: map[string]bool{collectorImageGeneric: true}}
			},
			wantAvailable: true,
			wantLookups:   []string{collectorImageEL9, collectorImageGeneric},
		},
		{
			name:               "When no image is available and none was required it should be unavailable without requiring it",
			envType:            infra.EnvironmentKind,
			runtime:            func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantAvailable:      false,
			wantRequired:       false,
			wantReasonContains: "no collector image available",
			wantLookups:        []string{collectorImageEL9, collectorImageGeneric},
		},
		{
			name:               "When no image is available but the run required one it should be unavailable and required",
			envType:            infra.EnvironmentKind,
			env:                map[string]string{infra.EnvCollectorRequired: "true"},
			runtime:            func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantAvailable:      false,
			wantRequired:       true,
			wantReasonContains: "no collector image available",
			wantLookups:        []string{collectorImageEL9, collectorImageGeneric},
		},
		{
			name:               "When the deployment type cannot load images it should be unavailable without consulting the container runtime",
			envType:            infra.EnvironmentOCP,
			runtime:            func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantAvailable:      false,
			wantRequired:       false,
			wantReasonContains: "only implemented for kind",
			// No image could make this deployment type work, so probing a
			// container runtime here could only turn a skip into a failure.
			wantLookups: nil,
		},
		{
			name:    "When the deployment type cannot load images but the run required one it should be unavailable and required",
			envType: infra.EnvironmentOCP,
			env: map[string]string{
				infra.EnvCollectorImage:    ciImage,
				infra.EnvCollectorRequired: "true",
			},
			runtime:            func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantAvailable:      false,
			wantRequired:       true,
			wantReasonContains: "only implemented for kind",
			wantLookups:        nil,
		},
		{
			name:    "When the container runtime fails it should return the error instead of reporting unavailable",
			envType: infra.EnvironmentKind,
			runtime: func() *fakeCollectorRuntime {
				return &fakeCollectorRuntime{lookupErr: errors.New("cannot connect to Podman socket")}
			},
			wantLookups: []string{collectorImageEL9},
			wantErr:     true,
		},
		{
			name:    "When the preloaded setting is not a boolean it should fail rather than guess",
			envType: infra.EnvironmentKind,
			env:     map[string]string{infra.EnvCollectorImagePreloaded: "yes-please"},
			runtime: func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantErr: true,
		},
		{
			name:    "When the required setting is not a boolean it should fail rather than guess",
			envType: infra.EnvironmentKind,
			env:     map[string]string{infra.EnvCollectorRequired: "maybe"},
			runtime: func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantErr: true,
		},
		{
			name: "When the required setting is not a boolean on an unsupported deployment type it should still fail",
			// The tempting shortcut is to answer "unsupported, so skip" without
			// reading the setting at all. That would silently ignore a typo in
			// the one variable that decides between skipping and failing.
			envType: infra.EnvironmentOCP,
			env:     map[string]string{infra.EnvCollectorRequired: "maybe"},
			runtime: func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantErr: true,
		},
		{
			name: "When an image is named without saying whether it is required it should treat the run as required",
			// Naming an exact image is itself a statement that the suite was
			// meant to run against it, so an unsupported deployment type fails
			// rather than skipping. This is the default the suite README
			// documents; the generic unsupported deployer does not share it,
			// which TestUnsupportedCollectorDeployer pins separately.
			envType:            infra.EnvironmentOCP,
			env:                map[string]string{infra.EnvCollectorImage: ciImage},
			runtime:            func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantAvailable:      false,
			wantRequired:       true,
			wantReasonContains: "only implemented for kind",
			wantLookups:        nil,
		},
		{
			name:    "When an image is named but required is explicitly false it should treat the run as optional",
			envType: infra.EnvironmentOCP,
			env: map[string]string{
				infra.EnvCollectorImage:    ciImage,
				infra.EnvCollectorRequired: "false",
			},
			runtime:            func() *fakeCollectorRuntime { return &fakeCollectorRuntime{} },
			wantAvailable:      false,
			wantRequired:       false,
			wantReasonContains: "only implemented for kind",
			wantLookups:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCollectorEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			runtime := tt.runtime()
			deployer := &CollectorDeployer{runtime: runtime, envType: tt.envType}

			got, err := deployer.Available(context.Background())

			require.Equal(t, tt.wantLookups, runtime.lookups, "unexpected host runtime image lookups")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantAvailable, got.Available)
			require.Equal(t, tt.wantRequired, got.Required)
			if tt.wantReasonContains != "" {
				require.Contains(t, got.Reason, tt.wantReasonContains)
			} else {
				require.Empty(t, got.Reason, "an available deployment has nothing to explain")
			}
		})
	}
}

// TestCollectorDeployerConfigContext covers the values a caller renders its
// configuration from, including the failure that must not be swallowed.
func TestCollectorDeployerConfigContext(t *testing.T) {
	t.Run("When the endpoint resolves it should report the server and the mounted paths", func(t *testing.T) {
		deployer := &CollectorDeployer{env: &fakeEnvironment{host: "flightctl-api", port: 8443}}

		got, err := deployer.ConfigContext(context.Background())

		require.NoError(t, err)
		require.Equal(t, "https://flightctl-api:8443", got.FlightControlServer)
		require.Equal(t, collectorOAuthMountPath+"/"+collectorOAuthClientIDKey, got.OAuthClientIDFile)
		require.Equal(t, collectorOAuthMountPath+"/"+collectorOAuthSecretKey, got.OAuthClientSecretFile)
		require.Equal(t, collectorCertsMountPath+"/"+collectorCAFileName, got.CertificateAuthorityFile)
	})

	t.Run("When the endpoint cannot be resolved it should return the error", func(t *testing.T) {
		wantErr := errors.New("no API service")
		deployer := &CollectorDeployer{env: &fakeEnvironment{endpointErr: wantErr}}

		_, err := deployer.ConfigContext(context.Background())

		require.ErrorIs(t, err, wantErr)
	})
}

// startDeployer builds a deployer whose CA bundle lookup succeeds.
func startDeployer(runtime *fakeCollectorRuntime) *CollectorDeployer {
	env := &fakeEnvironment{namespace: "flightctl", host: "flightctl-api", port: 8443}
	return &CollectorDeployer{
		runtime: runtime,
		env:     env,
		envType: infra.EnvironmentKind,
		secrets: &fakeSecrets{data: map[string][]byte{
			fmt.Sprintf("%s/%s/%s", env.namespace, flightctlCABundleSecret, flightctlCABundleSecretKey): []byte("ca"),
		}},
	}
}

func startConfig() infra.CollectorDeploymentConfig {
	return infra.CollectorDeploymentConfig{
		InstanceID:        "abc123",
		Config:            []byte("service:\n  logLevel: debug\n"),
		OAuthClientID:     "client",
		OAuthClientSecret: "secret",
		StartTimeout:      time.Minute,
		StopTimeout:       time.Minute,
	}
}

// TestCollectorDeployerStartImagePaths covers the two ways the image reaches
// the cluster: already there, or loaded from the host runtime.
func TestCollectorDeployerStartImagePaths(t *testing.T) {
	const ciImage = "quay.io/flightctl/flightctl-catalog-collector-el9:v0.9.0-123"
	namespace := collectorNamespace("abc123")
	release := collectorReleaseName("abc123")

	tests := []struct {
		name string
		env  map[string]string
		// present is what the host runtime holds.
		present map[string]bool

		wantCalls []string
		// wantImage is the repository and tag the chart must be given.
		wantRepository string
		wantTag        string
	}{
		{
			name: "When the image is already in the cluster it should not be loaded again",
			env: map[string]string{
				infra.EnvCollectorImage:          ciImage,
				infra.EnvCollectorImagePreloaded: "true",
			},
			wantCalls: []string{
				"ensure-namespace " + namespace,
				"apply-secret " + namespace + "/" + collectorOAuthSecretName,
				"apply-configmap " + namespace + "/" + collectorCAConfigMapName,
				"install " + namespace + "/" + release,
			},
			wantRepository: "quay.io/flightctl/flightctl-catalog-collector-el9",
			wantTag:        "v0.9.0-123",
		},
		{
			name:    "When the image is a local build it should be loaded into the cluster first",
			present: map[string]bool{collectorImageEL9: true},
			wantCalls: []string{
				"ensure-namespace " + namespace,
				"apply-secret " + namespace + "/" + collectorOAuthSecretName,
				"apply-configmap " + namespace + "/" + collectorCAConfigMapName,
				"load-image " + collectorImageEL9,
				"install " + namespace + "/" + release,
			},
			wantRepository: "localhost/flightctl-catalog-collector-el9",
			wantTag:        "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCollectorEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			runtime := &fakeCollectorRuntime{present: tt.present}
			instance, err := startDeployer(runtime).Start(context.Background(), startConfig())
			require.NoError(t, err)
			require.NotNil(t, instance)
			t.Cleanup(func() { _ = instance.Cleanup(context.Background()) })

			require.Equal(t, tt.wantCalls, runtime.calls)
			require.NotNil(t, runtime.installedRelease)
			require.Equal(t, tt.wantRepository, runtime.installedRelease.setValues["image.image"])
			require.Equal(t, tt.wantTag, runtime.installedRelease.setValues["image.tag"])
			require.Equal(t, collectorChartPath, runtime.installedRelease.chartPath)

			// The configuration the caller supplied has to reach the chart
			// through the rendered values, not through anything the caller
			// had to write itself.
			require.Len(t, runtime.installedRelease.valuesFiles, 1)
			values, err := os.ReadFile(runtime.installedRelease.valuesFiles[0])
			require.NoError(t, err)
			require.Contains(t, string(values), "logLevel: debug")
			require.Contains(t, string(values), collectorOAuthSecretName)
			require.Contains(t, string(values), collectorCAConfigMapName)
		})
	}
}

// TestCollectorDeployerStartFailures covers the two properties a failing start
// has to keep: a real failure is reported as one rather than as an unavailable
// deployment, and whatever was already created comes back on the instance so
// teardown can remove it.
func TestCollectorDeployerStartFailures(t *testing.T) {
	namespace := collectorNamespace("abc123")

	tests := []struct {
		name    string
		runtime *fakeCollectorRuntime
		// wantInstance is whether a handle must be returned despite the error.
		wantInstance bool
		wantCalls    []string
	}{
		{
			name:         "When the namespace cannot be created it should still return the instance",
			runtime:      &fakeCollectorRuntime{ensureNamespaceErr: errors.New("forbidden")},
			wantInstance: true,
			wantCalls:    []string{"ensure-namespace " + namespace},
		},
		{
			name:         "When the credentials cannot be applied it should still return the instance",
			runtime:      &fakeCollectorRuntime{applySecretErr: errors.New("conflict")},
			wantInstance: true,
			wantCalls: []string{
				"ensure-namespace " + namespace,
				"apply-secret " + namespace + "/" + collectorOAuthSecretName,
			},
		},
		{
			name:         "When the image cannot be loaded it should still return the instance",
			runtime:      &fakeCollectorRuntime{loadImageErr: errors.New("kind load failed")},
			wantInstance: true,
			wantCalls: []string{
				"ensure-namespace " + namespace,
				"apply-secret " + namespace + "/" + collectorOAuthSecretName,
				"apply-configmap " + namespace + "/" + collectorCAConfigMapName,
				"load-image " + collectorImageEL9,
			},
		},
		{
			name:         "When the install fails its readiness wait it should still return the instance",
			runtime:      &fakeCollectorRuntime{installErr: errors.New("timed out waiting for the condition")},
			wantInstance: true,
			wantCalls: []string{
				"ensure-namespace " + namespace,
				"apply-secret " + namespace + "/" + collectorOAuthSecretName,
				"apply-configmap " + namespace + "/" + collectorCAConfigMapName,
				"load-image " + collectorImageEL9,
				"install " + namespace + "/" + collectorReleaseName("abc123"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCollectorEnv(t)
			tt.runtime.present = map[string]bool{collectorImageEL9: true}

			instance, err := startDeployer(tt.runtime).Start(context.Background(), startConfig())

			require.Error(t, err)
			// A genuine failure of the deployment is never an unsupported one:
			// a caller must fail on it instead of skipping.
			require.NotErrorIs(t, err, infra.ErrCollectorDeploymentUnsupported)
			if tt.wantInstance {
				require.NotNil(t, instance, "a partial start must hand back the state it created")
				t.Cleanup(func() { _ = instance.Cleanup(context.Background()) })
			}
			require.Equal(t, tt.wantCalls, tt.runtime.calls)
		})
	}
}

// TestCollectorDeployerStartRejectsIncompleteConfig covers the inputs a caller
// has to supply, which are checked before anything is created.
func TestCollectorDeployerStartRejectsIncompleteConfig(t *testing.T) {
	valid := startConfig()

	tests := []struct {
		name    string
		mutate  func(c *infra.CollectorDeploymentConfig)
		wantErr string
	}{
		{
			name:    "When no instance ID is given it should refuse",
			mutate:  func(c *infra.CollectorDeploymentConfig) { c.InstanceID = " " },
			wantErr: "instance ID is required",
		},
		{
			name:    "When no configuration is given it should refuse",
			mutate:  func(c *infra.CollectorDeploymentConfig) { c.Config = nil },
			wantErr: "configuration is required",
		},
		{
			name:    "When the start timeout is not positive it should refuse",
			mutate:  func(c *infra.CollectorDeploymentConfig) { c.StartTimeout = 0 },
			wantErr: "start timeout must be positive",
		},
		{
			name:    "When the stop timeout is not positive it should refuse",
			mutate:  func(c *infra.CollectorDeploymentConfig) { c.StopTimeout = 0 },
			wantErr: "stop timeout must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCollectorEnv(t)
			config := valid
			tt.mutate(&config)

			runtime := &fakeCollectorRuntime{present: map[string]bool{collectorImageEL9: true}}
			instance, err := startDeployer(runtime).Start(context.Background(), config)

			require.ErrorContains(t, err, tt.wantErr)
			require.Nil(t, instance, "nothing was created, so there is nothing to tear down")
			require.Empty(t, runtime.calls)
		})
	}
}

// newInstance builds an instance as Start would have left it.
func newInstance(runtime collectorRuntime) *collectorInstance {
	const id = "abc123"
	return &collectorInstance{
		runtime:     runtime,
		namespace:   collectorNamespace(id),
		releaseName: collectorReleaseName(id),
		selector:    "app.kubernetes.io/name=" + collectorChartName,
		stopTimeout: time.Minute,
	}
}

// TestCollectorInstanceStop covers the split between "teardown succeeded" and
// "the collector can no longer act", which are not the same answer: a
// namespace-deletion fallback that worked stops the collector even though the
// step still has to report the release it could not remove.
func TestCollectorInstanceStop(t *testing.T) {
	uninstallFailure := errors.New("helm uninstall timed out")
	fallbackFailure := errors.New("namespace stuck terminating")

	tests := []struct {
		name    string
		runtime *fakeCollectorRuntime

		wantStopped bool
		// wantErrs are the underlying failures the returned error must still
		// carry, so the report names what actually broke.
		wantErrs []error
		// wantFallback is whether the namespace deletion fallback had to run.
		wantFallback bool
	}{
		{
			name:         "When the release is removed normally it should record the collector stopped without the namespace fallback",
			runtime:      &fakeCollectorRuntime{},
			wantStopped:  true,
			wantFallback: false,
		},
		{
			name:         "When removal fails but the namespace fallback succeeds it should still record the collector stopped",
			runtime:      &fakeCollectorRuntime{uninstallErr: uninstallFailure},
			wantStopped:  true,
			wantErrs:     []error{uninstallFailure},
			wantFallback: true,
		},
		{
			name: "When the workload outlives the release and the fallback succeeds it should still record the collector stopped",
			runtime: &fakeCollectorRuntime{
				releaseStillInstalled: true,
				workloadGoneErr:       errors.New("pod still running"),
			},
			wantStopped:  true,
			wantFallback: true,
		},
		{
			name: "When removal and the namespace fallback both fail it should record the collector not stopped",
			runtime: &fakeCollectorRuntime{
				uninstallErr:       uninstallFailure,
				deleteNamespaceErr: fallbackFailure,
			},
			wantStopped:  false,
			wantErrs:     []error{uninstallFailure, fallbackFailure},
			wantFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := newInstance(tt.runtime)

			stopped, err := instance.Stop(context.Background())

			if len(tt.wantErrs) == 0 && tt.wantStopped && !tt.wantFallback {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "a teardown that could not remove the release must report it")
			}
			for _, wantErr := range tt.wantErrs {
				require.ErrorIs(t, err, wantErr, "the original failure must survive in the report")
			}
			require.Equal(t, tt.wantStopped, stopped,
				"whether the collector is down is reported separately from whether the step failed")

			wantCalls := []string{
				fmt.Sprintf("uninstall %s/%s", instance.namespace, instance.releaseName),
				fmt.Sprintf("release-installed %s/%s", instance.namespace, instance.releaseName),
				fmt.Sprintf("wait-workload-gone %s", instance.namespace),
			}
			if tt.wantFallback {
				wantCalls = append(wantCalls, fmt.Sprintf("delete-namespace %s", instance.namespace))
			}
			require.Equal(t, wantCalls, tt.runtime.calls, "unexpected teardown calls")
		})
	}
}

// TestCollectorInstanceCleanupIsIdempotent covers the teardown step that runs
// regardless of how stopping went: it must remove the namespace and the
// rendered values file, tolerate both already being gone, and keep going when
// one of them fails.
func TestCollectorInstanceCleanupIsIdempotent(t *testing.T) {
	newValuesFile := func(t *testing.T) string {
		t.Helper()
		file, err := os.CreateTemp(t.TempDir(), "values-*.yaml")
		require.NoError(t, err)
		require.NoError(t, file.Close())
		return file.Name()
	}

	t.Run("When called twice it should remove the values file once and stay successful", func(t *testing.T) {
		runtime := &fakeCollectorRuntime{}
		instance := newInstance(runtime)
		instance.valuesPath = newValuesFile(t)

		require.NoError(t, instance.Cleanup(context.Background()))
		require.NoFileExists(t, instance.valuesPath)
		require.NoError(t, instance.Cleanup(context.Background()), "cleanup must be safe to repeat")

		require.Equal(t, []string{
			"delete-namespace " + instance.namespace,
			"delete-namespace " + instance.namespace,
		}, runtime.calls, "a repeated cleanup still re-asserts the namespace is gone")
	})

	t.Run("When the namespace cannot be removed it should still remove the values file", func(t *testing.T) {
		namespaceFailure := errors.New("namespace stuck terminating")
		instance := newInstance(&fakeCollectorRuntime{deleteNamespaceErr: namespaceFailure})
		valuesPath := newValuesFile(t)
		instance.valuesPath = valuesPath

		err := instance.Cleanup(context.Background())

		require.ErrorIs(t, err, namespaceFailure)
		require.NoFileExists(t, valuesPath,
			"a failing namespace deletion must not strand the local file")
	})
}

// TestCollectorInstanceObservationDelegates covers the two read paths a suite
// uses, which must reach the deployment rather than anything the suite names
// itself, and must keep their different failure contracts.
func TestCollectorInstanceObservationDelegates(t *testing.T) {
	t.Run("When logs are read it should ask the deployment with the requested bound", func(t *testing.T) {
		runtime := &fakeCollectorRuntime{logs: "collection failed; applying backoff"}
		instance := newInstance(runtime)

		logs, err := instance.Logs(context.Background(), 1000)

		require.NoError(t, err)
		require.Equal(t, "collection failed; applying backoff", logs)
		require.Equal(t,
			[]string{fmt.Sprintf("logs %s %s tail=1000", instance.namespace, instance.selector)},
			runtime.calls)
	})

	t.Run("When the logs cannot be read it should return the error rather than empty output", func(t *testing.T) {
		readFailure := errors.New("no pods match the selector")
		instance := newInstance(&fakeCollectorRuntime{logsErr: readFailure})

		logs, err := instance.Logs(context.Background(), 1000)

		require.ErrorIs(t, err, readFailure)
		require.Empty(t, logs, "a failed read must not look like a collector that logged nothing")
	})

	t.Run("When diagnostics are collected it should ask the deployment and never fail", func(t *testing.T) {
		runtime := &fakeCollectorRuntime{diagnostics: "pod flightctl-catalog-collector-0: phase=Running"}
		instance := newInstance(runtime)

		require.Equal(t, "pod flightctl-catalog-collector-0: phase=Running", instance.Diagnostics(context.Background()))
		require.Equal(t,
			[]string{fmt.Sprintf("diagnostics %s %s", instance.namespace, instance.selector)},
			runtime.calls)
	})

	t.Run("When readiness is awaited it should ask the deployment and report its failure", func(t *testing.T) {
		readyFailure := errors.New("pods did not become ready")
		runtime := &fakeCollectorRuntime{workloadReadyErr: readyFailure}
		instance := newInstance(runtime)

		require.ErrorIs(t, instance.WaitReady(context.Background(), time.Minute), readyFailure)
		require.Equal(t, []string{"wait-workload-ready " + instance.namespace}, runtime.calls)
	})
}

// TestUnsupportedCollectorDeployer covers the deployment types that cannot host
// a collector: they answer the same question every other deployment does, and
// a run that required one still fails rather than skipping.
func TestUnsupportedCollectorDeployer(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantRequired bool
		// wantErr is for a setting that could not be read at all, which is
		// never a skip: the question of whether to skip was not answered.
		wantErr bool
	}{
		{
			name:         "When nothing required a collector it should be unavailable without requiring one",
			wantRequired: false,
		},
		{
			name:         "When the run required a collector it should be unavailable and required",
			env:          map[string]string{infra.EnvCollectorRequired: "true"},
			wantRequired: true,
		},
		{
			name:    "When the required setting is not a boolean it should fail rather than skip",
			env:     map[string]string{infra.EnvCollectorRequired: "maybe"},
			wantErr: true,
		},
		{
			name: "When only an image is named it should stay optional",
			// A deployment type that cannot host a collector at all cannot be
			// made to by naming an image, so the "an explicit image means the
			// run was meant to happen" default does not apply here: only an
			// explicit setting makes this run required.
			env:          map[string]string{infra.EnvCollectorImage: "quay.io/flightctl/flightctl-catalog-collector-el9:v0.9.0-123"},
			wantRequired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCollectorEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			deployer := infra.NewUnsupportedCollectorDeployer(infra.EnvironmentQuadlet)

			got, err := deployer.Available(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				require.False(t, got.Available)
				return
			}
			require.NoError(t, err)
			require.False(t, got.Available)
			require.Equal(t, tt.wantRequired, got.Required)
			require.Contains(t, got.Reason, infra.EnvironmentQuadlet)

			// Starting one anyway reports the unsupported deployment type
			// rather than a cryptic failure, and creates nothing.
			instance, err := deployer.Start(context.Background(), infra.CollectorDeploymentConfig{})
			require.ErrorIs(t, err, infra.ErrCollectorDeploymentUnsupported)
			require.Nil(t, instance)

			_, err = deployer.ConfigContext(context.Background())
			require.ErrorIs(t, err, infra.ErrCollectorDeploymentUnsupported)
		})
	}
}

// TestCollectorDeployerStartRefusesUnsupportedEnvironment is the capability
// contract's backstop. Available already reports a non-kind deployment as
// unavailable, but nothing forces a caller to ask: a preloaded image needs no
// container runtime and no image load, so without this guard it would sail
// past every other check and install a collector onto nodes this package
// cannot support. Nothing may be created before the refusal.
func TestCollectorDeployerStartRefusesUnsupportedEnvironment(t *testing.T) {
	const ciImage = "quay.io/flightctl/flightctl-catalog-collector-el9:v0.9.0-123"

	tests := []struct {
		name    string
		envType string
		env     map[string]string
	}{
		{
			name:    "When an explicitly preloaded image is supplied on an unsupported deployment type it should still refuse",
			envType: infra.EnvironmentOCP,
			env: map[string]string{
				infra.EnvCollectorImage:          ciImage,
				infra.EnvCollectorImagePreloaded: "true",
			},
		},
		{
			name:    "When the run required a collector on an unsupported deployment type it should still refuse",
			envType: infra.EnvironmentOCP,
			env: map[string]string{
				infra.EnvCollectorImage:          ciImage,
				infra.EnvCollectorImagePreloaded: "true",
				infra.EnvCollectorRequired:       "true",
			},
		},
		{
			name:    "When no image is named on an unsupported deployment type it should refuse without consulting the container runtime",
			envType: infra.EnvironmentOCP,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCollectorEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			runtime := &fakeCollectorRuntime{present: map[string]bool{collectorImageEL9: true}}
			deployer := startDeployer(runtime)
			deployer.envType = tt.envType

			instance, err := deployer.Start(context.Background(), startConfig())

			require.ErrorIs(t, err, infra.ErrCollectorDeploymentUnsupported)
			require.Nil(t, instance, "nothing was created, so there is nothing to tear down")
			require.Empty(t, runtime.calls, "no namespace, Secret, image load, or release may be attempted")
			require.Empty(t, runtime.lookups, "the container runtime is irrelevant to an unsupported deployment type")
		})
	}
}

// TestCollectorDeployerStartOnSupportedEnvironmentIsNotRefused pins the other
// half of the guard: it must reject only the unsupported deployment type, not
// the supported one.
func TestCollectorDeployerStartOnSupportedEnvironmentIsNotRefused(t *testing.T) {
	clearCollectorEnv(t)
	t.Setenv(infra.EnvCollectorImage, "quay.io/flightctl/flightctl-catalog-collector-el9:v0.9.0-123")
	t.Setenv(infra.EnvCollectorImagePreloaded, "true")

	runtime := &fakeCollectorRuntime{}
	instance, err := startDeployer(runtime).Start(context.Background(), startConfig())

	require.NoError(t, err)
	require.NotNil(t, instance)
	t.Cleanup(func() { _ = instance.Cleanup(context.Background()) })
	require.NotEmpty(t, runtime.calls)
}

// TestCollectorInstanceTeardownAfterPartialStart covers teardown of a start
// that never got as far as a release. Uninstalling a release that was never
// created must not be reported as a failure, the collector must be confirmed
// stopped (it was never running, so it cannot write anything back), and the
// namespace must still be removed.
func TestCollectorInstanceTeardownAfterPartialStart(t *testing.T) {
	clearCollectorEnv(t)

	// A start that fails applying the credentials, i.e. before the image is
	// loaded and before any release exists.
	runtime := &fakeCollectorRuntime{
		present:        map[string]bool{collectorImageEL9: true},
		applySecretErr: errors.New("forbidden"),
	}
	instance, err := startDeployer(runtime).Start(context.Background(), startConfig())
	require.Error(t, err)
	require.NotNil(t, instance)

	runtime.calls = nil
	stopped, stopErr := instance.Stop(context.Background())

	require.True(t, stopped, "a collector that was never installed cannot write, so it is stopped")
	require.NoError(t, stopErr, "uninstalling a release that was never created is not a failure")

	require.NoError(t, instance.Cleanup(context.Background()))
	require.Equal(t, []string{
		"uninstall " + collectorNamespace("abc123") + "/" + collectorReleaseName("abc123"),
		"release-installed " + collectorNamespace("abc123") + "/" + collectorReleaseName("abc123"),
		"wait-workload-gone " + collectorNamespace("abc123"),
		"delete-namespace " + collectorNamespace("abc123"),
	}, runtime.calls, "the namespace must still be removed")
}

// TestCollectorInstanceTeardownAfterFailedInstall covers the other partial
// start: the release exists but never became ready. Teardown must remove it,
// confirm the collector is down, and leave no rendered values file behind.
func TestCollectorInstanceTeardownAfterFailedInstall(t *testing.T) {
	clearCollectorEnv(t)

	runtime := &fakeCollectorRuntime{
		present:    map[string]bool{collectorImageEL9: true},
		installErr: errors.New("timed out waiting for the condition"),
	}
	instance, err := startDeployer(runtime).Start(context.Background(), startConfig())
	require.Error(t, err)
	require.NotNil(t, instance)

	// The values file was rendered before the install, so teardown owns it.
	concrete, ok := instance.(*collectorInstance)
	require.True(t, ok)
	valuesPath := concrete.valuesPath
	require.NotEmpty(t, valuesPath)
	require.FileExists(t, valuesPath)

	stopped, stopErr := instance.Stop(context.Background())
	require.True(t, stopped)
	require.NoError(t, stopErr)

	require.NoError(t, instance.Cleanup(context.Background()))
	require.NoFileExists(t, valuesPath, "a failed install must not leak its rendered values file")
}

// TestRenderCollectorValuesRemovesPartialFileOnFailure covers the temp-file
// lifecycle this package took over. When the write or the close fails no path
// is returned, so nothing else could ever remove the file: it has to be
// removed here or it is leaked for the life of the machine.
func TestRenderCollectorValuesRemovesPartialFileOnFailure(t *testing.T) {
	t.Run("When rendering succeeds it should return a path the caller owns", func(t *testing.T) {
		path, err := renderCollectorValues([]byte("service:\n  logLevel: debug\n"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.Remove(path) })
		require.FileExists(t, path)
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(contents), "logLevel: debug")
	})

	t.Run("When the file cannot be written it should remove it and return no path", func(t *testing.T) {
		// A file that is already closed fails every write, which is the one
		// failure the real implementation has to clean up after.
		var created string
		restore := stubValuesFile(t, func() (*os.File, error) {
			file, err := os.CreateTemp(t.TempDir(), "values-*.yaml")
			if err != nil {
				return nil, err
			}
			created = file.Name()
			if err := file.Close(); err != nil {
				return nil, err
			}
			return file, nil
		})
		defer restore()

		path, err := renderCollectorValues([]byte("service:\n  logLevel: debug\n"))

		require.Error(t, err)
		require.Empty(t, path, "a failed render must not hand back a path")
		require.NotEmpty(t, created, "the stub must have created a file to begin with")
		require.NoFileExists(t, created, "the partial file must be removed, since no caller holds its path")
	})

	t.Run("When the file cannot be created it should return no path and create nothing", func(t *testing.T) {
		restore := stubValuesFile(t, func() (*os.File, error) {
			return nil, errors.New("no space left on device")
		})
		defer restore()

		path, err := renderCollectorValues([]byte("service:\n  logLevel: debug\n"))

		require.ErrorContains(t, err, "create values file")
		require.Empty(t, path)
	})
}

// stubValuesFile swaps the values-file creator for the duration of a test.
func stubValuesFile(t *testing.T, stub func() (*os.File, error)) func() {
	t.Helper()
	previous := createValuesFile
	createValuesFile = stub
	return func() { createValuesFile = previous }
}
