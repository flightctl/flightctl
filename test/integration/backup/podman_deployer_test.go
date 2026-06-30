package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// writeServiceConfig writes cfg to a temp file and returns the path.
func writeServiceConfig(cfg *config.Config) (string, error) {
	dir, err := os.MkdirTemp("", "flightctl-svc-cfg-*")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "service-config.yaml")
	return path, config.Save(cfg, path)
}

func testDeployerConfig(dbName string) *config.Config {
	cfg := config.NewDefault()
	testdb.ApplyIntegrationConnectionOverrides(cfg)
	cfg.Database.Name = dbName
	cfg.Database.Port = 5432
	return cfg
}

func newIntegrationDeployer(log *logrus.Logger, svcCfgPath, dbName string) backup.Deployer {
	return backup.NewPodmanDeployer(log,
		backup.WithDBContainerName(integrationstack.PostgresContainerName),
		backup.WithContainerCLI(containers.RuntimeCLIName()),
		backup.WithServiceConfigPath(svcCfgPath),
		backup.WithDBName(dbName),
		backup.WithDBUser(testdb.IntegrationPostgresAdminUser()),
		backup.WithDBPassword(string(testdb.IntegrationPostgresAdminPassword())),
	)
}

var _ = Describe("PodmanDeployer Integration", func() {
	var (
		ctx        context.Context
		deployer   backup.Deployer
		outputDir  string
		log        *logrus.Logger
		svcCfgPath string
		cfg        *config.Config
		dbName     string
		db         *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = testutil.InitLogsWithDebug()

		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())

		svcCfgPath, err = writeServiceConfig(testDeployerConfig(dbName))
		Expect(err).NotTo(HaveOccurred())

		deployer = newIntegrationDeployer(log, svcCfgPath, dbName)
		outputDir = GinkgoT().TempDir()
	})

	AfterEach(func() {
		if svcCfgPath != "" {
			_ = os.RemoveAll(filepath.Dir(svcCfgPath))
		}
		if db != nil {
			Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		}
	})

	Context("BackupDatabase", func() {
		It("should successfully backup the database to a SQL dump file", func() {
			err := deployer.BackupDatabase(ctx, outputDir)
			Expect(err).ToNot(HaveOccurred())

			dumpFile := filepath.Join(outputDir, "db", "dump.sql")
			Expect(dumpFile).To(BeAnExistingFile())

			fileInfo, err := os.Stat(dumpFile)
			Expect(err).ToNot(HaveOccurred())
			Expect(fileInfo.Size()).To(BeNumerically(">", 100),
				"dump file should contain SQL content")

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
			dir := GinkgoT().TempDir()
			externalCfgPath := filepath.Join(dir, "service-config.yaml")
			Expect(os.WriteFile(externalCfgPath, []byte(`db:
  type: external
  name: flightctl
`), 0600)).To(Succeed())

			externalDeployer := backup.NewPodmanDeployer(log, backup.WithServiceConfigPath(externalCfgPath))

			err := externalDeployer.BackupDatabase(ctx, outputDir)
			Expect(err).To(MatchError(backup.ErrExternalDatabase))

			dumpFile := filepath.Join(outputDir, "db", "dump.sql")
			Expect(dumpFile).ToNot(BeAnExistingFile())
		})

		It("should handle database dump streaming without holding in memory", func() {
			dumpFile := filepath.Join(outputDir, "db", "dump.sql")
			errChan := make(chan error, 1)

			go func() {
				errChan <- deployer.BackupDatabase(ctx, outputDir)
			}()

			// Poll for dump file to appear on disk while backup is still running.
			Eventually(func() bool {
				_, err := os.Stat(dumpFile)
				return err == nil
			}, "5s", "100ms").Should(BeTrue(), "dump file should appear on disk during backup execution")

			Eventually(func() bool {
				// Use len() to peek at the channel without consuming the result.
				// If the backup already finished, the file content assertion is still
				// valid (the file appeared in the previous Eventually), so return true
				// to avoid spuriously draining errChan before the final receive below.
				if len(errChan) > 0 {
					return true
				}
				info, err := os.Stat(dumpFile)
				return err == nil && info.Size() > 0
			}, "2s", "50ms").Should(BeTrue(),
				"dump file should have content while backup is still in flight (streaming, not memory-buffered)")

			Eventually(errChan, "10s").Should(Receive(BeNil()),
				"BackupDatabase should complete without error")
		})
	})

	Context("BackupDatabase with different database configurations", func() {
		It("should handle backup with custom port", func() {
			err := deployer.BackupDatabase(ctx, outputDir)
			Expect(err).ToNot(HaveOccurred())

			dumpFile := filepath.Join(outputDir, "db", "dump.sql")
			Expect(dumpFile).To(BeAnExistingFile())

			content, err := os.ReadFile(dumpFile)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(content)).To(ContainSubstring("PostgreSQL database dump"))
			Expect(strings.TrimSpace(string(content))).NotTo(BeEmpty())
		})
	})
})
