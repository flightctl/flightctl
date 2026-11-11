package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	agentv1beta1 "github.com/flightctl/flightctl/api/agent/v1beta1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	convertv1beta1 "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/api/server"
	agentserver "github.com/flightctl/flightctl/internal/api/server/agent"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/api_server/versioning"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	agenttransportv1beta1 "github.com/flightctl/flightctl/internal/transport/agent/v1beta1"
	transportv1beta1 "github.com/flightctl/flightctl/internal/transport/v1beta1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"go.uber.org/mock/gomock"
)

const (
	observationSchemaDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	historicalSchemaDigest  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	statusDigest            = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	attemptErasureMessage   = "status.firmware.attempt: cannot erase an outstanding attempt"
)

// retainedObservationSchema is a closed observation schema for the
// test-only retained evidence served by retainedFirmwareEvidence.
const retainedObservationSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "dmiVersion": {"type": "string"},
    "esrt": {"type": "object", "additionalProperties": false, "required": ["resourceGuid"],
      "properties": {"resourceGuid": {"type": "string", "format": "uuid"}, "lastAttemptStatus": {"type": "integer", "minimum": 0}}}
  }
}`

// retainedFirmwareEvidence is a test stub of the approved descriptor provider
// that serves the retained schema evidence of a current and a historical
// release and counts its lookups. Release lookups are counted too, so tests
// can check that status writes never ask for current authorization.
type retainedFirmwareEvidence struct {
	schemaLookups  atomic.Int32
	releaseLookups atomic.Int32
}

func (p *retainedFirmwareEvidence) LookupRelease(context.Context, string, string) (*common.ApprovedDescriptor, error) {
	p.releaseLookups.Add(1)
	return nil, errors.New("status writes must not look up current release authorization")
}

func (p *retainedFirmwareEvidence) LookupSchemaByDigest(_ context.Context, updater, schemaDigest string) (*common.RetainedSchemaEvidence, error) {
	p.schemaLookups.Add(1)
	if updater != "nvidia-orin" || (schemaDigest != observationSchemaDigest && schemaDigest != historicalSchemaDigest) {
		return nil, nil
	}
	return &common.RetainedSchemaEvidence{
		SchemaDigest:      schemaDigest,
		ObservationSchema: json.RawMessage(retainedObservationSchema),
		BudgetKeySet:      []string{"maxArtifactBytes", "operationTimeoutSeconds"},
	}, nil
}

// firmwareStatusDocument returns a firmware status with an attempt in the
// given phase and an observation produced under the given schema digest.
func firmwareStatusDocument(attemptID string, phase api.FirmwareAttemptPhase, observationDigest string) map[string]any {
	document := `{
  "support": {"apiVersion": 1, "supported": true, "updaters": []},
  "attempt": {
    "id": "` + attemptID + `", "renderedVersion": "1", "specHash": "` + statusDigest + `", "retryGeneration": 0,
    "updater": {"name": "nvidia-orin", "version": "0.1.0", "buildDigest": "` + statusDigest + `", "protocol": 1,
      "schemaDigest": "` + observationSchemaDigest + `", "configDigest": "` + statusDigest + `"},
    "phase": "` + string(phase) + `", "startedAt": "2026-11-09T10:00:00Z", "updatedAt": "2026-11-09T10:05:00Z", "changed": false, "recovery": []
  },
  "components": [{
    "name": "system", "type": "firmware", "managed": true, "state": "Pending",
    "observation": {"backend": {"updater": "nvidia-orin", "schemaDigest": "` + observationDigest + `",
      "data": {"dmiVersion": "36.4.4", "esrt": {"resourceGuid": "00000000-0000-4000-8000-000000000001", "lastAttemptStatus": 0}}}},
    "observedAt": "2026-11-09T10:05:00Z", "bootID": "boot-1"
  }],
  "observedAt": "2026-11-09T10:05:00Z",
  "bootID": "boot-1"
}`
	var value map[string]any
	Expect(json.Unmarshal([]byte(document), &value)).To(Succeed())
	return value
}

func firmwareStatusOf(document map[string]any) *api.FirmwareStatus {
	raw, err := json.Marshal(document)
	Expect(err).ToNot(HaveOccurred())
	var status api.FirmwareStatus
	Expect(json.Unmarshal(raw, &status)).To(Succeed())
	return &status
}

var _ = Describe("Firmware status writes at the write boundary", func() {
	var (
		suite     *ServiceTestSuite
		ctx       context.Context
		orgId     uuid.UUID
		evidence  *retainedFirmwareEvidence
		devStore  *devicestore.DeviceStore
		deviceSvc deviceservice.Service
	)

	const deviceName = "device-1"

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		ctx = suite.Ctx
		orgId = suite.OrgID
		evidence = &retainedFirmwareEvidence{}
		devStore = devicestore.NewDeviceStore(suite.DB, suite.Log.WithField("pkg", "device-store"))
		fleetStore := fleetstore.NewFleetStore(suite.DB, suite.Log.WithField("pkg", "fleet-store"))
		ctrl := gomock.NewController(GinkgoT())
		producer := queues.NewMockQueueProducer(ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		eventsSvc := events.NewServiceHandler(suite.EventStore, worker_client.NewWorkerClient(producer, suite.Log), suite.Log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(devStore, nil, fleetStore, eventsSvc, nil, "", suite.Log,
			deviceservice.WithApprovedDescriptorProvider(evidence), deviceservice.WithRepositoryGetter(suite.Repository.GetRepository))
		_, status := deviceSvc.CreateDevice(ctx, orgId, api.Device{
			ApiVersion: "v1beta1", Kind: api.DeviceKind,
			Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName)},
			Spec:     &api.DeviceSpec{},
		})
		Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
	})

	AfterEach(func() {
		suite.Teardown()
	})

	stored := func() *api.Device {
		device, err := devStore.Get(ctx, orgId, deviceName)
		Expect(err).ToNot(HaveOccurred())
		return device
	}
	statusWrite := func(firmware *api.FirmwareStatus, osImage string) api.Device {
		status := api.NewDeviceStatus()
		status.Firmware = firmware
		status.Os.Image = osImage
		return api.Device{ApiVersion: "v1beta1", Kind: api.DeviceKind, Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName)}, Status: &status}
	}
	// storeFirmwareStatus records a firmware status as an earlier write would
	// have left it.
	storeFirmwareStatus := func(firmware *api.FirmwareStatus) *api.Device {
		_, status := deviceSvc.ReplaceDeviceStatus(ctx, orgId, deviceName, statusWrite(firmware, "quay.io/example/os:1"), true)
		Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
		return stored()
	}
	// commitConcurrently makes the device store write the given firmware status
	// through a second store, once, between a write's load and its persistence.
	commitConcurrently := func(firmware *api.FirmwareStatus) *atomic.Bool {
		concurrentStore := devicestore.NewDeviceStore(suite.DB, suite.Log)
		var done atomic.Bool
		devStore.SetIntegrationTestCreateOrUpdateCallback(func() {
			if !done.CompareAndSwap(false, true) {
				return
			}
			current, err := concurrentStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			current.Status.Firmware = firmware
			_, _, err = concurrentStore.UpdateStatus(ctx, orgId, current, nil)
			Expect(err).ToNot(HaveOccurred())
		})
		DeferCleanup(func() { devStore.SetIntegrationTestCreateOrUpdateCallback(func() {}) })
		return &done
	}

	Context("status writes on real Postgres", func() {
		It("When a valid firmware status is reported it should store it with every observation key", func() {
			after := storeFirmwareStatus(firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)))

			Expect(after.Status.Firmware.Attempt.Id).To(Equal("attempt-1"))
			data := after.Status.Firmware.Components[0].Observation.Backend.Data
			Expect(data).To(HaveKeyWithValue("dmiVersion", "36.4.4"))
			Expect(data["esrt"]).To(HaveKeyWithValue("resourceGuid", "00000000-0000-4000-8000-000000000001"))
			Expect(evidence.schemaLookups.Load()).To(BeNumerically(">", 0))
			Expect(evidence.releaseLookups.Load()).To(BeZero())
		})

		It("When a historical observation is reported without an attempt it should accept it", func() {
			document := firmwareStatusDocument("unused", api.FirmwareAttemptPhaseStaging, historicalSchemaDigest)
			delete(document, "attempt")

			after := storeFirmwareStatus(firmwareStatusOf(document))

			Expect(after.Status.Firmware.Attempt).To(BeNil())
			Expect(after.Status.Firmware.Components[0].Observation.Backend.SchemaDigest).To(Equal(historicalSchemaDigest))
		})

		It("When a reported firmware status is invalid it should reject it and leave the status and resource version unchanged", func() {
			before := storeFirmwareStatus(firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)))
			cases := map[string]func(document map[string]any){
				"status.firmware.components[0].observation.backend.data": func(document map[string]any) {
					document["components"].([]any)[0].(map[string]any)["observation"].(map[string]any)["backend"].(map[string]any)["data"].(map[string]any)["flashCount"] = 3
				},
				"status.firmware.components[0].observation.backend.schemaDigest": func(document map[string]any) {
					document["components"].([]any)[0].(map[string]any)["observation"].(map[string]any)["backend"].(map[string]any)["schemaDigest"] = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
				},
				"status.firmware.attempt.error.exceededBudget": func(document map[string]any) {
					document["attempt"].(map[string]any)["error"] = map[string]any{"reason": "ArtifactUnauthenticated", "message": "limit", "retryable": false, "exceededBudget": "maxCapsuleCount"}
				},
			}
			for wantPath, mutate := range cases {
				document := firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)
				mutate(document)

				_, status := deviceSvc.ReplaceDeviceStatus(ctx, orgId, deviceName, statusWrite(firmwareStatusOf(document), "quay.io/example/os:2"), true)

				Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), wantPath)
				Expect(status.Message).To(ContainSubstring(wantPath))
				after := stored()
				Expect(after.Status).To(Equal(before.Status), wantPath)
				Expect(after.Metadata.ResourceVersion).To(Equal(before.Metadata.ResourceVersion), wantPath)
			}
		})

		It("When a status write omits the firmware status it should keep the outstanding attempt", func() {
			before := storeFirmwareStatus(firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)))

			_, status := deviceSvc.ReplaceDeviceStatus(ctx, orgId, deviceName, statusWrite(nil, "quay.io/example/os:2"), true)

			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			after := stored()
			Expect(after.Status.Firmware).To(Equal(before.Status.Firmware))
			Expect(after.Status.Os.Image).To(Equal("quay.io/example/os:2"))
		})

		It("When a status write drops an outstanding attempt it should reject it, and accept the same attempt finishing", func() {
			before := storeFirmwareStatus(firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)))
			dropped := firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)
			delete(dropped, "attempt")

			_, status := deviceSvc.ReplaceDeviceStatus(ctx, orgId, deviceName, statusWrite(firmwareStatusOf(dropped), "quay.io/example/os:2"), true)

			Expect(status.Code).To(Equal(int32(http.StatusBadRequest)))
			Expect(status.Message).To(Equal(attemptErasureMessage))
			after := stored()
			Expect(after.Status).To(Equal(before.Status))
			Expect(after.Metadata.ResourceVersion).To(Equal(before.Metadata.ResourceVersion))

			finished := firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseSucceeded, observationSchemaDigest))
			_, status = deviceSvc.ReplaceDeviceStatus(ctx, orgId, deviceName, statusWrite(finished, "quay.io/example/os:2"), true)
			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			Expect(stored().Status.Firmware.Attempt.Phase).To(Equal(api.FirmwareAttemptPhaseSucceeded))
		})

		It("When an outstanding attempt is committed between a status write's load and its persistence it should keep it for an omitting write", func() {
			committed := firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest))
			done := commitConcurrently(committed)

			_, status := deviceSvc.ReplaceDeviceStatus(ctx, orgId, deviceName, statusWrite(nil, "quay.io/example/os:2"), true)

			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			Expect(done.Load()).To(BeTrue())
			after := stored()
			Expect(after.Status.Firmware).ToNot(BeNil())
			Expect(after.Status.Firmware.Attempt.Id).To(Equal("attempt-1"))
			Expect(after.Status.Os.Image).To(Equal("quay.io/example/os:2"))
		})

		It("When an outstanding attempt is committed between a status write's load and its persistence it should reject a stale replacement", func() {
			committed := firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest))
			done := commitConcurrently(committed)
			replacement := firmwareStatusOf(firmwareStatusDocument("attempt-2", api.FirmwareAttemptPhaseStaging, observationSchemaDigest))

			_, status := deviceSvc.ReplaceDeviceStatus(ctx, orgId, deviceName, statusWrite(replacement, "quay.io/example/os:2"), true)

			Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), status.Message)
			Expect(status.Message).To(Equal(attemptErasureMessage))
			Expect(done.Load()).To(BeTrue())
			after := stored()
			Expect(after.Status.Firmware.Attempt.Id).To(Equal("attempt-1"))
			Expect(after.Status.Os.Image).ToNot(Equal("quay.io/example/os:2"))
		})

		It("When an outstanding attempt is committed between a status patch's load and its persistence it should reject a stale removal", func() {
			finished := firmwareStatusOf(firmwareStatusDocument("attempt-0", api.FirmwareAttemptPhaseFailed, observationSchemaDigest))
			storeFirmwareStatus(finished)
			committed := firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest))
			done := commitConcurrently(committed)

			_, status := deviceSvc.PatchDeviceStatus(ctx, orgId, deviceName, api.PatchRequest{{Op: "remove", Path: "/status/firmware"}}, true)

			Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), status.Message)
			Expect(status.Message).To(ContainSubstring(attemptErasureMessage))
			Expect(done.Load()).To(BeTrue())
			Expect(stored().Status.Firmware.Attempt.Id).To(Equal("attempt-1"))
		})

		It("When an outstanding attempt is committed between a status patch's load and its persistence it should keep it for a patch that omits the firmware status", func() {
			done := commitConcurrently(firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)))
			replacement := api.NewDeviceStatus()
			replacement.Os.Image = "quay.io/example/os:patched"
			raw, err := json.Marshal(replacement)
			Expect(err).ToNot(HaveOccurred())
			var value map[string]any
			Expect(json.Unmarshal(raw, &value)).To(Succeed())
			delete(value, "firmware")

			_, status := deviceSvc.PatchDeviceStatus(ctx, orgId, deviceName, api.PatchRequest{{Op: "replace", Path: "/status", Value: value}}, true)

			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			Expect(done.Load()).To(BeTrue())
			after := stored()
			Expect(after.Status.Firmware.Attempt.Id).To(Equal("attempt-1"))
			Expect(after.Status.Os.Image).To(Equal("quay.io/example/os:patched"))
		})
	})

	Context("raw HTTP requests", func() {
		var userServer, agentServer *httptest.Server

		BeforeEach(func() {
			identity := ctx.Value(consts.MappedIdentityCtxKey)

			userSwagger, err := api.GetSpec()
			Expect(err).ToNot(HaveOccurred())
			userHandler := transportv1beta1.NewTransportHandler(nil, nil, deviceSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				convertv1beta1.NewConverter(), nil, nil, nil, nil)
			userRouter := chi.NewRouter()
			userRouter.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestCtx := context.WithValue(util.WithOrganizationID(r.Context(), orgId), consts.MappedIdentityCtxKey, identity)
					next.ServeHTTP(w, r.WithContext(requestCtx))
				})
			})
			userRouter.Route(server.ServerUrlApiv1, func(r chi.Router) {
				r.Use(oapimiddleware.OapiRequestValidatorWithOptions(userSwagger, &oapimiddleware.Options{
					ErrorHandler:          apiserver.OapiErrorHandlerForVersion(versioning.V1Beta1),
					MultiErrorHandler:     apiserver.OapiMultiErrorHandler,
					SilenceServersWarning: true,
				}))
				server.HandlerWithOptions(userHandler, server.ChiServerOptions{
					BaseRouter: r,
					ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
						apiserver.OapiErrorHandlerForVersion(versioning.V1Beta1)(w, err.Error(), http.StatusBadRequest)
					},
				})
			})
			userServer = httptest.NewServer(userRouter)
			DeferCleanup(userServer.Close)

			agentSwagger, err := agentv1beta1.GetSpec()
			Expect(err).ToNot(HaveOccurred())
			agentHandler := agenttransportv1beta1.NewAgentTransportHandler(deviceSvc, nil, nil, convertv1beta1.NewConverter(), nil, suite.Log)
			agentIdentity := middleware.NewAgentIdentity(deviceName, orgId.String(), deviceName, time.Now().Add(time.Hour))
			agentRouter := chi.NewRouter()
			agentRouter.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestCtx := context.WithValue(util.WithOrganizationID(r.Context(), orgId), consts.IdentityCtxKey, agentIdentity)
					next.ServeHTTP(w, r.WithContext(requestCtx))
				})
			})
			agentRouter.Route(agentserver.ServerUrlApiv1, func(r chi.Router) {
				r.Use(oapimiddleware.OapiRequestValidatorWithOptions(agentSwagger, &oapimiddleware.Options{
					ErrorHandler:          apiserver.OapiErrorHandler,
					SilenceServersWarning: true,
				}))
				agentserver.HandlerWithOptions(agentHandler, agentserver.ChiServerOptions{
					BaseRouter: r,
					ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
						apiserver.OapiErrorHandler(w, err.Error(), http.StatusBadRequest)
					},
				})
			})
			agentServer = httptest.NewServer(agentRouter)
			DeferCleanup(agentServer.Close)
		})

		send := func(target *httptest.Server, method, path string, body any) (int, string) {
			raw, err := json.Marshal(body)
			Expect(err).ToNot(HaveOccurred())
			if text, ok := body.(string); ok {
				raw = []byte(text)
			}
			contentType := "application/json"
			if method == http.MethodPatch {
				contentType = "application/json-patch+json"
			}
			req, err := http.NewRequestWithContext(ctx, method, target.URL+server.ServerUrlApiv1+path, strings.NewReader(string(raw)))
			Expect(err).ToNot(HaveOccurred())
			req.Header.Set("Content-Type", contentType)
			resp, err := http.DefaultClient.Do(req)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()
			response, err := io.ReadAll(resp.Body)
			Expect(err).ToNot(HaveOccurred())
			return resp.StatusCode, string(response)
		}
		statusBody := func(firmware any) map[string]any {
			status := api.NewDeviceStatus()
			status.Os.Image = "quay.io/example/os:http"
			raw, err := json.Marshal(status)
			Expect(err).ToNot(HaveOccurred())
			var value map[string]any
			Expect(json.Unmarshal(raw, &value)).To(Succeed())
			if firmware == nil {
				delete(value, "firmware")
			} else {
				value["firmware"] = firmware
			}
			return map[string]any{"apiVersion": "v1beta1", "kind": "Device", "metadata": map[string]any{"name": deviceName}, "status": value}
		}

		for _, route := range []string{"user", "agent"} {
			target := func() *httptest.Server {
				if route == "agent" {
					return agentServer
				}
				return userServer
			}

			It("When the "+route+" route reports firmware status it should enforce raw fields, keep observation keys, keep omissions and reject attempt erasure", func() {
				By("rejecting a case-variant attempt field before decoding")
				document := firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)
				document["attempt"].(map[string]any)["Id"] = "attempt-2"
				code, body := send(target(), http.MethodPut, "/devices/"+deviceName+"/status", statusBody(document))
				Expect(code).To(Equal(http.StatusBadRequest), body)
				Expect(stored().Status.Firmware).To(BeNil())

				By("rejecting a null firmware status")
				code, body = send(target(), http.MethodPut, "/devices/"+deviceName+"/status", strings.Replace(mustJSON(statusBody(nil)), `"os":`, `"firmware": null, "os":`, 1))
				Expect(code).To(Equal(http.StatusBadRequest), body)

				By("storing a valid firmware status with every observation key")
				code, body = send(target(), http.MethodPut, "/devices/"+deviceName+"/status", statusBody(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)))
				Expect(code).To(Equal(http.StatusOK), body)
				after := stored()
				Expect(after.Status.Firmware.Components[0].Observation.Backend.Data).To(HaveKey("dmiVersion"))

				By("rejecting an unknown observation key through the retained schema")
				unknown := firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseVerifying, observationSchemaDigest)
				unknown["components"].([]any)[0].(map[string]any)["observation"].(map[string]any)["backend"].(map[string]any)["data"].(map[string]any)["flashCount"] = 3
				code, body = send(target(), http.MethodPut, "/devices/"+deviceName+"/status", statusBody(unknown))
				Expect(code).To(Equal(http.StatusBadRequest), body)
				Expect(body).To(ContainSubstring("flashCount"))
				Expect(stored().Status).To(Equal(after.Status))

				By("keeping the firmware status when a status write omits it")
				code, body = send(target(), http.MethodPut, "/devices/"+deviceName+"/status", statusBody(nil))
				Expect(code).To(Equal(http.StatusOK), body)
				Expect(stored().Status.Firmware).To(Equal(after.Status.Firmware))

				By("rejecting a status write that drops the outstanding attempt")
				dropped := firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)
				delete(dropped, "attempt")
				code, body = send(target(), http.MethodPut, "/devices/"+deviceName+"/status", statusBody(dropped))
				Expect(code).To(Equal(http.StatusBadRequest), body)
				Expect(body).To(ContainSubstring(attemptErasureMessage))

				By("rejecting a patch that removes the outstanding attempt")
				code, body = send(target(), http.MethodPatch, "/devices/"+deviceName+"/status", `[{"op": "remove", "path": "/status/firmware/attempt"}]`)
				Expect(code).To(Equal(http.StatusBadRequest), body)
				Expect(body).To(ContainSubstring(attemptErasureMessage))

				By("rejecting a patch with a null value")
				code, body = send(target(), http.MethodPatch, "/devices/"+deviceName+"/status", `[{"op": "replace", "path": "/status/firmware/attempt", "value": null}]`)
				Expect(code).To(Equal(http.StatusBadRequest), body)
				Expect(body).To(ContainSubstring("not nullable"))

				By("rejecting a patch that adds a case-variant attempt field")
				code, body = send(target(), http.MethodPatch, "/devices/"+deviceName+"/status", `[{"op": "add", "path": "/status/firmware/attempt/Id", "value": "attempt-2"}]`)
				Expect(code).To(Equal(http.StatusBadRequest), body)
				Expect(body).To(ContainSubstring(`property \"Id\" is unsupported`))
				Expect(stored().Status.Firmware.Attempt.Id).To(Equal("attempt-1"))

				By("accepting progress of the same attempt")
				code, body = send(target(), http.MethodPatch, "/devices/"+deviceName+"/status", `[{"op": "replace", "path": "/status/firmware/attempt/phase", "value": "Verifying"}]`)
				Expect(code).To(Equal(http.StatusOK), body)
				Expect(stored().Status.Firmware.Attempt.Phase).To(Equal(api.FirmwareAttemptPhaseVerifying))
				Expect(evidence.releaseLookups.Load()).To(BeZero())
			})
		}

		It("When a resource patch tries to change the firmware status it should reject it", func() {
			storeFirmwareStatus(firmwareStatusOf(firmwareStatusDocument("attempt-1", api.FirmwareAttemptPhaseStaging, observationSchemaDigest)))
			before := stored()

			code, body := send(userServer, http.MethodPatch, "/devices/"+deviceName, `[{"op": "remove", "path": "/status/firmware/attempt"}]`)

			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("status is immutable"))
			Expect(stored().Status).To(Equal(before.Status))
		})
	})
})

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	Expect(err).ToNot(HaveOccurred())
	return string(raw)
}
