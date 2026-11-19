//go:build linux

package packagemodemigration_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	periodic "github.com/flightctl/flightctl/internal/periodic_checker"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	packagemodemigration "github.com/flightctl/flightctl/test/e2e/package_mode_migration"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	migrationFleetPrefix   = "mixed-os-mode-migration"
	migrationFleetLabelKey = "mixed-os-mode-migration-suite"
	migrationInlineName    = "mixed-os-mode-migration-inline"
	migrationConfigPath    = "/etc/flightctl/mixed-os-mode-migration.conf"

	// The two revisions of the common configuration. The second one is applied
	// after the migration, so that both OS modes have to accept a template
	// version that the migration itself did not write.
	migrationConfigBefore = "mixed-os-mode-migration=before"
	migrationConfigAfter  = "mixed-os-mode-migration=after"

	migrationEnrollTimeout = 5 * time.Minute
	migrationPollInterval  = 5 * time.Second
	periodicReadyTimeout   = 3 * time.Minute

	// osImageGuardFormat is the conditional the migration is expected to wrap a
	// fleet template OS image in. It is spelled out rather than imported so that
	// a change to the emitted guard fails here instead of quietly changing what
	// devices render.
	osImageGuardFormat = "{{ if deviceHasImageMode }}%s{{ end }}"
)

var _ = Describe("Mixed OS-mode fleet template migration", Ordered, Label("package-mode", "migration", "slow"), func() {
	var (
		harness       *e2e.Harness
		imageHarness  *e2e.Harness
		providers     *infra.Providers
		packageAgent  *e2e.PackageModeAgent
		fleetName     string
		selector      v1beta1.LabelSelector
		legacyOsImage string
		packageDevice string
		imageDevice   string

		// periodicStopRequested stays true from the moment the stop is asked
		// for until the worker is confirmed running again, so a stop that
		// partially succeeds and then fails is still restored.
		periodicStopRequested bool
		packageVersion        int
	)

	BeforeAll(func() {
		harness = e2e.GetWorkerHarness()
		providers = setup.GetDefaultProviders()
		Expect(providers).ToNot(BeNil())
		Expect(providers.Lifecycle).ToNot(BeNil(), "service lifecycle provider is required to sequence the periodic worker")

		// Step 1, before anything is created or changed: refuse to run unless
		// a census of the whole database says this deployment is really ours.
		By("verifying the whole deployment is empty and exclusively ours")
		orgID := requireExclusiveDeployment(harness, providers)

		// Step 2, still before any fixture exists: take the migration out of
		// play. On a genuinely fresh deployment the task has not completed yet
		// and would otherwise run over the fleet while it is being built, so
		// the gating has to come first rather than being repaired afterwards.
		//
		// The worker must already be running. This suite stops and restarts
		// it, and it has no business starting a worker that somebody else
		// deliberately stopped, so an already-stopped worker is a refusal
		// rather than a state to overwrite.
		By("holding the periodic worker down before any fixture exists")
		wasRunning, err := providers.Lifecycle.IsRunning(infra.ServicePeriodic)
		Expect(err).ToNot(HaveOccurred(), "checking whether the periodic worker is running")
		Expect(wasRunning).To(BeTrue(),
			"the periodic worker must be running before this suite starts: it stops and restarts the worker, "+
				"and will not start one that was already stopped")
		DeferCleanup(func() {
			// Restore only what this suite stopped, and treat a failure to
			// restore as a failure: leaving the worker down would silently
			// break whatever runs next.
			if !periodicStopRequested {
				return
			}
			Expect(providers.Lifecycle.Start(infra.ServicePeriodic)).
				To(Succeed(), "the periodic worker was stopped by this suite and could not be restarted")
			Expect(providers.Lifecycle.WaitForReady(infra.ServicePeriodic, periodicReadyTimeout)).
				To(Succeed(), "the periodic worker was restarted by this suite but never became ready")
		})
		// Recorded before the call, so a stop that half succeeds and then
		// reports an error is still restored.
		periodicStopRequested = true
		Expect(providers.Lifecycle.Stop(infra.ServicePeriodic)).To(Succeed())

		// Step 3: discard the migration's own checkpoint while nothing can
		// write it. A deployment that has already completed the migration
		// would otherwise return from every later poll without reading a
		// single fleet.
		By("discarding the one-shot migration checkpoint")
		Expect(resetMigrationCheckpoint(providers, orgID)).To(Succeed())

		// Only now does any fixture come into existence. A label-scoped sweep
		// is registered first so that anything created from here on is
		// collected even when the creation that would have registered its own
		// cleanup never returns. Harness.CleanUpTestResources deletes by this
		// test's test-id label and also removes the enrollment requests that
		// belong to it.
		ctx := harness.GetTestContext()
		DeferCleanup(func() {
			if err := harness.CleanUpTestResources(testutil.ResourceTypes[:]...); err != nil {
				GinkgoWriter.Printf("CleanUpTestResources(api harness): %v\n", err)
			}
		})
		fleetName = uniqueMigrationName(migrationFleetPrefix)
		selector = v1beta1.LabelSelector{
			MatchLabels: &map[string]string{migrationFleetLabelKey: fleetName},
		}
		legacyOsImage = harness.GetDeviceImageRefForFleet(
			migrationAuxSvcs.Registry.Host,
			migrationAuxSvcs.Registry.Port,
			testutil.DeviceTags.V2,
		)

		By("creating a legacy fleet whose template names an unguarded OS image")
		legacySpec, err := migrationDeviceSpec(legacyOsImage, migrationConfigBefore)
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.CreateOrUpdateTestFleet(fleetName, selector, legacySpec)).To(Succeed())
		DeferCleanup(func() {
			if err := harness.DeleteFleetIgnoreNotFound(fleetName); err != nil {
				GinkgoWriter.Printf("DeleteFleetIgnoreNotFound(%s): %v\n", fleetName, err)
			}
		})

		By("enrolling an image-mode device into the fleet")
		workerID := GinkgoParallelProcess()*100 + 1
		imageHarness, err = e2e.NewTestHarnessWithVMPool(ctx, workerID)
		Expect(err).ToNot(HaveOccurred())
		imageHarness.SetTestContext(ctx)
		DeferCleanup(func() {
			imageHarness.PrintAgentLogsIfFailed()
			imageHarness.CaptureDeploymentLogsIfFailed()
			// Scoped to this harness's own test-id labels; see
			// Harness.CleanUpTestResources.
			if err := imageHarness.CleanUpAllTestResources(); err != nil {
				GinkgoWriter.Printf("CleanUpAllTestResources(image-mode harness): %v\n", err)
			}
		})
		Expect(imageHarness.SetupVMFromPoolAndStartAgent(workerID)).To(Succeed())
		imageDevice, _ = imageHarness.EnrollAndWaitForOnlineStatus(map[string]string{migrationFleetLabelKey: fleetName})
		Expect(imageDevice).ToNot(BeEmpty())
		DeferCleanup(func() { deleteDeviceIfNamed(harness, imageDevice) })

		By("enrolling a package-mode device into the same fleet")
		startCtx, startCancel := context.WithTimeout(ctx, migrationEnrollTimeout)
		defer startCancel()
		packageAgent, err = e2e.StartPackageModeAgent(
			startCtx,
			e2e.GetAgentConfigDir(),
			migrationAuxSvcs.Registry.Host,
			migrationAuxSvcs.Registry.Port,
		)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer stopCancel()
			if err := packageAgent.Stop(stopCtx); err != nil {
				GinkgoWriter.Printf("Stop(package-mode agent): %v\n", err)
			}
		})
		// Approval creates the device before it reports online, and the helper
		// returns the identifier alongside a timeout error for exactly that
		// reason, so the cleanup is registered before the error is asserted.
		packageDevice, err = e2e.EnrollPackageModeAgent(ctx, harness, packageAgent,
			map[string]string{migrationFleetLabelKey: fleetName}, migrationEnrollTimeout, migrationPollInterval)
		DeferCleanup(func() { deleteDeviceIfNamed(harness, packageDevice) })
		Expect(err).ToNot(HaveOccurred())

		By("waiting for both devices to join the fleet")
		for _, deviceID := range []string{packageDevice, imageDevice} {
			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(deviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Metadata.Owner).ToNot(BeNil())
				g.Expect(*device.Metadata.Owner).To(Equal("Fleet/" + fleetName))
			}, migrationEnrollTimeout, migrationPollInterval).Should(Succeed())
		}

		// Rolling a fleet out to its devices is driven by the worker from
		// resource events (internal/tasks/fleet_rollout.go), not by the
		// periodic worker, so both devices still receive the legacy template
		// while the migration stays parked.
		By("confirming the unguarded template leaves the package-mode device behind")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(packageDevice)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(device).ToNot(BeNil())
			g.Expect(device.Spec).ToNot(BeNil())
			g.Expect(device.Spec.Os).ToNot(BeNil())
			g.Expect(device.Spec.Os.Image).To(Equal(legacyOsImage))
			g.Expect(device.Status).ToNot(BeNil())
			g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())

		By("waiting for the image-mode device to apply the OS target")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(imageDevice)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(device).ToNot(BeNil())
			g.Expect(device.Status).ToNot(BeNil())
			g.Expect(device.Status.Os.Image).To(ContainSubstring(testutil.DeviceTags.V2))
			g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusUpToDate))
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())

		By("reading back the pre-migration template")
		storedFleet, err := harness.GetFleet(fleetName)
		Expect(err).ToNot(HaveOccurred())
		Expect(storedFleet.Spec.Template.Spec.Os).ToNot(BeNil())
		Expect(storedFleet.Spec.Template.Spec.Os.Image).To(Equal(legacyOsImage),
			"the stored template must still name the raw image, or the migration would have nothing to prove")

		// Only the package-mode member's version is snapshotted: it is the one
		// whose rendered spec the migration actually changes.
		packageVersion, err = harness.GetCurrentDeviceRenderedVersion(packageDevice)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		// Dump while the fleet and both devices still exist: the cleanups
		// registered above remove them once the container finishes.
		if CurrentSpecReport().Failed() {
			dumpMigrationDiagnostics(harness, packageAgent, fleetName, packageDevice, imageDevice)
		}
	})

	It("guards the fleet template OS image and releases the package-mode device", func() {
		By("starting the periodic worker so its registered migration task runs")
		Expect(providers.Lifecycle.Start(infra.ServicePeriodic)).To(Succeed())
		Expect(providers.Lifecycle.WaitForReady(infra.ServicePeriodic, periodicReadyTimeout)).To(Succeed())
		periodicStopRequested = false

		By("waiting for the migration to rewrite the persisted fleet template")
		Eventually(func(g Gomega) {
			fleet, err := harness.GetFleet(fleetName)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(fleet.Spec.Template.Spec.Os).ToNot(BeNil())
			g.Expect(fleet.Spec.Template.Spec.Os.Image).To(Equal(guardedImage(legacyOsImage)))
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())

		By("waiting for the package-mode device to render no OS target and catch up")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(packageDevice)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(device).ToNot(BeNil())
			g.Expect(device.Spec).ToNot(BeNil())
			// The guard renders empty for a package-mode device, so the rendered
			// spec carries no OS target: an os block with an empty image, or none.
			if device.Spec.Os != nil {
				g.Expect(device.Spec.Os.Image).To(BeEmpty())
				g.Expect(device.Spec.Os.CatalogItemRef).To(BeNil())
			}
			g.Expect(device.Status).ToNot(BeNil())
			g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusUpToDate))
			g.Expect(renderedVersionOf(device)).To(BeNumerically(">", packageVersion),
				"the package-mode device must advance past the version it was stuck on")
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())

		// No rendered-version advance is asserted for the image-mode device
		// here, and none would be correct: the guard renders to the same image
		// string for a device that can apply one and the configuration is
		// untouched, so the rendered spec is identical and deviceRender leaves
		// the version alone by design (internal/tasks/device_render.go, "If
		// the device spec hasn't changed ... the rendered version is not
		// bumped"). What matters for this member is that the guard did not
		// take its OS target away. The next spec changes the configuration and
		// does require both members to advance.
		By("confirming the image-mode device keeps the OS target the guard renders for it")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(imageDevice)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(device).ToNot(BeNil())
			g.Expect(device.Spec).ToNot(BeNil())
			g.Expect(device.Spec.Os).ToNot(BeNil())
			g.Expect(device.Spec.Os.Image).To(Equal(legacyOsImage))
			g.Expect(device.Status).ToNot(BeNil())
			g.Expect(device.Status.Os.Image).To(ContainSubstring(testutil.DeviceTags.V2))
			g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusUpToDate))
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())
	})

	It("rolls a new common configuration change out to both OS modes", func() {
		packageBefore, err := harness.GetCurrentDeviceRenderedVersion(packageDevice)
		Expect(err).ToNot(HaveOccurred())
		imageBefore, err := harness.GetCurrentDeviceRenderedVersion(imageDevice)
		Expect(err).ToNot(HaveOccurred())

		By("changing the common configuration on the migrated fleet")
		updatedSpec, err := migrationDeviceSpec(guardedImage(legacyOsImage), migrationConfigAfter)
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.CreateOrUpdateTestFleet(fleetName, selector, updatedSpec)).To(Succeed())

		By("waiting for the package-mode device to apply the change")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(packageDevice)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(device).ToNot(BeNil())
			g.Expect(device.Status).ToNot(BeNil())
			g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusUpToDate))
			g.Expect(renderedVersionOf(device)).To(BeNumerically(">", packageBefore))
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())

		Eventually(func(g Gomega) {
			readCtx, readCancel := context.WithTimeout(harness.GetTestContext(), 30*time.Second)
			defer readCancel()
			content, err := packageAgent.ReadFile(readCtx, migrationConfigPath)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(content).To(ContainSubstring(migrationConfigAfter))
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())

		By("waiting for the image-mode device to apply the same change")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(imageDevice)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(device).ToNot(BeNil())
			g.Expect(device.Spec).ToNot(BeNil())
			g.Expect(device.Spec.Os).ToNot(BeNil())
			g.Expect(device.Spec.Os.Image).To(Equal(legacyOsImage))
			g.Expect(device.Status).ToNot(BeNil())
			g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusUpToDate))
			g.Expect(renderedVersionOf(device)).To(BeNumerically(">", imageBefore))
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())

		// Status alone would not show the file reached the disk, so the
		// image-mode member is read the same way the package-mode one is.
		Eventually(func(g Gomega) {
			stdout, err := imageHarness.VM.RunSSH([]string{"sudo", "cat", migrationConfigPath}, nil)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(stdout.String()).To(ContainSubstring(migrationConfigAfter))
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())

		By("confirming the fleet rollout finished instead of stalling on the package-mode device")
		Eventually(func(g Gomega) {
			fleet, err := harness.GetFleet(fleetName)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(fleet.Status).ToNot(BeNil())
			g.Expect(fleet.Status.DevicesSummary).ToNot(BeNil())
			g.Expect(fleet.Status.DevicesSummary.Total).To(BeEquivalentTo(2))
			g.Expect(fleet.Status.DevicesSummary.UpdateStatus[string(v1beta1.DeviceUpdatedStatusUpToDate)]).
				To(BeEquivalentTo(2))
			g.Expect(fleet.Status.DevicesSummary.UpdateStatus[string(v1beta1.DeviceUpdatedStatusOutOfDate)]).
				To(BeEquivalentTo(0))
		}, testutil.LONG_TIMEOUT, migrationPollInterval).Should(Succeed())
	})
})

// requireExclusiveDeployment fails the suite unless a read-only census of the
// whole database shows the deployment holds nothing.
//
// The census deliberately does not go through the API: ListFleets and
// ListDevices are filtered to the caller's organization
// (internal/service/fleet/handler.go passes orgId into a store whose queries
// carry "WHERE org_id = ..."), so an empty answer there says nothing about
// other tenants. Stopping the periodic worker scales a single deployment-wide
// service and its publisher schedules work for every organization, so the
// evidence has to be global too. infra.QueryDB reads the same deployment's
// database and is the mechanism other suites already use for this.
func requireExclusiveDeployment(harness *e2e.Harness, providers *infra.Providers) string {
	GinkgoHelper()

	orgID, err := harness.GetOrganizationID()
	Expect(err).ToNot(HaveOccurred(), "resolving the organization under test")

	query, err := packagemodemigration.InventoryQuery(orgID)
	Expect(err).ToNot(HaveOccurred(), "building the inventory query")

	raw, err := infra.QueryDB(providers, query)
	Expect(err).ToNot(HaveOccurred(), "reading the deployment inventory")

	inventory, err := packagemodemigration.ParseInventory(raw)
	Expect(err).ToNot(HaveOccurred(), "parsing the deployment inventory")

	Expect(packagemodemigration.EvaluateInventory(inventory, orgID)).To(Succeed())
	return orgID
}

// resetMigrationCheckpoint discards the one-shot fleet template migration
// checkpoint for the organization under test, so the next poll of the deployed
// periodic worker runs the migration again.
//
// It is only reached after requireExclusiveDeployment has confirmed, against
// the database rather than the API, that this is the deployment's only
// organization and that nothing exists in it.
func resetMigrationCheckpoint(providers *infra.Providers, orgID string) error {
	parsed, err := uuid.Parse(strings.TrimSpace(orgID))
	if err != nil {
		return fmt.Errorf("organization %q is not a UUID: %w", orgID, err)
	}
	consumer := string(periodic.PeriodicTaskTypePackageModeTemplateMigration)
	_, err = infra.QueryDB(providers, fmt.Sprintf(
		"DELETE FROM checkpoints WHERE consumer = '%s' AND key LIKE '%s/%%'", consumer, parsed.String()))
	return err
}

// deleteDeviceIfNamed removes a device that was actually created, so a cleanup
// registered before enrollment finished does nothing instead of failing.
func deleteDeviceIfNamed(harness *e2e.Harness, deviceID string) {
	if harness == nil || strings.TrimSpace(deviceID) == "" {
		return
	}
	if err := harness.DeleteDeviceIgnoreNotFound(deviceID); err != nil {
		GinkgoWriter.Printf("DeleteDeviceIgnoreNotFound(%s): %v\n", deviceID, err)
	}
}

// guardedImage returns the fleet template OS image as the migration is expected
// to leave it.
func guardedImage(osImage string) string {
	return fmt.Sprintf(osImageGuardFormat, osImage)
}

// migrationDeviceSpec returns the fleet template used by this suite: one OS
// image target and one inline configuration file shared by both OS modes.
func migrationDeviceSpec(osImage, configContent string) (v1beta1.DeviceSpec, error) {
	inlineConfig, err := e2e.NewInlineConfigSpec(migrationInlineName, []v1beta1.FileSpec{{
		Path:    migrationConfigPath,
		Content: configContent,
	}})
	if err != nil {
		return v1beta1.DeviceSpec{}, err
	}
	return v1beta1.DeviceSpec{
		Os:     &v1beta1.DeviceOsSpec{Image: osImage},
		Config: &[]v1beta1.ConfigProviderSpec{inlineConfig},
	}, nil
}

// renderedVersionOf returns the device's reported rendered version, or -1 when
// it has not reported one yet, so a comparison against a recorded version never
// mistakes a missing value for progress.
func renderedVersionOf(device *v1beta1.Device) int {
	if device == nil || device.Status == nil {
		return -1
	}
	version, err := strconv.Atoi(strings.TrimSpace(device.Status.Config.RenderedVersion))
	if err != nil {
		return -1
	}
	return version
}

// uniqueMigrationName returns a fleet name unique to this test process.
func uniqueMigrationName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// dumpMigrationDiagnostics writes the fleet template, both device statuses and
// the package-mode agent log for a failed run.
func dumpMigrationDiagnostics(harness *e2e.Harness, agent *e2e.PackageModeAgent, fleetName, packageDevice, imageDevice string) {
	GinkgoHelper()
	if harness == nil {
		return
	}
	if fleet, err := harness.GetFleet(fleetName); err != nil {
		GinkgoWriter.Printf("=== fleet %s: get failed: %v ===\n", fleetName, err)
	} else if fleet.Spec.Template.Spec.Os != nil {
		GinkgoWriter.Printf("=== fleet %s template os.image=%q ===\n", fleetName, fleet.Spec.Template.Spec.Os.Image)
	} else {
		GinkgoWriter.Printf("=== fleet %s template has no os ===\n", fleetName)
	}

	for label, deviceID := range map[string]string{"package-mode": packageDevice, "image-mode": imageDevice} {
		if strings.TrimSpace(deviceID) == "" {
			continue
		}
		device, err := harness.GetDevice(deviceID)
		if err != nil {
			GinkgoWriter.Printf("=== %s device %s: get failed: %v ===\n", label, deviceID, err)
			continue
		}
		var specImage string
		if device.Spec != nil && device.Spec.Os != nil {
			specImage = device.Spec.Os.Image
		}
		if device.Status == nil {
			GinkgoWriter.Printf("=== %s device %s specOsImage=%q status=nil ===\n", label, deviceID, specImage)
			continue
		}
		GinkgoWriter.Printf("=== %s device %s specOsImage=%q updated=%s rendered=%s statusOsImage=%s conditions=%v ===\n",
			label, deviceID, specImage, device.Status.Updated.Status,
			device.Status.Config.RenderedVersion, device.Status.Os.Image, device.Status.Conditions)
	}

	if agent == nil {
		return
	}
	logCtx, logCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer logCancel()
	logs, err := agent.GetAgentLogs(logCtx)
	if err != nil {
		GinkgoWriter.Printf("=== package-mode agent logs: get failed: %v ===\n", err)
		return
	}
	GinkgoWriter.Printf("=== package-mode agent logs ===\n%s\n", logs)
}
