package catalogcollector_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCatalogCollector(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Catalog Collector E2E Suite")
}

var (
	providers *infra.Providers
	auxSvcs   *auxiliary.Services
	resources *testResources
	// suiteHarness is held explicitly rather than read through
	// e2e.GetWorkerHarness() in teardown: when BeforeSuite skips before the
	// harness exists, that helper fails the suite instead of returning nil.
	suiteHarness *e2e.Harness

	valuesFilePath  string
	chartInstalled  bool
	authProviderSet bool
)

var _ = BeforeSuite(func() {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	// The collector is deployed from the shipped Helm chart using the image
	// built from this branch, so the suite needs a Helm-managed cluster whose
	// nodes can be fed a locally built image. That is kind; Quadlet ships the
	// collector as a Podman unit instead and is out of scope here.
	infra.SkipIfNotK8s("Catalog collector e2e requires a Kubernetes deployment")

	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	providers = setup.GetDefaultProviders()
	Expect(providers).ToNot(BeNil(), "infra providers must be initialized")
	Expect(providers.Charts).ToNot(BeNil(), "chart deployer is required to install the collector chart")

	if providers.Infra.GetEnvironmentType() != infra.EnvironmentKind {
		Skip(fmt.Sprintf(
			"Catalog collector e2e loads the locally built collector image into the cluster, which is only implemented for kind (current environment: %s)",
			providers.Infra.GetEnvironmentType(),
		))
	}

	image, err := resolveCollectorImage(ctx, providers.Charts)
	Expect(err).ToNot(HaveOccurred(), "failed to look up the collector image under test")
	if image.Reference == "" {
		// Required means a CI job asked for this suite: skipping there would
		// report a green run that executed nothing, so it fails instead. The
		// skips above, for deployment types that cannot run it at all, stay.
		Expect(image.Required).To(BeFalse(), missingImageMessage())
		Skip(missingImageMessage())
	}
	GinkgoWriter.Printf("[catalogcollector] using collector image %s (source=%s, preloaded=%t, required=%t)\n",
		image.Reference, image.Source, image.Preloaded, image.Required)

	// Only the API and the CLI are needed; no device agent takes part in
	// catalog synchronization.
	_, _, err = e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred(), "failed to set up harness")
	suiteHarness = e2e.GetWorkerHarness()

	resources, err = newTestResources()
	Expect(err).ToNot(HaveOccurred(), "failed to generate isolated resource names")
	GinkgoWriter.Printf("[catalogcollector] run suffix %s, namespace %s, catalog %s, pipeline %s\n",
		resources.suffix, resources.namespace, resources.catalogName, resources.pipelineName)

	By("starting Keycloak and the Kubeflow Model Registry aux services")
	auxSvcs, err = auxiliary.StartServices(ctx, []auxiliary.Service{
		auxiliary.ServiceKeycloak,
		auxiliary.ServiceModelRegistry,
	})
	Expect(err).ToNot(HaveOccurred(), "failed to start aux services")
	Expect(auxSvcs.Keycloak).ToNot(BeNil())
	Expect(auxSvcs.Keycloak.URL).ToNot(BeEmpty())
	Expect(auxSvcs.ModelRegistry).ToNot(BeNil())
	Expect(auxSvcs.ModelRegistry.URL).ToNot(BeEmpty())

	By("logging in to the Flight Control API as an administrator")
	_, err = login.LoginToAPIWithToken(suiteHarness)
	Expect(err).ToNot(HaveOccurred(), "admin login failed")

	By("registering the Keycloak OIDC provider the collector authenticates through")
	authProviderYAML := buildAuthProviderYAML(
		resources.authProviderName,
		auxSvcs.Keycloak.IssuerURL(),
		auxiliary.KeycloakE2ECatalogCollectorClientID,
		auxiliary.KeycloakE2ECatalogCollectorSecret,
	)
	out, err := applyAuthProvider(suiteHarness, authProviderYAML)
	Expect(err).ToNot(HaveOccurred(), "apply AuthProvider: %s", out)
	authProviderSet = true

	By("seeding the Model Registry with a LIVE model, SemVer version, and ModelCar artifact")
	Expect(seedModelRegistry(ctx, auxSvcs.ModelRegistry, resources)).To(Succeed(), "failed to seed the Model Registry")
	GinkgoWriter.Printf("[catalogcollector] seeded model %s (id=%s) version %s (id=%s) artifact %s\n",
		resources.modelName, resources.modelID, resources.modelVersionName, resources.modelVersionID, resources.artifactURI())

	By("preparing the collector namespace, credentials, and CA bundle")
	Expect(providers.Charts.EnsureNamespace(ctx, resources.namespace)).To(Succeed())
	Expect(providers.Charts.ApplySecret(ctx, resources.namespace, collectorOAuthSecretName, map[string]string{
		collectorOAuthClientIDKey: auxiliary.KeycloakE2ECatalogCollectorClientID,
		collectorOAuthSecretKey:   auxiliary.KeycloakE2ECatalogCollectorSecret,
	})).To(Succeed())

	caBundle, err := providers.Secrets.GetSecretData(ctx, providers.Infra.GetExternalNamespace(), flightctlCABundleSecret, flightctlCABundleSecretKey)
	Expect(err).ToNot(HaveOccurred(), "read the Flight Control CA bundle")
	Expect(caBundle).ToNot(BeEmpty(), "the Flight Control CA bundle must not be empty")
	Expect(providers.Charts.ApplyConfigMap(ctx, resources.namespace, collectorCAConfigMapName, map[string]string{
		collectorCAFileName: string(caBundle),
	})).To(Succeed())

	if image.Preloaded {
		By("using the collector image already present in the cluster image store")
	} else {
		By("loading the collector image built from this branch into the cluster")
		Expect(providers.Charts.LoadLocalImage(ctx, image.Reference)).To(Succeed())
	}

	By("installing the collector Helm chart")
	apiHost, apiPort, err := providers.Infra.GetServiceEndpoint(infra.ServiceAPI)
	Expect(err).ToNot(HaveOccurred(), "resolve the in-cluster Flight Control API endpoint")
	flightctlServer := fmt.Sprintf("https://%s:%d", apiHost, apiPort)

	collectorConfig := renderCollectorConfig(
		resources,
		auxSvcs.ModelRegistry.URL,
		flightctlServer,
		auxSvcs.Keycloak.TokenURL(),
	)
	GinkgoWriter.Printf("[catalogcollector] collector configuration:\n%s\n", collectorConfig)

	valuesFilePath, err = renderCollectorValues(collectorConfig)
	Expect(err).ToNot(HaveOccurred(), "render the collector Helm values")

	imageRepository, imageTag := splitImageRef(image.Reference)
	installErr := providers.Charts.InstallChart(ctx, infra.ChartRelease{
		Name:        resources.releaseName,
		Namespace:   resources.namespace,
		ChartPath:   collectorChartPath,
		ValuesFiles: []string{valuesFilePath},
		SetValues: map[string]string{
			"image.image":      imageRepository,
			"image.tag":        imageTag,
			"image.pullPolicy": "IfNotPresent",
		},
		Timeout: chartInstallTimeout,
	})
	// Set before asserting: an install that fails its readiness wait still
	// leaves the release and its pods behind, and teardown has to remove them.
	chartInstalled = true
	if installErr != nil {
		// A failing BeforeSuite never reaches AfterEach, so the pod state that
		// explains the failure has to be dumped here.
		dumpDiagnostics(ctx, providers.Charts, suiteHarness, resources)
	}
	Expect(installErr).ToNot(HaveOccurred(), "install the collector chart")
})

var _ = BeforeEach(func() {
	if suiteHarness == nil {
		return
	}
	suiteHarness.SetTestContext(testutil.StartSpecTracerForGinkgo(e2e.GetWorkerContext()))
})

var _ = AfterEach(func() {
	if suiteHarness == nil {
		return
	}
	if CurrentSpecReport().Failed() {
		// Dump while the collector is still running: once teardown removes the
		// release, the pod logs that explain the failure are gone.
		var charts infra.ChartDeployer
		if providers != nil {
			charts = providers.Charts
		}
		dumpDiagnostics(context.Background(), charts, suiteHarness, resources)
		suiteHarness.CaptureDeploymentLogsIfFailed()
	}
	suiteHarness.SetTestContext(e2e.GetWorkerContext())
})

var _ = AfterSuite(func() {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	// infra.RunCleanup runs every step even after an earlier one fails, then
	// reports the failures together. Asserting between steps instead would
	// abort this callback on the first problem and strand everything the later
	// steps own: a Helm release that refuses to uninstall would leave the
	// catalog resources, the AuthProvider, and the namespace behind, on a
	// cluster the next run reuses.
	//
	// Failures are returned, not logged and swallowed: a step that could not
	// remove suite-owned state has to fail the suite, because the state it
	// left is what breaks the following run.
	Expect(infra.RunCleanup(ctx, suiteCleanupSteps())).To(Succeed(), "suite teardown left resources behind")
})

// suiteCleanupSteps lists teardown in the order it has to happen. Each step
// guards on the flag its own setup step set, so a suite that skipped or failed
// early tears down only what it actually created.
//
// The order matters in one place only, and that dependency is kept inside a
// single step: nothing may delete the Catalog resources until the collector
// can no longer recreate them. Everything after that is independent.
func suiteCleanupSteps() []infra.CleanupStep {
	chartsAvailable := func() bool { return providers != nil && providers.Charts != nil && resources != nil }

	return []infra.CleanupStep{
		{
			Name: "stop the collector",
			Skip: func() bool { return !chartInstalled || !chartsAvailable() },
			Run:  stopCollector,
		},
		{
			// Safe only after the step above confirmed the collector is gone.
			Name: "delete the catalog resources the collector created",
			Skip: func() bool { return !chartInstalled || suiteHarness == nil || resources == nil },
			Run: func(context.Context) error {
				return errors.Join(
					suiteHarness.DeleteCatalogItemIgnoreNotFound(resources.catalogName, resources.itemName),
					suiteHarness.DeleteCatalogIgnoreNotFound(resources.catalogName),
				)
			},
		},
		{
			// The Model Registry REST API has no delete, so seeded resources
			// are archived out of the LIVE selection instead.
			Name: "archive the seeded Model Registry resources",
			Skip: func() bool { return auxSvcs == nil || auxSvcs.ModelRegistry == nil || resources == nil },
			Run: func(ctx context.Context) error {
				var errs []error
				if resources.modelVersionID != "" {
					errs = append(errs, auxSvcs.ModelRegistry.ArchiveModelVersion(ctx, resources.modelVersionID))
				}
				if resources.modelID != "" {
					errs = append(errs, auxSvcs.ModelRegistry.ArchiveRegisteredModel(ctx, resources.modelID))
				}
				return errors.Join(errs...)
			},
		},
		{
			Name: "delete the suite's AuthProvider",
			Skip: func() bool { return !authProviderSet || suiteHarness == nil || resources == nil },
			Run: func(context.Context) error {
				out, err := suiteHarness.ManageResource("delete", "authprovider", resources.authProviderName)
				if err != nil {
					return fmt.Errorf("delete authprovider %s: %w: %s", resources.authProviderName, err, out)
				}
				return nil
			},
		},
		{
			Name: "delete the collector namespace",
			Skip: func() bool { return !chartsAvailable() || resources.namespace == "" },
			Run: func(ctx context.Context) error {
				return providers.Charts.DeleteNamespace(ctx, resources.namespace, namespaceDeleteTimeout)
			},
		},
		{
			Name: "remove the rendered Helm values file",
			Skip: func() bool { return valuesFilePath == "" },
			Run: func(context.Context) error {
				if err := os.Remove(valuesFilePath); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove values file %s: %w", valuesFilePath, err)
				}
				return nil
			},
		},
		{
			Name: "clean up the aux services",
			Skip: func() bool { return auxSvcs == nil },
			Run: func(ctx context.Context) error {
				// Aux containers are reused between runs, so this is a no-op
				// unless reuse is turned off; it keeps the suite consistent
				// with the others.
				auxSvcs.Cleanup(ctx)
				return nil
			},
		},
	}
}

// stopCollector removes the Helm release and confirms the workload is gone, so
// that nothing can recreate the Catalog resources the next step deletes.
//
// If any of that fails the namespace is deleted as a fallback, which stops the
// collector just as effectively. The original failure is still reported: the
// release record is suite-owned state and leaving it behind is a problem in its
// own right.
func stopCollector(ctx context.Context) error {
	err := errors.Join(
		providers.Charts.UninstallChart(ctx, resources.releaseName, resources.namespace, chartUninstallTimeout),
		releaseGone(ctx),
		providers.Charts.WaitForWorkloadGone(ctx, resources.namespace, resources.podSelector(), workloadGoneTimeout),
	)
	if err == nil {
		return nil
	}
	if deleteErr := providers.Charts.DeleteNamespace(ctx, resources.namespace, namespaceDeleteTimeout); deleteErr != nil {
		return errors.Join(err, fmt.Errorf("namespace deletion fallback: %w", deleteErr))
	}
	return fmt.Errorf("%w (the collector was stopped by deleting namespace %s instead)", err, resources.namespace)
}

// releaseGone reports an error unless the Helm release no longer exists.
func releaseGone(ctx context.Context) error {
	installed, err := providers.Charts.ReleaseInstalled(ctx, resources.releaseName, resources.namespace)
	if err != nil {
		return fmt.Errorf("check whether release %s is gone: %w", resources.releaseName, err)
	}
	if installed {
		return fmt.Errorf("Helm release %s in namespace %s survived teardown", resources.releaseName, resources.namespace)
	}
	return nil
}
