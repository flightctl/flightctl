package encryption_test

import (
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
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

	// S3: backup archive contains encryption key material (§6.1).
	Context("When a backup is taken with encryption at rest enabled", func() {
		It("S3: backup archive should include encryption key material (§6.1)", func() {
			By("running flightctl-backup to create an archive")
			outputDir := GinkgoT().TempDir()
			archivePath, _, err := br.RunFlightCtlBackup(outputDir)
			Expect(err).ToNot(HaveOccurred(), "backup must succeed")
			Expect(archivePath).ToNot(BeEmpty(), "backup must return an archive path")
			Expect(archivePath).To(BeAnExistingFile(), "backup archive must exist on disk")

			By("listing archive entries")
			entries, err := listTarGzEntries(archivePath)
			Expect(err).ToNot(HaveOccurred(), "must be able to read archive entries")
			Expect(entries).ToNot(BeEmpty(), "archive must have entries")

			By("[§6.1] verifying at least one encryption entry is present in the archive")
			prefix := encryptionKeyArchiveEntryPrefix()
			var encryptionEntries []string
			for _, e := range entries {
				if strings.HasPrefix(e, prefix) {
					encryptionEntries = append(encryptionEntries, e)
				}
			}
			Expect(encryptionEntries).ToNot(BeEmpty(),
				"[§6.1] backup archive must contain encryption key material under %q; entries: %v",
				prefix, entries)

			By("logging found encryption archive entries")
			GinkgoWriter.Printf("Encryption entries in backup: %v\n", encryptionEntries)
		})
	})
})
