package device_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	flightlog "github.com/flightctl/flightctl/pkg/log"
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
		mockBootc     *client.MockBootc
		statusManager *status.MockManager
		specManager   *spec.MockManager
		log           *flightlog.PrefixLogger
		controller    *device.OSImageController
	)

	BeforeEach(func() {
		ctx = context.Background()
		log = flightlog.NewPrefixLogger("")
		ctrl = gomock.NewController(GinkgoT())
		statusManager = status.NewMockManager(ctrl)
		specManager = spec.NewMockManager(ctrl)
		mockBootc = client.NewMockBootc(ctrl)
		controller = device.NewOSImageController(mockBootc, statusManager, specManager, log)
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
			mockBootc.EXPECT().Status(ctx).Return(nil, fmt.Errorf("status error"))
			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "image"}}
			err := controller.Sync(ctx, &desired)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When the image is already reconciled", func() {
		It("should return with no action", func() {
			host := container.BootcHost{
				Status: container.Status{
					Booted: container.ImageStatus{
						Image: container.ImageDetails{
							Image: container.ImageSpec{
								Image: "myimage",
							},
						},
					},
				},
			}
			mockBootc.EXPECT().Status(ctx).Return(&host, nil)
			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "myimage"}}
			err := controller.Sync(ctx, &desired)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When we fail to switch images", func() {
		It("should return the error", func() {
			host := container.BootcHost{
				Status: container.Status{
					Booted: container.ImageStatus{
						Image: container.ImageDetails{
							Image: container.ImageSpec{
								Image: "myoldimage",
							},
						},
					},
				},
			}

			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "mynewimage"}}
			mockBootc.EXPECT().Status(ctx).Return(&host, nil)
			mockBootc.EXPECT().Switch(gomock.Any(), "mynewimage").Return(fmt.Errorf("switch error"))

			err := controller.Sync(ctx, &desired)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When we fail to apply the image", func() {
		It("should return the error and set a condition", func() {
			host := container.BootcHost{
				Status: container.Status{
					Booted: container.ImageStatus{
						Image: container.ImageDetails{
							Image: container.ImageSpec{
								Image: "myoldimage",
							},
						},
					},
				},
			}

			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "mynewimage"}}
			mockBootc.EXPECT().Status(ctx).Return(&host, nil)
			mockBootc.EXPECT().Switch(gomock.Any(), "mynewimage").Return(nil)
			mockBootc.EXPECT().Apply(gomock.Any()).Return(fmt.Errorf("apply error"))
			specManager.EXPECT().PrepareRollback(gomock.Any()).Return(nil)

			statusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil).Times(1)
			statusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil).Times(1)

			err := controller.Sync(ctx, &desired)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When we successfully apply the image", func() {
		It("should return the error and set a condition", func() {
			host := container.BootcHost{
				Status: container.Status{
					Booted: container.ImageStatus{
						Image: container.ImageDetails{
							Image: container.ImageSpec{
								Image: "myoldimage",
							},
						},
					},
				},
			}

			desired := v1alpha1.RenderedDeviceSpec{Os: &v1alpha1.DeviceOSSpec{Image: "mynewimage"}}
			mockBootc.EXPECT().Status(ctx).Return(&host, nil)
			mockBootc.EXPECT().Switch(gomock.Any(), "mynewimage").Return(nil)
			mockBootc.EXPECT().Apply(gomock.Any()).Return(nil)
			summaryStatus := v1alpha1.DeviceSummaryStatusRebooting
			infoMsg := fmt.Sprintf("Device is rebooting into os image: %s", "mynewimage")
			statusManager.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, fn status.UpdateStatusFn) (*v1alpha1.DeviceStatus, error) {
					status := v1alpha1.NewDeviceStatus()
					err := fn(&status)
					Expect(err).To(BeNil())
					Expect(status.Summary.Status).To(Equal(summaryStatus))
					Expect(status.Summary.Info).To(Equal(&infoMsg))
					return &status, nil
				},
			).Times(1)
			statusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil).Times(1)
			specManager.EXPECT().PrepareRollback(gomock.Any()).Return(nil)

			err := controller.Sync(ctx, &desired)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
