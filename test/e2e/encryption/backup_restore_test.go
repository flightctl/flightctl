package encryption_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Encryption at rest — Backup", Label("encryption", "backup-restore"), func() {
	var (
		harness *e2e.Harness
		br      *e2e.BackupRestore
	)

	BeforeEach(func() {
		if reason := backupRestoreExternalDBSkipReason(); reason != "" {
			Skip(reason)
		}
		harness = e2e.GetWorkerHarness()
		p := setup.GetDefaultProviders()
		br = harness.NewBackupRestore(p)
	})

	Context("When a backup is taken with encryption at rest enabled", func() {
		It("S3: backup archive should include encryption key material", Label("90094"), func() {
			By("running flightctl-backup to create an archive")
			outputDir := GinkgoT().TempDir()
			archivePath, _, err := br.RunFlightCtlBackup(outputDir)
			Expect(err).ToNot(HaveOccurred(), "backup must succeed")
			Expect(archivePath).ToNot(BeEmpty(), "backup must return an archive path")
			Expect(archivePath).To(BeAnExistingFile(), "backup archive must exist on disk")
			GinkgoWriter.Printf("Backup archive path: %s\n", archivePath)

			By("listing archive entries")
			entries, err := listTarGzEntries(archivePath)
			Expect(err).ToNot(HaveOccurred(), "must be able to read archive entries")
			Expect(entries).ToNot(BeEmpty(), "archive must have entries")

			By("verifying at least one encryption entry is present in the archive")
			prefix := encryptionKeyArchiveEntryPrefix()
			var encryptionEntries []string
			for _, e := range entries {
				if strings.HasPrefix(e, prefix) {
					encryptionEntries = append(encryptionEntries, e)
				}
			}
			Expect(encryptionEntries).ToNot(BeEmpty(),
				"backup archive must contain encryption key material under %q; entries: %v",
				prefix, entries)

			By("logging found encryption archive entries")
			GinkgoWriter.Printf("Encryption entries in backup: %v\n", encryptionEntries)
		})
	})

	Context("When restoring from a backup taken before a key rotation", Serial, func() {
		var (
			providers   *infra.Providers
			savedConfig string
		)

		BeforeEach(func() {
			providers = setup.GetDefaultProviders()
			savedConfig = ""
		})

		// Safety net: if the test mutated the encryption config and did not clean up (e.g. due to
		// a mid-test failure), restore the original config so subsequent specs can start cleanly.
		AfterEach(func() {
			if savedConfig != "" {
				// Remove stale canary rows before restarting. If S4 failed after rotating the key
				// (line 123) but before the restore completed, the DB contains a canary for the
				// rotated key (post-backup-key). Restoring the original config (which only has
				// "default") without deleting that canary causes ValidateCanaries to crash-loop
				// on startup (cannot decrypt the rotated-key canary with the restored config).
				deleteStaleCanaries(defaultKeyID)
				// Reset migration checkpoints so the restored default config does not inherit a
				// Complete=true checkpoint from this run's key rotation.
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

		It("S4: restore should revert DB ciphertexts to the backup-time key and services should decrypt correctly", Label("90151"), func() {
			const rotatedKeyID = "post-backup-key"

			By("creating a pre-backup AuthProvider with a clientSecret encrypted under the default key")
			Expect(auxSvcs).ToNot(BeNil(), "auxiliary services must be initialized")
			apName := "enc-restore-ap-" + harness.GetTestIDFromContext()
			secretBytes, err := generateAESKey()
			Expect(err).ToNot(HaveOccurred(), "generate AuthProvider client secret")
			clientSecret := fmt.Sprintf("pre-backup-secret-%x", secretBytes[:8])
			manifest := buildOIDCAuthProviderYAML(apName, auxSvcs.Keycloak.IssuerURL(), "flightctl-client", clientSecret)
			_, err = applyManifest(harness, manifest)
			Expect(err).ToNot(HaveOccurred(), "create pre-backup AuthProvider")
			DeferCleanup(func() { _ = deleteAuthProvider(harness, apName) })

			By("confirming pre-backup DB ciphertext uses the default key")
			apCipherBefore, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", apName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(apCipherBefore, defaultKeyID)).To(BeTrue(),
				"pre-backup AuthProvider ciphertext must use default key; got: %s", apCipherBefore)

			By("running flightctl-backup to capture the pre-rotation state")
			outputDir := GinkgoT().TempDir()
			archivePath, _, err := br.RunFlightCtlBackup(outputDir)
			Expect(err).ToNot(HaveOccurred(), "backup must succeed")
			Expect(archivePath).ToNot(BeEmpty(), "backup must return an archive path")
			Expect(archivePath).To(BeAnExistingFile(), "backup archive must exist on disk")
			GinkgoWriter.Printf("Backup archive: %s\n", archivePath)

			By("rotating the active encryption key to mutate state post-backup")
			newKeyBytes, err := generateAESKey()
			Expect(err).ToNot(HaveOccurred(), "generate new AES key for post-backup rotation")
			savedConfig, err = rotateEncryptionKey(rotatedKeyID, newKeyBytes)
			Expect(err).ToNot(HaveOccurred(), "rotate encryption key post-backup")
			Expect(restartServicesAndWait()).To(Succeed(), "restart services after key rotation")
			_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
			Expect(err).ToNot(HaveOccurred(), "re-login after key rotation restart")

			By("triggering re-encryption so the DB ciphertext uses the rotated key")
			updatedSecret := fmt.Sprintf("post-rotation-secret-%x", newKeyBytes[:8])
			updatedManifest := buildOIDCAuthProviderYAML(apName, auxSvcs.Keycloak.IssuerURL(), "flightctl-client", updatedSecret)
			_, err = applyManifest(harness, updatedManifest)
			Expect(err).ToNot(HaveOccurred(), "update AuthProvider to trigger re-encryption")

			By("confirming DB ciphertext now uses the rotated key")
			apCipherRotated, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", apName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(apCipherRotated, rotatedKeyID)).To(BeTrue(),
				"post-rotation AuthProvider ciphertext must use rotated key; got: %s", apCipherRotated)
			GinkgoWriter.Printf("Post-rotation ciphertext prefix confirmed: %s\n", apCipherRotated[:min(40, len(apCipherRotated))])

			By("running flightctl-restore to revert to the pre-rotation backup")
			Expect(br.RunFlightCtlRestoreFromArchive(archivePath)).To(Succeed(), "restore from backup must succeed")

			By("waiting for all services to be running after restore")
			Eventually(br.VerifyAllServicesRunning,
				testutil.LONG_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(Succeed(), "all services must be running after restore")

			By("waiting for the API to be reachable after restore")
			Eventually(func() error {
				_, loginErr := login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
				return loginErr
			}, testutil.LONG_TIMEOUT, testutil.EVENTUALLY_POLLING_250,
			).Should(Succeed(), "API must be reachable after restore")

			By("verifying DB ciphertext reverted to the backup-time default key")
			apCipherRestored, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", apName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(apCipherRestored, defaultKeyID)).To(BeTrue(),
				"restored AuthProvider ciphertext must use default key; got: %s", apCipherRestored)
			GinkgoWriter.Printf("Restored ciphertext prefix confirmed: %s\n", apCipherRestored[:min(40, len(apCipherRestored))])

			By("verifying the API can read and decrypt the restored AuthProvider")
			out, err := harness.CLI("get", "authprovider", apName)
			Expect(err).ToNot(HaveOccurred(), "get AuthProvider after restore must succeed")
			Expect(out).To(ContainSubstring(apName), "get output must mention the AuthProvider name")

			// Restore succeeded — clear savedConfig so AfterEach does not double-restore.
			// The restore binary already brought services back up with the backup-time key config.
			savedConfig = ""

			By("verifying services are ready after restore")
			for _, svc := range []infra.ServiceName{infra.ServiceAPI, infra.ServiceWorker, infra.ServicePeriodic} {
				Expect(providers.Lifecycle.WaitForReady(svc, testutil.LONG_TIMEOUT)).To(Succeed(),
					"service %s must be ready after restore", svc)
			}
		})
	})
})
