package redis_restart

import (
	"fmt"
	"time"

	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Redis Restart Tests", func() {

	var (
		deploymentMode string // "podman" or "kubernetes"
		namespace      = "flightctl-internal"
	)

	BeforeEach(func() {
		// Detect deployment mode
		deploymentMode = util.DetectDeploymentMode()
		GinkgoWriter.Printf("Detected deployment mode: %s\n", deploymentMode)
	})

	It("should recover and continue processing tasks after Redis restart", Label("OCP-84786", "sanity"), func() {
		By("verifying initial system state")
		Expect(util.IsRedisRunning()).To(BeTrue(), "Redis should be running")
		Expect(util.AreFlightCtlServicesHealthy()).To(BeTrue(), "FlightCtl services should be healthy")

		By("creating FlightCtl resources to generate queue tasks")
		harness := e2e.GetWorkerHarness()
		testID := harness.GetTestIDFromContext()
		repoURL := "https://github.com/flightctl/flightctl-demos"

		// Create test resources
		repoNames := make([]string, 2)
		for i := range repoNames {
			repoNames[i] = fmt.Sprintf("test-repo-redis-restart-%d-%s", i+1, testID)
			_, err := resources.CreateRepository(harness, repoNames[i], repoURL, &map[string]string{"test-id": testID})
			Expect(err).ToNot(HaveOccurred())
			defer func(name string) { _, _ = resources.Delete(harness, util.Repository, name) }(repoNames[i])
		}

		fleetName := fmt.Sprintf("test-fleet-redis-restart-%s", testID)
		_, err := resources.CreateFleet(harness, fleetName, "quay.io/fedora/fedora-coreos:latest", &map[string]string{"test-id": testID})
		Expect(err).ToNot(HaveOccurred())
		defer func() { _, _ = resources.Delete(harness, util.Fleet, fleetName) }()

		// Wait for tasks to be queued
		time.Sleep(3 * time.Second)
		queueStateBefore := util.CheckQueueState()
		Expect(queueStateBefore.Accessible).To(BeTrue(), "Queue should be accessible before restart")
		GinkgoWriter.Printf("Queue state before restart: %+v\n", queueStateBefore)

		By("restarting Redis and verifying recovery")
		err = util.RestartRedis(deploymentMode, namespace)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return util.VerifyRedisRecovery(deploymentMode, namespace)
		}, 3*time.Minute, 5*time.Second).Should(BeTrue(), "Redis and services should recover after restart")

		By("verifying resources continue processing after restart")
		time.Sleep(5 * time.Second)

		Eventually(func() bool {
			// Verify all repositories
			for _, repoName := range repoNames {
				_, err := resources.GetByName(harness, resources.Repositories, repoName)
				if err != nil {
					return false
				}
			}
			// Verify fleet
			_, err := resources.GetByName(harness, resources.Fleets, fleetName)
			return err == nil
		}, 1*time.Minute, 5*time.Second).Should(BeTrue(), "All resources should remain accessible after restart")

		queueStateFinal := util.CheckQueueState()
		Expect(queueStateFinal.Accessible).To(BeTrue(), "Queue should remain accessible")
		GinkgoWriter.Printf("✓ Redis restart completed successfully - OCP-84786\n")
	})

	It("should handle Redis stop and restart during active operations and maintain data consistency", Label("OCP-84787"), func() {
		By("verifying initial system state")
		Expect(util.IsRedisRunning()).To(BeTrue(), "Redis should be running")
		Expect(util.AreFlightCtlServicesHealthy()).To(BeTrue(), "FlightCtl services should be healthy")

		By("creating FlightCtl resources to generate queue tasks")
		harness := e2e.GetWorkerHarness()
		testID := harness.GetTestIDFromContext()
		repoURL := "https://github.com/flightctl/flightctl-demos"

		// Create test resources
		repoNames := make([]string, 3)
		for i := range repoNames {
			repoNames[i] = fmt.Sprintf("test-repo-redis-stop-%d-%s", i+1, testID)
			_, err := resources.CreateRepository(harness, repoNames[i], repoURL, &map[string]string{"test-id": testID})
			Expect(err).ToNot(HaveOccurred())
			defer func(name string) { _, _ = resources.Delete(harness, util.Repository, name) }(repoNames[i])
		}

		fleetName := fmt.Sprintf("test-fleet-redis-stop-%s", testID)
		_, err := resources.CreateFleet(harness, fleetName, "quay.io/fedora/fedora-coreos:latest", &map[string]string{"test-id": testID})
		Expect(err).ToNot(HaveOccurred())
		defer func() { _, _ = resources.Delete(harness, util.Fleet, fleetName) }()

		// Wait for tasks to be queued
		time.Sleep(3 * time.Second)
		queueStateBefore := util.CheckQueueState()
		Expect(queueStateBefore.Accessible).To(BeTrue(), "Queue should be accessible before stop")
		GinkgoWriter.Printf("Queue state before stop: %+v\n", queueStateBefore)

		By("stopping Redis and verifying it's stopped")
		err = util.StopRedis(deploymentMode, namespace)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return !util.IsRedisRunning()
		}, 30*time.Second, 2*time.Second).Should(BeTrue(), "Redis should be stopped")

		queueStateDuringStop := util.CheckQueueState()
		Expect(queueStateDuringStop.Accessible).To(BeFalse(), "Queue should not be accessible while Redis is stopped")
		time.Sleep(5 * time.Second) // Allow services to handle disconnect

		By("starting Redis and verifying recovery")
		err = util.StartRedis(deploymentMode, namespace)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return util.VerifyRedisRecovery(deploymentMode, namespace)
		}, 3*time.Minute, 5*time.Second).Should(BeTrue(), "Redis and services should recover after start")

		By("performing second restart to verify multiple restarts")
		time.Sleep(10 * time.Second)
		err = util.RestartRedis(deploymentMode, namespace)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return util.VerifyRedisRecovery(deploymentMode, namespace)
		}, 3*time.Minute, 5*time.Second).Should(BeTrue(), "Redis should recover from second restart")

		By("testing operations during Redis downtime")
		err = util.StopRedis(deploymentMode, namespace)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return !util.IsRedisRunning()
		}, 30*time.Second, 2*time.Second).Should(BeTrue(), "Redis should be stopped")

		repoNameDown := fmt.Sprintf("test-repo-redis-down-%s", testID)
		_, err = resources.CreateRepository(harness, repoNameDown, repoURL, &map[string]string{"test-id": testID})
		if err != nil {
			GinkgoWriter.Printf("⚠️  Repository creation failed while Redis is down: %v\n", err)
		}
		defer func() { _, _ = resources.Delete(harness, util.Repository, repoNameDown) }()

		By("starting Redis and verifying all resources are processed")
		err = util.StartRedis(deploymentMode, namespace)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return util.VerifyRedisRecovery(deploymentMode, namespace)
		}, 3*time.Minute, 5*time.Second).Should(BeTrue(), "Redis should recover and process pending operations")

		time.Sleep(5 * time.Second)

		By("verifying all resources remain accessible after multiple restarts")
		Eventually(func() bool {
			// Verify all repositories
			for _, repoName := range repoNames {
				_, err := resources.GetByName(harness, resources.Repositories, repoName)
				if err != nil {
					return false
				}
			}
			// Verify fleet
			_, err := resources.GetByName(harness, resources.Fleets, fleetName)
			if err != nil {
				return false
			}
			// Verify repository created during downtime
			_, err = resources.GetByName(harness, resources.Repositories, repoNameDown)
			return err == nil
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "All resources should remain accessible after Redis restarts")

		queueStateFinal := util.CheckQueueState()
		Expect(queueStateFinal.Accessible && queueStateFinal.TaskQueueExists).To(BeTrue(), "Queue should be functional")
		GinkgoWriter.Printf("Final queue state: %+v\n", queueStateFinal)
		GinkgoWriter.Printf("✓ Redis stop and restart with data consistency maintained - OCP-84787\n")
	})

	It("should maintain checkpoint and in-flight task state across Redis restarts", Label("checkpoint-recovery"), func() {
		By("verifying initial system state")
		Expect(util.IsRedisRunning()).To(BeTrue(), "Redis should be running")
		Expect(util.AreFlightCtlServicesHealthy()).To(BeTrue(), "FlightCtl services should be healthy")

		By("creating resources to generate queue tasks")
		harness := e2e.GetWorkerHarness()
		testID := harness.GetTestIDFromContext()
		repoURL := "https://github.com/flightctl/flightctl-demos"

		// Create multiple resources to ensure we have in-flight tasks
		repoNames := make([]string, 5)
		for i := range repoNames {
			repoNames[i] = fmt.Sprintf("test-repo-checkpoint-%d-%s", i+1, testID)
			_, err := resources.CreateRepository(harness, repoNames[i], repoURL, &map[string]string{"test-id": testID})
			Expect(err).ToNot(HaveOccurred())
			defer func(name string) { _, _ = resources.Delete(harness, util.Repository, name) }(repoNames[i])
		}

		fleetName := fmt.Sprintf("test-fleet-checkpoint-%s", testID)
		_, err := resources.CreateFleet(harness, fleetName, "quay.io/fedora/fedora-coreos:latest", &map[string]string{"test-id": testID})
		Expect(err).ToNot(HaveOccurred())
		defer func() { _, _ = resources.Delete(harness, util.Fleet, fleetName) }()

		// Wait for tasks to be queued and processing to start
		time.Sleep(5 * time.Second)

		By("checking queue state before restart (checkpoint and in-flight tasks)")
		queueStateBefore := util.CheckQueueState()
		Expect(queueStateBefore.Accessible).To(BeTrue(), "Queue should be accessible")
		GinkgoWriter.Printf("Queue state before restart: %+v\n", queueStateBefore)
		GinkgoWriter.Printf("In-flight tasks: %d, Queue length: %d, Failed messages: %d\n",
			queueStateBefore.InFlightTasks, queueStateBefore.QueueLength, queueStateBefore.FailedMessages)

		// Verify we have some activity (either in-flight tasks or queue length > 0)
		hasActivity := queueStateBefore.InFlightTasks > 0 || queueStateBefore.QueueLength > 0
		Expect(hasActivity).To(BeTrue(), "Should have some tasks in queue or in-flight before restart")

		By("restarting Redis during active processing")
		err = util.RestartRedis(deploymentMode, namespace)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			return util.VerifyRedisRecovery(deploymentMode, namespace)
		}, 3*time.Minute, 5*time.Second).Should(BeTrue(), "Redis should recover after restart")

		By("verifying checkpoint and queue state after restart")
		time.Sleep(5 * time.Second) // Allow checkpoint advancement

		queueStateAfter := util.CheckQueueState()
		GinkgoWriter.Printf("Queue state after restart: %+v\n", queueStateAfter)
		Expect(queueStateAfter.Accessible).To(BeTrue(), "Queue should be accessible after restart")
		Expect(queueStateAfter.TaskQueueExists).To(BeTrue(), "Task queue should exist after restart")
		Expect(queueStateAfter.HasConsumerGroup).To(BeTrue(), "Consumer group should exist after restart")

		By("verifying all resources are processed after restart")
		Eventually(func() bool {
			// Verify all repositories
			for _, repoName := range repoNames {
				_, err := resources.GetByName(harness, resources.Repositories, repoName)
				if err != nil {
					return false
				}
			}
			// Verify fleet
			_, err := resources.GetByName(harness, resources.Fleets, fleetName)
			return err == nil
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "All resources should be processed after restart")

		By("verifying final queue state (tasks should be processed)")
		queueStateFinal := util.CheckQueueState()
		GinkgoWriter.Printf("Final queue state: %+v\n", queueStateFinal)
		// After processing, in-flight tasks should decrease, but queue should remain functional
		Expect(queueStateFinal.Accessible && queueStateFinal.TaskQueueExists).To(BeTrue(), "Queue should remain functional")
		GinkgoWriter.Printf("✓ Checkpoint and in-flight task recovery verified\n")
	})

	It("should handle rapid Redis restarts without data loss", Label("rapid-restart"), func() {
		By("verifying initial system state")
		Expect(util.IsRedisRunning()).To(BeTrue(), "Redis should be running")
		Expect(util.AreFlightCtlServicesHealthy()).To(BeTrue(), "FlightCtl services should be healthy")

		By("creating resources to generate queue tasks")
		harness := e2e.GetWorkerHarness()
		testID := harness.GetTestIDFromContext()
		repoURL := "https://github.com/flightctl/flightctl-demos"

		repoNames := make([]string, 3)
		for i := range repoNames {
			repoNames[i] = fmt.Sprintf("test-repo-rapid-%d-%s", i+1, testID)
			_, err := resources.CreateRepository(harness, repoNames[i], repoURL, &map[string]string{"test-id": testID})
			Expect(err).ToNot(HaveOccurred())
			defer func(name string) { _, _ = resources.Delete(harness, util.Repository, name) }(repoNames[i])
		}

		By("performing rapid restarts (3 restarts with short intervals)")
		for restartNum := 1; restartNum <= 3; restartNum++ {
			GinkgoWriter.Printf("Performing restart #%d\n", restartNum)

			// Wait a bit before restart
			time.Sleep(3 * time.Second)

			err := util.RestartRedis(deploymentMode, namespace)
			Expect(err).ToNot(HaveOccurred(), "Should be able to restart Redis")

			Eventually(func() bool {
				return util.VerifyRedisRecovery(deploymentMode, namespace)
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Redis should recover from restart #%d", restartNum)

			queueState := util.CheckQueueState()
			Expect(queueState.Accessible).To(BeTrue(), "Queue should be accessible after restart #%d", restartNum)
			GinkgoWriter.Printf("Queue state after restart #%d: %+v\n", restartNum, queueState)
		}

		By("verifying all resources are processed after rapid restarts")
		time.Sleep(10 * time.Second) // Allow final processing

		Eventually(func() bool {
			for _, repoName := range repoNames {
				_, err := resources.GetByName(harness, resources.Repositories, repoName)
				if err != nil {
					return false
				}
			}
			return true
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "All resources should be processed after rapid restarts")

		queueStateFinal := util.CheckQueueState()
		Expect(queueStateFinal.Accessible && queueStateFinal.TaskQueueExists).To(BeTrue(), "Queue should remain functional")
		GinkgoWriter.Printf("Final queue state: %+v\n", queueStateFinal)
		GinkgoWriter.Printf("✓ Rapid restart scenario completed successfully\n")
	})
})
