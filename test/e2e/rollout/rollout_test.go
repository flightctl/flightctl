package rollout_test

import (
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Rollout Policies", func() {

	const (
		fleetName      = "rollout"
		siteMadrid     = "madrid"
		siteParis      = "paris"
		siteRome       = "rome"
		functionWeb    = "web"
		functionDb     = "db"
		labelSite      = "site"
		labelFunction  = "function"
		
		defaultSleepTime = 30 * time.Second
		setupSleepTime   = 2 * time.Minute
		completionTimeout = 5 * time.Minute
	)

	var (
		harness *e2e.Harness

		testFleetSelector = api.LabelSelector{
			MatchLabels: &map[string]string{"fleet": fleetName},
		}

		extIP = harness.RegistryEndpoint()
		sleepAppImage = fmt.Sprintf("%s/sleep-app:v1", extIP)

		applicationConfig = api.ImageApplicationProviderSpec{
			Image: sleepAppImage,
		}

		appType = api.AppType("compose")

		applicationSpec = api.ApplicationProviderSpec{
			Name:    lo.ToPtr("sleepApp"),
			AppType: &appType,
		}
	)

	percentageLimit := func(p api.Percentage) *api.Batch_Limit {
		ret := &api.Batch_Limit{}
		Expect(ret.FromPercentage(p)).ToNot(HaveOccurred())
		return ret
	}
	intLimit := func(i int) *api.Batch_Limit {
		ret := &api.Batch_Limit{}
		Expect(ret.FromBatchLimit1(i)).ToNot(HaveOccurred())
		return ret
	}

	rolloutDeviceSelection := func(b api.BatchSequence) *api.RolloutDeviceSelection {
		ret := &api.RolloutDeviceSelection{}
		Expect(ret.FromBatchSequence(b)).ToNot(HaveOccurred())
		return ret
	}

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

	bsq2 := api.BatchSequence{
		Sequence: &[]api.Batch{
			{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{labelSite: siteMadrid},
				},
				Limit: &api.Batch_Limit{},
				SuccessThreshold: lo.ToPtr(api.Percentage("80%")),
			},
			{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{labelSite: siteMadrid},
				},
				Limit: &api.Batch_Limit{},
				SuccessThreshold: lo.ToPtr(api.Percentage("80%")),
			},
			{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{},
				},
				Limit: &api.Batch_Limit{},
				SuccessThreshold: lo.ToPtr(api.Percentage("50%")),
			},
			{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{labelSite: siteParis},
				},
				SuccessThreshold: lo.ToPtr(api.Percentage("80%")),
			},
			{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{},
				},
				Limit: &api.Batch_Limit{},
				SuccessThreshold: lo.ToPtr(api.Percentage("100%")),
			},
		},
	}

	bsq3 := api.BatchSequence{
	Sequence: &[]api.Batch{
		{
			Selector: &api.LabelSelector{
				MatchLabels: &map[string]string{"site": "madrid"},
			},
			Limit: intLimit(1),
		},
		{
			Selector: &api.LabelSelector{
				MatchLabels: &map[string]string{"site": "paris"},
			},
			Limit: intLimit(1),
		},
	},
}

	createFleetSpec := func(b api.BatchSequence, threshold *api.Percentage, testFleetSpec api.DeviceSpec) api.FleetSpec {
		fleetspec := api.FleetSpec{
			RolloutPolicy: &api.RolloutPolicy{
				DeviceSelection:  rolloutDeviceSelection(b),
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
		return fleetspec
	}
	
	createFleetSpecWithoutDeviceSelection := func(threshold *api.Percentage, testFleetSpec api.DeviceSpec) api.FleetSpec {
		fleetspec := api.FleetSpec{
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
		return fleetspec
	}

	createDisruptionBudget := func(maxUnavailable, minAvailable int, groupBy []string) *api.DisruptionBudget {
		return &api.DisruptionBudget{
			GroupBy:        &groupBy,
			MaxUnavailable: lo.ToPtr(maxUnavailable),
			MinAvailable:   lo.ToPtr(minAvailable),
		}
	}

	BeforeEach(func() {
		// Initialize the test harness
		harness = e2e.NewTestHarness()
	})

	AfterEach(func() {
		// Cleanup the test harness
		harness.Cleanup(true)
		err := harness.CleanUpAllResources()
		Expect(err).ToNot(HaveOccurred())
	})

	Context("Device Selection", func() {
		It("should select devices correctly based on BatchSequence strategy", func() {
			By("create a fleet and Enroll devices into the fleet")

			err := applicationSpec.FromImageApplicationProviderSpec(applicationConfig)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec := api.DeviceSpec{
				Applications: &[]api.ApplicationProviderSpec{applicationSpec},
			}

			err = harness.CreateTestFleetWithSpec(fleetName, createFleetSpec(bsq1, lo.ToPtr(api.Percentage("50%")), api.DeviceSpec{}))
			Expect(err).ToNot(HaveOccurred())

			deviceIDs := harness.StartMultipleVMAndEnroll(4)

			labelsList := []map[string]string{
				{labelSite: siteMadrid},
				{labelSite: siteMadrid},
				{labelSite: siteMadrid},
				{labelSite: siteParis},
			}

			err = harness.SetLabelsForDevicesByIndex(deviceIDs, labelsList, fleetName)
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(setupSleepTime)

			//Update fleet with template
			err = harness.CreateTestFleetWithSpec(fleetName, createFleetSpec(bsq1, lo.ToPtr(api.Percentage("50%")), deviceSpec))
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(defaultSleepTime)

			harness.WaitForBatchCompletion(fleetName, -1, completionTimeout)

			By("Verifying the first batch selects 1 device")
			selectedDevices, err := harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(1))
			Expect((*selectedDevices[0].Metadata.Labels)[labelSite]).To(Equal(siteMadrid))

			harness.WaitForBatchCompletion(fleetName, 0, completionTimeout)

			By("Verifying the second batch selects 50% of remaining devices")
			selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(1))
			Expect((*selectedDevices[0].Metadata.Labels)[labelSite]).To(Equal(siteMadrid))

			Expect(err).ToNot(HaveOccurred())
			By("Here we expect remaining 2 devices to be selected")
			harness.WaitForBatchCompletion(fleetName, 1, completionTimeout)
			selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(2))

		})
	})

	Context("Rollout Disruption Budget", Label("79649"), func() {
		It("should enforce the rollout disruption budget during rollouts", func() {
			By("creating a fleet with disruption budget")

			err := applicationSpec.FromImageApplicationProviderSpec(applicationConfig)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec := api.DeviceSpec{
				Applications: &[]api.ApplicationProviderSpec{applicationSpec},
			}

			fleetSpec := createFleetSpecWithoutDeviceSelection(lo.ToPtr(api.Percentage("100%")), api.DeviceSpec{})

			fleetSpec.RolloutPolicy.DisruptionBudget = createDisruptionBudget(1, 1, []string{})

			err = harness.CreateTestFleetWithSpec(fleetName, fleetSpec)
			Expect(err).ToNot(HaveOccurred())

			deviceIDs := harness.StartMultipleVMAndEnroll(2)

			labelsList := []map[string]string{
				{labelSite: siteMadrid, labelFunction: functionWeb},
				{labelSite: siteParis, labelFunction: functionDb},
			}

			err = harness.SetLabelsForDevicesByIndex(deviceIDs, labelsList, fleetName)
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(setupSleepTime)

			fleetSpec = createFleetSpecWithoutDeviceSelection(lo.ToPtr(api.Percentage("100%")), deviceSpec)

			fleetSpec.RolloutPolicy.DisruptionBudget = createDisruptionBudget(1, 1, []string{})

			err = harness.CreateTestFleetWithSpec(fleetName, fleetSpec)
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(defaultSleepTime)

			By("Verifying that the disruption budget is respected")
			// Get unavailable devices per group
			unavailableDevices, err := harness.GetUnavailableDevicesPerGroup(fleetName, []string{labelSite, labelFunction})
			Expect(err).ToNot(HaveOccurred())

			for _, group := range unavailableDevices {
				Expect(len(group)).To(BeNumerically("<=", 1), "Should have at most 1 unavailable device per group")
			}

			time.Sleep(defaultSleepTime)

			By("Verifying all devices are eventually updated")
			updatedDevices, err := harness.GetUpdatedDevices(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(updatedDevices)).To(Equal(2), "All devices should be updated eventually")
		})
	})

	Context("Multi-batch rollout with complex selection criteria", Label("79648"),  func() {
		It("should select devices correctly across multiple batches with different selection criteria", func() {
			By("Onboarding 10 VM devices and assigning labels")
			
			// Start 10 VMs and enroll them
			deviceIDs := harness.StartMultipleVMAndEnroll(10)
			
			// Create label sets for the devices
			labelsList := []map[string]string{}
			
			// Add 4 devices with site=madrid
			for i := 0; i < 4; i++ {
				labelsList = append(labelsList, map[string]string{
					"fleet": fleetName,
					labelSite: siteMadrid,
				})
			}
			
			// Add 4 devices with site=paris
			for i := 0; i < 4; i++ {
				labelsList = append(labelsList, map[string]string{
					"fleet": fleetName,
					labelSite: siteParis,
				})
			}
			
			// Add 2 devices with site=rome
			for i := 0; i < 2; i++ {
				labelsList = append(labelsList, map[string]string{
					"fleet": fleetName,
					labelSite: siteRome,
				})
			}
			
			// Set labels for all devices
			err := harness.SetLabelsForDevicesByIndex(deviceIDs, labelsList, fleetName)
			Expect(err).ToNot(HaveOccurred())

			// Wait for the devices to be ready
			time.Sleep(setupSleepTime)

			err = applicationSpec.FromImageApplicationProviderSpec(applicationConfig)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec := api.DeviceSpec{
				Applications: &[]api.ApplicationProviderSpec{applicationSpec},
			}

			err = harness.CreateTestFleetWithSpec(fleetName, createFleetSpec(bsq2, lo.ToPtr(api.Percentage("100%")), deviceSpec))
			Expect(err).ToNot(HaveOccurred())


			// Wait for the fleet to be created
			time.Sleep(defaultSleepTime)

			// Wait for the batch completion
			harness.WaitForBatchCompletion(fleetName, -1, completionTimeout)

			By("Verifying the batch selection criteria")
			// Get the selected devices for the first batch
			selectedDevices, err := harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(1), "First batch should select 1 device")
			Expect((*selectedDevices[0].Metadata.Labels)[labelSite]).To(Equal(siteMadrid), "First batch should select devices from site=madrid")
			
			// Wait for the first batch to complete
			harness.WaitForBatchCompletion(fleetName, 0, completionTimeout)
			// Get the selected devices for the second batch
			selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(2), "Second batch should select 2 devices")
			Expect((*selectedDevices[0].Metadata.Labels)[labelSite]).To(Equal(siteMadrid), "Second batch should select devices from site=madrid")
			Expect((*selectedDevices[1].Metadata.Labels)[labelSite]).To(Equal(siteMadrid), "Second batch should select devices from site=madrid")

			// Wait for the second batch to complete
			harness.WaitForBatchCompletion(fleetName, 1, completionTimeout)
			// Get the selected devices for the third batch
			selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(2), "Third batch should select 2 devices")
			//one should madrid and another rome and in any order
			Expect([]string{
				(*selectedDevices[0].Metadata.Labels)[labelSite],
				(*selectedDevices[1].Metadata.Labels)[labelSite],
			}).To(ConsistOf(siteMadrid, siteRome), "Third batch should include devices from site=madrid and site=rome")
			
			

			// Wait for the third batch to complete
			harness.WaitForBatchCompletion(fleetName, 2, completionTimeout)
			// Get the selected devices for the fourth batch
			selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(4), "Fourth batch should select 4 devices")
			// All devices should be selected from site=paris
			for _, device := range selectedDevices {
				Expect((*device.Metadata.Labels)[labelSite]).To(Equal(siteParis), "Fourth batch should select devices from site=paris")
			}

			// Wait for the fourth batch to complete
			harness.WaitForBatchCompletion(fleetName, 3, completionTimeout)
			// Get the selected devices for the fifth batch
			selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(2), "Fifth batch should select 2 devices")
			//one should madrid and another rome and in any order
			Expect([]string{
				(*selectedDevices[0].Metadata.Labels)[labelSite],
				(*selectedDevices[1].Metadata.Labels)[labelSite],
			}).To(ConsistOf(siteMadrid, siteRome), "Third batch should include devices from site=madrid and site=rome")
		})	

	})

	Context("Success Threshold", func() {
		It("should pause the rollout if the success threshold is not met", func() {
			By("creating a fleet with success threshold")

			// Create fleet with 100% success threshold
			err := harness.CreateTestFleetWithSpec(fleetName, createFleetSpec(bsq3, lo.ToPtr(api.Percentage("100%")), api.DeviceSpec{}))
			Expect(err).ToNot(HaveOccurred())

			deviceIDs := harness.StartMultipleVMAndEnroll(2)

			labelsList := []map[string]string{
				{"site": "madrid"},
				{"site": "paris"},
			}

			err = harness.SetLabelsForDevicesByIndex(deviceIDs, labelsList, fleetName)
			Expect(err).ToNot(HaveOccurred())

			err = applicationSpec.FromImageApplicationProviderSpec(applicationConfig)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec := api.DeviceSpec{
				Applications: &[]api.ApplicationProviderSpec{applicationSpec},
			}

			// Wait for the devices to be ready
			time.Sleep(setupSleepTime)

			err = harness.CreateOrUpdateTestFleet(fleetName, testFleetSelector, deviceSpec)
			Expect(err).ToNot(HaveOccurred())

			By("Simulating a failure in the first batch")
			err = harness.SimulateNetworkFailure()
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(setupSleepTime)

			By("Verifying that the rollout is paused due to unmet success threshold")
			rolloutStatus, err := harness.GetRolloutStatus(fleetName)
			Expect(err).ToNot(HaveOccurred())
			//check rollout status tand reason to be api.ConditionStatusFalse and api.RolloutWaitingReason
			Expect(rolloutStatus.Status).To(Equal(api.ConditionStatusFalse), "Rollout should be paused when success threshold is not met")
			Expect(rolloutStatus.Reason).To(Equal(api.RolloutWaitingReason), "Rollout should be paused when success threshold is not met")

			By("Fixing the failed device and verifying the rollout continues")
			err = harness.FixNetworkFailure()
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(setupSleepTime)

			// Wait for rollout to continue
			harness.WaitForBatchCompletion(fleetName, 1, completionTimeout)

			// Verify that all devices are eventually updated
			updatedDevices, err := harness.GetUpdatedDevices(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(updatedDevices)).To(Equal(2), "All devices should be updated once threshold is met again")
		})
	})
	

})