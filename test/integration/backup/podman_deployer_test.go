package backup_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/config"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PodmanDeployer Integration", func() {
	var (
		ctx             context.Context
		cfg             *config.Config
		deployer        backup.Deployer
		outputDir       string
		log             = testutil.InitLogsWithDebug()
		containerExists bool
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)

		// Check if flightctl-db container is accessible with timeout to prevent hanging
		// Note: The container may be running under rootful podman (via sudo/systemd)
		probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		testCmd := exec.CommandContext(probeCtx, "podman", "exec", "flightctl-db", "echo", "test")
		err := testCmd.Run()
		containerExists = err == nil

		if !containerExists {
			// Container might be running under sudo/system podman - check with timeout
			sudoCtx, sudoCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer sudoCancel()

			testCmd = exec.CommandContext(sudoCtx, "sudo", "-n", "podman", "ps", "--filter", "name=flightctl-db", "--format", "{{.Names}}")
			output, err := testCmd.CombinedOutput()
			if err == nil && strings.Contains(string(output), "flightctl-db") {
				// Container exists but under sudo - we'll skip tests that require exec
				GinkgoWriter.Printf("Note: flightctl-db running under rootful podman. " +
					"Some tests will be skipped. Run with rootless podman for full test coverage.\n")
			}
		}

		// Create config pointing to the integration test database
		cfg = config.NewDefault()
		cfg.Database.Hostname = "localhost"
		cfg.Database.Port = 5432
		cfg.Database.User = "flightctl_app"
		cfg.Database.Password = "adminpass"
		cfg.Database.Name = "flightctl"

		deployer = backup.NewPodmanDeployer(cfg, log, "")

		// Create temporary output directory
		outputDir = GinkgoT().TempDir()
	})

	Context("BackupDatabase", func() {
		It("should successfully backup the database to a SQL dump file", func() {
			if !containerExists {
				Skip("flightctl-db container not accessible without sudo. " +
					"Requires rootless podman or sudo permissions.")
			}

			err := deployer.BackupDatabase(ctx, outputDir)
			Expect(err).ToNot(HaveOccurred())

			// Verify dump file was created
			dumpFile := filepath.Join(outputDir, "db", "dump.sql")
			Expect(dumpFile).To(BeAnExistingFile())

			// Verify dump file has content (non-zero size)
			fileInfo, err := os.Stat(dumpFile)
			Expect(err).ToNot(HaveOccurred())
			Expect(fileInfo.Size()).To(BeNumerically(">", 100),
				"dump file should contain SQL content")

			// Verify dump file contains expected PostgreSQL dump markers
			content, err := os.ReadFile(dumpFile)
			Expect(err).ToNot(HaveOccurred())

			contentStr := string(content)
			Expect(contentStr).To(ContainSubstring("PostgreSQL database dump"),
				"dump should contain PostgreSQL header")
			Expect(contentStr).To(Or(
				ContainSubstring("CREATE TABLE"),
				ContainSubstring("SET "),
			), "dump should contain SQL statements")
		})

		It("should return ErrExternalDatabase for external database", func() {
			// Create config with external database
			externalCfg := config.NewDefault()
			externalCfg.Database.Hostname = "external-db.example.com"
			externalDeployer := backup.NewPodmanDeployer(externalCfg, log, "")

			err := externalDeployer.BackupDatabase(ctx, outputDir)
			Expect(err).To(MatchError(backup.ErrExternalDatabase))

			// Verify no dump file was created
			dumpFile := filepath.Join(outputDir, "db", "dump.sql")
			Expect(dumpFile).ToNot(BeAnExistingFile())
		})

		It("should handle database dump streaming without holding in memory", func() {
			if !containerExists {
				Skip("flightctl-db container not accessible without sudo")
			}

			// This test verifies that the dump is streamed to file, not buffered in memory.
			// We run BackupDatabase in a goroutine and verify the file appears and grows
			// while the backup is still in-flight.
			dumpFile := filepath.Join(outputDir, "db", "dump.sql")
			errChan := make(chan error, 1)

			// Start backup in background
			go func() {
				errChan <- deployer.BackupDatabase(ctx, outputDir)
			}()

			// Poll for dump file to appear on disk while backup is still running
			Eventually(func() bool {
				_, err := os.Stat(dumpFile)
				return err == nil
			}, "5s", "100ms").Should(BeTrue(), "dump file should appear on disk during backup execution")

			// Verify file is growing (being streamed) while backup is in-flight
			var initialSize int64
			Eventually(func() bool {
				if info, err := os.Stat(dumpFile); err == nil {
					initialSize = info.Size()
					return initialSize > 0
				}
				return false
			}, "2s", "100ms").Should(BeTrue(), "dump file should start growing")

			// Wait a moment and verify size increased (proves streaming, not buffering)
			Eventually(func() int64 {
				if info, err := os.Stat(dumpFile); err == nil {
					return info.Size()
				}
				return 0
			}, "3s", "200ms").Should(BeNumerically(">", initialSize),
				"dump file should grow during execution (streaming, not memory-buffered)")

			// Wait for backup to complete and verify no error
			Eventually(errChan, "10s").Should(Receive(BeNil()),
				"BackupDatabase should complete without error")
		})
	})

	Context("BackupDatabase with different database configurations", func() {
		It("should handle backup with custom port", func() {
			if !containerExists {
				Skip("flightctl-db container not accessible without sudo")
			}

			// The flightctl-db container runs on port 5432, so this should work
			cfg.Database.Port = 5432

			err := deployer.BackupDatabase(ctx, outputDir)
			Expect(err).ToNot(HaveOccurred())

			dumpFile := filepath.Join(outputDir, "db", "dump.sql")
			Expect(dumpFile).To(BeAnExistingFile())
		})
	})
})
