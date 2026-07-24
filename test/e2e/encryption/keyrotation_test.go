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

var _ = Describe("Encryption at rest — Key rotation", Label("encryption"), Serial, func() {
	var (
		harness     *e2e.Harness
		providers   *infra.Providers
		savedConfig string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		providers = setup.GetDefaultProviders()
		savedConfig = ""
	})

	AfterEach(func() {
		// Safety net: restore original config if a test left it mutated.
		if savedConfig != "" {
			if err := restoreEncryptionConfig(savedConfig); err != nil {
				GinkgoWriter.Printf("Warning: restore encryption config failed: %v\n", err)
			}
			if err := restartServicesAndWait(); err != nil {
				GinkgoWriter.Printf("Warning: service restart after config restore failed: %v\n", err)
			}
			// Re-authenticate after restart so the suite-level AfterEach (CleanUpAllTestResources)
			// inherits a valid session rather than failing with a stale token.
			if _, err := login.LoginToAPIWithToken(harness); err != nil {
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

		BeforeAll(func() {
			Expect(auxSvcs).ToNot(BeNil(), "auxiliary services must be initialized")

			By("creating pre-rotation resources encrypted under the default key")
			issuerURL := auxSvcs.Keycloak.IssuerURL()
			manifest := buildOIDCAuthProviderYAML(authProviderName, issuerURL, "flightctl-client", "pre-rotation-secret-stable")
			out, err := applyManifest(harness, manifest)
			Expect(err).ToNot(HaveOccurred(), "create pre-rotation AuthProvider")
			Expect(out).ToNot(BeEmpty(), "apply must produce output")

			keyContent, err := auxSvcs.GetGitSSHPrivateKey()
			Expect(err).ToNot(HaveOccurred(), "get git SSH key")
			repoURL := fmt.Sprintf("user@%s:%d:/home/user/repos/test.git",
				auxSvcs.GitServer.Host, auxSvcs.GitServer.Port)
			Expect(harness.CreateRepositoryWithSSHCredentials(repoName, repoURL, keyContent)).To(
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
		})

		It("S1: should use the new key ID for new encryptions after rotation (§5.1–5.3)", func() {
			By("generating a new 256-bit AES encryption key")
			newKeyBytes, err := generateAESKey()
			Expect(err).ToNot(HaveOccurred(), "generate new AES key")

			By("rotating the active encryption key to " + rotatedKeyID)
			savedConfig, err = rotateEncryptionKey(rotatedKeyID, newKeyBytes)
			Expect(err).ToNot(HaveOccurred(), "rotate encryption key")

			By("restarting services to pick up the new config")
			Expect(restartServicesAndWait()).To(Succeed(), "restart services after key rotation")

			By("restoring admin login after service restart")
			_, err = login.LoginToAPIWithToken(harness)
			Expect(err).ToNot(HaveOccurred(), "re-login after restart")

			By("creating a new Repository with credentials encrypted under the rotated key")
			newRepoName := "enc-post-rot-" + harness.GetTestIDFromContext()
			keyContent, err := auxSvcs.GetGitSSHPrivateKey()
			Expect(err).ToNot(HaveOccurred())
			repoURL := fmt.Sprintf("user@%s:%d:/home/user/repos/new.git",
				auxSvcs.GitServer.Host, auxSvcs.GitServer.Port)
			Expect(harness.CreateRepositoryWithSSHCredentials(newRepoName, repoURL, keyContent)).To(
				Succeed(), "create post-rotation Repository")

			By("[§5.3] verifying new Repository uses the rotated key in DB")
			newRepoCipher, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->'sshConfig'->>'sshPrivateKey' FROM repositories WHERE name = '%s'", newRepoName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(newRepoCipher, rotatedKeyID)).To(BeTrue(),
				"new Repository must use rotated key; got: %s", newRepoCipher)

			By("[§5.1] verifying original Repository (old key) is still accessible after rotation")
			// Decryption happens in the background task; WaitForRepositoryAccessible polls Accessible=True.
			Expect(harness.WaitForRepositoryAccessible(repoName, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
				Succeed(), "original Repository must stay accessible when old key is still in key ring")

			By("[§5.1] verifying original Repository DB ciphertext is unchanged (not re-encrypted until touched)")
			origRepoCipher, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->'sshConfig'->>'sshPrivateKey' FROM repositories WHERE name = '%s'", repoName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(origRepoCipher, defaultKeyID)).To(BeTrue(),
				"untouched Repository must still use default key in DB; got: %s", origRepoCipher)

			By("[§5.3] updating the original AuthProvider to trigger re-encryption with rotated key")
			newSecret := base64.StdEncoding.EncodeToString(newKeyBytes[:12])
			updatedManifest := buildOIDCAuthProviderYAML(authProviderName, auxSvcs.Keycloak.IssuerURL(),
				"flightctl-client", newSecret)
			out, err := applyManifest(harness, updatedManifest)
			Expect(err).ToNot(HaveOccurred(), "update AuthProvider after rotation")
			Expect(out).ToNot(BeEmpty(), "update must produce output")

			By("[§5.3] verifying updated AuthProvider uses the rotated key in DB")
			updatedAPCipher, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", authProviderName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(updatedAPCipher, rotatedKeyID)).To(BeTrue(),
				"updated AuthProvider must use rotated key after update; got: %s", updatedAPCipher)

			By("cleanup: restoring original config and restarting")
			Expect(restoreEncryptionConfig(savedConfig)).To(Succeed())
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithToken(harness)
			Expect(err).ToNot(HaveOccurred())
			savedConfig = "" // cleared so AfterEach safety net does not double-restore
		})

		It("S2: should report inaccessible condition when old key is removed, and recover when key is restored (§5.4)", func() {
			By("re-rotating to a new key so 'default' becomes the old key")
			newKeyBytes, err := generateAESKey()
			Expect(err).ToNot(HaveOccurred())

			savedConfig, err = rotateEncryptionKey(rotatedKeyID, newKeyBytes)
			Expect(err).ToNot(HaveOccurred(), "rotate key to set up old-key scenario")
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithToken(harness)
			Expect(err).ToNot(HaveOccurred())

			By("confirming original Repository is accessible while old default key is still present")
			Expect(harness.WaitForRepositoryAccessible(repoName, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
				Succeed(), "pre-removal: Repository must be accessible when old key is present")

			By("removing the default key from the config")
			savedConfig2, err := removeEncryptionKey(defaultKeyID)
			Expect(err).ToNot(HaveOccurred(), "remove default key from config")
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithToken(harness)
			Expect(err).ToNot(HaveOccurred())

			By("[§5.4] waiting for the background task to detect decryption failure and mark Repository inaccessible")
			// The background task sets Accessible=False when decryption of sshPrivateKey fails.
			Expect(harness.WaitForRepositoryNotAccessible(repoName, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
				Succeed(), "[§5.4] Repository must become inaccessible when its encryption key is removed")

			By("restoring the default key to the config")
			Expect(restoreEncryptionConfig(savedConfig2)).To(Succeed())
			Expect(restartServicesAndWait()).To(Succeed())
			_, err = login.LoginToAPIWithToken(harness)
			Expect(err).ToNot(HaveOccurred())

			By("[§5.4] verifying Repository becomes accessible again after key restore")
			Expect(harness.WaitForRepositoryAccessible(repoName, testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
				Succeed(), "[§5.4] Repository must be accessible again after encryption key is restored")

			By("final cleanup: restore original config")
			Expect(restoreEncryptionConfig(savedConfig)).To(Succeed())
			Expect(restartServicesAndWait()).To(Succeed())
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
