package catalogcollector_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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

	// smokeModel is the content seeded before the collector is installed, so
	// the first scenario observes a genuine initial synchronization. Every
	// other scenario seeds its own content from its own spec.
	smokeModel *seededModel

	// seededModels records every Model Registry model this run created, in
	// creation order, so teardown archives exactly what was seeded and deletes
	// exactly the catalog items those models produced. Specs seed as they run,
	// so this cannot be derived up front.
	//
	// Ginkgo runs the specs of one suite serially within a process (parallel
	// runs are separate processes with their own copy of this state), so no
	// synchronization is needed here.
	seededModels []*seededModel

	valuesFilePath  string
	chartInstalled  bool
	authProviderSet bool
	// collectorStopped records that the collector workload is confirmed gone,
	// so nothing can recreate the Catalog resources teardown deletes next. It
	// is deliberately separate from the error the stop step returns: that error
	// answers "did teardown leave suite-owned state behind?", while this flag
	// answers the narrower question the dependent step needs, "can the
	// collector still write?". A namespace-deletion fallback that worked
	// answers the second question with "no" even though the step as a whole
	// failed.
	collectorStopped bool
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
	GinkgoWriter.Printf("[catalogcollector] run suffix %s, namespace %s, release %s\n",
		resources.suffix, resources.namespace, resources.releaseName)
	for _, name := range allScenarios {
		sc := resources.scenario(name)
		GinkgoWriter.Printf("[catalogcollector] scenario %s: catalog %s, pipeline %s, selection %q\n",
			sc.name, sc.catalogName, sc.pipelineName, sc.modelFilter())
	}

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
	// Seeded before the collector is installed so the first scenario observes
	// a real initial synchronization rather than an update to a catalog the
	// collector had already built.
	smoke := resources.scenario(scenarioSmoke)
	smokeModel, err = seedModel(ctx, auxSvcs.ModelRegistry, resources, smoke, "model",
		"Flight Control catalog collector e2e smoke test model",
		[]versionSpec{eligibleVersion(resources, smoke, artifactVersion)})
	Expect(err).ToNot(HaveOccurred(), "failed to seed the Model Registry")
	GinkgoWriter.Printf("[catalogcollector] seeded model %s (id=%s) version %s (id=%s) artifact %s\n",
		smokeModel.name, smokeModel.id, smokeModel.versions[0].name, smokeModel.versions[0].id, smokeModel.versions[0].uri)

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

// recordSeededModel adds a model to the fixture list teardown works from. It
// is called as soon as the Model Registry accepts the model, before its
// versions exist, so content created by a seeding call that fails halfway is
// still retired.
func recordSeededModel(model *seededModel) {
	seededModels = append(seededModels, model)
}

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
			// Safe only once the step above confirmed the collector is gone,
			// which is what collectorStopped reports. Without that confirmation
			// the deletion is skipped rather than attempted: a collector that
			// is still running would simply write the Catalog resources back,
			// turning a reported failure into a silent one. The stop step's own
			// error already says the resources were left behind.
			Name: "delete the catalog resources the collector created",
			Skip: func() bool {
				return !chartInstalled || !collectorStopped || suiteHarness == nil || resources == nil
			},
			Run: func(context.Context) error {
				var errs []error
				// Items first: a catalog cannot be removed while it still
				// holds items. Every seeded model maps to exactly one item
				// name in exactly one scenario catalog, and a scenario whose
				// item was already pruned by the collector is a not-found,
				// which these helpers accept.
				for _, model := range seededModels {
					errs = append(errs, suiteHarness.DeleteCatalogItemIgnoreNotFound(
						model.scenario.catalogName, model.itemName()))
				}
				// Every scenario's catalog is created by its source on the
				// first cycle, with or without items, so all of them are
				// deleted rather than only those that produced an item.
				for _, catalogName := range resources.catalogNames() {
					errs = append(errs, suiteHarness.DeleteCatalogIgnoreNotFound(catalogName))
				}
				return errors.Join(errs...)
			},
		},
		{
			// The Model Registry REST API has no delete, so seeded resources
			// are archived out of the LIVE selection instead.
			Name: "archive the seeded Model Registry resources",
			Skip: func() bool {
				return auxSvcs == nil || auxSvcs.ModelRegistry == nil || len(seededModels) == 0
			},
			Run: func(ctx context.Context) error {
				var errs []error
				for _, model := range seededModels {
					// Versions before their model, so nothing is left LIVE
					// under an archived parent.
					for _, version := range model.versions {
						if version.id != "" {
							errs = append(errs, auxSvcs.ModelRegistry.ArchiveModelVersion(ctx, version.id))
						}
					}
					if model.id != "" {
						errs = append(errs, auxSvcs.ModelRegistry.ArchiveRegisteredModel(ctx, model.id))
					}
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
//
// Whether the collector is gone and whether the step succeeded are two
// different answers, so they are reported through two different channels.
// collectorStopped is set on both paths that end with the workload down - the
// normal removal and a fallback that worked - and only on those, because it is
// what the dependent catalog deletion keys off. The returned error is
// unconditional once anything went wrong, and always wraps the original
// failures so the report names what actually broke.
func stopCollector(ctx context.Context) error {
	err := errors.Join(
		providers.Charts.UninstallChart(ctx, resources.releaseName, resources.namespace, chartUninstallTimeout),
		releaseGone(ctx),
		providers.Charts.WaitForWorkloadGone(ctx, resources.namespace, resources.podSelector(), workloadGoneTimeout),
	)
	if err == nil {
		collectorStopped = true
		return nil
	}
	if deleteErr := providers.Charts.DeleteNamespace(ctx, resources.namespace, namespaceDeleteTimeout); deleteErr != nil {
		// Neither path confirmed the collector is down, so collectorStopped
		// stays false and the catalog deletion that depends on it is skipped.
		// Saying so here is the only place a reader of the failure learns that
		// those resources were left behind on purpose.
		return errors.Join(
			err,
			fmt.Errorf("namespace deletion fallback: %w", deleteErr),
			fmt.Errorf("catalogs %s were left in place because the collector could not be confirmed stopped",
				strings.Join(resources.catalogNames(), ", ")),
		)
	}
	collectorStopped = true
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
