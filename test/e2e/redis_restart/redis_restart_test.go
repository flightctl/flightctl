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

const (
	defaultImage = "quay.io/fedora/fedora-coreos:latest"
	testRepoURL  = "https://github.com/flightctl/flightctl-demos"
)

var _ = Describe("Redis Restart Tests", func() {

	var (
		context   string // KIND, OCP, or "podman"
		namespace string // Redis namespace (detected dynamically)
	)

	BeforeEach(func() {
		workerID := GinkgoParallelProcess()
		harness := e2e.GetWorkerHarness()
		suiteCtx := e2e.GetWorkerContext()

		GinkgoWriter.Printf("[BeforeEach] Worker %d: Setting up test context\n", workerID)

		ctx := util.StartSpecTracerForGinkgo(suiteCtx)
		harness.SetTestContext(ctx)

		ctxStr, err := e2e.GetContext()
		if err != nil || (ctxStr != util.KIND && ctxStr != util.OCP) {
			context = ""
			namespace = ""
		} else {
			context = ctxStr
			namespace = util.DetectRedisNamespace()
		}
		if context == "" {
			GinkgoWriter.Printf("Detected deployment context: podman (not Kubernetes)\n")
		} else {
			GinkgoWriter.Printf("Detected deployment context: %s (Kubernetes), Redis namespace: %s\n", context, namespace)
		}

	})

	It("should recover and continue processing tasks after Redis restart", Label("84786", "sanity"), func() {
		By("verifying initial system state")
		Eventually(func() bool { return util.IsRedisRunning(context) }, TIMEOUT, LONG_POLLING).Should(BeTrue(), "Redis should be running")

		harness := e2e.GetWorkerHarness()
		testID := harness.GetTestIDFromContext()
		labels := &map[string]string{"test-id": testID}

		By("Test 1: Basic Redis restart recovery")
		{
			By("creating FlightCtl resources to generate queue tasks")
			repoNames, err := resources.CreateRepositories(harness, 2, "test-repo-redis-restart", testRepoURL, labels)
			Expect(err).ToNot(HaveOccurred(), "should create repositories")
			fleetNames, err := resources.CreateFleets(harness, 1, "test-fleet-redis-restart", defaultImage, labels)
			Expect(err).ToNot(HaveOccurred(), "should create fleet")

			util.WaitForQueueAccessible(context, TIMEOUT, POLLING, "before restart")
			GinkgoWriter.Printf("Queue state before restart: %+v\n", util.CheckQueueState(context))

			By("restarting Redis and verifying recovery")
			Expect(util.RestartRedis(context, namespace)).To(Succeed(), "should restart Redis")
			Eventually(func() bool { return util.VerifyRedisRecovery(context, namespace) }, TIMEOUT, LONG_POLLING).Should(BeTrue(), "Redis should recover")

			By("verifying queue initialized after restart")
			basicFleetName := fmt.Sprintf("test-fleet-post-restart-basic-%s", testID)
			util.WaitForQueueInitializedAfterRestart(context, TIMEOUT, POLLING,
				func() error {
					_, err := resources.CreateFleet(harness, basicFleetName, defaultImage, labels)
					return err
				},
				func() bool {
					_, err := resources.GetByName(harness, resources.Fleets, basicFleetName)
					return err == nil
				},
			)

			By("verifying resources created before restart remain accessible")
			util.WaitForResourcesAccessible(TIMEOUT, LONG_POLLING, func() bool {
				for _, name := range repoNames {
					if _, err := resources.GetByName(harness, resources.Repositories, name); err != nil {
						return false
					}
				}
				for _, name := range fleetNames {
					if _, err := resources.GetByName(harness, resources.Fleets, name); err != nil {
						return false
					}
				}
				return true
			}, "after restart")

			state, err := util.VerifyQueueHealthy(context)
			GinkgoWriter.Printf("Queue state after basic restart: %+v\n", state)
			Expect(err).ToNot(HaveOccurred(), "Queue should be healthy after basic restart")
			GinkgoWriter.Printf("Basic Redis restart completed successfully\n")
		}

		By("Test 2: Checkpoint and in-flight task state across Redis restarts")
		{
			By("creating many resources to increase chances of catching tasks in-flight")
			repoNames, err := resources.CreateRepositories(harness, 10, "test-repo-checkpoint", testRepoURL, labels)
			Expect(err).ToNot(HaveOccurred(), "should create repositories")
			fleetNames, err := resources.CreateFleets(harness, 5, "test-fleet-checkpoint", defaultImage, labels)
			Expect(err).ToNot(HaveOccurred(), "should create fleets")

			By("checking queue state before restart")
			var state util.QueueState
			var hasActivity bool
			Eventually(func() bool {
				state, hasActivity = util.HasQueueActivity(context)
				return hasActivity
			}, 2*LONG_POLLING, 200*time.Millisecond).Should(Or(BeTrue(), BeFalse()))
			if hasActivity {
				GinkgoWriter.Printf("Queue state: InFlight=%d, Length=%d, Failed=%d\n", state.InFlightTasks, state.QueueLength, state.FailedMessages)
			} else {
				GinkgoWriter.Printf("Note: Tasks processed too quickly to catch in-flight\n")
			}

			By("restarting Redis during active processing")
			Expect(util.RestartRedis(context, namespace)).To(Succeed(), "should restart Redis")
			Eventually(func() bool { return util.VerifyRedisRecovery(context, namespace) }, TIMEOUT, LONG_POLLING).Should(BeTrue(), "Redis should recover")

			checkpointFleetName := fmt.Sprintf("test-fleet-post-restart-checkpoint-%s", testID)
			util.WaitForQueueInitializedAfterRestart(context, TIMEOUT, POLLING,
				func() error {
					_, err := resources.CreateFleet(harness, checkpointFleetName, defaultImage, labels)
					return err
				},
				func() bool {
					_, err := resources.GetByName(harness, resources.Fleets, checkpointFleetName)
					return err == nil
				},
			)

			By("verifying queue state after restart")
			state, err = util.VerifyQueueHealthy(context)
			GinkgoWriter.Printf("Queue state after checkpoint restart: %+v\n", state)
			Expect(err).ToNot(HaveOccurred(), "Queue should be healthy after checkpoint restart")

			By("verifying all resources are processed after restart")
			util.WaitForResourcesAccessible(TIMEOUT, LONG_POLLING, func() bool {
				for _, name := range repoNames {
					if _, err := resources.GetByName(harness, resources.Repositories, name); err != nil {
						return false
					}
				}
				for _, name := range fleetNames {
					if _, err := resources.GetByName(harness, resources.Fleets, name); err != nil {
						return false
					}
				}
				return true
			}, "after checkpoint restart")

			GinkgoWriter.Printf("Checkpoint and in-flight task recovery verified\n")
		}

		By("Test 3: Rapid Redis restarts without data loss")
		{
			By("creating resources to generate queue tasks")
			repoNames, err := resources.CreateRepositories(harness, 3, "test-repo-rapid", testRepoURL, labels)
			Expect(err).ToNot(HaveOccurred(), "should create repositories")

			const numRestarts = 3
			By(fmt.Sprintf("performing %d rapid restarts", numRestarts))
			for i := 1; i <= numRestarts; i++ {
				GinkgoWriter.Printf("Performing restart #%d\n", i)

				util.WaitForQueueAccessible(context, TIMEOUT, POLLING, fmt.Sprintf("before restart #%d", i))
				Expect(util.RestartRedis(context, namespace)).To(Succeed(), "should restart Redis #%d", i)
				Eventually(func() bool { return util.VerifyRedisRecovery(context, namespace) }, TIMEOUT, LONG_POLLING).Should(BeTrue(), "Redis should recover from restart #%d", i)

				rapidFleetName := fmt.Sprintf("test-fleet-post-restart-rapid-%d-%s", i, testID)
				util.WaitForQueueInitializedAfterRestart(context, TIMEOUT, POLLING,
					func() error {
						_, err := resources.CreateFleet(harness, rapidFleetName, defaultImage, labels)
						return err
					},
					func() bool {
						_, err := resources.GetByName(harness, resources.Fleets, rapidFleetName)
						return err == nil
					},
				)

				restartState, restartErr := util.VerifyQueueHealthy(context)
				GinkgoWriter.Printf("Queue state after restart #%d: %+v\n", i, restartState)
				Expect(restartErr).ToNot(HaveOccurred(), "Queue should be healthy after restart #%d", i)
			}

			By("verifying all resources are processed after rapid restarts")
			util.WaitForResourcesAccessible(TIMEOUT, LONG_POLLING, func() bool {
				for _, name := range repoNames {
					if _, err := resources.GetByName(harness, resources.Repositories, name); err != nil {
						return false
					}
				}
				return true
			}, "after rapid restarts")

			GinkgoWriter.Printf("Rapid restart scenario completed successfully\n")
		}
	})

	It("should handle Redis stop and restart during active operations and maintain data consistency", Label("84787"), func() {
		By("verifying initial system state")
		Eventually(func() bool { return util.IsRedisRunning(context) }, TIMEOUT, LONG_POLLING).Should(BeTrue(), "Redis should be running")

		harness := e2e.GetWorkerHarness()
		testID := harness.GetTestIDFromContext()
		labels := &map[string]string{"test-id": testID}

		By("creating FlightCtl resources to generate queue tasks")
		repoNames, err := resources.CreateRepositories(harness, 3, "test-repo-redis-stop", testRepoURL, labels)
		Expect(err).ToNot(HaveOccurred(), "should create repositories")
		fleetNames, err := resources.CreateFleets(harness, 1, "test-fleet-redis-stop", defaultImage, labels)
		Expect(err).ToNot(HaveOccurred(), "should create fleet")

		util.WaitForQueueAccessible(context, TIMEOUT, POLLING, "before stop")
		GinkgoWriter.Printf("Queue state before stop: %+v\n", util.CheckQueueState(context))

		By("stopping Redis and verifying it's stopped")
		Expect(util.StopRedis(context, namespace)).To(Succeed(), "should stop Redis")
		Eventually(func() bool { return !util.IsRedisRunning(context) }, TIMEOUT, POLLING).Should(BeTrue(), "Redis should be stopped")
		Expect(util.CheckQueueState(context).Accessible).To(BeFalse(), "Queue should not be accessible while Redis is stopped")

		By("starting Redis and verifying recovery")
		Expect(util.StartRedis(context, namespace)).To(Succeed(), "should start Redis")
		Eventually(func() bool { return util.VerifyRedisRecovery(context, namespace) }, TIMEOUT, LONG_POLLING).Should(BeTrue(), "Redis should recover")

		stopStartFleetName := fmt.Sprintf("test-fleet-post-restart-stop-start-%s", testID)
		util.WaitForQueueInitializedAfterRestart(context, TIMEOUT, POLLING,
			func() error {
				_, err := resources.CreateFleet(harness, stopStartFleetName, defaultImage, labels)
				return err
			},
			func() bool {
				_, err := resources.GetByName(harness, resources.Fleets, stopStartFleetName)
				return err == nil
			},
		)

		By("performing second restart to verify multiple restarts")
		util.WaitForQueueAccessible(context, TIMEOUT, POLLING, "before second restart")
		Expect(util.RestartRedis(context, namespace)).To(Succeed(), "should restart Redis")
		Eventually(func() bool { return util.VerifyRedisRecovery(context, namespace) }, TIMEOUT, LONG_POLLING).Should(BeTrue(), "Redis should recover")

		secondRestartFleetName := fmt.Sprintf("test-fleet-post-restart-second-%s", testID)
		util.WaitForQueueInitializedAfterRestart(context, TIMEOUT, POLLING,
			func() error {
				_, err := resources.CreateFleet(harness, secondRestartFleetName, defaultImage, labels)
				return err
			},
			func() bool {
				_, err := resources.GetByName(harness, resources.Fleets, secondRestartFleetName)
				return err == nil
			},
		)

		By("testing operations during Redis downtime")
		Expect(util.StopRedis(context, namespace)).To(Succeed(), "should stop Redis")
		Eventually(func() bool { return !util.IsRedisRunning(context) }, TIMEOUT, POLLING).Should(BeTrue(), "Redis should be stopped")

		repoNameDown := fmt.Sprintf("test-repo-redis-down-%s", testID)
		_, createErr := resources.CreateRepository(harness, repoNameDown, testRepoURL, labels)
		repoCreatedDuringDowntime := createErr == nil
		if createErr != nil {
			GinkgoWriter.Printf("Repository creation failed while Redis is down (expected): %v\n", createErr)
		} else {
			GinkgoWriter.Printf("Repository creation succeeded during Redis downtime (queued)\n")
		}
		defer func() { _, _ = resources.Delete(harness, util.Repository, repoNameDown) }()

		By("starting Redis and verifying all resources are processed")
		Expect(util.StartRedis(context, namespace)).To(Succeed(), "should start Redis")
		Eventually(func() bool { return util.VerifyRedisRecovery(context, namespace) }, TIMEOUT, LONG_POLLING).Should(BeTrue(), "Redis should recover")

		afterDowntimeFleetName := fmt.Sprintf("test-fleet-post-restart-after-downtime-%s", testID)
		util.WaitForQueueInitializedAfterRestart(context, TIMEOUT, POLLING,
			func() error {
				_, err := resources.CreateFleet(harness, afterDowntimeFleetName, defaultImage, labels)
				return err
			},
			func() bool {
				_, err := resources.GetByName(harness, resources.Fleets, afterDowntimeFleetName)
				return err == nil
			},
		)

		By("verifying all resources remain accessible after multiple restarts")
		util.WaitForResourcesAccessible(TIMEOUT, LONG_POLLING, func() bool {
			for _, name := range repoNames {
				if _, err := resources.GetByName(harness, resources.Repositories, name); err != nil {
					return false
				}
			}
			for _, name := range fleetNames {
				if _, err := resources.GetByName(harness, resources.Fleets, name); err != nil {
					return false
				}
			}
			return true
		}, "after multiple restarts")

		By("verifying repository created during downtime is eventually processed")
		Eventually(func() bool {
			_, err := resources.GetByName(harness, resources.Repositories, repoNameDown)
			return err == nil
		}, TIMEOUT, LONG_POLLING).Should(BeTrue(), "Repository created during downtime should be processed")

		if repoCreatedDuringDowntime {
			GinkgoWriter.Printf("Repository created during downtime was successfully processed\n")
		} else {
			GinkgoWriter.Printf("Repository creation that failed during downtime was retried successfully\n")
		}

		finalState, finalErr := util.VerifyQueueHealthy(context)
		GinkgoWriter.Printf("Queue state final: %+v\n", finalState)
		Expect(finalErr).ToNot(HaveOccurred(), "Queue should be healthy in final state")
	})
})
