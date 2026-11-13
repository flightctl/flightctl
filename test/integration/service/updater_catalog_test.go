package service_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	catalogservice "github.com/flightctl/flightctl/internal/service/updatercatalog"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	catalogstore "github.com/flightctl/flightctl/internal/store/updatercatalog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

func catalogHash(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
func approvedCatalog() catalogservice.AdministratorConfig {
	key, err := x509.MarshalPKIXPublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public())
	Expect(err).NotTo(HaveOccurred())
	policy := catalogservice.PublisherPolicy{Version: 1, Type: "sigstoreSigned", KeyData: base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: key})), SignedIdentity: catalogservice.PublisherIdentity{Type: "exactRepository", DockerRepository: "quay.io/example/updater"}}
	raw, err := json.Marshal(policy)
	Expect(err).NotTo(HaveOccurred())
	var object map[string]any
	Expect(json.Unmarshal(raw, &object)).To(Succeed())
	raw, err = json.Marshal(object)
	Expect(err).NotTo(HaveOccurred())
	schema := func(raw string) catalogservice.SchemaDocument {
		return catalogservice.SchemaDocument{Digest: catalogHash([]byte(raw)), Bytes: raw}
	}
	closed := schema(`{"type":"object","additionalProperties":false,"properties":{}}`)
	release := catalogservice.ReleaseBinding{Name: "nvidia-orin", Version: "0.1.0", PackageDigest: catalogHash([]byte("image")), Protocol: 1, Publisher: catalogservice.PublisherBinding{Identity: "example", PolicyDigest: catalogHash(raw), Policy: policy}, Schemas: catalogservice.SchemaBundle{Config: schema(firmwareConfigSchema), Observation: schema(`{"type":"object","additionalProperties":false,"properties":{"version":{"type":"string"}}}`), Request: closed, Result: closed, Checkpoint: closed}, Access: &catalogservice.PhaseAccess{Prepare: &catalogservice.AccessGrant{}, Apply: &catalogservice.AccessGrant{}, Verify: &catalogservice.AccessGrant{}}, Limits: catalogservice.ExecutionLimits{MaxRequestBytes: 1024, MaxResultBytes: 1024, MaxStderrBytes: 1024, MaxCheckpointBytes: 1024, MaxScratchBytes: 1024, MaxMemoryBytes: 1024, MaxProcesses: 1, MaxInvocationSeconds: 60, MaxCycles: 3, MaxReboots: 3, MaxAttempts: 3}, BudgetKeySet: []string{"maxCycles"}, GUIDPaths: []string{"resources[].targetGuid"}, ReferencePaths: []catalogservice.ReferencePath{{Path: "releases[].artifact.source.repository", Kind: common.FirmwareReferenceArtifact}, {Path: "overlays[].artifact.authentication.signature.source.repository", Kind: common.FirmwareReferenceSignature}}}
	return catalogservice.AdministratorConfig{APIVersion: "firmware-catalog/v1", Revision: 1, Releases: []catalogservice.ReleaseBinding{release}}
}

var _ = Describe("Durable updater catalog provider", func() {
	var (
		suite    *ServiceTestSuite
		provider *catalogservice.Service
	)
	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		st, err := catalogstore.NewStore(suite.DB)
		Expect(err).NotTo(HaveOccurred())
		provider, err = catalogservice.NewService(st)
		Expect(err).NotTo(HaveOccurred())
	})
	AfterEach(func() { suite.Teardown() })
	It("When no catalog is accepted it should fail closed while unmanaged intent remains valid", func() {
		_, err := provider.LookupRelease(suite.Ctx, "nvidia-orin", "0.1.0")
		Expect(errors.Is(err, common.ErrApprovedDescriptorsUnavailable)).To(BeTrue())
		_, status := common.ValidateFirmwareIntent(suite.Ctx, suite.OrgID, provider, nil, &domain.DeviceSpec{}, common.FirmwareStandalone)
		Expect(status.Code).To(Equal(int32(http.StatusOK)))
	})
	It("When another replica revokes a release it should preserve both evidence identities and avoid cached approvals", func() {
		config := approvedCatalog()
		_, err := provider.Activate(suite.Ctx, config)
		Expect(err).NotTo(HaveOccurred())
		descriptor, err := provider.LookupRelease(suite.Ctx, "nvidia-orin", "0.1.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(descriptor.Approved).To(BeTrue())
		descriptor.BudgetKeySet[0] = "mutated"
		other, err := catalogservice.Bootstrap(suite.Ctx, suite.DB, "")
		Expect(err).NotTo(HaveOccurred())
		config.Revision = 2
		config.Releases = nil
		_, err = other.Activate(suite.Ctx, config)
		Expect(err).NotTo(HaveOccurred())
		descriptor, err = provider.LookupRelease(suite.Ctx, "nvidia-orin", "0.1.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(descriptor.Approved).To(BeFalse())
		Expect(descriptor.BudgetKeySet).To(Equal([]string{"maxCycles"}))
		for _, digest := range []string{descriptor.SchemaDigest, descriptor.ObservationSchemaDigest} {
			evidence, err := provider.LookupSchemaByDigest(suite.Ctx, "nvidia-orin", digest)
			Expect(err).NotTo(HaveOccurred())
			Expect(evidence.SchemaDigest).To(Equal(digest))
			Expect(evidence.ObservationSchema).To(Equal(descriptor.ObservationSchema))
			Expect(evidence.BudgetKeySet).To(Equal(descriptor.BudgetKeySet))
		}
		unknown, err := provider.LookupRelease(suite.Ctx, "unknown", "1")
		Expect(err).NotTo(HaveOccurred())
		Expect(unknown).To(BeNil())
	})
	It("When bootstrap input is stale or invalid it should preserve accepted state and replay identical input idempotently", func() {
		config := approvedCatalog()
		data, err := json.Marshal(config)
		Expect(err).NotTo(HaveOccurred())
		path := filepath.Join(GinkgoT().TempDir(), "catalog.yaml")
		Expect(os.WriteFile(path, data, 0o600)).To(Succeed())
		_, err = catalogservice.Bootstrap(suite.Ctx, suite.DB, path)
		Expect(err).NotTo(HaveOccurred())
		_, err = catalogservice.Bootstrap(suite.Ctx, suite.DB, path)
		Expect(err).NotTo(HaveOccurred())
		config.Revision = 2
		_, err = provider.Activate(suite.Ctx, config)
		Expect(err).NotTo(HaveOccurred())
		_, err = catalogservice.Bootstrap(suite.Ctx, suite.DB, path)
		Expect(err).To(HaveOccurred())
		snapshot, err := provider.Snapshot(suite.Ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.Revision).To(Equal(int64(2)))
		Expect(os.WriteFile(path, []byte("invalid"), 0o600)).To(Succeed())
		_, err = catalogservice.Bootstrap(suite.Ctx, suite.DB, path)
		Expect(err).To(HaveOccurred())
		snapshot, err = provider.Snapshot(suite.Ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.Revision).To(Equal(int64(2)))
	})
	It("When historical status uses distinct schema digests it should validate pinned and component budgets after revocation", func() {
		config := approvedCatalog()
		r := config.Releases[0]
		Expect(r.Schemas.Config.Digest).NotTo(Equal(r.Schemas.Observation.Digest))
		_, err := provider.Activate(suite.Ctx, config)
		Expect(err).NotTo(HaveOccurred())
		config.Revision = 2
		config.Releases = nil
		_, err = provider.Activate(suite.Ctx, config)
		Expect(err).NotTo(HaveOccurred())
		raw := fmt.Sprintf(`{"support":{"apiVersion":1,"supported":true,"updaters":[]},"bootID":"boot-1","observedAt":"2026-11-09T10:05:00Z","attempt":{"id":"attempt-1","renderedVersion":"7","specHash":"%s","retryGeneration":0,"updater":{"name":"nvidia-orin","version":"0.1.0","buildDigest":"%s","protocol":1,"schemaDigest":"%s","configDigest":"%s"},"phase":"Failed","startedAt":"2026-11-09T10:00:00Z","updatedAt":"2026-11-09T10:05:00Z","changed":false,"error":{"reason":"ResourceLimitExceeded","message":"limit","retryable":false,"exceededBudget":"maxCycles"},"recovery":[]},"components":[{"name":"system","type":"firmware","managed":true,"state":"Failed","bootID":"boot-1","observedAt":"2026-11-09T10:05:00Z","observation":{"producerIdentity":"nvidia-orin/0.1.0","backend":{"updater":"nvidia-orin","schemaDigest":"%s","data":{"version":"1"}}},"error":{"reason":"ResourceLimitExceeded","message":"limit","retryable":false,"exceededBudget":"maxCycles"}}]}`, r.PackageDigest, r.PackageDigest, r.Schemas.Config.Digest, r.PackageDigest, r.Schemas.Observation.Digest)
		var reported domain.FirmwareStatus
		Expect(json.Unmarshal([]byte(raw), &reported)).To(Succeed())
		status := common.ValidateFirmwareStatus(suite.Ctx, provider, &reported)
		Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
		reported.Attempt = nil
		status = common.ValidateFirmwareStatus(suite.Ctx, provider, &reported)
		Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
		reported.Components[0].Error.ExceededBudget = lo.ToPtr("unknownBudget")
		status = common.ValidateFirmwareStatus(suite.Ctx, provider, &reported)
		Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), status.Message)
	})

	It("When direct activation is invalid or database reads fail it should fail closed and preserve the accepted revision", func() {
		config := approvedCatalog()
		_, err := provider.Activate(suite.Ctx, config)
		Expect(err).NotTo(HaveOccurred())
		config.Revision = 2
		config.Releases[0].Access.Apply.NetworkRepositories = []string{"forbidden"}
		_, err = provider.Activate(suite.Ctx, config)
		Expect(err).To(HaveOccurred())
		snapshot, err := provider.Snapshot(suite.Ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.Revision).To(Equal(int64(1)))
		unknown, err := provider.LookupSchemaByDigest(suite.Ctx, "unknown", catalogHash([]byte("unknown")))
		Expect(err).NotTo(HaveOccurred())
		Expect(unknown).To(BeNil())
		canceled, cancel := context.WithCancel(suite.Ctx)
		cancel()
		_, err = provider.LookupRelease(canceled, "nvidia-orin", "0.1.0")
		Expect(errors.Is(err, common.ErrApprovedDescriptorsUnavailable)).To(BeTrue())
		_, err = provider.LookupSchemaByDigest(canceled, "nvidia-orin", config.Releases[0].Schemas.Config.Digest)
		Expect(errors.Is(err, common.ErrApprovedDescriptorsUnavailable)).To(BeTrue())
	})

	It("When Device and Fleet writes use the real provider it should admit approved intent and reject revoked intent", func() {
		config := approvedCatalog()
		_, err := provider.Activate(suite.Ctx, config)
		Expect(err).NotTo(HaveOccurred())
		for _, name := range []string{"firmware-artifacts", "firmware-signatures"} {
			var spec domain.RepositorySpec
			Expect(spec.FromHttpRepoSpec(domain.HttpRepoSpec{Type: domain.HttpRepoSpecTypeHttp, Url: "https://example.com/" + name})).To(Succeed())
			_, status := suite.Repository.CreateRepository(suite.Ctx, suite.OrgID, domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}, Spec: spec})
			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
		}
		fs := fleetstore.NewFleetStore(suite.DB, suite.Log)
		ev := events.NewServiceHandler(suite.EventStore, suite.workerClient, suite.Log)
		devices := deviceservice.NewDeviceServiceHandler(suite.DeviceStore, nil, fs, ev, nil, "", suite.Log, deviceservice.WithApprovedDescriptorProvider(provider), deviceservice.WithRepositoryGetter(suite.Repository.GetRepository))
		fleets := fleetservice.NewServiceHandler(fs, nil, ev, suite.Log, fleetservice.WithApprovedDescriptorProvider(provider), fleetservice.WithRepositoryGetter(suite.Repository.GetRepository))
		var fw domain.FirmwareSpec
		Expect(json.Unmarshal([]byte(firmwareJSON), &fw)).To(Succeed())
		device := domain.Device{ApiVersion: "v1beta1", Kind: domain.DeviceKind, Metadata: domain.ObjectMeta{Name: lo.ToPtr("approved")}, Spec: &domain.DeviceSpec{Firmware: &fw}}
		_, status := devices.CreateDevice(suite.Ctx, suite.OrgID, device)
		Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
		fleet := domain.Fleet{ApiVersion: "v1beta1", Kind: domain.FleetKind, Metadata: domain.ObjectMeta{Name: lo.ToPtr("approved")}, Spec: domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &map[string]string{"site": "lab"}}}}
		fleet.Spec.Template.Spec.Firmware = &fw
		_, status = fleets.CreateFleet(suite.Ctx, suite.OrgID, fleet)
		Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
		config.Revision = 2
		config.Releases = nil
		_, err = provider.Activate(suite.Ctx, config)
		Expect(err).NotTo(HaveOccurred())
		device.Metadata.Name = lo.ToPtr("revoked")
		_, status = devices.CreateDevice(suite.Ctx, suite.OrgID, device)
		Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), status.Message)
		fleet.Metadata.Name = lo.ToPtr("revoked")
		_, status = fleets.CreateFleet(suite.Ctx, suite.OrgID, fleet)
		Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), status.Message)
	})
})
