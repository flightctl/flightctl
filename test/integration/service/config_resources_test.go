package service_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/api/server"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/store"
	transport "github.com/flightctl/flightctl/internal/transport/v1beta1"
	"github.com/go-chi/chi/v5"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Configuration resource authored API boundaries", func() {
	var suite *ServiceTestSuite
	var handler http.Handler
	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		spec, err := api.GetSpec()
		Expect(err).NotTo(HaveOccurred())
		router := chi.NewRouter()
		router.Use(oapimiddleware.OapiRequestValidatorWithOptions(spec, &oapimiddleware.Options{
			ErrorHandler:          apiserver.OapiErrorHandler,
			MultiErrorHandler:     apiserver.OapiMultiErrorHandler,
			SilenceServersWarning: true,
		}))
		routes := transport.NewTransportHandler(nil, nil, suite.Device, nil, nil, nil, nil, nil, suite.Fleet, nil, nil, nil, nil, convert.NewConverter(), nil, nil, nil, nil)
		handler = server.HandlerWithOptions(routes, server.ChiServerOptions{BaseRouter: router, BaseURL: "/api/v1"})
	})
	AfterEach(func() { suite.Teardown() })

	It("When authored fields are submitted it should reject every mutation route without changing stored state or events", func() {
		name := "config-contract"
		_, status := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, api.Device{Metadata: api.ObjectMeta{Name: &name}, Spec: &api.DeviceSpec{}})
		Expect(status.Code).To(Equal(int32(http.StatusCreated)))
		_, status = suite.Fleet.CreateFleet(suite.Ctx, suite.OrgID, api.Fleet{Metadata: api.ObjectMeta{Name: &name}})
		Expect(status.Code).To(Equal(int32(http.StatusCreated)))
		beforeDevice, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, name)
		Expect(err).NotTo(HaveOccurred())
		beforeFleet, status := suite.Fleet.GetFleet(suite.Ctx, suite.OrgID, name, api.GetFleetParams{})
		Expect(status.Code).To(Equal(int32(http.StatusOK)))
		beforeEvents, err := suite.EventStore.List(suite.Ctx, suite.OrgID, store.ListParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())

		for _, resource := range []string{"devices", "fleets"} {
			root := "/spec"
			kind := "Device"
			if resource == "fleets" {
				root = "/spec/template/spec"
				kind = "Fleet"
			}
			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
				for _, value := range []any{nil, []any{}} {
					spec := map[string]any{"configResources": value}
					var payload any
					if method == http.MethodPatch {
						payload = []any{map[string]any{"op": "add", "path": root, "value": spec}}
					} else {
						authoredSpec := spec
						if resource == "fleets" {
							authoredSpec = map[string]any{"template": map[string]any{"spec": spec}}
						}
						payload = map[string]any{"apiVersion": "flightctl.io/v1beta1", "kind": kind, "metadata": map[string]any{"name": name}, "spec": authoredSpec}
					}
					data, err := json.Marshal(payload)
					Expect(err).NotTo(HaveOccurred())
					path := "/api/v1/" + resource
					if method != http.MethodPost {
						path += "/" + name
					}
					request := httptest.NewRequest(method, path, bytes.NewReader(data)).WithContext(suite.Ctx)
					request.Header.Set("Content-Type", "application/json")
					if method == http.MethodPatch {
						request.Header.Set("Content-Type", "application/json-patch+json")
					}
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					Expect(response.Code).To(Equal(http.StatusBadRequest), fmt.Sprintf("%s %s with configResources: %s", method, resource, response.Body.String()))
					Expect(response.Body.String()).To(ContainSubstring("configResources"))
				}
			}
		}
		afterDevice, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(afterDevice).To(Equal(beforeDevice))
		afterFleet, status := suite.Fleet.GetFleet(suite.Ctx, suite.OrgID, name, api.GetFleetParams{})
		Expect(status.Code).To(Equal(int32(http.StatusOK)))
		Expect(afterFleet).To(Equal(beforeFleet))
		afterEvents, err := suite.EventStore.List(suite.Ctx, suite.OrgID, store.ListParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		Expect(afterEvents.Items).To(HaveLen(len(beforeEvents.Items)))
	})

	It("When JSON syntax is invalid it should preserve bad request responses on every mutation route", func() {
		for _, resource := range []string{"devices", "fleets"} {
			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
				path := "/api/v1/" + resource
				if method != http.MethodPost {
					path += "/config-contract"
				}
				request := httptest.NewRequest(method, path, bytes.NewBufferString(`{"spec":`)).WithContext(suite.Ctx)
				request.Header.Set("Content-Type", "application/json")
				if method == http.MethodPatch {
					request.Header.Set("Content-Type", "application/json-patch+json")
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				Expect(response.Code).To(Equal(http.StatusBadRequest), method+" "+resource+": "+response.Body.String())
			}
		}
	})

	It("When ordinary configuration and unrelated Helm values are authored it should retain them", func() {
		name := "ordinary-config"
		payload := map[string]any{
			"apiVersion": "flightctl.io/v1beta1", "kind": "Device", "metadata": map[string]any{"name": name},
			"spec": map[string]any{"applications": []any{map[string]any{"appType": "helm", "name": "app", "image": "quay.io/example/chart:1.0.0", "values": map[string]any{"configResources": []any{}}}}},
		}
		data, err := json.Marshal(payload)
		Expect(err).NotTo(HaveOccurred())
		request := httptest.NewRequest(http.MethodPost, "/api/v1/devices", bytes.NewReader(data)).WithContext(suite.Ctx)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		Expect(response.Code).To(Equal(http.StatusCreated), response.Body.String())
		saved, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(saved.Spec.Applications)).To(HaveLen(1))
	})
})
