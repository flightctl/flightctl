package catalogcollector_test

import (
	"context"
	"errors"
	"fmt"
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

	// collectorInstance is the handle to the deployed collector. It is set as
	// soon as the deployment created anything, including when starting it
	// failed partway, because teardown has to remove what was created either
	// way. Nil means nothing was deployed and there is nothing to tear down.
	collectorInstance infra.CollectorInstance

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

	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	providers = setup.GetDefaultProviders()
	Expect(providers).ToNot(BeNil(), "infra providers must be initialized")
	Expect(providers.CollectorDeployment).ToNot(BeNil(), "a collector deployment provider is required")

	// Whether a collector can be deployed here at all is the deployment's
	// answer, not this suite's: it knows what it needs and whether it has it.
	// An error means the question could not be answered and fails the run; a
	// settled "no" skips, unless the run explicitly asked for this suite, in
	// which case skipping would report a green run that executed nothing.
	availability, err := providers.CollectorDeployment.Available(ctx)
	Expect(err).ToNot(HaveOccurred(), "determine whether a catalog collector can be deployed")
	if !availability.Available {
		Expect(availability.Required).To(BeFalse(), availability.Reason)
		Skip(availability.Reason)
	}

	// Only the API and the CLI are needed; no device agent takes part in
	// catalog synchronization.
	_, _, err = e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred(), "failed to set up harness")
	suiteHarness = e2e.GetWorkerHarness()

	resources, err = newTestResources()
	Expect(err).ToNot(HaveOccurred(), "failed to generate isolated resource names")
	GinkgoWriter.Printf("[catalogcollector] run suffix %s\n", resources.suffix)
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

	// The client the collector authenticates with comes from
	// flightctl-realm.json, which Keycloak imports when it starts with an
	// empty database. A Keycloak container left behind by a run that predates
	// that client keeps its old realm; "make stop-keycloak" once makes the
	// next run import the current file. See infra/auxiliary/keycloak.go.

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

	By("rendering the collector configuration for this deployment")
	deploymentContext, err := providers.CollectorDeployment.ConfigContext(ctx)
	Expect(err).ToNot(HaveOccurred(), "resolve the deployment's collector configuration context")

	collectorConfig := renderCollectorConfig(
		resources,
		deploymentContext,
		auxSvcs.ModelRegistry.URL,
		auxSvcs.Keycloak.TokenURL(),
	)
	GinkgoWriter.Printf("[catalogcollector] collector configuration:\n%s\n", collectorConfig)

	By("deploying the collector")
	var startErr error
	// The instance is assigned before the error is asserted: a start that
	// failed partway still created state, and this handle is what teardown
	// removes it with.
	collectorInstance, startErr = providers.CollectorDeployment.Start(ctx, infra.CollectorDeploymentConfig{
		InstanceID:        resources.suffix,
		Config:            []byte(collectorConfig),
		OAuthClientID:     auxiliary.KeycloakE2ECatalogCollectorClientID,
		OAuthClientSecret: auxiliary.KeycloakE2ECatalogCollectorSecret,
		StartTimeout:      collectorStartTimeout,
		StopTimeout:       collectorStopTimeout,
	})
	if startErr != nil {
		// A failing BeforeSuite never reaches AfterEach, so the state that
		// explains the failure has to be dumped here.
		dumpDiagnostics(ctx, collectorInstance, suiteHarness)
	}
	Expect(startErr).ToNot(HaveOccurred(), "deploy the collector")
	Expect(collectorInstance).ToNot(BeNil(), "a successful deployment must return an instance")

	By("waiting for the collector to report ready")
	readyErr := collectorInstance.WaitReady(ctx, collectorReadyTimeout)
	if readyErr != nil {
		dumpDiagnostics(ctx, collectorInstance, suiteHarness)
	}
	Expect(readyErr).ToNot(HaveOccurred(), "the collector must become ready")
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
		// deployment, the logs that explain the failure are gone.
		dumpDiagnostics(context.Background(), collectorInstance, suiteHarness)
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
	// steps own: a collector that refuses to stop would leave the catalog
	// resources, the AuthProvider, and the deployment behind, in an
	// environment the next run reuses.
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
	return []infra.CleanupStep{
		{
			Name: "stop the collector",
			Skip: func() bool { return collectorInstance == nil },
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
				return collectorInstance == nil || !collectorStopped || suiteHarness == nil || resources == nil
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
			// Whatever the deployment still owns - a namespace, a rendered
			// values file, a unit - is its own to remove, and it is idempotent
			// so this is safe after a stop that already deleted some of it as
			// its fallback.
			Name: "remove the collector deployment",
			Skip: func() bool { return collectorInstance == nil },
			Run:  func(ctx context.Context) error { return collectorInstance.Cleanup(ctx) },
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

// stopCollector takes the collector down so that nothing can recreate the
// Catalog resources the next step deletes.
//
// How that is done, and what to fall back to when it does not work, belongs to
// the deployment. What belongs here is the consequence: whether the collector
// is confirmed stopped and whether the step failed are two different answers,
// and this suite needs both. collectorStopped records the first, because it is
// what the dependent catalog deletion keys off, and it is set whenever the
// deployment confirms the collector is down - including when it had to fall
// back to a blunter mechanism and still has a failure to report. The error
// records the second, and always carries the original failures so the report
// names what actually broke.
func stopCollector(ctx context.Context) error {
	stopped, err := collectorInstance.Stop(ctx)
	collectorStopped = stopped
	if err == nil {
		return nil
	}
	if !stopped {
		// Which catalogs were left behind is this suite's knowledge, not the
		// deployment's. Saying so here is the only place a reader of the
		// failure learns they were skipped on purpose rather than forgotten.
		return errors.Join(err, fmt.Errorf(
			"catalogs %s were left in place because the collector could not be confirmed stopped",
			strings.Join(resources.catalogNames(), ", ")))
	}
	return err
}
