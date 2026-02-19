package rollout_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Rollout Policies", Label("rollout"), func() {
	var (
		ctx context.Context
		tc  *TestContext
	)

	BeforeEach(func() {
		// Get harness and context directly - no shared package-level variables
		harness := e2e.GetWorkerHarness()
		testCtx := e2e.GetWorkerContext()

		// Initialize the test context
		ctx = testCtx
		testID := harness.GetTestIDFromContext()
		GinkgoWriter.Printf("Test ID: %s\n", testID)
		tc = setupTestContext(ctx)

	})

	AfterEach(func() {
		for i, harness := range tc.harnesses {
			if harness != nil {
				GinkgoWriter.Printf("Cleaning up harness for rollout worker %d\n", i)
				err := harness.CleanUpAllTestResources()
				if err != nil {
					Fail(fmt.Sprintf("Error cleaning up test resources: %v", err))
				}
				e2e.Cleanup(harness)
			}
		}
		tc.harnesses = nil
		tc.deviceIDs = nil

	})

	Context("Multi Device Selection", Label("79648"), func() {
		It("should select devices correctly based on BatchSequence strategy", func() {
			By("create a fleet and Enroll devices into the fleet")

			bsq1 := api.BatchSequence{
				Sequence: &[]api.Batch{
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteMadrid},
						},
						Limit: intLimit(1),
					},
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteMadrid},
						},
						Limit: percentageLimit("50%"),
					},
				},
			}

			labelsList := []map[string]string{
				{labelSite: siteMadrid},
				{labelSite: siteMadrid},
				{labelSite: siteMadrid},
				{labelSite: siteParis},
			}

			err := tc.setupFleetAndDevices(ctx, 4, labelsList)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Fleet and devices setup completed\n")

			deviceSpec, err := tc.createDeviceSpec()
			Expect(err).ToNot(HaveOccurred())

			// Wait for devices to stabilize after enrollment
			waitDuration, _ := time.ParseDuration(DEVICEWAITTIME)
			time.Sleep(waitDuration)

			// Update fleet with template
			err = tc.harness.CreateOrUpdateTestFleet(fleetName, createFleetSpec(bsq1, lo.ToPtr(api.Percentage("50%")), deviceSpec))
			Expect(err).ToNot(HaveOccurred())

			By("Verifying the first batch selects 1 device")

			tc.harness.WaitForBatchStart(fleetName, 0)

			By("Verifying the first batch selects 1 device")
			selectedDevices, err := tc.harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(1))
			Expect((*selectedDevices[0].Metadata.Labels)[labelSite]).To(Equal(siteMadrid))

			tc.harness.WaitForBatchStart(fleetName, 1)

			By("Verifying the second batch selects 50% of remaining devices")
			selectedDevices, err = tc.harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(1))
			Expect((*selectedDevices[0].Metadata.Labels)[labelSite]).To(Equal(siteMadrid))

			Expect(err).ToNot(HaveOccurred())
			By("Here we expect remaining 2 devices to be selected")
			tc.harness.WaitForBatchStart(fleetName, 2)
			selectedDevices, err = tc.harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(2))

			By("Verifying the application is updated on all devices")
			tc.harness.WaitForBatchStart(fleetName, 3)
			err = tc.verifyAllDevicesUpdated(4)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("Rollout Disruption Budget", Label("79649"), func() {
		It("should enforce the rollout disruption budget during rollouts", func() {
			By("creating a fleet with disruption budget")

			labelsList := []map[string]string{
				{labelSite: siteMadrid, labelFunction: functionWeb},
				{labelSite: siteMadrid, labelFunction: functionDb},
				{labelSite: siteMadrid, labelFunction: functionWeb},
			}

			err := tc.setupFleetAndDevices(ctx, 3, labelsList)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec, err := tc.createDeviceSpec()
			Expect(err).ToNot(HaveOccurred())

			newRenderedVersion, err := tc.harness.PrepareNextDeviceVersion(tc.deviceIDs[0])
			Expect(err).ToNot(HaveOccurred())

			fleetSpec := createFleetSpecWithoutDeviceSelection(lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec)
			fleetSpec.RolloutPolicy.DisruptionBudget = createDisruptionBudget(2, 2, []string{})

			err = tc.harness.CreateOrUpdateTestFleet(fleetName, fleetSpec)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying that the disruption budget is respected")
			// Get unavailable devices per group
			unavailableDevices, err := tc.harness.GetUnavailableDevicesPerGroup(fleetName, []string{labelSite, labelFunction})
			Expect(err).ToNot(HaveOccurred())

			for _, group := range unavailableDevices {
				Expect(len(group)).To(BeNumerically("<=", 1), "Should have at most 1 unavailable device per group")
			}

			for _, deviceID := range tc.deviceIDs {
				err = tc.harness.WaitForDeviceNewRenderedVersion(deviceID, newRenderedVersion)
				Expect(err).ToNot(HaveOccurred())
			}

			By("Verifying all devices are eventually updated")
			err = tc.verifyAllDevicesUpdated(3)
			Expect(err).ToNot(HaveOccurred())

			By("Checking Disruption Budget by Grouping Devices")
			err = tc.updateAppVersion(util.SleepAppTags.V2)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec, err = tc.createDeviceSpec()
			Expect(err).ToNot(HaveOccurred())

			fleetSpec = createFleetSpecWithoutDeviceSelection(lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec)
			fleetSpec.RolloutPolicy.DisruptionBudget = createDisruptionBudget(2, 0, []string{labelSite})

			err = tc.harness.CreateOrUpdateTestFleet(fleetName, fleetSpec)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying that the disruption budget is respected")
			// Get unavailable devices per group
			unavailableDevices, err = tc.harness.GetUnavailableDevicesPerGroup(fleetName, []string{labelSite, labelFunction})
			Expect(err).ToNot(HaveOccurred())

			for _, group := range unavailableDevices {
				Expect(len(group)).To(BeNumerically("==", 2), "Should select 2 devices in 1st batch")
			}
		})
	})

	Context("Failed Rollout", Label("79650"), func() {
		It("should pause the rollout if the success threshold is not met", func() {
			By("creating a fleet with success threshold")

			bsq2 := api.BatchSequence{
				Sequence: &[]api.Batch{
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteMadrid},
						},
						Limit: intLimit(1),
					},
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteParis},
						},
						Limit: intLimit(1),
					},
				},
			}

			labelsList := []map[string]string{
				{labelSite: siteMadrid},
				{labelSite: siteParis},
			}

			err := tc.setupFleetAndDevices(ctx, 2, labelsList)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec, err := tc.createDeviceSpec()
			Expect(err).ToNot(HaveOccurred())

			deviceVersions := make(map[string]int)
			for _, deviceID := range tc.deviceIDs {
				deviceVersions[deviceID], err = tc.harness.PrepareNextDeviceVersion(deviceID)
				Expect(err).ToNot(HaveOccurred())
			}

			By("Simulating a failure in the first batch")
			for _, harness := range tc.harnesses {
				h := harness // capture per-iteration
				DeferCleanup(func() { _ = h.FixNetworkFailure() })
				err = h.SimulateNetworkFailure()
				Expect(err).ToNot(HaveOccurred())
			}

			err = tc.harness.CreateOrUpdateTestFleet(fleetName, createFleetSpec(bsq2, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec))
			Expect(err).ToNot(HaveOccurred())

			tc.harness.WaitForBatchStart(fleetName, 0)

			tc.harness.WaitForFleetUpdateToFail(fleetName)

			By("Verifying that the rollout is paused due to unmet success threshold")
			rolloutStatus, err := tc.harness.GetRolloutStatus(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(rolloutStatus.Status).To(Equal(api.ConditionStatusFalse), "Rollout Update Status should be false")
			Expect(rolloutStatus.Reason).To(Equal(api.RolloutSuspendedReason), "Rollout should be paused when success threshold is not met")

			By("Fixing the failed device and verifying the rollout continues")
			for _, harness := range tc.harnesses {
				h := harness // capture per-iteration
				err = h.FixNetworkFailure()
				Expect(err).ToNot(HaveOccurred())
			}

			// Wait for rollout to continue
			By("Verifying that the rollout is resumed")
			err = tc.harness.WaitForDeviceNewRenderedVersion(tc.deviceIDs[0], deviceVersions[tc.deviceIDs[0]])
			Expect(err).ToNot(HaveOccurred())

			By("Verifying the first device is updated")
			// Verify that the first device is updated
			updatedDevices, err := tc.harness.GetUpdatedDevices(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(updatedDevices)).To(Equal(1), "Only One device should be updated")

			By("Verifying a new rollout can be triggered")
			deviceVersions[tc.deviceIDs[0]], err = tc.harness.PrepareNextDeviceVersion(tc.deviceIDs[0])
			Expect(err).ToNot(HaveOccurred())

			// update Name of app version in device spec to trigger a new rollout
			err = tc.updateAppVersion(util.SleepAppTags.V2)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec, err = tc.createDeviceSpec()
			Expect(err).ToNot(HaveOccurred())

			err = tc.harness.CreateOrUpdateTestFleet(fleetName, createFleetSpec(bsq2, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec))
			Expect(err).ToNot(HaveOccurred())

			// Wait for all devices to be updated
			for _, deviceID := range tc.deviceIDs {
				err = tc.harness.WaitForDeviceNewRenderedVersion(deviceID, deviceVersions[deviceID])
				Expect(err).ToNot(HaveOccurred())
			}

			err = tc.verifyAllDevicesUpdated(2)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("Template Version Update During Rollout", Label("79651"), func() {
		It("should handle updating template version during an active rollout", func() {
			By("creating a fleet with batch sequence rollout policy")

			bsq3 := api.BatchSequence{
				Sequence: &[]api.Batch{
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteMadrid},
						},
						Limit: intLimit(1),
					},
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteParis},
						},
						Limit: intLimit(1),
					},
				},
			}

			labelsList := []map[string]string{
				{labelSite: siteMadrid},
				{labelSite: siteParis},
			}

			err := tc.setupFleetAndDevices(ctx, 2, labelsList)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec, err := tc.createDeviceSpec()
			Expect(err).ToNot(HaveOccurred())

			err = tc.harness.CreateOrUpdateTestFleet(fleetName, createFleetSpec(bsq3, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec))
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for the first batch to start rolling out")
			tc.harness.WaitForBatchStart(fleetName, 1)

			By("Updating the fleet template version during rollout")
			err = tc.updateAppVersion(util.SleepAppTags.V2)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec, err = tc.createDeviceSpec()
			Expect(err).ToNot(HaveOccurred())

			err = tc.harness.CreateOrUpdateTestFleet(fleetName, createFleetSpec(bsq3, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec))
			Expect(err).ToNot(HaveOccurred())

			newRenderedVersion, err := tc.harness.PrepareNextDeviceVersion(tc.deviceIDs[0])
			Expect(err).ToNot(HaveOccurred())

			By("Verifying that devices ae updated to new template version of the updated template")
			for _, deviceID := range tc.deviceIDs {
				err = tc.harness.WaitForDeviceNewRenderedVersion(deviceID, newRenderedVersion)
				Expect(err).ToNot(HaveOccurred())
			}

			err = tc.verifyAllDevicesUpdated(2)
			Expect(err).ToNot(HaveOccurred())

		})
	})

	Context("Changing Rollout Flow During Application Upgrade", Label("79652"), func() {
		It("should handle changes to batch sequence during an application rollout", func() {
			By("creating a fleet with initial batch sequence rollout policy")

			// Define the initial batch sequence with minimum required batches
			initialBatchSequence := api.BatchSequence{
				Sequence: &[]api.Batch{
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteMadrid},
						},
						Limit:            intLimit(1),
						SuccessThreshold: lo.ToPtr(api.Percentage(SuccessThreshold)),
					},
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteRome},
						},
						Limit:            intLimit(1),
						SuccessThreshold: lo.ToPtr(api.Percentage(SuccessThreshold)),
					},
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{},
						},
						Limit:            percentageLimit(SuccessThreshold),
						SuccessThreshold: lo.ToPtr(api.Percentage(SuccessThreshold)),
					},
				},
			}

			labelsList := []map[string]string{
				{labelSite: siteMadrid},
				{labelSite: siteParis},
				{labelSite: siteRome},
			}

			err := tc.setupFleetAndDevices(ctx, 3, labelsList)

			Expect(err).ToNot(HaveOccurred())

			deviceSpec, err := tc.createDeviceSpec()
			Expect(err).ToNot(HaveOccurred())

			err = tc.harness.CreateOrUpdateTestFleet(fleetName, createFleetSpec(initialBatchSequence, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec))
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for the first batch to start rolling out")
			tc.harness.WaitForBatchStart(fleetName, 1)

			rolloutStatus, err := tc.harness.GetRolloutStatus(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(rolloutStatus.Status).To(Equal(api.ConditionStatusTrue), "A new rollout should be active")

			updatedBatchSequence := api.BatchSequence{
				Sequence: &[]api.Batch{
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteParis},
						},
						Limit:            intLimit(1),
						SuccessThreshold: lo.ToPtr(api.Percentage(SuccessThreshold)),
					},
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteRome},
						},
						Limit:            intLimit(1),
						SuccessThreshold: lo.ToPtr(api.Percentage(SuccessThreshold)),
					},
					{
						Selector: &api.LabelSelector{
							MatchLabels: &map[string]string{labelSite: siteMadrid},
						},
						Limit:            intLimit(1),
						SuccessThreshold: lo.ToPtr(api.Percentage(SuccessThreshold)),
					},
				},
			}

			err = tc.harness.CreateOrUpdateTestFleet(fleetName, createFleetSpec(updatedBatchSequence, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec))
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for the first batch of the new flow to start and verifying Paris device is selected")
			// Poll quickly to catch batch 0 before it completes and verify Paris is selected
			Eventually(func() error {
				// Get current batch number
				batchNumber, err := tc.getCurrentBatchNumber(ctx, fleetName)
				if err != nil {
					return err
				}

				// Only check when we're exactly at batch 0
				if batchNumber != 0 {
					return fmt.Errorf("batch 0 already completed, now at batch %d", batchNumber)
				}

				// Now get selected devices for batch 0
				selectedDevices, err := tc.harness.GetSelectedDevicesForBatch(fleetName)
				if err != nil {
					return fmt.Errorf("failed to get selected devices: %w", err)
				}
				if len(selectedDevices) != 1 {
					return fmt.Errorf("expected 1 selected device, got %d", len(selectedDevices))
				}

				// Verify the Paris device is selected
				site := (*selectedDevices[0].Metadata.Labels)[labelSite]
				if site != siteParis {
					return fmt.Errorf("expected Paris device, got %s", site)
				}
				return nil
			}, LONGTIMEOUT, FASTPOLLING).Should(Succeed())

		})
	})
})

const (
	fleetName        = "rollout"
	siteMadrid       = "madrid"
	siteParis        = "paris"
	siteRome         = "rome"
	functionWeb      = "web"
	functionDb       = "db"
	labelSite        = "site"
	labelFunction    = "function"
	SuccessThreshold = "100%"
)

var testFleetSelector = api.LabelSelector{
	MatchLabels: &map[string]string{"fleet": fleetName},
}

func percentageLimit(p api.Percentage) *api.Batch_Limit {
	ret := &api.Batch_Limit{}
	Expect(ret.FromPercentage(p)).ToNot(HaveOccurred())
	return ret
}

func intLimit(i int) *api.Batch_Limit {
	ret := &api.Batch_Limit{}
	Expect(ret.FromBatchLimit1(i)).ToNot(HaveOccurred())
	return ret
}

func rolloutDeviceSelection(b api.BatchSequence) *api.RolloutDeviceSelection {
	ret := &api.RolloutDeviceSelection{}
	Expect(ret.FromBatchSequence(b)).ToNot(HaveOccurred())
	return ret
}

func createFleetSpec(b api.BatchSequence, threshold *api.Percentage, testFleetSpec api.DeviceSpec) api.FleetSpec {
	return api.FleetSpec{
		RolloutPolicy: &api.RolloutPolicy{
			DefaultUpdateTimeout: lo.ToPtr(DEFAULTUPDATETIMEOUT),
			DeviceSelection:      rolloutDeviceSelection(b),
			SuccessThreshold:     threshold,
		},
		Selector: &testFleetSelector,
		Template: struct {
			Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
			Spec     api.DeviceSpec  "json:\"spec\""
		}{
			Spec: testFleetSpec,
		},
	}
}

func createFleetSpecWithoutDeviceSelection(threshold *api.Percentage, testFleetSpec api.DeviceSpec) api.FleetSpec {
	return api.FleetSpec{
		RolloutPolicy: &api.RolloutPolicy{
			SuccessThreshold: threshold,
		},
		Selector: &testFleetSelector,
		Template: struct {
			Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
			Spec     api.DeviceSpec  "json:\"spec\""
		}{
			Spec: testFleetSpec,
		},
	}
}

func createDisruptionBudget(maxUnavailable, minAvailable int, groupBy []string) *api.DisruptionBudget {
	return &api.DisruptionBudget{
		GroupBy:        &groupBy,
		MaxUnavailable: lo.ToPtr(maxUnavailable),
		MinAvailable:   lo.ToPtr(minAvailable),
	}
}

// TestContext encapsulates common test setup and configuration
type TestContext struct {
	harness       *e2e.Harness
	harnesses     []*e2e.Harness
	deviceIDs     []string
	composeApp    api.ComposeApplication
	sleepAppImage string
}

func setupTestContext(ctx context.Context) *TestContext {
	// Get harness directly - no shared package-level variable
	harness := e2e.GetWorkerHarness()

	sleepAppImage := util.NewSleepAppImageReference(util.SleepAppTags.V1).String()

	composeApp := api.ComposeApplication{
		Name:    lo.ToPtr("sleepapp"),
		AppType: api.AppTypeCompose,
	}
	err := composeApp.FromImageApplicationProviderSpec(api.ImageApplicationProviderSpec{
		Image: sleepAppImage,
	})
	if err != nil {
		panic(fmt.Sprintf("failed to set image on compose app: %v", err))
	}

	return &TestContext{
		harness:       harness,
		composeApp:    composeApp,
		sleepAppImage: sleepAppImage,
	}
}

func (tc *TestContext) setupFleetAndDevices(context context.Context, numDevices int, labelsList []map[string]string) error {

	err := tc.harness.CreateOrUpdateTestFleet(fleetName, testFleetSelector, api.DeviceSpec{})
	if err != nil {
		return err
	}
	// Create multiple devices using the resources package
	tc.deviceIDs = make([]string, numDevices)
	tc.harnesses = make([]*e2e.Harness, numDevices)

	// Use goroutines to set up devices concurrently
	GinkgoWriter.Printf("Creating %d device VMs in parallel...\n", numDevices)
	var wg sync.WaitGroup
	errChan := make(chan error, numDevices)

	for i := 0; i < numDevices; i++ {
		wg.Add(1)
		go func(index int) {
			defer GinkgoRecover()
			defer wg.Done()
			testID := tc.harness.GetTestIDFromContext()
			GinkgoWriter.Printf("📦 [VM %d] Starting creation (Test ID: %s)\n", index+1, testID)

			// Use fresh VMs from pool (no snapshots, overlay disks, 1GB RAM)
			// This avoids snapshot revert issues while still using pool management
			vmHarness, err := e2e.NewTestHarnessWithFreshVMFromPool(context, 1000+index)
			if err != nil {
				errChan <- fmt.Errorf("VM %d: %w", index+1, err)
				return
			}

			// Set the test context to inherit the same test ID as the main harness
			vmHarness.SetTestContext(tc.harness.GetTestContext())
			testID = vmHarness.GetTestIDFromContext()
			GinkgoWriter.Printf("✅ [VM %d] Created successfully (Test ID: %s)\n", index+1, testID)
			tc.harnesses[index] = vmHarness

			labels := labelsList[index]
			labels["fleet"] = fleetName

			GinkgoWriter.Printf("🔄 [VM %d] Enrolling device...\n", index+1)
			deviceID, device := vmHarness.EnrollAndWaitForOnlineStatus(labels)
			if device == nil {
				errChan <- fmt.Errorf("VM %d: failed to enroll - device is nil", index+1)
				return
			}
			tc.deviceIDs[index] = deviceID
			GinkgoWriter.Printf("✅ [VM %d] Enrolled successfully (ID: %s)\n", index+1, deviceID)

		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	GinkgoWriter.Printf("✅ All %d devices created and enrolled successfully\n", numDevices)
	return nil
}

func (tc *TestContext) createDeviceSpec() (api.DeviceSpec, error) {
	var appSpec api.ApplicationProviderSpec
	err := appSpec.FromComposeApplication(tc.composeApp)
	if err != nil {
		return api.DeviceSpec{}, err
	}

	return api.DeviceSpec{
		Applications: &[]api.ApplicationProviderSpec{appSpec},
	}, nil
}

func (tc *TestContext) updateAppVersion(version string) error {
	tc.composeApp.Name = lo.ToPtr(fmt.Sprintf("sleepapp-%s", version))
	return tc.composeApp.FromImageApplicationProviderSpec(api.ImageApplicationProviderSpec{
		Image: util.NewSleepAppImageReference(version).String(),
	})
}

func (tc *TestContext) verifyAllDevicesUpdated(expectedCount int) error {
	// Add retries for 5 minutes with Eventually pattern
	Eventually(func() error {
		updatedDevices, err := tc.harness.GetUpdatedDevices(fleetName)
		if err != nil {
			return err
		}

		if len(updatedDevices) != expectedCount {
			return fmt.Errorf("expected %d devices to be updated, but got %d", expectedCount, len(updatedDevices))
		}

		for _, device := range updatedDevices {
			deviceName := *device.Metadata.Name
			if len(device.Status.Applications) == 0 {
				return fmt.Errorf("device %s has no applications reported yet", deviceName)
			}

			app := device.Status.Applications[0]
			if app.Status != api.ApplicationStatusRunning {
				return fmt.Errorf("device %s application %q is not running (status=%q)", deviceName, app.Name, app.Status)
			}
			if tc.composeApp.Name != nil && app.Name != *tc.composeApp.Name {
				return fmt.Errorf("device %s application name is %q, expected %q", deviceName, app.Name, *tc.composeApp.Name)
			}
		}
		return nil
	}, MEDIUMTIMEOUT, POLLINGINTERVAL).Should(Succeed())
	return nil
}

// getCurrentBatchNumber retrieves the current batch number from fleet annotations
func (tc *TestContext) getCurrentBatchNumber(ctx context.Context, fleetName string) (int, error) {
	response, err := tc.harness.Client.GetFleetWithResponse(ctx, fleetName, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get fleet: %w", err)
	}
	if response == nil || response.JSON200 == nil {
		return 0, fmt.Errorf("fleet response is nil")
	}

	fleet := response.JSON200
	annotations := fleet.Metadata.Annotations
	if annotations == nil {
		return 0, fmt.Errorf("fleet annotations are nil")
	}

	batchNumberStr, ok := (*annotations)[api.FleetAnnotationBatchNumber]
	if !ok {
		return 0, fmt.Errorf("batch number annotation not found")
	}

	batchNumber, err := strconv.Atoi(batchNumberStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse batch number: %w", err)
	}

	return batchNumber, nil
}
