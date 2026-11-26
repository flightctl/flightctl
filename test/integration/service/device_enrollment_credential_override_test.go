package service_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	server "github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/tasks"
	transport "github.com/flightctl/flightctl/internal/transport/v1beta1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Device enrollment credential override API", func() {
	var suite *ServiceTestSuite
	var handler http.Handler

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		DeferCleanup(suite.Teardown)
		authorizer := authz.NewStaticAuthZ(suite.Log)
		transportHandler := transport.NewTransportHandler(nil, suite.CertificateSigningRequest, suite.Device, nil, nil, nil, suite.EnrollmentRequest, nil, nil, nil, nil, nil, nil, nil, convert.NewConverter(), nil, nil, nil, authorizer)
		router := chi.NewRouter()
		router.Use(auth.CreateAuthZMiddleware(authorizer, suite.Log))
		spec, err := api.GetSpec()
		Expect(err).NotTo(HaveOccurred())
		router.Use(oapimiddleware.OapiRequestValidatorWithOptions(spec, &oapimiddleware.Options{SilenceServersWarning: true}))
		handler = server.HandlerWithOptions(transportHandler, server.ChiServerOptions{BaseURL: "/api/v1", BaseRouter: router})
	})

	request := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body)).WithContext(util.WithOrganizationID(ctx, suite.OrgID))
		req.Header.Set("Content-Type", "application/json")
		if method == http.MethodPatch {
			req.Header.Set("Content-Type", "application/json-patch+json")
		}
		handler.ServeHTTP(resp, req)
		return resp
	}
	readDevice := func(resp *httptest.ResponseRecorder) api.Device {
		Expect(resp.Code).To(Equal(http.StatusOK), resp.Body.String())
		var device api.Device
		Expect(json.Unmarshal(resp.Body.Bytes(), &device)).To(Succeed())
		Expect(device.Status).NotTo(BeNil())
		return device
	}

	seedDevice := func(name string, status domain.DeviceStatus) {
		_, err := suite.DeviceStore.Create(suite.Ctx, suite.OrgID, &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status:   &status,
		}, nil)
		Expect(err).NotTo(HaveOccurred())
	}
	seedFlaggedDevice := func(name string) {
		status := domain.NewDeviceStatus()
		status.EnrollmentCredential = &domain.DeviceEnrollmentCredentialStatus{Revoked: true}
		status.Conditions = []domain.Condition{{
			Type:               domain.ConditionTypeDeviceEnrollmentCredentialRevoked,
			Status:             domain.ConditionStatusTrue,
			Reason:             domain.EnrollmentCredentialRevokedReason,
			LastTransitionTime: time.Now().UTC(),
		}}
		seedDevice(name, status)
	}

	It("When an admin dismisses a revoked credential flag it should persist the original attribution", func() {
		seedFlaggedDevice("reviewed-device")
		path := "/devices/reviewed-device"
		before := time.Now()
		response := request(suite.Ctx, http.MethodPost, path+"/enrollmentcredential/override", `{"reason":" Reviewed device enrollment "}`+"\n")
		dismissed := readDevice(response)
		Expect(api.FindStatusCondition(dismissed.Status.Conditions, api.ConditionTypeDeviceEnrollmentCredentialRevoked)).To(BeNil())
		credential := dismissed.Status.EnrollmentCredential
		Expect(credential).NotTo(BeNil())
		Expect(credential.Revoked).To(BeTrue())
		Expect(credential.Override).NotTo(BeNil())
		Expect(credential.Override.OverriddenBy).To(Equal("test-admin"))
		Expect(credential.Override.OverriddenAt).To(BeTemporally(">=", before))
		Expect(credential.Override.OverriddenAt).To(BeTemporally("<=", time.Now()))
		Expect(credential.Override.Reason).To(Equal("Reviewed device enrollment"))

		secondAdmin := identity.NewMappedIdentity("second-admin", "second-admin", nil, nil, true, nil)
		secondCtx := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, secondAdmin)
		retry := request(secondCtx, http.MethodPost, path+"/enrollmentcredential/override", `{"reason":"Changed reason"}`)
		Expect(readDevice(retry).Status.EnrollmentCredential).To(Equal(credential))
		patch := request(suite.Ctx, http.MethodPatch, path+"/status", `[{"op":"remove","path":"/status/enrollmentCredential/override"}]`)
		Expect(patch.Code).To(Equal(http.StatusBadRequest), patch.Body.String())
		Expect(readDevice(request(suite.Ctx, http.MethodGet, path, "")).Status.EnrollmentCredential).To(Equal(credential))
	})

	It("When an operator can update Devices it should still require override permission", func() {
		seedFlaggedDevice("reviewed-device")
		operator := identity.NewMappedIdentity("test-operator", "test-operator", nil,
			map[string][]string{suite.OrgID.String(): {string(api.RoleOperator)}}, false, nil)
		ctx := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, operator)
		override := request(ctx, http.MethodPost, "/devices/reviewed-device/enrollmentcredential/override", `{"reason":"Reviewed device enrollment"}`)
		Expect(override.Code).To(Equal(http.StatusForbidden), override.Body.String())
		device := readDevice(request(ctx, http.MethodGet, "/devices/reviewed-device", ""))
		Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceEnrollmentCredentialRevoked)).To(BeTrue())
	})

	DescribeTable("When the override request has trailing data it should leave the flag active", func(body string) {
		seedFlaggedDevice("reviewed-device")
		path := "/devices/reviewed-device"
		response := request(suite.Ctx, http.MethodPost, path+"/enrollmentcredential/override", body)
		Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		device := readDevice(request(suite.Ctx, http.MethodGet, path, ""))
		Expect(device.Status.EnrollmentCredential.Override).To(BeNil())
		Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceEnrollmentCredentialRevoked)).To(BeTrue())
	},
		Entry("with a second JSON document", `{"reason":"Reviewed device enrollment"} {}`),
		Entry("with malformed trailing text", `{"reason":"Reviewed device enrollment"} invalid`),
	)

	It("When a status patch changes the revocation condition it should reject the patch", func() {
		seedFlaggedDevice("reviewed-device")
		path := "/devices/reviewed-device"
		flagged := readDevice(request(suite.Ctx, http.MethodGet, path, ""))
		checkRejectedConditions := func(conditions []api.Condition) {
			patch, err := json.Marshal(api.PatchRequest{{Op: "replace", Path: "/status/conditions", Value: conditions}})
			Expect(err).NotTo(HaveOccurred())
			response := request(suite.Ctx, http.MethodPatch, path+"/status", string(patch))
			Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		}
		checkRejectedConditions([]api.Condition{})
		checkRejectedConditions(slices.Concat(flagged.Status.Conditions, flagged.Status.Conditions))

		dismissed := readDevice(request(suite.Ctx, http.MethodPost, path+"/enrollmentcredential/override", `{"reason":"Reviewed device enrollment"}`))
		checkRejectedConditions(flagged.Status.Conditions)

		update := request(suite.Ctx, http.MethodPatch, path+"/status", `[{"op":"add","path":"/status/systemInfo/agentVersion","value":"2.0"}]`)
		Expect(readDevice(update).Status.SystemInfo.AgentVersion).To(Equal("2.0"))
		stored := readDevice(request(suite.Ctx, http.MethodGet, path, ""))
		Expect(stored.Status.EnrollmentCredential).To(Equal(dismissed.Status.EnrollmentCredential))
		Expect(api.FindStatusCondition(stored.Status.Conditions, api.ConditionTypeDeviceEnrollmentCredentialRevoked)).To(BeNil())
	})

	It("When a Device flag is dismissed it should move from the active filter to the audit filter", func() {
		seedFlaggedDevice("reviewed-device")
		seedFlaggedDevice("other-device")
		unrevoked := domain.NewDeviceStatus()
		unrevoked.EnrollmentCredential = &domain.DeviceEnrollmentCredentialStatus{Revoked: false}
		seedDevice("unrevoked-device", unrevoked)
		seedDevice("device-without-credential-status", domain.NewDeviceStatus())
		dismissed := request(suite.Ctx, http.MethodPost, "/devices/reviewed-device/enrollmentcredential/override", `{"reason":"Reviewed device enrollment"}`)
		Expect(dismissed.Code).To(Equal(http.StatusOK), dismissed.Body.String())

		listDeviceNames := func(selector string) []string {
			response := request(suite.Ctx, http.MethodGet, "/devices?fieldSelector="+url.QueryEscape(selector), "")
			Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
			var devices api.DeviceList
			Expect(json.Unmarshal(response.Body.Bytes(), &devices)).To(Succeed())
			return lo.Map(devices.Items, func(device api.Device, _ int) string { return lo.FromPtr(device.Metadata.Name) })
		}
		Expect(listDeviceNames("status.enrollmentCredential.revoked=true")).To(ConsistOf("other-device"))
		Expect(listDeviceNames("status.enrollmentCredential.revoked=false")).To(ConsistOf("reviewed-device", "unrevoked-device"))
		Expect(listDeviceNames("status.enrollmentCredential.revoked!=true")).To(ConsistOf("reviewed-device", "unrevoked-device", "device-without-credential-status"))
		Expect(listDeviceNames("status.enrollmentCredential.override")).To(ConsistOf("reviewed-device"))
	})

	It("When revocation is reconciled again it should keep the dismissal and enforce revocation", func() {
		csr, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, CreateTestCSR())
		Expect(status).To(Equal(domain.StatusCreated()))
		Expect(csr.Status.Certificate).NotTo(BeNil())
		credentialName := lo.FromPtr(csr.Metadata.Name)
		certificatePEM, _ := pem.Decode(*csr.Status.Certificate)
		Expect(certificatePEM).NotTo(BeNil())
		certificate, err := x509.ParseCertificate(certificatePEM.Bytes)
		Expect(err).NotTo(HaveOccurred())

		cache, err := middleware.NewRedisEnrollmentCSRStatusCache(suite.kvStore, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		enrollmentAuth, err := middleware.NewEnrollmentAuthMiddleware(suite.caClient, suite.CertificateSigningRequest, cache, suite.Log)
		Expect(err).NotTo(HaveOccurred())
		authenticate := func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "https://enrollment.example/api/v1/enrollmentrequests", nil)
			req.TLS.PeerCertificates = []*x509.Certificate{certificate}
			req = req.WithContext(context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, certificate))
			resp := httptest.NewRecorder()
			enrollmentAuth.AuthenticateEnrollment(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(resp, req)
			return resp
		}
		Expect(authenticate().Code).To(Equal(http.StatusNoContent))

		erStore := enrollmentrequeststore.NewEnrollmentRequestStore(suite.DB, suite.Log)
		bind := func(name string, admitted bool) {
			er := CreateTestER()
			er.Metadata.Name = &name
			er.Status = &domain.EnrollmentRequestStatus{EnrollmentCertificateName: &credentialName}
			if admitted {
				domain.SetStatusCondition(&er.Status.Conditions, domain.Condition{
					Type:   domain.ConditionTypeEnrollmentRequestApproved,
					Status: domain.ConditionStatusTrue,
					Reason: "ManuallyApproved",
				})
			}
			_, err := erStore.Create(suite.Ctx, suite.OrgID, &er)
			Expect(err).NotTo(HaveOccurred())
		}
		admit := func(name string) {
			bind(name, true)
			_, err := suite.DeviceStore.Create(suite.Ctx, suite.OrgID, &domain.Device{Metadata: domain.ObjectMeta{Name: &name}}, nil)
			Expect(err).NotTo(HaveOccurred())
		}
		admit("reviewed-device")
		bind("pending-device", false)
		revocation := request(suite.Ctx, http.MethodPost, "/certificatesigningrequests/"+credentialName+"/revocation", `{"reason":"Credential compromised"}`)
		Expect(revocation.Code).To(Equal(http.StatusOK), revocation.Body.String())

		ctx, cancel := context.WithCancel(suite.Ctx)
		DeferCleanup(cancel)
		broker, err := queues.NewRedisProvider(ctx, suite.Log, uuid.NewString(), redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			cancel()
			broker.Stop()
			broker.Wait()
		})
		producer, err := worker_client.QueuePublisher(ctx, broker)
		Expect(err).NotTo(HaveOccurred())
		eventSvc := event.NewServiceHandler(suite.EventStore, events.NewServiceHandler(suite.EventStore, suite.workerClient, suite.Log))
		Expect(tasks.LaunchConsumers(ctx, broker, tasks.TaskConsumer{
			DeviceSvc: suite.Device, EnrollmentRequestSvc: suite.EnrollmentRequest, CSRService: suite.CertificateSigningRequest, EventSvc: eventSvc,
		}, 1, 1)).To(Succeed())
		eventList, status := eventSvc.ListEvents(ctx, suite.OrgID, domain.ListEventsParams{})
		Expect(status).To(Equal(domain.StatusOK()))
		revoked, found := lo.Find(eventList.Items, func(e domain.Event) bool {
			return e.Reason == domain.EventReasonEnrollmentCredentialRevoked && e.InvolvedObject.Name == credentialName
		})
		Expect(found).To(BeTrue())
		payload, err := json.Marshal(worker_client.EventWithOrgId{OrgId: suite.OrgID, Event: revoked})
		Expect(err).NotTo(HaveOccurred())

		getDevice := func(name string) api.Device {
			return readDevice(request(suite.Ctx, http.MethodGet, "/devices/"+name, ""))
		}
		waitForFlag := func(name string) {
			Eventually(func() bool {
				return api.IsStatusConditionTrue(getDevice(name).Status.Conditions, api.ConditionTypeDeviceEnrollmentCredentialRevoked)
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
		}
		Expect(producer.Enqueue(ctx, payload, time.Now().UnixMicro())).To(Succeed())
		waitForFlag("reviewed-device")
		const reviewedPath = "/devices/reviewed-device/enrollmentcredential/override"
		dismissal := request(suite.Ctx, http.MethodPost, reviewedPath, `{"reason":"Reviewed device enrollment"}`)
		Expect(dismissal.Code).To(Equal(http.StatusOK), dismissal.Body.String())
		original := getDevice("reviewed-device").Status.EnrollmentCredential.Override
		Expect(original).NotTo(BeNil())

		// A racing admission gives the replay another eligible device to flag.
		admit("second-device")
		Expect(producer.Enqueue(ctx, payload, time.Now().UnixMicro())).To(Succeed())
		waitForFlag("second-device")
		reviewed := getDevice("reviewed-device")
		Expect(api.FindStatusCondition(reviewed.Status.Conditions, api.ConditionTypeDeviceEnrollmentCredentialRevoked)).To(BeNil())
		Expect(reviewed.Status.EnrollmentCredential.Override).To(Equal(original))

		pending := request(suite.Ctx, http.MethodGet, "/enrollmentrequests/pending-device", "")
		Expect(pending.Code).To(Equal(http.StatusOK), pending.Body.String())
		var denied api.EnrollmentRequest
		Expect(json.Unmarshal(pending.Body.Bytes(), &denied)).To(Succeed())
		Expect(api.IsStatusConditionTrue(denied.Status.Conditions, domain.ConditionTypeEnrollmentRequestDenied)).To(BeTrue())
		Expect(api.IsStatusConditionTrue(denied.Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved)).To(BeFalse())
		blocked := authenticate()
		Expect(blocked.Code).To(Equal(http.StatusUnauthorized), blocked.Body.String())
		Expect(blocked.Body.String()).To(ContainSubstring("Enrollment certificate is revoked"))
	})
})
