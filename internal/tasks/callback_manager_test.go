package tasks

import (
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type MockPublisher struct {
	publishCallCount int
}

func (m *MockPublisher) Publish(payload []byte) error {
	m.publishCallCount++
	return nil
}

func (m *MockPublisher) Close() {
}

var (
	mockPublisher = &MockPublisher{}
	logger        = flightlog.InitLogs()
)

var _ = Describe("FleetUpdatedCallback", func() {
	BeforeEach(func() {
		mockPublisher.publishCallCount = 0
	})

	When("both before and after are nil", func() {
		It("does nothing", func() {
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.FleetUpdatedCallback(nil, nil)
			Expect(mockPublisher.publishCallCount).To(Equal(0))
		})
	})

	When("before is nil and after is not nil", func() {
		It("submits FleetValidateTask and FleetSelectorMatchTask", func() {
			after := createTestFleet("after", `json:"spec"`, "selector")
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.FleetUpdatedCallback(nil, after)
			Expect(mockPublisher.publishCallCount).To(Equal(2))
		})
	})

	When("before is not nil and after is nil", func() {
		It("submits FleetSelectorMatchTask", func() {
			before := createTestFleet("before", `json:"spec"`, "selector")
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.FleetUpdatedCallback(before, nil)
			Expect(mockPublisher.publishCallCount).To(Equal(1))
		})
	})

	When("template is updated", func() {
		It("submits FleetValidateTask", func() {
			before := createTestFleet("before", "spec", "selector1")
			after := createTestFleet("after", "image", "selector2")
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.FleetUpdatedCallback(before, after)
			Expect(mockPublisher.publishCallCount).To(Equal(2))
		})
	})

	When("selector is updated", func() {
		It("submits FleetSelectorMatchTask", func() {
			before := createTestFleet("before", "spec", "selector1")
			after := createTestFleet("after", "spec", "selector2")
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.FleetUpdatedCallback(before, after)
			Expect(mockPublisher.publishCallCount).To(Equal(1))
		})
	})
})

var _ = Describe("DeviceUpdatedCallback", func() {
	BeforeEach(func() {
		mockPublisher.publishCallCount = 0
	})

	When("both before and after are nil", func() {
		It("does nothing", func() {
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.DeviceUpdatedCallback(nil, nil)
			Expect(mockPublisher.publishCallCount).To(Equal(0))
		})
	})

	When("before is nil and after is not nil", func() {
		It("submits FleetRolloutTask and DeviceRenderTask", func() {
			after := createTestDevice("after", "label1", "spec1")
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.DeviceUpdatedCallback(nil, after)
			Expect(mockPublisher.publishCallCount).To(Equal(3))
		})
	})

	When("before is not nil and after is nil", func() {
		It("submits FleetSelectorMatchTask", func() {
			before := createTestDevice("before", "label1", "spec1")
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.DeviceUpdatedCallback(before, nil)
			Expect(mockPublisher.publishCallCount).To(Equal(2))
		})
	})

	When("labels are updated", func() {
		It("submits FleetSelectorMatchTask", func() {
			before := createTestDevice("before", "label1", "spec1")
			after := createTestDevice("after", "label2", "spec2")
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.DeviceUpdatedCallback(before, after)
			Expect(mockPublisher.publishCallCount).To(Equal(3))
		})
	})

	When("spec is updated", func() {
		It("submits DeviceRenderTask", func() {
			before := createTestDevice("before", "label1", "spec1")
			after := createTestDevice("after", "label2", "spec2")
			callbackManager := NewCallbackManager(mockPublisher, logger)
			callbackManager.DeviceUpdatedCallback(before, after)
			Expect(mockPublisher.publishCallCount).To(Equal(3))
		})
	})
})

func createTestFleet(name string, templateSpec string, selector string) *model.Fleet {
	resource := api.Fleet{
		ApiVersion: "v1",
		Kind:       "Fleet",
		Metadata: api.ObjectMeta{
			Name:   util.StrToPtr(name),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: api.FleetSpec{
			Selector: &api.LabelSelector{
				MatchLabels: map[string]string{"selector": selector},
			},
			Template: struct {
				Metadata *api.ObjectMeta `json:"metadata,omitempty"`
				Spec     api.DeviceSpec  `json:"spec"`
			}{
				Spec: api.DeviceSpec{
					Os: &api.DeviceOSSpec{
						Image: templateSpec,
					},
				},
			},
		},
		Status: &api.FleetStatus{
			Conditions: []api.Condition{
				{
					Type:   "Approved",
					Status: "True",
				},
			},
		},
	}

	fleet, err := model.NewFleetFromApiResource(&resource)
	Expect(err).ToNot(HaveOccurred())

	return fleet
}

func createTestDevice(name string, labelValue string, spec string) *model.Device {
	status := api.NewDeviceStatus()

	resource := api.Device{
		ApiVersion: "v1",
		Kind:       "Device",
		Metadata: api.ObjectMeta{
			Name:   util.StrToPtr(name),
			Labels: &map[string]string{"labelKey": labelValue},
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOSSpec{Image: spec},
		},
		Status: &status,
	}

	device, err := model.NewDeviceFromApiResource(&resource)
	Expect(err).ToNot(HaveOccurred())

	return device
}
