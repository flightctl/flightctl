package restore_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/restore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	"github.com/flightctl/flightctl/test/util/testdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// writeServiceConfig saves cfg to a temp file and returns its path.
// The caller is responsible for removing the parent directory.
func writeServiceConfig(cfg *config.Config) (string, error) {
	dir, err := os.MkdirTemp("", "flightctl-restore-test-*")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "service-config.yaml")
	return path, config.Save(cfg, path)
}

// testDeployerConfig returns a config pointing at the integration DB using
// the app user with port 5432 (the container-internal port used by psql exec).
func testDeployerConfig(s RestoreTestSuite) *config.Config {
	cfg := config.NewDefault()
	testdb.ApplyIntegrationConnectionOverrides(cfg)
	cfg.Database.Name = s.dbName
	cfg.Database.Port = 5432
	return cfg
}

// newTestDeployer creates a PodmanRestoreDeployer wired to the integration stack.
func newTestDeployer(s RestoreTestSuite, cfgPath string) *restore.PodmanRestoreDeployer {
	return restore.NewPodmanRestoreDeployer(s.Log,
		restore.WithDBContainerName(integrationstack.PostgresContainerName),
		restore.WithContainerCLI(containers.RuntimeCLIName()),
		restore.WithServiceHandler(restore.NewSystemctlServiceHandler([]string{})),
		restore.WithServiceConfigPath(cfgPath),
		restore.WithDBName(s.dbName),
	)
}

// createDumpViaBackup runs PodmanDeployer.BackupDatabase against the integration
// Postgres container and returns the path to db/dump.sql.
func createDumpViaBackup(ctx context.Context, log *logrus.Logger, cfgPath, dbName, outputDir string) string {
	deployer := backup.NewPodmanDeployer(log,
		backup.WithDBContainerName(integrationstack.PostgresContainerName),
		backup.WithContainerCLI(containers.RuntimeCLIName()),
		backup.WithServiceConfigPath(cfgPath),
		backup.WithDBName(dbName),
		backup.WithDBUser(testdb.IntegrationPostgresAdminUser()),
		backup.WithDBPassword(string(testdb.IntegrationPostgresAdminPassword())),
	)
	Expect(deployer.BackupDatabase(ctx, outputDir)).To(Succeed())
	dumpPath := filepath.Join(outputDir, "db", "dump.sql")
	Expect(dumpPath).To(BeAnExistingFile())
	return dumpPath
}

func adminDBConfig(s RestoreTestSuite) *config.Config {
	adminCfg := config.NewDefault()
	adminCfg.Database.Hostname = s.cfg.Database.Hostname
	adminCfg.Database.Port = s.cfg.Database.Port
	adminCfg.Database.Name = s.dbName
	adminCfg.Database.User = testdb.IntegrationPostgresAdminUser()
	adminCfg.Database.Password = testdb.IntegrationPostgresAdminPassword()
	return adminCfg
}

func insertMarker(s RestoreTestSuite, marker string) {
	adminDB, err := testdb.OpenMinimalAdminDB(adminDBConfig(s), s.Log)
	Expect(err).NotTo(HaveOccurred())
	defer testdb.CloseDB(adminDB)

	Expect(adminDB.Exec(`CREATE TABLE IF NOT EXISTS restore_integration_marker (id SERIAL PRIMARY KEY, val TEXT)`).Error).To(Succeed())
	Expect(adminDB.Exec(`GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE restore_integration_marker TO flightctl_app`).Error).To(Succeed())
	Expect(adminDB.Exec(`INSERT INTO restore_integration_marker (val) VALUES (?)`, marker).Error).To(Succeed())
}

func dropMarkerTable(s RestoreTestSuite) {
	adminDB, err := testdb.OpenMinimalAdminDB(adminDBConfig(s), s.Log)
	Expect(err).NotTo(HaveOccurred())
	defer testdb.CloseDB(adminDB)

	Expect(adminDB.Exec(`DROP TABLE IF EXISTS restore_integration_marker`).Error).To(Succeed())
}

func overwriteMarker(s RestoreTestSuite, marker string) {
	adminDB, err := testdb.OpenMinimalAdminDB(adminDBConfig(s), s.Log)
	Expect(err).NotTo(HaveOccurred())
	defer testdb.CloseDB(adminDB)

	Expect(adminDB.Exec(`DELETE FROM restore_integration_marker`).Error).To(Succeed())
	Expect(adminDB.Exec(`INSERT INTO restore_integration_marker (val) VALUES (?)`, marker).Error).To(Succeed())
}

func reconnectDB(s RestoreTestSuite) *gorm.DB {
	freshDB, err := store.InitDB(s.cfg, s.Log)
	Expect(err).NotTo(HaveOccurred())
	return freshDB
}

func copyDumpToExtractDir(dumpPath, extractDir string) {
	content, err := os.ReadFile(dumpPath)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(filepath.Join(extractDir, "db", "dump.sql"), content, 0600)).To(Succeed())
}

var _ = Describe("RestoreDatabase", func() {
	var (
		s          RestoreTestSuite
		extractDir string
		cfgPath    string
	)

	BeforeEach(func() {
		s.Setup()
		var err error
		extractDir, err = os.MkdirTemp("", "restore-db-test-*")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(extractDir, "db"), 0700)).To(Succeed())

		cfgPath, err = writeServiceConfig(testDeployerConfig(s))
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(extractDir)
		_ = os.RemoveAll(filepath.Dir(cfgPath))
		s.Teardown()
	})

	Context("PodmanRestoreDeployer", func() {
		It("When no dump file is present it should succeed and log an external-DB notice", func() {
			Expect(newTestDeployer(s, cfgPath).RestoreDatabase(s.Ctx, extractDir)).To(Succeed())
		})

		It("When the database is empty it should create tables and insert backup data", func() {
			backupMarker := fmt.Sprintf("backup_state_%d", time.Now().UnixNano())

			insertMarker(s, backupMarker)

			backupOutputDir, err := os.MkdirTemp("", "restore-backup-out-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(backupOutputDir)

			dumpPath := createDumpViaBackup(s.Ctx, s.Log, cfgPath, s.dbName, backupOutputDir)
			copyDumpToExtractDir(dumpPath, extractDir)

			dropMarkerTable(s)

			Expect(newTestDeployer(s, cfgPath).RestoreDatabase(s.Ctx, extractDir)).To(Succeed())

			freshDB := reconnectDB(s)
			defer func() {
				if sqlDB, closeErr := freshDB.DB(); closeErr == nil {
					sqlDB.Close()
				}
			}()

			var val string
			Expect(freshDB.Raw("SELECT val FROM restore_integration_marker WHERE val = ?", backupMarker).Scan(&val).Error).To(Succeed())
			Expect(val).To(Equal(backupMarker), "backup-state row must be present after restore into empty DB")
		})

		It("When the database has pre-existing data it should replace it with backup data", func() {
			backupMarker := fmt.Sprintf("backup_state_%d", time.Now().UnixNano())
			staleMarker := fmt.Sprintf("stale_post_backup_%d", time.Now().UnixNano())

			insertMarker(s, backupMarker)

			backupOutputDir, err := os.MkdirTemp("", "restore-backup-out-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(backupOutputDir)

			dumpPath := createDumpViaBackup(s.Ctx, s.Log, cfgPath, s.dbName, backupOutputDir)
			copyDumpToExtractDir(dumpPath, extractDir)

			overwriteMarker(s, staleMarker)

			Expect(newTestDeployer(s, cfgPath).RestoreDatabase(s.Ctx, extractDir)).To(Succeed())

			freshDB := reconnectDB(s)
			defer func() {
				if sqlDB, closeErr := freshDB.DB(); closeErr == nil {
					sqlDB.Close()
				}
			}()

			var backupVal string
			Expect(freshDB.Raw("SELECT val FROM restore_integration_marker WHERE val = ?", backupMarker).Scan(&backupVal).Error).To(Succeed())
			Expect(backupVal).To(Equal(backupMarker), "backup-state row must be present after restore")

			var staleCount int64
			Expect(freshDB.Raw("SELECT count(*) FROM restore_integration_marker WHERE val = ?", staleMarker).Scan(&staleCount).Error).To(Succeed())
			Expect(staleCount).To(Equal(int64(0)), "stale post-backup data must not survive restore")
		})

		It("When the dump contains invalid SQL it should return an error", func() {
			dumpPath := filepath.Join(extractDir, "db", "dump.sql")
			Expect(os.WriteFile(dumpPath, []byte(`THIS IS NOT VALID SQL;`), 0600)).To(Succeed())

			Expect(newTestDeployer(s, cfgPath).RestoreDatabase(s.Ctx, extractDir)).To(HaveOccurred(),
				"invalid SQL must cause RestoreDatabase to return an error")
		})
	})
})
