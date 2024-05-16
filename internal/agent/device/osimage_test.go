package device_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/bootimage"
	"github.com/flightctl/flightctl/pkg/executer"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Device Suite")
}

var _ = Describe("Calling osimages Sync", func() {
	var (
		ctx           context.Context
		ctrl          *gomock.Controller
		execMock      *executer.MockExecuter
		statusManager *status.MockManager
		imageManager  *bootimage.MockManager
		log           *flightlog.PrefixLogger
		controller    *device.OSImageController
	)

	BeforeEach(func() {
		ctx = context.Background()
		log = flightlog.NewPrefixLogger("")
		ctrl = gomock.NewController(GinkgoT())
		execMock = executer.NewMockExecuter(ctrl)
		statusManager = status.NewMockManager(ctrl)
		imageManager = bootimage.NewMockManager(ctrl)
		controller = device.NewOSImageController(execMock, statusManager, imageManager, log)
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	Context("When the desired spec has no OS defined", func() {
		It("should return with no action", func() {
			desired := v1alpha1.RenderedDeviceSpec{}
			err := controller.Sync(ctx, &desired)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When we fail to get the bootc status", func() {
		It("should return the error and set a condition", func() {
			imageManager.EXPECT().IsDisabled().Return(false)
			imageManager.EXPECT().Status(gomock.Any()).Return(nil, fmt.Errorf("get status: status error"))
			statusManager.EXPECT().UpdateConditionError(gomock.Any(), device.OsImageDegradedReason, fmt.Errorf("get status: status error"))
			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "image"}}
			err := controller.Sync(ctx, &desired)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When the image is already reconciled", func() {
		It("should return with no action", func() {
			imageManager.EXPECT().IsDisabled().Return(false)
			imageManager.EXPECT().Status(gomock.Any()).Return(util.CreateTestImageManagerBootedStatus("myimage"), nil)
			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "myimage"}}
			err := controller.Sync(ctx, &desired)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When the image manager is disabled", func() {
		It("should return with no action", func() {
			imageManager.EXPECT().IsDisabled().Return(true)
			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "myimage"}}
			err := controller.Sync(ctx, &desired)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When we fail to switch images", func() {
		It("should return the error and set a condition", func() {
			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "mynewimage"}}
			imageManager.EXPECT().IsDisabled().Return(false)
			imageManager.EXPECT().Status(gomock.Any()).Return(util.CreateTestImageManagerBootedStatus("myimage"), nil)
			imageManager.EXPECT().Switch(gomock.Any(), "mynewimage").Return(fmt.Errorf("switch error"))
			statusManager.EXPECT().UpdateConditionError(gomock.Any(), device.OsImageDegradedReason, fmt.Errorf("switch error"))

			err := controller.Sync(ctx, &desired)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When we fail to apply the image", func() {
		It("should return the error and set a condition", func() {
			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "mynewimage"}}
			imageManager.EXPECT().IsDisabled().Return(false)
			imageManager.EXPECT().Status(gomock.Any()).Return(util.CreateTestImageManagerBootedStatus("myoldimage"), nil)
			imageManager.EXPECT().Switch(gomock.Any(), "mynewimage").Return(nil)
			imageManager.EXPECT().Apply(gomock.Any()).Return(fmt.Errorf("apply failed"))
			statusManager.EXPECT().UpdateConditionError(gomock.Any(), device.OsImageDegradedReason, fmt.Errorf("apply failed"))
			statusManager.EXPECT().UpdateCondition(gomock.Any(), v1alpha1.DeviceProgressing, v1alpha1.ConditionStatusTrue, gomock.Any(), gomock.Any())

			err := controller.Sync(ctx, &desired)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When we successfully apply the image", func() {
		It("should return the error and set a condition", func() {
			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "mynewimage"}}
			imageManager.EXPECT().IsDisabled().Return(false)
			imageManager.EXPECT().Status(gomock.Any()).Return(util.CreateTestImageManagerBootedStatus("myoldimage"), nil)
			imageManager.EXPECT().Switch(gomock.Any(), "mynewimage").Return(nil)
			imageManager.EXPECT().Apply(gomock.Any()).Return(nil)
			statusManager.EXPECT().UpdateCondition(gomock.Any(), v1alpha1.DeviceProgressing, v1alpha1.ConditionStatusTrue, gomock.Any(), gomock.Any())

			err := controller.Sync(ctx, &desired)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
