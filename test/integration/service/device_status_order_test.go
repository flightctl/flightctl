package service_test

import (
	"net/http"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const delayedStatusDeviceName = "delayed-status-report"

var _ = Describe("Device status ordering", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	It("rejects older status PUT and PATCH without changing a terminal result or its events", func() {
		created, result := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr(delayedStatusDeviceName)},
			Spec:     &api.DeviceSpec{},
		})
		Expect(result.Code).To(Equal(int32(http.StatusCreated)), result.Message)
		Expect(created.Metadata.ResourceVersion).NotTo(BeNil())

		terminal := domain.NewDeviceStatus()
		terminal.Conditions = []domain.Condition{{
			Type: domain.ConditionTypeDeviceUpdating, Status: domain.ConditionStatusFalse,
			Reason: string(domain.UpdateStateError), Message: "Update failed",
			LastTransitionTime: time.Now().UTC(),
		}}
		accepted, result := suite.Device.ReplaceDeviceStatus(suite.Ctx, suite.OrgID, delayedStatusDeviceName, api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr(delayedStatusDeviceName), ResourceVersion: created.Metadata.ResourceVersion},
			Status:   &terminal,
		}, false)
		Expect(result.Code).To(Equal(int32(http.StatusOK)), result.Message)
		Expect(accepted.Metadata.ResourceVersion).NotTo(Equal(created.Metadata.ResourceVersion))

		eventsBefore, err := suite.EventStore.List(suite.Ctx, suite.OrgID, store.ListParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())

		stale := terminal
		stale.Conditions = []domain.Condition{{
			Type: domain.ConditionTypeDeviceUpdating, Status: domain.ConditionStatusTrue,
			Reason: string(domain.UpdateStatePreparing), Message: "Preparing",
			LastTransitionTime: time.Now().UTC(),
		}}
		_, result = suite.Device.ReplaceDeviceStatus(suite.Ctx, suite.OrgID, delayedStatusDeviceName, api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr(delayedStatusDeviceName), ResourceVersion: created.Metadata.ResourceVersion},
			Status:   &stale,
		}, false)
		Expect(result.Code).To(Equal(int32(http.StatusConflict)), result.Message)

		patch := domain.PatchRequest{
			{Op: domain.PatchOpTest, Path: "/metadata/resourceVersion", Value: *created.Metadata.ResourceVersion},
			{Op: domain.PatchOpReplace, Path: "/status/conditions", Value: stale.Conditions},
		}
		_, result = suite.Device.PatchDeviceStatus(suite.Ctx, suite.OrgID, delayedStatusDeviceName, patch, false)
		Expect(result.Code).To(Equal(int32(http.StatusConflict)), result.Message)

		stored, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, delayedStatusDeviceName)
		Expect(err).NotTo(HaveOccurred())
		condition := domain.FindStatusCondition(stored.Status.Conditions, domain.ConditionTypeDeviceUpdating)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Reason).To(Equal(string(domain.UpdateStateError)))

		eventsAfter, err := suite.EventStore.List(suite.Ctx, suite.OrgID, store.ListParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		Expect(eventsAfter.Items).To(HaveLen(len(eventsBefore.Items)))
	})
})
