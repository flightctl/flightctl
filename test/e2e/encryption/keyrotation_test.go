package encryption_test

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Encryption at rest — Key rotation metrics and migration", Label("encryption", "observability"), Serial, func() {
	var (
		harness     *e2e.Harness
		providers   *infra.Providers
		promURL     string
		cleanupProm func()
		savedConfig string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		providers = setup.GetDefaultProviders()
		infra.SkipIfObservabilityNotConfigured(harness.GetTestContext(), providers)

		Expect(auxSvcs).ToNot(BeNil(), "auxiliary services must be initialized")

		var err error
		promURL, cleanupProm, err = prometheusURL()
		Expect(err).ToNot(HaveOccurred(), "must be able to reach Prometheus")
		Expect(promURL).ToNot(BeEmpty(), "Prometheus URL must not be empty")
		savedConfig = ""
	})

	AfterEach(func() {
		if cleanupProm != nil {
			cleanupProm()
		}
		if savedConfig != "" {
			deleteStaleCanaries(defaultKeyID)
			// Reset migration checkpoints so the restored default config does not inherit a stale
			// Complete=true checkpoint for rotated-metrics-key from this run (S8 rotates to that key
			// and lets the migration worker finish). Without this reset, a subsequent run that
			// re-uses the same key ID would skip re-encryption and time out.
			resetMigrationCheckpoints()
			if err := restoreEncryptionConfig(savedConfig); err != nil {
				GinkgoWriter.Printf("Warning: restore encryption config failed: %v\n", err)
			}
			if err := restartServicesAndWait(); err != nil {
				GinkgoWriter.Printf("Warning: service restart after config restore failed: %v\n", err)
			}
			if _, err := login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout); err != nil {
				GinkgoWriter.Printf("Warning: re-login after config restore failed: %v\n", err)
			}
		}
	})

	Context("When the active encryption key is rotated", func() {
		It("S8: should update the active-key metric and auto-migrate pre-existing resources", Label("90130"), func() {
			const rotatedKeyID = "rotated-metrics-key"

			By("creating pre-rotation resources encrypted under the default key")
			apName := "enc-rot-metrics-ap-" + harness.GetTestIDFromContext()
			repoName := "enc-rot-metrics-repo-" + harness.GetTestIDFromContext()

			issuerURL := auxSvcs.Keycloak.IssuerURL()
			secretBytes, err := generateAESKey()
			Expect(err).ToNot(HaveOccurred())
			preSecret := base64.StdEncoding.EncodeToString(secretBytes)
			manifest := buildOIDCAuthProviderYAML(apName, issuerURL, "flightctl-client", preSecret)
			_, err = applyManifest(harness, manifest)
			Expect(err).ToNot(HaveOccurred(), "create pre-rotation AuthProvider")
			DeferCleanup(func() { _ = deleteAuthProvider(harness, apName) })

			keyContent, err := auxSvcs.GetGitSSHPrivateKey()
			Expect(err).ToNot(HaveOccurred())
			repoURL := fmt.Sprintf("user@%s:%d:/home/user/repos/rot-metrics.git",
				auxSvcs.GitServer.Host, auxSvcs.GitServer.Port)
			Expect(harness.CreateRepositoryWithSSHCredentials(repoName, repoURL, keyContent)).To(Succeed())
			DeferCleanup(func() {
				_, _ = harness.ManageResource("delete", "repository", repoName)
			})

			By("confirming pre-rotation DB ciphertexts use the default key")
			apCipher, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", apName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(apCipher, defaultKeyID)).To(BeTrue(),
				"pre-rotation AuthProvider must use default key; got: %s", apCipher)

			By("rotating the active encryption key to " + rotatedKeyID)
			newKeyBytes, err := generateAESKey()
			Expect(err).ToNot(HaveOccurred())
			savedConfig, err = rotateEncryptionKey(rotatedKeyID, newKeyBytes)
			Expect(err).ToNot(HaveOccurred())

			By("restarting services to pick up the new config and trigger the migration worker")
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
			Expect(err).ToNot(HaveOccurred(), "re-login after restart")

			By("verifying active-key metric reflects the rotated key")
			activeKeyQuery := `flightctl_encryption_active_key_info{key_id="` + rotatedKeyID + `"}`
			Eventually(harness.PromQueryResultCount(promURL, activeKeyQuery),
				testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeNumerically(">", 0),
				"active-key gauge must report rotated-key after rotation and restart")

			By("waiting for the migration worker to re-encrypt the pre-rotation AuthProvider")
			Eventually(func() bool {
				cipher, err := queryDB(providers, fmt.Sprintf(
					"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", apName,
				))
				if err != nil {
					return false
				}
				return ciphertextMatchesKeyID(cipher, rotatedKeyID)
			}, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeTrue(),
				"migration worker must re-encrypt pre-rotation AuthProvider under the rotated key")

			By("waiting for the migration worker to re-encrypt the pre-rotation Repository")
			Eventually(func() bool {
				cipher, err := queryDB(providers, fmt.Sprintf(
					"SELECT spec->'sshConfig'->>'sshPrivateKey' FROM repositories WHERE name = '%s'", repoName,
				))
				if err != nil {
					return false
				}
				return ciphertextMatchesKeyID(cipher, rotatedKeyID)
			}, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeTrue(),
				"migration worker must re-encrypt pre-rotation Repository under the rotated key")

			By("cleanup: removing stale canary rows, resetting migration checkpoints, and restoring original config")
			deleteStaleCanaries(defaultKeyID)
			// Reset migration checkpoints so a subsequent run (with the same rotated-metrics-key ID)
			// does not inherit a Complete=true checkpoint from this run's successful migration.
			resetMigrationCheckpoints()
			Expect(restoreEncryptionConfig(savedConfig)).To(Succeed())
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
			Expect(err).ToNot(HaveOccurred())
			savedConfig = ""
		})
	})
})

var _ = Describe("Encryption at rest — Key rotation", Label("encryption"), Serial, func() {
	var (
		harness     *e2e.Harness
		providers   *infra.Providers
		savedConfig string
		// preRotationSecret holds the randomized client secret for pre-rotation resources.
		// It is set in BeforeAll and shared across S1/S2 within the Ordered context.
		preRotationSecret string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		providers = setup.GetDefaultProviders()
		savedConfig = ""
	})

	AfterEach(func() {
		// Safety net: restore original config if a test left it mutated.
		if savedConfig != "" {
			// Remove stale canary rows before restarting so ValidateCanaries doesn't
			// crash-loop on a key that no longer exists in the restored config.
			deleteStaleCanaries(defaultKeyID)
			// Reset migration checkpoints so subsequent tests don't inherit a stale
			// Complete=true checkpoint for a key ID that was used in this test.
			resetMigrationCheckpoints()
			if err := restoreEncryptionConfig(savedConfig); err != nil {
				GinkgoWriter.Printf("Warning: restore encryption config failed: %v\n", err)
			}
			if err := restartServicesAndWait(); err != nil {
				GinkgoWriter.Printf("Warning: service restart after config restore failed: %v\n", err)
			}
			// Re-authenticate after restart so the suite-level AfterEach (CleanUpAllTestResources)
			// inherits a valid session rather than failing with a stale token.
			if _, err := login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout); err != nil {
				GinkgoWriter.Printf("Warning: re-login after config restore failed: %v\n", err)
			}
		}
	})

	// S1 and S2 share state (S2 needs data encrypted under the old "default" key).
	// Ordered ensures S1 runs before S2 within the same process.
	//
	// Resources use stable constant names so AfterEach/CleanUpAllTestResources (which
	// matches the per-spec test-ID label) does not delete them between S1 and S2.
	// AfterAll performs explicit cleanup.
	Context("When the active encryption key is rotated", Ordered, func() {
		const (
			authProviderName = "enc-rot-ap-stable"
			repoName         = "enc-rot-repo-stable"
			rotatedKeyID     = "rotated-key"
		)
		// gitServerRepoName is the bare git repo name on the aux server.
		// Set in BeforeAll and used in AfterAll for cleanup.
		var gitServerRepoName string
		// sharedRepoURL is the cluster-internal URL for enc-rot-repo-stable.
		// Set in BeforeAll and used in S1 cleanup to recreate the repo under the default key.
		var sharedRepoURL string

		BeforeAll(func() {
			Expect(auxSvcs).ToNot(BeNil(), "auxiliary services must be initialized")
			Expect(auxSvcs.GitServer).ToNot(BeNil(), "git server must be started")

			// Best-effort cleanup of any stale resources and checkpoints from a previous run
			// that may have crashed before AfterAll / cleanup executed. Ignore errors — resources
			// may not exist. resetMigrationCheckpoints is critical for S2-standalone runs: if a
			// prior S1 left a Complete=true checkpoint for rotated-key, S2's migration worker
			// skips re-encryption and the test times out.
			_ = deleteAuthProvider(harness, authProviderName)
			_, _ = harness.ManageResource("delete", "repository", repoName)
			resetMigrationCheckpoints()

			// Generate a random secret so static analysis does not flag a hardcoded credential literal.
			secretBytes, err := generateAESKey()
			Expect(err).ToNot(HaveOccurred(), "generate pre-rotation secret")
			preRotationSecret = base64.StdEncoding.EncodeToString(secretBytes)

			By("creating pre-rotation resources encrypted under the default key")
			issuerURL := auxSvcs.Keycloak.IssuerURL()
			manifest := buildOIDCAuthProviderYAML(authProviderName, issuerURL, "flightctl-client", preRotationSecret)
			out, err := applyManifest(harness, manifest)
			Expect(err).ToNot(HaveOccurred(), "create pre-rotation AuthProvider")
			Expect(out).ToNot(BeEmpty(), "apply must produce output")

			keyPath, err := auxSvcs.GetGitSSHPrivateKeyPath()
			Expect(err).ToNot(HaveOccurred(), "get git SSH key path")
			keyContent, err := auxSvcs.GetGitSSHPrivateKey()
			Expect(err).ToNot(HaveOccurred(), "get git SSH key content")

			// Create a bare git repo on the server so repotester (running inside the cluster)
			// can establish a real SSH connection when the key is valid. Without a real repo
			// at the URL, WaitForRepositoryAccessible / WaitForRepositoryNotAccessible time out
			// or produce ambiguous results.
			gitServerRepoName = "enc-rot-s1s2-stable"
			gitServerConfig := e2e.GitServerConfig{
				Host: auxSvcs.GitServer.Host,
				Port: auxSvcs.GitServer.Port,
				User: "user",
			}
			Expect(harness.CreateGitRepositoryOnServer(gitServerConfig, keyPath, gitServerRepoName)).To(
				Succeed(), "create bare git repo on aux server for S1/S2")

			// Use InternalHost/InternalPort so repotester (running inside k8s) can reach the
			// git server via the cluster-reachable address, not the test-runner-only address.
			repoURL, err := harness.GetInternalGitRepoURL(
				auxSvcs.GitServer.InternalHost, auxSvcs.GitServer.InternalPort, gitServerRepoName)
			Expect(err).ToNot(HaveOccurred(), "build internal git repo URL")
			sharedRepoURL = repoURL
			// Use CreateSharedRepositoryWithSSHCredentials (no test-id label) so per-test AfterEach
			// cleanup does not delete this resource after S1. AfterAll handles deletion.
			Expect(harness.CreateSharedRepositoryWithSSHCredentials(repoName, repoURL, keyContent)).To(
				Succeed(), "create pre-rotation Repository")

			By("confirming pre-rotation DB ciphertexts use the default key")
			apCipher, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", authProviderName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(apCipher, defaultKeyID)).To(BeTrue(),
				"pre-rotation AuthProvider ciphertext must use default key, got: %s", apCipher)

			repoCipher, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->'sshConfig'->>'sshPrivateKey' FROM repositories WHERE name = '%s'", repoName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(repoCipher, defaultKeyID)).To(BeTrue(),
				"pre-rotation Repository ciphertext must use default key, got: %s", repoCipher)
		})

		AfterAll(func() {
			if err := deleteAuthProvider(harness, authProviderName); err != nil {
				GinkgoWriter.Printf("Warning: failed to delete authprovider %s: %v\n", authProviderName, err)
			}
			if _, err := harness.ManageResource("delete", "repository", repoName); err != nil {
				GinkgoWriter.Printf("Warning: failed to delete repository %s: %v\n", repoName, err)
			}
			if gitServerRepoName != "" && auxSvcs.GitServer != nil {
				keyPath, err := auxSvcs.GetGitSSHPrivateKeyPath()
				if err == nil {
					gitServerConfig := e2e.GitServerConfig{
						Host: auxSvcs.GitServer.Host,
						Port: auxSvcs.GitServer.Port,
						User: "user",
					}
					if err := harness.DeleteGitRepositoryOnServer(gitServerConfig, keyPath, gitServerRepoName); err != nil {
						GinkgoWriter.Printf("Warning: failed to delete git server repo %s: %v\n", gitServerRepoName, err)
					}
				}
			}
		})

		It("S1: should use the new key ID for new encryptions after rotation", Label("90091"), func() {
			By("generating a new 256-bit AES encryption key")
			newKeyBytes, err := generateAESKey()
			Expect(err).ToNot(HaveOccurred(), "generate new AES key")

			By("rotating the active encryption key to " + rotatedKeyID)
			savedConfig, err = rotateEncryptionKey(rotatedKeyID, newKeyBytes)
			Expect(err).ToNot(HaveOccurred(), "rotate encryption key")

			By("restarting services to pick up the new config")
			Expect(restartServicesAndWait()).To(Succeed(), "restart services after key rotation")

			By("restoring admin login after service restart")
			_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
			Expect(err).ToNot(HaveOccurred(), "re-login after restart")

			By("creating a new Repository with credentials encrypted under the rotated key")
			newRepoName := "enc-post-rot-" + harness.GetTestIDFromContext()
			keyContent, err := auxSvcs.GetGitSSHPrivateKey()
			Expect(err).ToNot(HaveOccurred())
			repoURL := fmt.Sprintf("user@%s:%d:/home/user/repos/new.git",
				auxSvcs.GitServer.Host, auxSvcs.GitServer.Port)
			Expect(harness.CreateRepositoryWithSSHCredentials(newRepoName, repoURL, keyContent)).To(
				Succeed(), "create post-rotation Repository")
			DeferCleanup(func() {
				if _, err := harness.ManageResource("delete", "repository", newRepoName); err != nil {
					GinkgoWriter.Printf("Warning: failed to delete post-rotation repository %s: %v\n", newRepoName, err)
				}
			})

			By("verifying new Repository uses the rotated key in DB")
			newRepoCipher, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->'sshConfig'->>'sshPrivateKey' FROM repositories WHERE name = '%s'", newRepoName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(newRepoCipher, rotatedKeyID)).To(BeTrue(),
				"new Repository must use rotated key; got: %s", newRepoCipher)

			By("verifying original Repository (old key) is still readable by the API after rotation")
			// Getting the repository proves the API can decrypt sshPrivateKey stored under the old
			// key when both keys are present in the key ring — no SSH connectivity required.
			out, getErr := harness.CLI("get", "repository", repoName)
			Expect(getErr).ToNot(HaveOccurred(), "get repository must succeed — API must decrypt old-key data")
			Expect(out).To(ContainSubstring(repoName), "get output must contain the repository name")

			By("updating the original AuthProvider to trigger re-encryption with rotated key")
			newSecret := base64.StdEncoding.EncodeToString(newKeyBytes[:12])
			updatedManifest := buildOIDCAuthProviderYAML(authProviderName, auxSvcs.Keycloak.IssuerURL(),
				"flightctl-client", newSecret)
			var applyOut string
			applyOut, err = applyManifest(harness, updatedManifest)
			Expect(err).ToNot(HaveOccurred(), "update AuthProvider after rotation")
			Expect(applyOut).ToNot(BeEmpty(), "update must produce output")

			By("verifying updated AuthProvider uses the rotated key in DB")
			updatedAPCipher, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", authProviderName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(updatedAPCipher, rotatedKeyID)).To(BeTrue(),
				"updated AuthProvider must use rotated key after update; got: %s", updatedAPCipher)

			By("cleanup: removing stale canary rows and migration checkpoints, then restoring original config")
			// Delete the canary row created for rotatedKeyID before restarting. If the
			// rotated-key canary remains in DB, ValidateCanaries on startup will try to
			// decrypt it with a key not present in the restored config → log.Fatalf → crash-loop.
			deleteStaleCanaries(defaultKeyID)
			// Reset migration checkpoints so S2 (which re-uses the same rotated-key ID) sees an
			// incomplete migration rather than a stale Complete=true checkpoint from S1's run.
			// Without this reset the worker skips re-encryption in S2 and the test times out.
			resetMigrationCheckpoints()
			Expect(restoreEncryptionConfig(savedConfig)).To(Succeed())
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
			Expect(err).ToNot(HaveOccurred())
			savedConfig = "" // cleared so AfterEach safety net does not double-restore

			By("resetting shared repo to default-key encryption for S2")
			// S1's migration re-encrypted enc-rot-repo-stable under rotated-key (S1's key bytes).
			// S2 rotates to the same key ID with NEW bytes. ProcessEncryption sees
			// parsed.KeyID == activeKeyID ("rotated-key" == "rotated-key") and returns the row
			// unchanged — leaving S1's ciphertext in the DB. S2's repotester then decrypts it with
			// S2's new key bytes → MAC failure. Fix: delete and recreate the repo under the
			// default key so S2's migration performs a real re-encryption with S2's key bytes.
			if _, err := harness.ManageResource("delete", "repository", repoName); err != nil {
				GinkgoWriter.Printf("Warning: failed to delete shared repo before S2: %v\n", err)
			}
			resetKeyContent, resetErr := auxSvcs.GetGitSSHPrivateKey()
			Expect(resetErr).ToNot(HaveOccurred(), "get git SSH key for repo reset")
			Expect(harness.CreateSharedRepositoryWithSSHCredentials(repoName, sharedRepoURL, resetKeyContent)).To(
				Succeed(), "recreate shared repo under default key for S2")
		})

		It("S2: should report inaccessible condition when active key is removed, and recover when key is restored", Label("90095"), func() {
			By("rotating to a new key to make rotatedKeyID the active key")
			newKeyBytes, err := generateAESKey()
			Expect(err).ToNot(HaveOccurred())

			savedConfig, err = rotateEncryptionKey(rotatedKeyID, newKeyBytes)
			Expect(err).ToNot(HaveOccurred(), "rotate key to set up old-key scenario")
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for migration to re-encrypt the Repository under the rotated key")
			// The migration worker automatically re-encrypts all resources under the active key.
			// We must wait for it to complete before removing rotatedKeyID so the test relies on
			// a deterministic DB state rather than a race between migration and key removal.
			Eventually(func() bool {
				cipher, err := queryDB(providers, fmt.Sprintf(
					"SELECT spec->'sshConfig'->>'sshPrivateKey' FROM repositories WHERE name = '%s'", repoName,
				))
				if err != nil {
					return false
				}
				return ciphertextMatchesKeyID(cipher, rotatedKeyID)
			}, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(BeTrue(), "migration must re-encrypt the Repository under the rotated key before key removal")

			By("confirming Repository is accessible while rotated key is still present")
			Expect(harness.WaitForRepositoryAccessible(repoName, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
				Succeed(), "pre-removal: Repository must be accessible when active key is present")

			By("removing the rotated key from the config")
			// Delete the rotated-key canary before restarting with the key removed.
			// ValidateCanaries runs at startup and fatals if it finds a canary for a key
			// that is no longer in the key ring — causing a crash-loop. The canary for
			// rotated-key was created during EnsureActiveCanary on the last restart; deleting
			// it here lets services start cleanly so repotester can detect inaccessibility.
			deleteStaleCanaries(defaultKeyID)
			// The repo is now encrypted under rotatedKeyID. Removing it makes the repo undecryptable.
			savedConfig2, err := removeEncryptionKey(rotatedKeyID)
			Expect(err).ToNot(HaveOccurred(), "remove rotated key from config")
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the background task to detect decryption failure and mark Repository inaccessible")
			// The background task sets Accessible=False when decryption of sshPrivateKey fails.
			Expect(harness.WaitForRepositoryNotAccessible(repoName, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
				Succeed(), "Repository must become inaccessible when its active encryption key is removed")

			By("restoring the rotated key to the config")
			Expect(restoreEncryptionConfig(savedConfig2)).To(Succeed())
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
			Expect(err).ToNot(HaveOccurred())

			By("verifying Repository becomes accessible again after key restore")
			Expect(harness.WaitForRepositoryAccessible(repoName, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
				Succeed(), "Repository must be accessible again after encryption key is restored")

			By("final cleanup: remove stale canaries and restore original config")
			deleteStaleCanaries(defaultKeyID)
			Expect(restoreEncryptionConfig(savedConfig)).To(Succeed())
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
			Expect(err).ToNot(HaveOccurred(), "re-login after final config restore")
			savedConfig = ""
		})
	})
})

// generateAESKey returns 32 random bytes suitable for use as an AES-256 key.
func generateAESKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate AES key: %w", err)
	}
	return key, nil
}
