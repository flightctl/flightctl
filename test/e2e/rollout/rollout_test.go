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

	Context("Rollout Disruption Budget", func() {
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

	// Context("Multi-batch rollout with complex selection criteria", func() {
	// 	It("should select devices correctly across multiple batches with different selection criteria", func() {
	// 		By("Onboarding 10 VM devices and assigning labels")
			
	// 		// Start 10 VMs and enroll them
	// 		deviceIDs := harness.StartMultipleVMAndEnroll(10)
			
	// 		// Create label sets for the devices
	// 		labelsList := []map[string]string{}
			
	// 		// Add 4 devices with site=madrid
	// 		for i := 0; i < 4; i++ {
	// 			labelsList = append(labelsList, map[string]string{
	// 				"fleet": fleetName,
	// 				labelSite: siteMadrid,
	// 			})
	// 		}
			
	// 		// Add 4 devices with site=paris
	// 		for i := 0; i < 4; i++ {
	// 			labelsList = append(labelsList, map[string]string{
	// 				"fleet": fleetName,
	// 				labelSite: siteParis,
	// 			})
	// 		}
			
	// 		// Add 2 devices with site=rome
	// 		for i := 0; i < 2; i++ {
	// 			labelsList = append(labelsList, map[string]string{
	// 				"fleet": fleetName,
	// 				labelSite: siteRome,
	// 			})
	// 		}
			
	// 		// Set labels for all devices
	// 		err := harness.SetLabelsForDevicesByIndex(deviceIDs, labelsList, "")
	// 		Expect(err).ToNot(HaveOccurred())
			
	// 		// Wait for labels to be applied
	// 		time.Sleep(setupSleepTime)
			
	// 		By("Creating fleet with batch sequence rollout policy")
	// 		batchSequence := createBatchSequence()
	// 		fleetSpec := createFleetSpec(batchSequence)
			
	// 		// Create the fleet with the defined spec
	// 		err = harness.CreateTestFleetWithSpec(fleetName, fleetSpec)
	// 		Expect(err).ToNot(HaveOccurred())
			
	// 		// Wait for the fleet creation to take effect
	// 		time.Sleep(defaultSleepTime)
			
	// 		By("Verifying batch 1 selects exactly 1 device with site=madrid")
	// 		// Wait for the first batch to be processed
	// 		harness.WaitForBatchNumber(fleetName, 1, completionTimeout)
			
	// 		// Verify that only 1 device with site=madrid is selected
	// 		selectedDevices, err := harness.GetSelectedDevicesForBatch(fleetName)
	// 		Expect(err).ToNot(HaveOccurred())
	// 		Expect(len(selectedDevices)).To(Equal(1))
	// 		Expect((*selectedDevices[0].Metadata.Labels)[labelSite]).To(Equal(siteMadrid))
			
	// 		// Check that the device has the correct annotation
	// 		deviceAnnotations, err := harness.GetDeviceAnnotations(selectedDevices[0].Metadata.Name)
	// 		Expect(err).ToNot(HaveOccurred())
	// 		Expect(deviceAnnotations).To(HaveKey("fleet-controller/selectedForRollout"))
			
	// 		// Wait for the batch to complete
	// 		harness.WaitForBatchCompletion(fleetName, 0, completionTimeout)
			
	// 		By("Verifying batch 2 selects 80% of remaining devices with site=madrid (2 more devices)")
	// 		// Wait for the second batch to be processed
	// 		harness.WaitForBatchNumber(fleetName, 2, completionTimeout)
			
	// 		// Verify that 2 more devices with site=madrid are selected (80% of remaining 3)
	// 		selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
	// 		Expect(err).ToNot(HaveOccurred())
	// 		Expect(len(selectedDevices)).To(Equal(2))
			
	// 		// Verify all devices have site=madrid label
	// 		for _, device := range selectedDevices {
	// 			Expect((*device.Metadata.Labels)[labelSite]).To(Equal(siteMadrid))
				
	// 			// Check that the device has the correct annotation
	// 			deviceAnnotations, err := harness.GetDeviceAnnotations(device.Metadata.Name)
	// 			Expect(err).ToNot(HaveOccurred())
	// 			Expect(deviceAnnotations).To(HaveKey("fleet-controller/selectedForRollout"))
	// 		}
			
	// 		// Wait for the batch to complete
	// 		harness.WaitForBatchCompletion(fleetName, 1, completionTimeout)
			
	// 		By("Verifying batch 3 selects 50% of remaining devices with any label (2 devices)")
	// 		// Wait for the third batch to be processed
	// 		harness.WaitForBatchNumber(fleetName, 3, completionTimeout)
			
	// 		// Verify that 2 devices are selected (50% of remaining 4 unprocessed)
	// 		selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
	// 		Expect(err).ToNot(HaveOccurred())
	// 		Expect(len(selectedDevices)).To(Equal(2))
			
	// 		// Count how many of each site we have
	// 		siteCount := map[string]int{
	// 			siteMadrid: 0,
	// 			siteParis:  0,
	// 			siteRome:   0,
	// 		}
			
	// 		for _, device := range selectedDevices {
	// 			site := (*device.Metadata.Labels)[labelSite]
	// 			siteCount[site]++
				
	// 			// Check that the device has the correct annotation
	// 			deviceAnnotations, err := harness.GetDeviceAnnotations(device.Metadata.Name)
	// 			Expect(err).ToNot(HaveOccurred())
	// 			Expect(deviceAnnotations).To(HaveKey("fleet-controller/selectedForRollout"))
	// 		}
			
	// 		// Wait for the batch to complete
	// 		harness.WaitForBatchCompletion(fleetName, 2, completionTimeout)
			
	// 		By("Verifying batch 4 selects all devices with site=paris (4 devices)")
	// 		// Wait for the fourth batch to be processed
	// 		harness.WaitForBatchNumber(fleetName, 4, completionTimeout)
			
	// 		// Verify that all 4 paris devices are selected
	// 		selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
	// 		Expect(err).ToNot(HaveOccurred())
	// 		Expect(len(selectedDevices)).To(Equal(4))
			
	// 		// Verify all devices have site=paris label
	// 		for _, device := range selectedDevices {
	// 			Expect((*device.Metadata.Labels)[labelSite]).To(Equal(siteParis))
				
	// 			// Check that the device has the correct annotation
	// 			deviceAnnotations, err := harness.GetDeviceAnnotations(device.Metadata.Name)
	// 			Expect(err).ToNot(HaveOccurred())
	// 			Expect(deviceAnnotations).To(HaveKey("fleet-controller/selectedForRollout"))
	// 		}
			
	// 		// Wait for the batch to complete
	// 		harness.WaitForBatchCompletion(fleetName, 3, completionTimeout)
			
	// 		By("Verifying batch 5 selects all remaining devices (1 madrid, 1 rome)")
	// 		// Wait for the fifth batch to be processed
	// 		harness.WaitForBatchNumber(fleetName, 5, completionTimeout)
			
	// 		// Verify that the final 2 devices are selected
	// 		selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
	// 		Expect(err).ToNot(HaveOccurred())
	// 		Expect(len(selectedDevices)).To(Equal(2))
			
	// 		// Reset site count
	// 		siteCount = map[string]int{
	// 			siteMadrid: 0,
	// 			siteParis:  0,
	// 			siteRome:   0,
	// 		}
			
	// 		// Count devices by site
	// 		for _, device := range selectedDevices {
	// 			site := (*device.Metadata.Labels)[labelSite]
	// 			siteCount[site]++
				
	// 			// Check that the device has the correct annotation
	// 			deviceAnnotations, err := harness.GetDeviceAnnotations(device.Metadata.Name)
	// 			Expect(err).ToNot(HaveOccurred())
	// 			Expect(deviceAnnotations).To(HaveKey("fleet-controller/selectedForRollout"))
	// 		}
			
	// 		// Verify we have the expected distribution of remaining devices
	// 		// Since we can't guarantee which devices were selected in batch 3,
	// 		// we can only verify the total count
	// 		Expect(siteCount[siteMadrid] + siteCount[siteRome]).To(Equal(2))
			
	// 		// Wait for the batch to complete
	// 		harness.WaitForBatchCompletion(fleetName, 4, completionTimeout)
			
	// 		By("Verifying batch 6 shows no more devices remain")
	// 		// Wait for the sixth batch to be processed
	// 		harness.WaitForBatchNumber(fleetName, 6, completionTimeout)
			
	// 		// Verify that no more devices are selected
	// 		selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
	// 		Expect(err).ToNot(HaveOccurred())
	// 		Expect(len(selectedDevices)).To(Equal(0))
			
	// 		By("Verifying inline config is properly rendered on each device")
	// 		// Get all devices in the fleet
	// 		devices, err := harness.GetAllDevicesInFleet(fleetName)
	// 		Expect(err).ToNot(HaveOccurred())
			
	// 		// Check each device for the correct config
	// 		for _, device := range devices {
	// 			site := (*device.Metadata.Labels)[labelSite]
				
	// 			// Verify the content of the config file through SSH
	// 			output, err := harness.SSHIntoDevice(device.Metadata.Name, "cat /var/home/user/file.txt")
	// 			Expect(err).ToNot(HaveOccurred())
	// 			Expect(output).To(Equal(fmt.Sprintf("This device is in %s", site)))
				
	// 			// Verify the OS image is set correctly
	// 			Expect(*device.Spec.OS.Image).To(Equal(deviceImage))
	// 		}
			
	// 		By("Verifying final rollout status")
	// 		// Check the fleet status
	// 		fleetStatus, err := harness.GetFleetStatus(fleetName)
	// 		Expect(err).ToNot(HaveOccurred())
			
	// 		// Verify the rollout is marked as complete
	// 		var rolloutCondition *api.Condition
	// 		for _, condition := range *fleetStatus.Conditions {
	// 			if condition.Type == "RolloutInProgress" {
	// 				rolloutCondition = &condition
	// 				break
	// 			}
	// 		}
			
	// 		Expect(rolloutCondition).ToNot(BeNil())
	// 		Expect(rolloutCondition.Status).To(Equal("False"))
	// 		Expect(rolloutCondition.Reason).To(Equal("Inactive"))
	// 		Expect(rolloutCondition.Message).To(Equal("Rollout is not in progress"))
			
	// 		// Check the last batch completion report
	// 		fleetAnnotations, err := harness.GetFleetAnnotations(fleetName)
	// 		Expect(err).ToNot(HaveOccurred())
	// 		Expect(fleetAnnotations).To(HaveKey("fleet-controller/lastBatchCompletionReport"))
			
	// 		// This is a simple string check, in a real test you might want to parse the JSON
	// 		Expect(fleetAnnotations["fleet-controller/lastBatchCompletionReport"]).To(ContainSubstring("batch 5"))
	// 		Expect(fleetAnnotations["fleet-controller/lastBatchCompletionReport"]).To(ContainSubstring("successPercentage\":100"))
	// 		Expect(fleetAnnotations["fleet-controller/lastBatchCompletionReport"]).To(ContainSubstring("total\":2"))
	// 		Expect(fleetAnnotations["fleet-controller/lastBatchCompletionReport"]).To(ContainSubstring("successful\":2"))
	// 		Expect(fleetAnnotations["fleet-controller/lastBatchCompletionReport"]).To(ContainSubstring("failed\":0"))
	// 		Expect(fleetAnnotations["fleet-controller/lastBatchCompletionReport"]).To(ContainSubstring("timedOut\":0"))
	// 	})
	// })

})