package tasks

import (
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
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

func (m *MockPublisher) ResetPublishCallCount() {
	m.publishCallCount = 0
}

var (
	mockPublisher = &MockPublisher{}
	logger        = flightlog.InitLogs()
)

var _ = Describe("FleetUpdatedCallback", func() {
	BeforeEach(func() {
		mockPublisher.ResetPublishCallCount()
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
		mockPublisher.ResetPublishCallCount()
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

var _ = Describe("FleetSourceUpdated", func() {
	BeforeEach(func() {
		mockPublisher.ResetPublishCallCount()
	})

	It("submits FleetValidateTask", func() {
		callbackManager := NewCallbackManager(mockPublisher, logger)
		callbackManager.FleetSourceUpdated(uuid.New(), "name")
		Expect(mockPublisher.publishCallCount).To(Equal(1))
	})
})

var _ = Describe("DeviceSourceUpdated", func() {
	BeforeEach(func() {
		mockPublisher.ResetPublishCallCount()
	})

	It("submits FleetValidateTask", func() {
		callbackManager := NewCallbackManager(mockPublisher, logger)
		callbackManager.DeviceSourceUpdated(uuid.New(), "name")
		Expect(mockPublisher.publishCallCount).To(Equal(1))
	})
})

var _ = Describe("RepositoryUpdatedCallback", func() {
	BeforeEach(func() {
		mockPublisher.ResetPublishCallCount()
	})

	It("submits RepositoryUpdatesTask", func() {
		repository := createTestRepository("name", "url")
		callbackManager := NewCallbackManager(mockPublisher, logger)
		callbackManager.RepositoryUpdatedCallback(repository)
		Expect(mockPublisher.publishCallCount).To(Equal(1))
	})
})

var _ = Describe("AllRepositoriesDeletedCallback", func() {
	BeforeEach(func() {
		mockPublisher.ResetPublishCallCount()
	})

	It("submits RepositoryUpdatesTask", func() {
		callbackManager := NewCallbackManager(mockPublisher, logger)
		callbackManager.AllRepositoriesDeletedCallback(uuid.New())
		Expect(mockPublisher.publishCallCount).To(Equal(1))
	})
})

var _ = Describe("AllFleetsDeletedCallback", func() {
	BeforeEach(func() {
		mockPublisher.ResetPublishCallCount()
	})

	It("submits FleetSelectorMatchTask", func() {
		callbackManager := NewCallbackManager(mockPublisher, logger)
		callbackManager.AllFleetsDeletedCallback(uuid.New())
		Expect(mockPublisher.publishCallCount).To(Equal(1))
	})
})

var _ = Describe("AllDevicesDeletedCallback", func() {
	BeforeEach(func() {
		mockPublisher.ResetPublishCallCount()
	})

	It("submits FleetSelectorMatchTask", func() {
		callbackManager := NewCallbackManager(mockPublisher, logger)
		callbackManager.AllDevicesDeletedCallback(uuid.New())
		Expect(mockPublisher.publishCallCount).To(Equal(1))
	})
})

var _ = Describe("TemplateVersionCreatedCallback", func() {
	BeforeEach(func() {
		mockPublisher.ResetPublishCallCount()
	})

	It("submits FleetSelectorMatchTask", func() {
		templateVersion := createTestTemplateVersion("name", "template")
		callbackManager := NewCallbackManager(mockPublisher, logger)
		callbackManager.TemplateVersionCreatedCallback(templateVersion)
		Expect(mockPublisher.publishCallCount).To(Equal(1))
	})
})

var _ = Describe("TemplateVersionValidatedCallback", func() {
	BeforeEach(func() {
		mockPublisher.ResetPublishCallCount()
	})

	It("submits FleetRolloutTask", func() {
		templateVersion := createTestTemplateVersion("name", "template")
		callbackManager := NewCallbackManager(mockPublisher, logger)
		callbackManager.TemplateVersionValidatedCallback(templateVersion)
		Expect(mockPublisher.publishCallCount).To(Equal(1))
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

func createTestRepository(name string, url string) *model.Repository {
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  url,
		Type: "git",
	})
	Expect(err).ToNot(HaveOccurred())
	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name: util.StrToPtr(name),
		},
		Spec:   spec,
		Status: nil,
	}

	repository, err := model.NewRepositoryFromApiResource(&resource)
	Expect(err).ToNot(HaveOccurred())

	return repository
}

func createTestTemplateVersion(name string, template string) *model.TemplateVersion {
	resource := api.TemplateVersion{
		ApiVersion: "v1",
		Kind:       "TemplateVersion",
		Metadata: api.ObjectMeta{
			Name: util.StrToPtr(name),
		},
		Spec: api.TemplateVersionSpec{
			Fleet: template,
		},
	}

	templateVersion, err := model.NewTemplateVersionFromApiResource(&resource)
	Expect(err).ToNot(HaveOccurred())

	return templateVersion
}
