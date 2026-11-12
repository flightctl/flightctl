package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"time"

	agentapi "github.com/flightctl/flightctl/api/agent/v1beta1"
	agentclient "github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	receiver "github.com/flightctl/flightctl/internal/agent/device/firmwarepolicy"
	generatedclient "github.com/flightctl/flightctl/internal/api/client/agent"
	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	agentserver "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/internal/rendered"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	catalogservice "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/flightctl/flightctl/internal/store/model"
	catalogstore "github.com/flightctl/flightctl/internal/store/updatercatalog"
	agenttransport "github.com/flightctl/flightctl/internal/transport/agent/v1beta1"
	"github.com/flightctl/flightctl/internal/util"
	prefixlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Private updater policy delivery", func() {
	var suite *ServiceTestSuite
	var catalog *catalogservice.Service
	var hub *rendered.FirmwarePolicyHub
	var broker queues.Provider
	var device deviceservice.Service
	var name string
	var input catalogservice.AdministratorConfig
	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		var err error
		broker, err = queues.NewRedisProvider(suite.Ctx, suite.Log, "policy-read-"+uuid.NewString(), redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
		Expect(err).NotTo(HaveOccurred())
		hub, err = rendered.NewFirmwarePolicyHub(suite.Ctx, broker)
		Expect(err).NotTo(HaveOccurred())
		st, err := catalogstore.NewStore(suite.DB)
		Expect(err).NotTo(HaveOccurred())
		catalog, err = catalogservice.NewService(st)
		Expect(err).NotTo(HaveOccurred())
		device = deviceservice.NewDeviceServiceHandler(suite.DeviceStore, nil, nil, nil, suite.kvStore, "", suite.Log, deviceservice.WithFirmwarePolicy(catalog, hub, 300*time.Millisecond))
		name = "policy-" + uuid.NewString()
		_, err = suite.DeviceStore.Create(suite.Ctx, suite.OrgID, &domain.Device{ApiVersion: "v1beta1", Kind: domain.DeviceKind, Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}, Spec: &domain.DeviceSpec{}}, nil)
		Expect(err).NotTo(HaveOccurred())
		input, err = firmwarecatalog.New()
		Expect(err).NotTo(HaveOccurred())
	})
	AfterEach(func() { hub.Close(); broker.Stop(); broker.Wait(); suite.Teardown() })
	read := func(revision int64, digest string) *firmwarepolicy.View {
		v, status := device.GetFirmwarePolicy(suite.Ctx, suite.OrgID, name, revision, digest)
		Expect(status.Code).To(Equal(int32(http.StatusOK)))
		Expect(v).NotTo(BeNil())
		return v
	}
	It("When policy is unavailable or the identity is absent it should fail closed", func() {
		_, status := device.GetFirmwarePolicy(suite.Ctx, suite.OrgID, name, 0, "")
		Expect(status.Code).To(Equal(int32(http.StatusServiceUnavailable)))
		_, err := catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		_, status = device.GetFirmwarePolicy(suite.Ctx, uuid.New(), name, 0, "")
		Expect(status.Code).To(Equal(int32(http.StatusNotFound)))
		_, status = device.GetFirmwarePolicy(suite.Ctx, suite.OrgID, "missing", 0, "")
		Expect(status.Code).To(Equal(int32(http.StatusNotFound)))
	})
	It("When desired firmware is invalid and no rendered target exists it should still deliver the full policy and revocation", func() {
		Expect(suite.DB.WithContext(suite.Ctx).Table("devices").Where("org_id = ? AND name = ?", suite.OrgID, name).Update("spec", `{"firmware":"invalid"}`).Error).To(Succeed())
		first, err := catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		before := read(0, "")
		Expect(before.Digest).To(Equal(first.Snapshot.Digest))
		document, err := firmwarepolicy.Decode(*before)
		Expect(err).NotTo(HaveOccurred())
		Expect(document.Bindings).To(HaveLen(1))
		input.Revision = 2
		input.Releases = nil
		_, err = catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		revoked := read(before.Revision, before.Digest)
		document, err = firmwarepolicy.Decode(*revoked)
		Expect(err).NotTo(HaveOccurred())
		Expect(document.Tombstones).To(HaveLen(1))
		Expect(document.Bindings).To(HaveLen(1))
		var spec string
		Expect(suite.DB.WithContext(suite.Ctx).Table("devices").Select("spec").Where("org_id = ? AND name = ?", suite.OrgID, name).Scan(&spec).Error).To(Succeed())
		Expect(spec).To(MatchJSON(`{"firmware":"invalid"}`))
	})
	It("When a hint is lost it should recheck the accepted head at the bounded poll deadline", func() {
		_, err := catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		before := read(0, "")
		result := make(chan *firmwarepolicy.View, 1)
		statuses := make(chan domain.Status, 1)
		go func() {
			v, s := device.GetFirmwarePolicy(suite.Ctx, suite.OrgID, name, before.Revision, before.Digest)
			result <- v
			statuses <- s
		}()
		input.Revision = 2
		input.Releases = nil
		_, err = catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		var next *firmwarepolicy.View
		Eventually(result, time.Second).Should(Receive(&next))
		var status domain.Status
		Eventually(statuses).Should(Receive(&status))
		Expect(status.Code).To(Equal(int32(http.StatusOK)))
		Expect(next.Revision).To(Equal(int64(2)))
	})
	It("When enrollment gates mutation it should deliver policy and preserve the gate across unchanged or conflicting reads", func() {
		_, err := catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.DB.WithContext(suite.Ctx).Table("devices").Where("org_id = ? AND name = ?", suite.OrgID, name).Update("service_conditions", `{"conditions":[{"type":"EnrollmentHooks","status":"False","reason":"NotifyPending","message":"waiting","lastTransitionTime":"2026-11-12T00:00:00Z"}]}`).Error).To(Succeed())
		v := read(0, "")
		before, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.IsDeviceEnrollmentHooksGated(before)).To(BeTrue())
		_, bad := device.GetFirmwarePolicy(suite.Ctx, suite.OrgID, name, -1, "bad")
		Expect(bad.Code).To(Equal(int32(http.StatusBadRequest)))
		Expect(device.AcknowledgeFirmwarePolicy(suite.Ctx, suite.OrgID, name, firmwarepolicy.Receipt{}).Code).To(Equal(int32(http.StatusBadRequest)))
		got, status := device.GetFirmwarePolicy(suite.Ctx, suite.OrgID, name, v.Revision, v.Digest)
		Expect(got).To(BeNil())
		Expect(status.Code).To(Equal(int32(http.StatusNoContent)))
		for _, known := range []firmwarepolicy.Receipt{{Revision: v.Revision, Digest: firmwarepolicy.Hash([]byte("wrong"))}, {Revision: v.Revision + 1, Digest: v.Digest}} {
			_, status = device.GetFirmwarePolicy(suite.Ctx, suite.OrgID, name, known.Revision, known.Digest)
			Expect(status.Code).To(Equal(int32(http.StatusConflict)))
		}
		after, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before))
	})
	It("When the agent acknowledges durable receipt it should track receipt separately and reject stale or foreign acknowledgements", func() {
		_, err := catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		v := read(0, "")
		receipt := firmwarepolicy.Receipt{Revision: v.Revision, Digest: v.Digest}
		Expect(device.AcknowledgeFirmwarePolicy(suite.Ctx, suite.OrgID, name, receipt).Code).To(Equal(int32(http.StatusNoContent)))
		Expect(device.AcknowledgeFirmwarePolicy(suite.Ctx, suite.OrgID, name, receipt).Code).To(Equal(int32(http.StatusNoContent)))
		var row model.UpdaterPolicyTarget
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND device_name = ?", suite.OrgID, name).First(&row).Error).To(Succeed())
		Expect(row.ReceivedRevision).To(Equal(int64(1)))
		Expect(row.NotifiedRevision).To(Equal(int64(0)))
		Expect(device.AcknowledgeFirmwarePolicy(suite.Ctx, uuid.New(), name, receipt).Code).To(Equal(int32(http.StatusNotFound)))
		input.Revision = 2
		input.Releases = nil
		_, err = catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(device.AcknowledgeFirmwarePolicy(suite.Ctx, suite.OrgID, name, receipt).Code).To(Equal(int32(http.StatusConflict)))
		next := read(v.Revision, v.Digest)
		Expect(device.AcknowledgeFirmwarePolicy(suite.Ctx, suite.OrgID, name, firmwarepolicy.Receipt{Revision: next.Revision, Digest: next.Digest}).Code).To(Equal(int32(http.StatusNoContent)))
	})
	It("When the private HTTP client reconnects it should read and acknowledge revocation without a target", func() {
		_, err := catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.DB.WithContext(suite.Ctx).Table("devices").Where("org_id = ? AND name = ?", suite.OrgID, name).Update("spec", `{"firmware":"invalid"}`).Error).To(Succeed())
		swagger, err := agentapi.GetSpec()
		Expect(err).NotTo(HaveOccurred())
		transport := agenttransport.NewAgentTransportHandler(device, nil, nil, convert.NewConverter(), nil, suite.Log)
		identity := middleware.NewAgentIdentity(name, suite.OrgID.String(), name, time.Now().Add(time.Hour))
		router := chi.NewRouter()
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := context.WithValue(util.WithOrganizationID(r.Context(), suite.OrgID), consts.IdentityCtxKey, identity)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})
		router.Route("/api/v1", func(r chi.Router) {
			r.Use(oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapimiddleware.Options{SilenceServersWarning: true}))
			agentserver.HandlerWithOptions(transport, agentserver.ChiServerOptions{BaseRouter: r})
		})
		server := httptest.NewServer(router)
		defer server.Close()
		generated, err := generatedclient.NewClientWithResponses(server.URL + "/api/v1")
		Expect(err).NotTo(HaveOccurred())
		client := agentclient.NewManagement(generated, nil)
		first, status, err := client.GetFirmwarePolicy(suite.Ctx, name, firmwarepolicy.Receipt{})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))
		Expect(first).NotTo(BeNil())
		status, err = client.AcknowledgeFirmwarePolicy(suite.Ctx, name, firmwarepolicy.Receipt{Revision: first.Revision, Digest: first.Digest})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(204))
		input.Revision = 2
		input.Releases = nil
		_, err = catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		next, status, err := client.GetFirmwarePolicy(suite.Ctx, name, firmwarepolicy.Receipt{Revision: first.Revision, Digest: first.Digest})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))
		Expect(next.Revision).To(Equal(int64(2)))
		document, err := firmwarepolicy.Decode(*next)
		Expect(err).NotTo(HaveOccurred())
		Expect(document.Tombstones).To(HaveLen(1))
		_, status, err = client.GetFirmwarePolicy(suite.Ctx, "another-device", firmwarepolicy.Receipt{})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(401))
	})

	It("When the agent receives a revocation over private HTTP it should retain it across offline restart", func() {
		_, err := catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.DB.WithContext(suite.Ctx).Table("devices").Where("org_id = ? AND name = ?", suite.OrgID, name).Update("spec", `{"firmware":"invalid"}`).Error).To(Succeed())
		transport := agenttransport.NewAgentTransportHandler(device, nil, nil, convert.NewConverter(), nil, suite.Log)
		identity := middleware.NewAgentIdentity(name, suite.OrgID.String(), name, time.Now().Add(time.Hour))
		router := chi.NewRouter()
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := context.WithValue(util.WithOrganizationID(r.Context(), suite.OrgID), consts.IdentityCtxKey, identity)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})
		router.Route("/api/v1", func(r chi.Router) {
			agentserver.HandlerWithOptions(transport, agentserver.ChiServerOptions{BaseRouter: r})
		})
		server := httptest.NewServer(router)
		defer server.Close()
		generated, err := generatedclient.NewClientWithResponses(server.URL + "/api/v1")
		Expect(err).NotTo(HaveOccurred())
		client := agentclient.NewManagement(generated, nil)
		dir := GinkgoT().TempDir()
		rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
		control := filepath.Join(dir, "current.json")
		Expect(rw.WriteFile(control, []byte("unchanged target"), 0600)).To(Succeed())
		manager, err := receiver.New(name, dir, rw, client, prefixlog.NewPrefixLogger("receipt-integration"), receiver.WithPollConfig(poll.Config{BaseDelay: 10 * time.Millisecond, Factor: 1, MaxDelay: 20 * time.Millisecond}))
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(suite.Ctx)
		done := make(chan struct{})
		go func() { manager.Run(ctx); close(done) }()
		defer func() { cancel(); Eventually(done, time.Second).Should(BeClosed()) }()
		accept := func(revision int64) {
			Eventually(func() bool {
				if manager.Sync(ctx) != nil {
					return false
				}
				view, err := manager.Current()
				return err == nil && view != nil && view.Revision == revision
			}, 3*time.Second, 10*time.Millisecond).Should(BeTrue())
		}
		accept(1)
		current, err := manager.Current()
		Expect(err).NotTo(HaveOccurred())
		document, err := firmwarepolicy.Decode(*current)
		Expect(err).NotTo(HaveOccurred())
		binding := document.Bindings[0]
		Expect(manager.IsApproved(binding.Name, binding.Version, binding.DescriptorDigest)).To(BeTrue())
		input.Revision = 2
		input.Releases = nil
		_, err = catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		accept(2)
		Expect(manager.IsApproved(binding.Name, binding.Version, binding.DescriptorDigest)).To(BeFalse())
		Eventually(func() int64 {
			var row model.UpdaterPolicyTarget
			suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND device_name = ?", suite.OrgID, name).First(&row)
			return row.ReceivedRevision
		}, 3*time.Second, 10*time.Millisecond).Should(Equal(int64(2)))
		cancel()
		Eventually(done, time.Second).Should(BeClosed())
		server.Close()
		restart, err := receiver.New(name, dir, rw, client, prefixlog.NewPrefixLogger("offline-restart"))
		Expect(err).NotTo(HaveOccurred())
		retained, err := restart.Current()
		Expect(err).NotTo(HaveOccurred())
		Expect(retained.Revision).To(Equal(int64(2)))
		Expect(restart.IsApproved(binding.Name, binding.Version, binding.DescriptorDigest)).To(BeFalse())
		bytes, err := rw.ReadFile(control)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(bytes)).To(Equal("unchanged target"))
	})

	It("When a long poll is canceled it should release its waiter", func() {
		_, err := catalog.Activate(suite.Ctx, input)
		Expect(err).NotTo(HaveOccurred())
		v := read(0, "")
		ctx, cancel := context.WithCancel(suite.Ctx)
		cancel()
		_, status := device.GetFirmwarePolicy(ctx, suite.OrgID, name, v.Revision, v.Digest)
		Expect(status.Code).NotTo(Equal(int32(http.StatusOK)))
	})
})
