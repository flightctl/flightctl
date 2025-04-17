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

	var (
		harness *e2e.Harness

		testFleetSelector = api.LabelSelector{
			MatchLabels: &map[string]string{"fleet": "test-fleet"},
		}

		applicationConfig = api.ImageApplicationProviderSpec{
			Image: "nginx:latest",
		}

		applicationSpec api.ApplicationProviderSpec
	)

	const (
		fleetName = "test-fleet"
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
					MatchLabels: &map[string]string{"site": "madrid"},
				},
				Limit: intLimit(1),
			},
			{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{"site": "madrid"},
				},
				Limit: percentageLimit("50%"),
			},
		},
	}

	bsqDisruption := api.BatchSequence{
		Sequence: &[]api.Batch{
			{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{"function": "web"},
				},
				Limit: intLimit(1),
			},
			{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{"function": "db"},
				},
				Limit: intLimit(1),
			},
		},
	}

	bsqSuccess := api.BatchSequence{
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

	createFleetSpec := func(fleetName string, b api.BatchSequence, threshold *api.Percentage) api.FleetSpec {

		fleetspec := api.FleetSpec{
			RolloutPolicy: &api.RolloutPolicy{
				DeviceSelection:  rolloutDeviceSelection(b),
				SuccessThreshold: threshold,
			},
		}
		return fleetspec
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
			By("c reate a fleet AND EEnroll devices into the fleet")

			err := harness.CreateTestFleetWithSpec(fleetName, createFleetSpec(fleetName, bsq1, lo.ToPtr(api.Percentage("50%"))))
			Expect(err).ToNot(HaveOccurred())

			deviceIDs := harness.StartMultipleVMAndEnroll(3)

			labelsList := []map[string]string{
				{"site": "madrid"},
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

			//Update fleet with template
			err = harness.CreateOrUpdateTestFleet(fleetName, testFleetSelector, deviceSpec)
			Expect(err).ToNot(HaveOccurred())

			harness.WaitForBatchCompletion(fleetName, 1, 1*time.Minute)

			By("Verifying the first batch selects 1 device")
			selectedDevices, err := harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(Equal(1))
			Expect((*selectedDevices[0].Metadata.Labels)["site"]).To(Equal("madrid"))

			harness.WaitForBatchCompletion(fleetName, 2, 1*time.Minute)

			By("Verifying the second batch selects 50% of remaining devices")
			selectedDevices, err = harness.GetSelectedDevicesForBatch(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(selectedDevices)).To(BeNumerically(">", 0))
			Expect(len(selectedDevices)).To(BeNumerically("<=", 1))
		})
	})

	Context("Rollout Disruption Budget", func() {
		It("should enforce the rollout disruption budget during rollouts", func() {
			By("creating a fleet with disruption budget")

			fleetSpec := createFleetSpec(fleetName, bsqDisruption, lo.ToPtr(api.Percentage("100%")))
			fleetSpec.RolloutPolicy.DisruptionBudget = &api.DisruptionBudget{
				GroupBy:        &[]string{"site", "function"},
				MaxUnavailable: lo.ToPtr(50),
			}

			err := harness.CreateTestFleetWithSpec(fleetName, fleetSpec)
			Expect(err).ToNot(HaveOccurred())

			deviceIDs := harness.StartMultipleVMAndEnroll(4)

			labelsList := []map[string]string{
				{"site": "madrid", "function": "web"},
				{"site": "madrid", "function": "db"},
				{"site": "paris", "function": "web"},
				{"site": "paris", "function": "db"},
			}

			err = harness.SetLabelsForDevicesByIndex(deviceIDs, labelsList, fleetName)
			Expect(err).ToNot(HaveOccurred())

			err = applicationSpec.FromImageApplicationProviderSpec(applicationConfig)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec := api.DeviceSpec{
				Applications: &[]api.ApplicationProviderSpec{applicationSpec},
			}

			err = harness.CreateOrUpdateTestFleet(fleetName, testFleetSelector, deviceSpec)
			Expect(err).ToNot(HaveOccurred())

			harness.WaitForBatchCompletion(fleetName, 1, 1*time.Minute)

			By("Verifying that the disruption budget is respected")
			// Get unavailable devices per group
			unavailableDevices, err := harness.GetUnavailableDevicesPerGroup(fleetName, []string{"site", "function"})
			Expect(err).ToNot(HaveOccurred())

			for _, group := range unavailableDevices {
				Expect(len(group)).To(BeNumerically("<=", 1), "Should have at most 1 unavailable device per group")
			}

			harness.WaitForBatchCompletion(fleetName, 2, 1*time.Minute)

			By("Verifying all devices are eventually updated")
			updatedDevices, err := harness.GetUpdatedDevices(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(updatedDevices)).To(Equal(4), "All devices should be updated eventually")
		})
	})

	Context("Success Threshold", func() {
		It("should pause the rollout if the success threshold is not met", func() {
			By("creating a fleet with success threshold")

			// Create fleet with 100% success threshold
			err := harness.CreateTestFleetWithSpec(fleetName, createFleetSpec(fleetName, bsqSuccess, lo.ToPtr(api.Percentage("100%"))))
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

			err = harness.CreateOrUpdateTestFleet(fleetName, testFleetSelector, deviceSpec)
			Expect(err).ToNot(HaveOccurred())

			By("Simulating a failure in the first batch")
			err = SimulateDeviceFailure(harness)
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(30 * time.Second)

			By("Verifying that the rollout is paused due to unmet success threshold")
			rolloutStatus, err := harness.GetRolloutStatus(fleetName)
			Expect(err).ToNot(HaveOccurred())
			//check rollout status tand reason to be api.ConditionStatusFalse and api.RolloutWaitingReason
			Expect(rolloutStatus.Status).To(Equal(api.ConditionStatusFalse), "Rollout should be paused when success threshold is not met")
			Expect(rolloutStatus.Reason).To(Equal(api.RolloutWaitingReason), "Rollout should be paused when success threshold is not met")

			By("Fixing the failed device and verifying the rollout continues")
			err = FixDeviceFailure(harness)
			Expect(err).ToNot(HaveOccurred())

			// Wait for rollout to continue
			harness.WaitForBatchCompletion(fleetName, 2, 1*time.Minute)

			// Verify that all devices are eventually updated
			updatedDevices, err := harness.GetUpdatedDevices(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(updatedDevices)).To(Equal(2), "All devices should be updated once threshold is met again")
		})
	})
})

func SimulateDeviceFailure(h *e2e.Harness) error {
	// Define a command to simulate failure (e.g., stop a critical service or crash the VM)
	failureCommand := []string{"sudo", "shutdown", "now"} // Example: Shut down the VM

	// Run the failure command on the VM
	_, err := h.VM().RunSSH(failureCommand, nil)
	if err != nil {
		return fmt.Errorf("failed to simulate failure on VM: %w", err)
	}

	return nil
}

func FixDeviceFailure(h *e2e.Harness) error {
	// Define a command to fix the failure (e.g., restart the VM or start a critical service)
	fixCommand := []string{"sudo", "reboot"} // Example: Reboot the VM

	// Run the fix command on the VM
	_, err := h.VM().RunSSH(fixCommand, nil)
	if err != nil {
		return fmt.Errorf("failed to fix failure on VM: %w", err)
	}

	// Wait for the VM to become ready again
	err = h.VM().WaitForSSHToBeReady()
	if err != nil {
		return fmt.Errorf("VM %s did not recover after fix: %w", err)
	}

	return nil
}
