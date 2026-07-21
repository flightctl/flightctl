package dependency_sync_test

import (
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	gitBranch        = "main"
	configFileName   = "configuration/etc/flightctl/config.conf"
	configFilePath   = "/configuration"
	deviceConfigFile = "/etc/flightctl/config.conf"
	httpMountPath    = "/etc/app/remote.conf"

	initialContent     = "initial-content=true\n"
	updatedContent     = "updated-content=true\nversion=2\n"
	initialHTTPContent = "initial-http-content"
	updatedHTTPContent = "updated-http-content-v2"

	gitConfigName    = "git-config"
	httpConfigName   = "http-config"
	secretConfigName = "secret-config"
	fleetLabelKey    = "fleet"

	secretDataKey      = "app-key"
	initialSecretValue = "initial-value"
	updatedSecretValue = "updated-value"
	secretMountPath    = "/etc/app/secret-data"

	repoNotAccessibleTimeout = 2 * time.Minute
	repoAccessInterval       = 5 * time.Second
	repoAccessible           = 3 * time.Minute
	updatedContentMarker     = "updated-content=true"

	probeCycleCompleteMsg = "Expected DependencySyncProbeFailed from invalid probe fleet — periodic completed a full probe cycle"

	invalidHTTPProbeMountPath = "/etc/invalid-probe"
	parameterizedBranchLabel  = "branch"
	parameterizedTargetBranch = "staging"
)

var _ = Describe("Dependency Sync", Label("dependency-sync"), func() {
	var (
		harness          *e2e.Harness
		testID           string
		gitServer        e2e.GitServerConfig
		gitInternalHost  string
		gitInternalPort  int
		sshKeyPath       util.SSHPrivateKeyPath
		sshKeyContent    util.SSHPrivateKeyContent
		secretNamespace  string
		releaseNamespace string
		fileServer       *auxiliary.FileServer
	)

	BeforeEach(func() {
		workerID := GinkgoParallelProcess()
		harness = e2e.GetWorkerHarness()

		suiteCtx := e2e.GetWorkerContext()
		ctx := util.StartSpecTracerForGinkgo(suiteCtx)
		harness.SetTestContext(ctx)
		testID = harness.GetTestIDFromContext()
		Expect(harness.SetupContainerFromPoolAndStartAgent(workerID)).To(Succeed())

		svc := auxiliary.Get(harness.Context)
		Expect(svc).ToNot(BeNil(), "auxiliary services must be initialized")
		fileServer = svc.FileServer
		gitServer = e2e.GitServerConfig{Host: svc.GitServer.Host, Port: svc.GitServer.Port, User: "user"}
		gitInternalHost = svc.GitServer.InternalHost
		gitInternalPort = svc.GitServer.InternalPort

		var sshErr error
		sshKeyPath, sshErr = svc.GetGitSSHPrivateKeyPath()
		Expect(sshErr).ToNot(HaveOccurred(), "failed to get git SSH private key path")
		Expect(string(sshKeyPath)).ToNot(BeEmpty())
		sshKeyContent, sshErr = svc.GetGitSSHPrivateKey()
		Expect(sshErr).ToNot(HaveOccurred(), "failed to get git SSH private key content")
		Expect(string(sshKeyContent)).ToNot(BeEmpty())

		p := setup.GetDefaultProviders()
		Expect(p).ToNot(BeNil(), "infra providers must be initialized")
		releaseNamespace = p.Infra.GetExternalNamespace()
		secretNamespace = p.Infra.GetInternalNamespace()
		if secretNamespace == "" {
			secretNamespace = releaseNamespace
		}

	})

	AfterEach(func() {
		workerID := GinkgoParallelProcess()
		GinkgoWriter.Printf("[AfterEach] Worker %d: Cleaning up test resources\n", workerID)

		harness.PrintAgentLogsIfFailed()
		harness.CaptureDeploymentLogsIfFailed()

		err := harness.CleanUpAllTestResources()
		Expect(err).ToNot(HaveOccurred())

		suiteCtx := e2e.GetWorkerContext()
		harness.SetTestContext(suiteCtx)

		GinkgoWriter.Printf("[AfterEach] Worker %d: Test cleanup completed\n", workerID)
	})

	Context("dependency sync", func() {
		It("should create a new TV, emit DependencyChangeDetected, and update device when a git commit is pushed", Label("89092", "sanity", "agent"), func() {
			repoName := fmt.Sprintf("dep-sync-git-%s", testID)
			fleetName := fmt.Sprintf("dep-sync-git-fleet-%s", testID)

			By("setting up a git repository with initial content and registering it")
			err := harness.SetupGitRepoWithContent(e2e.GitRepoSetupOpts{
				GitServer:     gitServer,
				SSHKeyPath:    sshKeyPath,
				SSHKeyContent: sshKeyContent,
				InternalHost:  gitInternalHost,
				InternalPort:  gitInternalPort,
				RepoName:      repoName,
				FilePath:      configFileName,
				Content:       initialContent,
				CommitMsg:     "Initial config commit",
				AccessTimeout: repoAccessible,
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = harness.DeleteGitRepositoryOnServer(gitServer, sshKeyPath, repoName) })

			By("creating a fleet referencing the git config and enrolling a device")
			gitConfig, err := util.BuildGitConfigSpec(gitConfigName, repoName, gitBranch, configFilePath)
			Expect(err).ToNot(HaveOccurred())

			deviceID, initialVersion, err := harness.CreateFleetAndEnrollDevice(fleetName, fleetLabelKey, gitConfig)
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceID).ToNot(BeEmpty())
			Expect(initialVersion).To(BeNumerically(">=", 1))

			By("capturing the initial template version count")
			initialTVCount, err := harness.CountTemplateVersions(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(initialTVCount).To(BeNumerically(">=", 1))
			GinkgoWriter.Printf("Initial template version count: %d\n", initialTVCount)

			By("waiting for the first fingerprint and a full git probe cycle before pushing changes")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, gitConfigName), e2e.TIMEOUT, POLLING).ShouldNot(BeEmpty())
			gitProbeRepoName, gitProbeErr := setupInvalidGitProbeFleet(harness, testID, gitInternalHost, gitInternalPort, sshKeyContent)
			Expect(gitProbeErr).ToNot(HaveOccurred(), "failed to create git probe canary")
			Eventually(dependencySyncProbeFailedPoll(harness, gitResourceKey(gitProbeRepoName, gitBranch)), e2e.TIMEOUT, POLLING).Should(BeTrue(), probeCycleCompleteMsg)

			By("pushing a new commit to the git repository")
			err = harness.PushContentToGitServerRepo(gitServer, sshKeyPath, repoName,
				configFileName, updatedContent, "Update config")
			Expect(err).ToNot(HaveOccurred())

			By("verifying a new template version is created")
			Eventually(templateVersionCountPoll(harness, fleetName), e2e.TIMEOUT, POLLING).Should(BeNumerically(">", initialTVCount))

			By("retrieving the latest commit SHA from the git server")
			commitSHA, err := harness.GetRemoteHeadSHA(gitServer, sshKeyPath, repoName, gitBranch)
			Expect(err).ToNot(HaveOccurred())
			Expect(commitSHA).ToNot(BeEmpty(), "Expected a non-empty commit SHA from the git server")
			GinkgoWriter.Printf("Expected commit SHA: %s\n", commitSHA)

			By("verifying DependencyChangeDetected event was emitted with the correct fingerprint")
			Eventually(dependencyChangeDetectedPoll(harness, gitResourceKey(repoName, gitBranch), commitSHA), EVENTTIMEOUT, POLLING).Should(BeTrue(),
				"Expected DependencyChangeDetected event with fingerprint matching commit SHA")

			By("verifying device dependencySync fingerprint matches the new commit SHA")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, gitConfigName), RENDERTIMEOUT, POLLING).Should(Equal(commitSHA),
				"Expected device dependencySync fingerprint to match the latest git commit SHA")

			By("verifying the device rendered version bumped after the dependency sync")
			Eventually(renderedVersionPoll(harness, deviceID), RENDERTIMEOUT, POLLING).Should(BeNumerically(">", initialVersion))

			By("verifying the updated content was delivered to the device")
			Eventually(deviceFileContentPoll(harness, deviceConfigFile), e2e.TIMEOUT, POLLING).Should(ContainSubstring(updatedContentMarker))
		})

		It("should re-render a standalone device when a git commit is pushed", Label("89098", "sanity", "agent"), func() {
			repoName := fmt.Sprintf("dep-sync-standalone-%s", testID)

			By("setting up a git repository with initial content and registering it")
			err := harness.SetupGitRepoWithContent(e2e.GitRepoSetupOpts{
				GitServer:     gitServer,
				SSHKeyPath:    sshKeyPath,
				SSHKeyContent: sshKeyContent,
				InternalHost:  gitInternalHost,
				InternalPort:  gitInternalPort,
				RepoName:      repoName,
				FilePath:      configFileName,
				Content:       initialContent,
				CommitMsg:     "Initial config commit",
				AccessTimeout: repoAccessible,
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = harness.DeleteGitRepositoryOnServer(gitServer, sshKeyPath, repoName) })

			By("enrolling a standalone device (no fleet)")
			deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
			Expect(deviceID).ToNot(BeEmpty())

			By("applying a git config spec directly to the standalone device")
			gitConfig, err := util.BuildGitConfigSpec(gitConfigName, repoName, gitBranch, configFilePath)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceID, []v1beta1.ConfigProviderSpec{gitConfig}, 1)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the device to render and become UpToDate")
			initialVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(initialVersion).To(BeNumerically(">=", 1))
			GinkgoWriter.Printf("Standalone device initial rendered version: %d\n", initialVersion)

			By("waiting for the first fingerprint and a full git probe cycle before pushing changes")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, gitConfigName), e2e.TIMEOUT, POLLING).ShouldNot(BeEmpty())
			gitProbeRepoName, gitProbeErr := setupInvalidGitProbeFleet(harness, testID, gitInternalHost, gitInternalPort, sshKeyContent)
			Expect(gitProbeErr).ToNot(HaveOccurred(), "failed to create git probe canary")
			Eventually(dependencySyncProbeFailedPoll(harness, gitResourceKey(gitProbeRepoName, gitBranch)), e2e.TIMEOUT, POLLING).Should(BeTrue(), probeCycleCompleteMsg)

			By("pushing a new commit to the git repository")
			err = harness.PushContentToGitServerRepo(gitServer, sshKeyPath, repoName,
				configFileName, updatedContent, "Update config")
			Expect(err).ToNot(HaveOccurred())

			By("verifying the standalone device re-renders with the new commit SHA")
			commitSHA, err := harness.GetRemoteHeadSHA(gitServer, sshKeyPath, repoName, gitBranch)
			Expect(err).ToNot(HaveOccurred())
			Expect(commitSHA).ToNot(BeEmpty())
			GinkgoWriter.Printf("Expected standalone device fingerprint: %s\n", commitSHA)

			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, gitConfigName), e2e.TIMEOUT, POLLING).Should(Equal(commitSHA),
				"Standalone device should re-render with the new git commit SHA")

			By("verifying the rendered version bumped")
			Eventually(renderedVersionPoll(harness, deviceID), RENDERTIMEOUT, POLLING).Should(BeNumerically(">", initialVersion))

			By("verifying the updated content was delivered to the device")
			Eventually(deviceFileContentPoll(harness, deviceConfigFile), e2e.TIMEOUT, POLLING).Should(ContainSubstring(updatedContentMarker))
		})

		It("should create a new TV and show fingerprint in device status when HTTP content changes", Label("89091", "sanity", "agent"), func() {
			fleetName := fmt.Sprintf("dep-sync-http-fleet-%s", testID)
			repoName := fmt.Sprintf("dep-sync-http-repo-%s", testID)
			httpFilePath := fmt.Sprintf("%s/http-content.txt", testID)

			By("setting up HTTP content and registering the repository")
			repo, err := harness.SetupHTTPRepoWithContent(e2e.HTTPRepoSetupOpts{
				FileServer: fileServer,
				RepoName:   repoName,
				FilePath:   httpFilePath,
				Content:    initialHTTPContent,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(repo).ToNot(BeNil())

			By("creating a fleet referencing the HTTP config and enrolling a device")
			httpConfig, err := util.BuildHTTPConfigSpec(httpConfigName, repoName, httpMountPath, nil)
			Expect(err).ToNot(HaveOccurred())

			deviceID, _, err := harness.CreateFleetAndEnrollDevice(fleetName, fleetLabelKey, httpConfig)
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceID).ToNot(BeEmpty())

			By("waiting for the initial template version and capturing count")
			Eventually(templateVersionCountPoll(harness, fleetName), e2e.TIMEOUT, POLLING).Should(BeNumerically(">=", 1))
			initialTVCount, err := harness.CountTemplateVersions(fleetName)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the first HTTP fingerprint and a full HTTP probe cycle")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, httpConfigName), RENDERTIMEOUT, POLLING).ShouldNot(BeEmpty())
			preUpdateFingerprint, err := harness.GetDeviceConfigRefFingerprint(deviceID, httpConfigName)
			Expect(err).ToNot(HaveOccurred())
			httpProbeRepoName, httpProbeErr := setupInvalidHTTPProbeFleet(harness, testID, fileServer.GetInternalURL())
			Expect(httpProbeErr).ToNot(HaveOccurred(), "failed to create HTTP probe canary")
			Eventually(dependencySyncProbeFailedPoll(harness, httpResourceKey(httpProbeRepoName, "")), e2e.TIMEOUT, POLLING).Should(BeTrue(), probeCycleCompleteMsg)

			By("updating the HTTP file content on the file server")
			err = fileServer.PushFile(httpFilePath, updatedHTTPContent)
			Expect(err).ToNot(HaveOccurred())

			By("verifying a new template version is created after HTTP content change")
			Eventually(templateVersionCountPoll(harness, fleetName), e2e.TIMEOUT, POLLING).Should(BeNumerically(">", initialTVCount))

			By("verifying device dependencySync fingerprint changed after HTTP update")
			Eventually(fingerprintChangedPoll(harness, deviceID, httpConfigName, preUpdateFingerprint), RENDERTIMEOUT, POLLING).Should(BeTrue())

			By("verifying the updated HTTP content was delivered to the device")
			Eventually(deviceFileContentPoll(harness, httpMountPath), e2e.TIMEOUT, POLLING).Should(ContainSubstring(updatedHTTPContent))
		})

		It("should re-render a standalone device when HTTP content changes", Label("89299", "sanity", "agent"), func() {
			repoName := fmt.Sprintf("dep-sync-http-standalone-%s", testID)
			httpFilePath := fmt.Sprintf("%s/http-standalone-content.txt", testID)

			By("setting up HTTP content and registering the repository")
			repo, err := harness.SetupHTTPRepoWithContent(e2e.HTTPRepoSetupOpts{
				FileServer: fileServer,
				RepoName:   repoName,
				FilePath:   httpFilePath,
				Content:    initialHTTPContent,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(repo).ToNot(BeNil())

			By("enrolling a standalone device (no fleet)")
			deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
			Expect(deviceID).ToNot(BeEmpty())

			By("applying an HTTP config spec directly to the standalone device")
			httpConfig, err := util.BuildHTTPConfigSpec(httpConfigName, repoName, httpMountPath, nil)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceID, []v1beta1.ConfigProviderSpec{httpConfig}, 1)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the device to render and capturing initial state")
			initialVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(initialVersion).To(BeNumerically(">=", 1))
			GinkgoWriter.Printf("Standalone HTTP device initial rendered version: %d\n", initialVersion)

			By("waiting for the first HTTP fingerprint and a full HTTP probe cycle")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, httpConfigName), RENDERTIMEOUT, POLLING).ShouldNot(BeEmpty())
			preUpdateFingerprint, err := harness.GetDeviceConfigRefFingerprint(deviceID, httpConfigName)
			Expect(err).ToNot(HaveOccurred())
			httpProbeRepoName, httpProbeErr := setupInvalidHTTPProbeFleet(harness, testID, fileServer.GetInternalURL())
			Expect(httpProbeErr).ToNot(HaveOccurred(), "failed to create HTTP probe canary")
			Eventually(dependencySyncProbeFailedPoll(harness, httpResourceKey(httpProbeRepoName, "")), e2e.TIMEOUT, POLLING).Should(BeTrue(), probeCycleCompleteMsg)

			By("updating the HTTP file content on the file server")
			err = fileServer.PushFile(httpFilePath, updatedHTTPContent)
			Expect(err).ToNot(HaveOccurred())

			By("verifying the standalone device fingerprint changed")
			Eventually(fingerprintChangedPoll(harness, deviceID, httpConfigName, preUpdateFingerprint), e2e.TIMEOUT, POLLING).Should(BeTrue())

			By("verifying the rendered version bumped")
			Eventually(renderedVersionPoll(harness, deviceID), RENDERTIMEOUT, POLLING).Should(BeNumerically(">", initialVersion))

			By("verifying the updated HTTP content was delivered to the device")
			Eventually(deviceFileContentPoll(harness, httpMountPath), e2e.TIMEOUT, POLLING).Should(ContainSubstring(updatedHTTPContent))
		})

		It("should create a new TV and update device fingerprint when a K8s secret is updated", Label("89094", "sanity", "agent"), func() {
			infra.SkipIfNotK8s("K8s secret sync requires Kubernetes")
			fleetName := fmt.Sprintf("dep-sync-secret-fleet-%s", testID)
			secretName := fmt.Sprintf("dep-sync-secret-%s", testID)

			By("creating a K8s secret for the test")
			err := harness.ManageK8sSecret(e2e.K8sSecretCreate, secretNamespace, secretName, releaseNamespace, map[string]string{secretDataKey: initialSecretValue})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = harness.ManageK8sSecret(e2e.K8sSecretDelete, secretNamespace, secretName, "", nil) })

			By("creating a fleet referencing the K8s secret config and enrolling a device")
			secretConfig, err := util.BuildK8sSecretConfigSpec(secretConfigName, secretName, secretNamespace, secretMountPath)
			Expect(err).ToNot(HaveOccurred())

			deviceID, initialVersion, err := harness.CreateFleetAndEnrollDevice(fleetName, fleetLabelKey, secretConfig)
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceID).ToNot(BeEmpty())
			Expect(initialVersion).To(BeNumerically(">=", 1))

			By("capturing the initial template version count")
			initialTVCount, err := harness.CountTemplateVersions(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(initialTVCount).To(BeNumerically(">=", 1))

			By("re-applying the K8s secret to seed the initial fingerprint via the informer")
			err = harness.ManageK8sSecret(e2e.K8sSecretPatch, secretNamespace, secretName, releaseNamespace, map[string]string{secretDataKey: initialSecretValue})
			Expect(err).ToNot(HaveOccurred())

			By("capturing the pre-update secret fingerprint")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, secretConfigName), RENDERTIMEOUT, POLLING).ShouldNot(BeEmpty())
			preUpdateSecretFingerprint, err := harness.GetDeviceConfigRefFingerprint(deviceID, secretConfigName)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Pre-update secret fingerprint: %s\n", preUpdateSecretFingerprint)

			By("verifying initial secret content was delivered to the device")
			secretDeviceFile := secretDeviceFilePath(secretMountPath)
			Eventually(deviceFileContentPoll(harness, secretDeviceFile), RENDERTIMEOUT, POLLING).Should(ContainSubstring(initialSecretValue))

			By("patching the K8s secret to trigger a change")
			err = harness.ManageK8sSecret(e2e.K8sSecretPatch, secretNamespace, secretName, releaseNamespace, map[string]string{secretDataKey: updatedSecretValue})
			Expect(err).ToNot(HaveOccurred())

			By("verifying a new template version is created after secret change")
			Eventually(templateVersionCountPoll(harness, fleetName), e2e.TIMEOUT, POLLING).Should(BeNumerically(">", initialTVCount))

			By("verifying device dependencySync fingerprint changed after secret update")
			Eventually(fingerprintChangedPoll(harness, deviceID, secretConfigName, preUpdateSecretFingerprint), RENDERTIMEOUT, POLLING).Should(BeTrue())

			By("verifying the updated secret content was delivered to the device")
			Eventually(deviceFileContentPoll(harness, secretDeviceFile), e2e.TIMEOUT, POLLING).Should(ContainSubstring(updatedSecretValue))

			By("verifying the device rendered version bumped after the secret sync")
			Eventually(renderedVersionPoll(harness, deviceID), RENDERTIMEOUT, POLLING).Should(BeNumerically(">", initialVersion))
		})

		It("should re-render a standalone device when a K8s secret is updated", Label("89321", "sanity", "agent"), func() {
			infra.SkipIfNotK8s("K8s secret sync requires Kubernetes")
			secretName := fmt.Sprintf("ds-secret-sa-%s", testID)

			By("creating a K8s secret for the test")
			err := harness.ManageK8sSecret(e2e.K8sSecretCreate, secretNamespace, secretName, releaseNamespace, map[string]string{secretDataKey: initialSecretValue})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = harness.ManageK8sSecret(e2e.K8sSecretDelete, secretNamespace, secretName, "", nil) })

			By("enrolling a standalone device (no fleet)")
			deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
			Expect(deviceID).ToNot(BeEmpty())

			By("applying a K8s secret config spec directly to the standalone device")
			secretConfig, err := util.BuildK8sSecretConfigSpec(secretConfigName, secretName, secretNamespace, secretMountPath)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceID, []v1beta1.ConfigProviderSpec{secretConfig}, 1)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the device to render its initial spec")
			initialVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(initialVersion).To(BeNumerically(">=", 1))

			By("re-applying the K8s secret to seed the initial fingerprint via the informer")
			err = harness.ManageK8sSecret(e2e.K8sSecretPatch, secretNamespace, secretName, releaseNamespace, map[string]string{secretDataKey: initialSecretValue})
			Expect(err).ToNot(HaveOccurred())

			By("capturing the pre-update secret fingerprint")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, secretConfigName), RENDERTIMEOUT, POLLING).ShouldNot(BeEmpty())
			preUpdateFingerprint, err := harness.GetDeviceConfigRefFingerprint(deviceID, secretConfigName)
			Expect(err).ToNot(HaveOccurred())

			By("verifying initial secret content was delivered to the device")
			secretDeviceFile := secretDeviceFilePath(secretMountPath)
			Eventually(deviceFileContentPoll(harness, secretDeviceFile), RENDERTIMEOUT, POLLING).Should(ContainSubstring(initialSecretValue))

			By("patching the K8s secret to trigger a change")
			err = harness.ManageK8sSecret(e2e.K8sSecretPatch, secretNamespace, secretName, releaseNamespace, map[string]string{secretDataKey: updatedSecretValue})
			Expect(err).ToNot(HaveOccurred())

			By("verifying device dependencySync fingerprint changed after secret update")
			Eventually(fingerprintChangedPoll(harness, deviceID, secretConfigName, preUpdateFingerprint), e2e.TIMEOUT, POLLING).Should(BeTrue())

			By("verifying the device rendered version bumped")
			Eventually(renderedVersionPoll(harness, deviceID), RENDERTIMEOUT, POLLING).Should(BeNumerically(">", initialVersion))

			By("verifying the updated secret content was delivered to the device")
			Eventually(deviceFileContentPoll(harness, secretDeviceFile), e2e.TIMEOUT, POLLING).Should(ContainSubstring(updatedSecretValue))
		})

		It("should sync the good fleet and emit DependencySyncProbeFailed for the bad fleet without blocking", Label("89093", "sanity"), func() {
			badRepoName := fmt.Sprintf("dep-sync-badcred-%s", testID)
			badFleetName := fmt.Sprintf("dep-sync-badcred-fleet-%s", testID)
			goodRepoName := fmt.Sprintf("dep-sync-goodcred-%s", testID)
			goodFleetName := fmt.Sprintf("dep-sync-goodcred-fleet-%s", testID)

			By("setting up a git repository with initial content for the good fleet")
			err := harness.SetupGitRepoWithContent(e2e.GitRepoSetupOpts{
				GitServer:     gitServer,
				SSHKeyPath:    sshKeyPath,
				SSHKeyContent: sshKeyContent,
				InternalHost:  gitInternalHost,
				InternalPort:  gitInternalPort,
				RepoName:      goodRepoName,
				FilePath:      configFileName,
				Content:       initialContent,
				CommitMsg:     "Initial good config",
				AccessTimeout: repoAccessible,
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = harness.DeleteGitRepositoryOnServer(gitServer, sshKeyPath, goodRepoName) })

			By("creating the fleet with good credentials")
			goodConfig, err := util.BuildGitConfigSpec("good-config", goodRepoName, gitBranch, configFilePath)
			Expect(err).ToNot(HaveOccurred())

			err = harness.CreateFleetWithLabelConfig(goodFleetName, fleetLabelKey, goodConfig)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the good fleet to get its initial template version")
			Eventually(templateVersionCountPoll(harness, goodFleetName), e2e.TIMEOUT, POLLING).Should(BeNumerically(">=", 1))
			initialGoodTVCount, err := harness.CountTemplateVersions(goodFleetName)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Good fleet initial TV count: %d\n", initialGoodTVCount)

			By("creating a Repository with invalid credentials for the bad fleet")
			err = harness.CreateRepositoryWithSSHCredentials(badRepoName, invalidGitRepositoryURL(gitInternalHost, gitInternalPort), sshKeyContent)
			Expect(err).ToNot(HaveOccurred())

			By("creating the fleet with bad credentials")
			badConfig, err := util.BuildGitConfigSpec("bad-config", badRepoName, gitBranch, configFilePath)
			Expect(err).ToNot(HaveOccurred())

			err = harness.CreateFleetWithLabelConfig(badFleetName, fleetLabelKey, badConfig)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the periodic to run its first probe (indicated by DependencySyncProbeFailed for the bad fleet)")
			Eventually(dependencySyncProbeFailedPoll(harness, gitResourceKey(badRepoName, gitBranch)), e2e.TIMEOUT, POLLING).Should(BeTrue(),
				"Expected the periodic to probe the bad fleet and emit a ProbeFailed event")

			By("pushing a new commit to the good repo to trigger dependency sync")
			err = harness.PushContentToGitServerRepo(gitServer, sshKeyPath, goodRepoName,
				configFileName, updatedContent, "Update good config")
			Expect(err).ToNot(HaveOccurred())

			By("verifying the good fleet gets a NEW template version from dependency sync")
			Eventually(templateVersionCountPoll(harness, goodFleetName), e2e.TIMEOUT, POLLING).Should(BeNumerically(">", initialGoodTVCount))

			By("verifying the bad fleet did not get a dependency-sync-triggered template version")
			Consistently(templateVersionCountPoll(harness, badFleetName), "10s", POLLING).Should(BeNumerically("<=", 1),
				"Bad fleet should have at most its initial TV (no dependency sync TV)")

			By("verifying the bad fleet repository is not accessible")
			err = harness.WaitForRepositoryNotAccessible(badRepoName, repoNotAccessibleTimeout, repoAccessInterval)
			Expect(err).ToNot(HaveOccurred())

			By("verifying DependencySyncProbeFailed event was emitted with resourceKey and error")
			Eventually(dependencySyncProbeFailedPoll(harness, gitResourceKey(badRepoName, gitBranch)), EVENTTIMEOUT, POLLING).Should(BeTrue(),
				"Expected DependencySyncProbeFailed event for invalid credentials")
		})

		It("should sync both git and HTTP providers independently in a multi-provider fleet", Label("89272", "sanity", "agent"), func() {
			repoName := fmt.Sprintf("dep-sync-multi-%s", testID)
			fleetName := fmt.Sprintf("dep-sync-multi-fleet-%s", testID)

			By("setting up a git repository with initial content and registering it")
			err := harness.SetupGitRepoWithContent(e2e.GitRepoSetupOpts{
				GitServer:     gitServer,
				SSHKeyPath:    sshKeyPath,
				SSHKeyContent: sshKeyContent,
				InternalHost:  gitInternalHost,
				InternalPort:  gitInternalPort,
				RepoName:      repoName,
				FilePath:      configFileName,
				Content:       initialContent,
				CommitMsg:     "Initial config commit",
				AccessTimeout: repoAccessible,
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = harness.DeleteGitRepositoryOnServer(gitServer, sshKeyPath, repoName) })

			By("setting up HTTP content and registering the repository")
			httpRepoName := fmt.Sprintf("dep-sync-multi-http-%s", testID)
			httpFilePath := fmt.Sprintf("%s/multi-http-content.txt", testID)
			repo, err := harness.SetupHTTPRepoWithContent(e2e.HTTPRepoSetupOpts{
				FileServer: fileServer,
				RepoName:   httpRepoName,
				FilePath:   httpFilePath,
				Content:    initialHTTPContent,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(repo).ToNot(BeNil())

			By("creating a fleet with both git and HTTP config providers")
			gitConfig, err := util.BuildGitConfigSpec(gitConfigName, repoName, gitBranch, configFilePath)
			Expect(err).ToNot(HaveOccurred())
			httpConfig, err := util.BuildHTTPConfigSpec(httpConfigName, httpRepoName, httpMountPath, nil)
			Expect(err).ToNot(HaveOccurred())

			deviceID, _, err := harness.CreateFleetAndEnrollDevice(fleetName, fleetLabelKey, gitConfig, httpConfig)
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceID).ToNot(BeEmpty())

			By("waiting for initial template version and both fingerprints to populate")
			Eventually(templateVersionCountPoll(harness, fleetName), e2e.TIMEOUT, POLLING).Should(BeNumerically(">=", 1))
			initialTVCount, err := harness.CountTemplateVersions(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, gitConfigName), RENDERTIMEOUT, POLLING).ShouldNot(BeEmpty())
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, httpConfigName), RENDERTIMEOUT, POLLING).ShouldNot(BeEmpty())
			preHTTPFingerprint, err := harness.GetDeviceConfigRefFingerprint(deviceID, httpConfigName)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for full git and HTTP probe cycles before pushing changes")
			gitProbeRepoName, gitProbeErr := setupInvalidGitProbeFleet(harness, testID, gitInternalHost, gitInternalPort, sshKeyContent)
			Expect(gitProbeErr).ToNot(HaveOccurred(), "failed to create git probe canary")
			Eventually(dependencySyncProbeFailedPoll(harness, gitResourceKey(gitProbeRepoName, gitBranch)), e2e.TIMEOUT, POLLING).Should(BeTrue(), probeCycleCompleteMsg)
			httpProbeRepoName, httpProbeErr := setupInvalidHTTPProbeFleet(harness, testID, fileServer.GetInternalURL())
			Expect(httpProbeErr).ToNot(HaveOccurred(), "failed to create HTTP probe canary")
			Eventually(dependencySyncProbeFailedPoll(harness, httpResourceKey(httpProbeRepoName, "")), e2e.TIMEOUT, POLLING).Should(BeTrue(), probeCycleCompleteMsg)

			By("pushing a new git commit to trigger dependency sync")
			err = harness.PushContentToGitServerRepo(gitServer, sshKeyPath, repoName,
				configFileName, updatedContent, "Update config")
			Expect(err).ToNot(HaveOccurred())

			commitSHA, err := harness.GetRemoteHeadSHA(gitServer, sshKeyPath, repoName, gitBranch)
			Expect(err).ToNot(HaveOccurred())

			By("updating the HTTP file content on the file server")
			err = fileServer.PushFile(httpFilePath, updatedHTTPContent)
			Expect(err).ToNot(HaveOccurred())

			By("verifying both fingerprints updated")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, gitConfigName), e2e.TIMEOUT, POLLING).Should(Equal(commitSHA),
				"Git fingerprint should update to new commit SHA")
			Eventually(fingerprintChangedPoll(harness, deviceID, httpConfigName, preHTTPFingerprint), e2e.TIMEOUT, POLLING).Should(BeTrue())

			By("verifying new template versions were created")
			Eventually(templateVersionCountPoll(harness, fleetName), e2e.TIMEOUT, POLLING).Should(
				BeNumerically(">", initialTVCount),
				"Multi-provider fleet should get new TVs from dependency sync")

			By("verifying both updated contents were delivered to the device")
			Eventually(deviceFileContentPoll(harness, deviceConfigFile), e2e.TIMEOUT, POLLING).Should(ContainSubstring(updatedContentMarker))
			Eventually(deviceFileContentPoll(harness, httpMountPath), e2e.TIMEOUT, POLLING).Should(ContainSubstring(updatedHTTPContent))
		})

		It("should sync a parameterized targetRevision resolved per-device label", Label("89301", "sanity", "agent"), func() {
			repoName := fmt.Sprintf("dep-sync-param-%s", testID)
			fleetName := fmt.Sprintf("dep-sync-param-fleet-%s", testID)

			By("setting up a git repository with initial content on main")
			err := harness.SetupGitRepoWithContent(e2e.GitRepoSetupOpts{
				GitServer:     gitServer,
				SSHKeyPath:    sshKeyPath,
				SSHKeyContent: sshKeyContent,
				InternalHost:  gitInternalHost,
				InternalPort:  gitInternalPort,
				RepoName:      repoName,
				FilePath:      configFileName,
				Content:       initialContent,
				CommitMsg:     "Initial config on main",
				AccessTimeout: repoAccessible,
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = harness.DeleteGitRepositoryOnServer(gitServer, sshKeyPath, repoName) })

			By("creating the staging branch with initial content")
			err = harness.CreateGitBranchOnServer(gitServer, sshKeyPath, repoName, parameterizedTargetBranch)
			Expect(err).ToNot(HaveOccurred())

			By("creating a fleet with parameterized targetRevision referencing device label")
			gitConfig, err := util.BuildGitConfigSpec(gitConfigName, repoName, "{{ .metadata.labels.branch }}", configFilePath)
			Expect(err).ToNot(HaveOccurred())

			err = harness.CreateFleetWithLabelConfig(fleetName, fleetLabelKey, gitConfig)
			Expect(err).ToNot(HaveOccurred())

			By("enrolling a device with the fleet selector and branch label")
			deviceID, _ := harness.EnrollAndWaitForOnlineStatus(
				map[string]string{fleetLabelKey: fleetName, parameterizedBranchLabel: parameterizedTargetBranch},
			)
			Expect(deviceID).ToNot(BeEmpty())

			By("waiting for the device to render its initial spec")
			initialVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceID)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Parameterized device initial rendered version: %d\n", initialVersion)
			Expect(initialVersion).To(BeNumerically(">=", 1))

			By("capturing the initial template version count")
			initialTVCount, err := harness.CountTemplateVersions(fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(initialTVCount).To(BeNumerically(">=", 1))

			By("waiting for the first fingerprint and a full git probe cycle before pushing changes")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, gitConfigName), e2e.TIMEOUT, POLLING).ShouldNot(BeEmpty())
			gitProbeRepoName, gitProbeErr := setupInvalidGitProbeFleet(harness, testID, gitInternalHost, gitInternalPort, sshKeyContent)
			Expect(gitProbeErr).ToNot(HaveOccurred(), "failed to create git probe canary")
			Eventually(dependencySyncProbeFailedPoll(harness, gitResourceKey(gitProbeRepoName, gitBranch)), e2e.TIMEOUT, POLLING).Should(BeTrue(), probeCycleCompleteMsg)

			By("pushing updated content to the staging branch")
			err = harness.PushContentToGitServerRepoBranch(gitServer, sshKeyPath, repoName,
				parameterizedTargetBranch, configFileName, updatedContent, "Update config on staging")
			Expect(err).ToNot(HaveOccurred())

			By("retrieving the latest commit SHA from the staging branch")
			commitSHA, err := harness.GetRemoteHeadSHA(gitServer, sshKeyPath, repoName, parameterizedTargetBranch)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Expected staging commit SHA: %s\n", commitSHA)
			Expect(commitSHA).ToNot(BeEmpty())

			By("verifying DependencyChangeDetected event was emitted for the device")
			Eventually(dependencyChangeDetectedPoll(harness, gitResourceKey(repoName, parameterizedTargetBranch), commitSHA), e2e.TIMEOUT, POLLING).Should(BeTrue(),
				"Expected DependencyChangeDetected event with fingerprint matching staging commit SHA")

			By("verifying device dependencySync fingerprint matches the new staging commit SHA")
			Eventually(deviceConfigRefFingerprintPoll(harness, deviceID, gitConfigName), e2e.TIMEOUT, POLLING).Should(Equal(commitSHA),
				"Expected device fingerprint to match staging branch commit SHA")

			By("verifying the template version count did NOT increase (parameterized path skips fleet-level TV)")
			Consistently(templateVersionCountPoll(harness, fleetName), "10s", POLLING).Should(Equal(initialTVCount),
				"Parameterized fleet should NOT get a new TV from dependency sync")

			By("verifying the device rendered version bumped after the dependency sync")
			Eventually(renderedVersionPoll(harness, deviceID), RENDERTIMEOUT, POLLING).Should(BeNumerically(">", initialVersion))

			By("verifying the updated content was delivered to the device")
			Eventually(deviceFileContentPoll(harness, deviceConfigFile), e2e.TIMEOUT, POLLING).Should(ContainSubstring(updatedContentMarker))
		})

	})
})

// invalidGitRepositoryURL returns a syntactically valid but unreachable git SSH URL.
func invalidGitRepositoryURL(gitInternalHost string, gitInternalPort int) string {
	return fmt.Sprintf("user@%s:%d:/home/user/repos/nonexistent.git", gitInternalHost, gitInternalPort)
}

// gitResourceKey returns the full resource key for a git dependency sync event.
func gitResourceKey(repoName, branch string) string {
	return fmt.Sprintf("git:%s/%s", repoName, branch)
}

// httpResourceKey returns the full resource key for an HTTP dependency sync event.
func httpResourceKey(repoName, suffix string) string {
	return fmt.Sprintf("http:%s/%s", repoName, suffix)
}

// secretDeviceFilePath returns the on-device path where the secret key is mounted.
func secretDeviceFilePath(mountPath string) string {
	return fmt.Sprintf("%s/%s", mountPath, secretDataKey)
}

// deviceConfigRefFingerprintPoll returns a poller that fetches the device's config-ref fingerprint.
func deviceConfigRefFingerprintPoll(h *e2e.Harness, deviceID, configProviderName string) func() (string, error) {
	return func() (string, error) {
		return h.GetDeviceConfigRefFingerprint(deviceID, configProviderName)
	}
}

// templateVersionCountPoll returns a poller that counts template versions for a fleet.
func templateVersionCountPoll(h *e2e.Harness, fleetName string) func() (int, error) {
	return func() (int, error) {
		return h.CountTemplateVersions(fleetName)
	}
}

// dependencyChangeDetectedPoll returns a poller that checks for a DependencyChangeDetected event.
func dependencyChangeDetectedPoll(h *e2e.Harness, resourceKey, fingerprint string) func() (bool, error) {
	return func() (bool, error) {
		return h.HasEventWithDetails(v1beta1.EventReasonDependencyChangeDetected, resourceKey, fingerprint)
	}
}

// deviceFileContentPoll returns a poller that reads a file from the enrolled device.
func deviceFileContentPoll(h *e2e.Harness, filePath string) func() (string, error) {
	return func() (string, error) {
		return h.ReadFileFromDevice(filePath)
	}
}

// fingerprintChangedPoll returns a poller that succeeds when the fingerprint differs from the previous one.
func fingerprintChangedPoll(h *e2e.Harness, deviceID, configProviderName, previousFingerprint string) func() (bool, error) {
	return func() (bool, error) {
		fp, err := h.GetDeviceConfigRefFingerprint(deviceID, configProviderName)
		if err != nil {
			return false, err
		}
		return fp != "" && fp != previousFingerprint, nil
	}
}

// dependencySyncProbeFailedPoll returns a poller that checks for a DependencySyncProbeFailed event.
func dependencySyncProbeFailedPoll(h *e2e.Harness, resourceKey string) func() (bool, error) {
	return func() (bool, error) {
		return h.HasEventWithDetails(v1beta1.EventReasonDependencySyncProbeFailed, resourceKey, "")
	}
}

// renderedVersionPoll returns a poller that fetches the device's current rendered version.
func renderedVersionPoll(h *e2e.Harness, deviceID string) func() (int, error) {
	return func() (int, error) {
		return h.GetCurrentDeviceRenderedVersion(deviceID)
	}
}

// setupInvalidGitProbeFleet registers a git repo with invalid credentials and a fleet
// that references it. Callers wait for DependencySyncProbeFailed on the returned repo
// name before pushing content changes: the periodic only emits that event after a full
// git probe cycle, by which time real dependency refs should already be stored as firstSeen.
// Parameterized targetRevision refs are skipped at fleet level but resolved per-device
// during rollout; those device-level git refs are polled in the same git periodic task.
func setupInvalidGitProbeFleet(h *e2e.Harness, testID, gitInternalHost string, gitInternalPort int, sshKey util.SSHPrivateKeyContent) (repoName string, err error) {
	if h == nil {
		return "", fmt.Errorf("harness must not be nil")
	}
	if testID == "" {
		return "", fmt.Errorf("test ID must not be empty")
	}
	repoName = fmt.Sprintf("invalid-git-probe-repo-%s", testID)
	fleetName := fmt.Sprintf("invalid-git-probe-fleet-%s", testID)

	if err := h.CreateRepositoryWithSSHCredentials(repoName, invalidGitRepositoryURL(gitInternalHost, gitInternalPort), sshKey); err != nil {
		return "", err
	}

	configSpec, err := util.BuildGitConfigSpec("invalid-probe-config", repoName, gitBranch, configFilePath)
	if err != nil {
		return "", err
	}
	if err := h.CreateFleetWithLabelConfig(fleetName, fleetLabelKey, configSpec); err != nil {
		return "", err
	}

	return repoName, nil
}

// setupInvalidHTTPProbeFleet registers an HTTP repo pointing at a missing file and a fleet
// that references it. Callers wait for DependencySyncProbeFailed on the returned repo name
// before pushing HTTP content changes — same firstSeen gate as setupInvalidGitProbeFleet,
// but for the separate HTTP periodic task.
func setupInvalidHTTPProbeFleet(h *e2e.Harness, testID, fileServerInternalURL string) (repoName string, err error) {
	if h == nil {
		return "", fmt.Errorf("harness must not be nil")
	}
	if testID == "" {
		return "", fmt.Errorf("test ID must not be empty")
	}
	if fileServerInternalURL == "" {
		return "", fmt.Errorf("file server internal URL must not be empty")
	}
	repoName = fmt.Sprintf("invalid-http-probe-repo-%s", testID)
	fleetName := fmt.Sprintf("invalid-http-probe-fleet-%s", testID)

	invalidURL := fmt.Sprintf("%s/invalid-http-probe-%s.txt", fileServerInternalURL, testID)
	if _, err := h.CreateHTTPRepository(repoName, invalidURL, nil, nil); err != nil {
		return "", err
	}

	configSpec, err := util.BuildHTTPConfigSpec("invalid-http-probe-config", repoName, invalidHTTPProbeMountPath, nil)
	if err != nil {
		return "", err
	}
	if err := h.CreateFleetWithLabelConfig(fleetName, fleetLabelKey, configSpec); err != nil {
		return "", err
	}

	return repoName, nil
}
