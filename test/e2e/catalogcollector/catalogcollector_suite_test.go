package catalogcollector_test

import (
	"context"
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
	"github.com/sirupsen/logrus"
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

	collectorImage, err := resolveCollectorImage(ctx, providers.Charts)
	Expect(err).ToNot(HaveOccurred(), "failed to look up the collector image under test")
	if collectorImage == "" {
		Skip(fmt.Sprintf(
			"collector image not found locally (looked for %s and %s). Build it from this branch with 'make flightctl-catalog-collector-container', or point %s at an image the cluster can use",
			collectorImageEL9, collectorImageGeneric, collectorImageEnv,
		))
	}
	GinkgoWriter.Printf("[catalogcollector] using collector image %s\n", collectorImage)

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

	By("loading the collector image built from this branch into the cluster")
	Expect(providers.Charts.LoadLocalImage(ctx, collectorImage)).To(Succeed())

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

	imageRepository, imageTag := splitImageRef(collectorImage)
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

	if valuesFilePath != "" {
		if err := os.Remove(valuesFilePath); err != nil && !os.IsNotExist(err) {
			logrus.Warnf("Failed to remove values file %s: %v", valuesFilePath, err)
		}
	}

	// Every step guards on the flag set by its own setup step, so a suite that
	// skipped or failed early tears down only what it actually created.
	if chartInstalled {
		By("uninstalling the collector Helm chart")
		Expect(providers.Charts.UninstallChart(ctx, resources.releaseName, resources.namespace, chartUninstallTimeout)).
			To(Succeed(), "uninstall the collector chart")

		installed, err := providers.Charts.ReleaseInstalled(ctx, resources.releaseName, resources.namespace)
		Expect(err).ToNot(HaveOccurred(), "check whether the collector release is gone")
		Expect(installed).To(BeFalse(), "the collector Helm release must not survive teardown")

		Expect(providers.Charts.WaitForWorkloadGone(ctx, resources.namespace, resources.podSelector(), workloadGoneTimeout)).
			To(Succeed(), "the collector pods must be removed by teardown")

		// The collector is gone by now, so nothing recreates the resources it
		// wrote and they can be deleted for good.
		By("deleting the catalog resources the collector created")
		if err := suiteHarness.DeleteCatalogItemIgnoreNotFound(resources.catalogName, resources.itemName); err != nil {
			logrus.Warnf("Failed to delete catalog item %s/%s: %v", resources.catalogName, resources.itemName, err)
		}
		if err := suiteHarness.DeleteCatalogIgnoreNotFound(resources.catalogName); err != nil {
			logrus.Warnf("Failed to delete catalog %s: %v", resources.catalogName, err)
		}
	}

	// The Model Registry REST API has no delete, so seeded resources are
	// archived out of the LIVE selection instead.
	if auxSvcs != nil && auxSvcs.ModelRegistry != nil && resources != nil {
		By("archiving the seeded Model Registry resources")
		if resources.modelVersionID != "" {
			if err := auxSvcs.ModelRegistry.ArchiveModelVersion(ctx, resources.modelVersionID); err != nil {
				logrus.Warnf("Failed to archive model version %s: %v", resources.modelVersionID, err)
			}
		}
		if resources.modelID != "" {
			if err := auxSvcs.ModelRegistry.ArchiveRegisteredModel(ctx, resources.modelID); err != nil {
				logrus.Warnf("Failed to archive registered model %s: %v", resources.modelID, err)
			}
		}
	}

	if authProviderSet {
		By("removing the suite's AuthProvider")
		if _, err := suiteHarness.ManageResource("delete", "authprovider", resources.authProviderName); err != nil {
			logrus.Warnf("Failed to delete authprovider %s: %v", resources.authProviderName, err)
		}
	}

	if providers != nil && providers.Charts != nil && resources != nil && resources.namespace != "" {
		By("deleting the collector namespace")
		Expect(providers.Charts.DeleteNamespace(ctx, resources.namespace, namespaceDeleteTimeout)).
			To(Succeed(), "the collector namespace must be removed by teardown")
	}

	if auxSvcs != nil {
		// Aux containers are reused between runs, so this is a no-op unless
		// reuse is turned off; it keeps the suite consistent with the others.
		auxSvcs.Cleanup(ctx)
	}
})
