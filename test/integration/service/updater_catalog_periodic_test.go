package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	periodic "github.com/flightctl/flightctl/internal/periodic_checker"
	"github.com/flightctl/flightctl/internal/service/events"
	resourcesyncservice "github.com/flightctl/flightctl/internal/service/resourcesync"
	catalogservice "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/flightctl/flightctl/internal/store"
	resourcesyncstore "github.com/flightctl/flightctl/internal/store/resourcesync"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

const periodicCatalogConfigEnv = "FLIGHTCTL_TEST_UPDATER_PERIODIC_CONFIG"

// A subprocess exercises the real Run composition and its signal-driven shutdown
// without installing process-wide signal handlers in the integration suite.
func TestUpdaterCatalogPeriodicChild(t *testing.T) {
	path := os.Getenv(periodicCatalogConfigEnv)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	cfg := config.NewDefault()
	require.NoError(t, json.Unmarshal(data, cfg))
	shutdown := testutil.InitTracerForTests()
	defer func() { require.NoError(t, shutdown(context.Background())) }()
	ctx, span := tracing.StartSpan(context.Background(), "flightctl/tests", "PeriodicRuntime")
	defer span.End()
	log := flightlog.InitLogs()
	integrationstack.EnsureTestEncryption(log)
	db, err := store.InitDB(cfg, log)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() { require.NoError(t, sqlDB.Close()) }()
	require.NoError(t, periodic.New(cfg, log, db).Run(ctx))
}

var _ = Describe("Updater catalog periodic runtime", func() {
	It("When ResourceSync changes firmware-bearing fleets it should use current authorization and preserve unmanaged synchronization", func() {
		suite := NewServiceTestSuite()
		suite.Setup()
		DeferCleanup(suite.Teardown)
		ctx := suite.Ctx
		cfg := suite.cfg
		cfg.KV.Hostname = redisHost
		cfg.KV.Port = redisPort
		cfg.KV.Password = redisPassword
		cfg.Metrics.Enabled = false
		// Task request-ID prefixes are process-global; this fixture tests provider wiring.
		Expect(json.Unmarshal([]byte(`{"periodic":{"consumers":1,"clusterLevelSecretAccess":false,"tasks":{"resourceSync":{"schedule":{"interval":"500ms"}}}}}`), cfg)).To(Succeed())
		dir := GinkgoT().TempDir()
		input := approvedCatalog()
		data, err := json.Marshal(input)
		Expect(err).NotTo(HaveOccurred())
		cfg.FirmwareUpdaterCatalogFile = filepath.Join(dir, "catalog.yaml")
		Expect(os.WriteFile(cfg.FirmwareUpdaterCatalogFile, data, 0o600)).To(Succeed())
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		DeferCleanup(endpoint.Close)
		for _, name := range []string{"firmware-artifacts", "firmware-signatures"} {
			var spec domain.RepositorySpec
			Expect(spec.FromHttpRepoSpec(domain.HttpRepoSpec{Type: domain.HttpRepoSpecTypeHttp, Url: endpoint.URL})).To(Succeed())
			_, status := suite.Repository.CreateRepository(ctx, suite.OrgID, domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}, Spec: spec})
			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
		}
		repoDir := filepath.Join(dir, "source")
		repo, err := git.PlainInit(repoDir, false)
		Expect(err).NotTo(HaveOccurred())
		worktree, err := repo.Worktree()
		Expect(err).NotTo(HaveOccurred())
		var firmware domain.FirmwareSpec
		Expect(json.Unmarshal([]byte(firmwareJSON), &firmware)).To(Succeed())
		managed := domain.Fleet{ApiVersion: "v1beta1", Kind: domain.FleetKind, Metadata: domain.ObjectMeta{Name: lo.ToPtr("runtime-firmware"), Labels: &map[string]string{"phase": "initial"}}, Spec: domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &map[string]string{"site": "managed"}}}}
		managed.Spec.Template.Spec.Firmware = &firmware
		legacy := domain.Fleet{ApiVersion: "v1beta1", Kind: domain.FleetKind, Metadata: domain.ObjectMeta{Name: lo.ToPtr("runtime-legacy"), Labels: &map[string]string{"phase": "initial"}}, Spec: domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &map[string]string{"site": "legacy"}}}}
		commit := func() string {
			first, err := json.Marshal(managed)
			Expect(err).NotTo(HaveOccurred())
			second, err := json.Marshal(legacy)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(repoDir, "firmware.yaml"), first, 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(repoDir, "legacy.yaml"), second, 0o600)).To(Succeed())
			_, err = worktree.Add("firmware.yaml")
			Expect(err).NotTo(HaveOccurred())
			_, err = worktree.Add("legacy.yaml")
			Expect(err).NotTo(HaveOccurred())
			hash, err := worktree.Commit("Update fleet configuration", &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()}})
			Expect(err).NotTo(HaveOccurred())
			return hash.String()
		}
		initialCommit := commit()
		head, err := repo.Head()
		Expect(err).NotTo(HaveOccurred())
		var repoSpec domain.RepositorySpec
		Expect(repoSpec.FromGitRepoSpec(domain.GitRepoSpec{Type: domain.GitRepoSpecTypeGit, Url: "file://" + repoDir})).To(Succeed())
		_, status := suite.Repository.CreateRepository(ctx, suite.OrgID, domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("runtime-source")}, Spec: repoSpec})
		Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
		rsStore := resourcesyncstore.NewResourceSyncStore(suite.DB, suite.Log)
		rsService := resourcesyncservice.NewServiceHandler(rsStore, suite.Catalog, suite.Fleet, events.NewServiceHandler(suite.EventStore, suite.workerClient, suite.Log), suite.Log)
		rs := domain.ResourceSync{Metadata: domain.ObjectMeta{Name: lo.ToPtr("runtime-sync")}, Spec: domain.ResourceSyncSpec{Repository: "runtime-source", Path: "/", TargetRevision: head.Name().Short()}}
		_, status = rsService.CreateResourceSync(ctx, suite.OrgID, rs)
		Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
		configPath := filepath.Join(dir, "periodic.json")
		data, err = json.Marshal(cfg)
		Expect(err).NotTo(HaveOccurred())
		// SecureString intentionally redacts serialization. Only the private
		// subprocess fixture needs the real test connection credentials.
		var childConfig map[string]any
		Expect(json.Unmarshal(data, &childConfig)).To(Succeed())
		childConfig["database"].(map[string]any)["password"] = cfg.Database.Password.Value()
		childConfig["kv"].(map[string]any)["password"] = cfg.KV.Password.Value()
		data, err = json.Marshal(childConfig)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(configPath, data, 0o600)).To(Succeed())
		executable, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		command := exec.Command(executable, "-test.run=^TestUpdaterCatalogPeriodicChild$") //nolint:gosec // Execute only this integration test binary.
		command.Env = append(os.Environ(), periodicCatalogConfigEnv+"="+configPath)
		output, err := os.Create(filepath.Join(dir, "periodic.log"))
		Expect(err).NotTo(HaveOccurred())
		command.Stdout = output
		command.Stderr = output
		Expect(command.Start()).To(Succeed())
		done := make(chan error, 1)
		exited := make(chan struct{})
		go func() { done <- command.Wait(); close(exited) }()
		DeferCleanup(func() {
			signalErr := command.Process.Signal(syscall.SIGTERM)
			var waitErr error
			select {
			case waitErr = <-done:
			case <-time.After(15 * time.Second):
				_ = command.Process.Kill()
				waitErr = <-done
				if waitErr == nil {
					waitErr = errors.New("periodic process did not shut down")
				}
			}
			Expect(output.Close()).To(Succeed())
			if CurrentSpecReport().Failed() || waitErr != nil {
				log, err := os.ReadFile(filepath.Join(dir, "periodic.log"))
				if err == nil {
					GinkgoWriter.Print(string(log))
				}
			}
			if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
				Expect(signalErr).NotTo(HaveOccurred())
			}
			Expect(waitErr).NotTo(HaveOccurred())
		})

		observed := func(wantedCommit string, wanted domain.ConditionStatus) bool {
			select {
			case <-exited:
				StopTrying("periodic helper exited; see subprocess output").Now()
			default:
			}
			value, status := rsService.GetResourceSync(ctx, suite.OrgID, "runtime-sync")
			if status.Code != http.StatusOK || value.Status == nil || lo.FromPtr(value.Status.ObservedCommit) != wantedCommit {
				return false
			}
			condition := domain.FindStatusCondition(value.Status.Conditions, domain.ConditionTypeResourceSyncSynced)
			return condition != nil && condition.Status == wanted
		}
		Eventually(func() bool { return observed(initialCommit, domain.ConditionStatusTrue) }, 30*time.Second, 100*time.Millisecond).Should(BeTrue())
		provider, err := catalogservice.Bootstrap(ctx, suite.DB, "")
		Expect(err).NotTo(HaveOccurred())
		input.Revision = 2
		input.Releases = nil
		_, err = provider.Activate(ctx, input)
		Expect(err).NotTo(HaveOccurred())
		for index, phase := range []string{"revoked", "unknown"} {
			firmware.RetryGeneration = lo.ToPtr(int64(index + 1))
			(*managed.Metadata.Labels)["phase"] = phase
			(*legacy.Metadata.Labels)["phase"] = phase
			if phase == "unknown" {
				firmware.Updater.Version = "missing"
			}
			hash := commit()
			Eventually(func() bool { return observed(hash, domain.ConditionStatusFalse) }, 30*time.Second, 100*time.Millisecond).Should(BeTrue())
			stored, status := suite.Fleet.GetFleet(ctx, suite.OrgID, "runtime-firmware", domain.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			Expect((*stored.Metadata.Labels)["phase"]).To(Equal("initial"))
			Expect(stored.Spec.Template.Spec.Firmware.Updater.Version).To(Equal("0.1.0"))
			Expect(stored.Spec.Template.Spec.Firmware.RetryGeneration).To(BeNil())
			Eventually(func() string {
				stored, status := suite.Fleet.GetFleet(ctx, suite.OrgID, "runtime-legacy", domain.GetFleetParams{})
				if status.Code != http.StatusOK || stored.Metadata.Labels == nil {
					return ""
				}
				return (*stored.Metadata.Labels)["phase"]
			}, 10*time.Second, 100*time.Millisecond).Should(Equal(phase))
		}
	})
})
