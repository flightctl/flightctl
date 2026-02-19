package service_test

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("EnrollmentRequest default alias", func() {
	var suite *ServiceTestSuite

	// handlerWithDefaultAliasKeys creates a handler with defaultAliasKeys set (e.g. []string{"hostname"}).
	// Must be called after suite.Setup() so Store, kvStore, caClient, etc. are initialized.
	handlerWithDefaultAliasKeys := func(keys []string) {
		suite.Handler = service.NewServiceHandler(
			suite.Store, suite.workerClient, suite.kvStore, suite.caClient, suite.Log,
			"", "", []string{}, keys)
	}

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	Context("when defaultAliasKeys is configured", func() {
		BeforeEach(func() {
			handlerWithDefaultAliasKeys([]string{"hostname", "architecture"})
		})

		It("sets default-alias annotation on create when DeviceStatus.SystemInfo is present", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Spec.DeviceStatus = &api.DeviceStatus{}
			er.Spec.DeviceStatus.SystemInfo.Set("hostname", "my-edge-device")

			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(created).ToNot(BeNil())
			Expect(created.Metadata.Annotations).ToNot(BeNil())
			Expect(*created.Metadata.Annotations).To(HaveKeyWithValue(domain.EnrollmentRequestAnnotationDefaultAlias, "my-edge-device"))

			retrieved, status := suite.Handler.GetEnrollmentRequest(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(*retrieved.Metadata.Annotations).To(HaveKeyWithValue(domain.EnrollmentRequestAnnotationDefaultAlias, "my-edge-device"))
		})

		It("updates default-alias annotation on replace when DeviceStatus.SystemInfo changes", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Spec.DeviceStatus = &api.DeviceStatus{}
			er.Spec.DeviceStatus.SystemInfo.Set("hostname", "original-host")

			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			Expect(*created.Metadata.Annotations).To(HaveKeyWithValue(domain.EnrollmentRequestAnnotationDefaultAlias, "original-host"))

			replacement := *created
			replacement.Spec.DeviceStatus.SystemInfo.Set("hostname", "updated-host")
			replaced, status := suite.Handler.ReplaceEnrollmentRequest(suite.Ctx, suite.OrgID, erName, replacement)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(*replaced.Metadata.Annotations).To(HaveKeyWithValue(domain.EnrollmentRequestAnnotationDefaultAlias, "updated-host"))
		})

		It("applies default-alias annotation to device alias on approval when alias not in approval labels", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Spec.DeviceStatus = &api.DeviceStatus{}
			er.Spec.DeviceStatus.SystemInfo.Set("hostname", "approved-device-alias")

			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			_, status := suite.Handler.CreateEnrollmentRequest(internalCtx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"env": "test"},
			}

			_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			device, status := suite.Handler.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(device.Metadata.Labels).ToNot(BeNil())
			Expect(*device.Metadata.Labels).To(HaveKeyWithValue("alias", "approved-device-alias"))
			Expect(*device.Metadata.Labels).To(HaveKeyWithValue("env", "test"))
		})

		It("keeps approval alias when alias is set in approval labels", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Spec.DeviceStatus = &api.DeviceStatus{}
			er.Spec.DeviceStatus.SystemInfo.Set("hostname", "from-systeminfo")

			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			_, status := suite.Handler.CreateEnrollmentRequest(internalCtx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))

			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"alias": "explicit-alias", "env": "test"},
			}

			_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			device, status := suite.Handler.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(*device.Metadata.Labels).To(HaveKeyWithValue("alias", "explicit-alias"))
		})
	})

	Context("when defaultAliasKeys is nil/empty", func() {
		It("does not set default-alias annotation and device has no alias when not in approval labels", func() {
			er := CreateTestER()
			erName := lo.FromPtr(er.Metadata.Name)
			er.Spec.DeviceStatus = &api.DeviceStatus{}
			er.Spec.DeviceStatus.SystemInfo.Set("hostname", "no-default-config")

			internalCtx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, true)
			created, status := suite.Handler.CreateEnrollmentRequest(internalCtx, suite.OrgID, er)
			Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
			if created.Metadata.Annotations != nil {
				Expect(*created.Metadata.Annotations).ToNot(HaveKey(domain.EnrollmentRequestAnnotationDefaultAlias))
			}

			defaultOrg := &model.Organization{
				ID:          org.DefaultID,
				ExternalID:  org.DefaultID.String(),
				DisplayName: org.DefaultID.String(),
			}
			mappedIdentity := identity.NewMappedIdentity("testuser", "", []*model.Organization{defaultOrg}, map[string][]string{}, false, nil)
			ctxApproval := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, mappedIdentity)
			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"env": "test"},
			}

			_, st := suite.Handler.ApproveEnrollmentRequest(ctxApproval, suite.OrgID, erName, approval)
			Expect(st.Code).To(BeEquivalentTo(http.StatusOK))

			device, status := suite.Handler.GetDevice(suite.Ctx, suite.OrgID, erName)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			if device.Metadata.Labels != nil {
				Expect(*device.Metadata.Labels).ToNot(HaveKey("alias"))
			}
		})
	})
})
