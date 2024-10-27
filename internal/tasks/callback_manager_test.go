package tasks

import (
	"encoding/json"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type MockPublisher struct {
	publishedResources []ResourceReference
}

func (m *MockPublisher) Publish(payload []byte) error {
	var resource ResourceReference
	err := json.Unmarshal(payload, &resource)
	if err != nil {
		return err
	}
	m.publishedResources = append(m.publishedResources, resource)
	return nil
}

func (m *MockPublisher) Close() {
	clear(m.publishedResources)
}

var (
	mockPublisher    *MockPublisher
	callbacksManager CallbackManager
	orgId            uuid.UUID
)

var _ = Describe("FleetUpdatedCallback", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	When("both before and after are nil", func() {
		It("does nothing", func() {
			callbacksManager.FleetUpdatedCallback(nil, nil)
			Expect(mockPublisher.publishedResources).To(BeEmpty())
		})
	})

	When("before is nil and after is not nil", func() {
		It("submits FleetValidateTask and FleetSelectorMatchTask", func() {
			after := CreateTestingFleet(orgId, "after", "image1", &map[string]string{"labelKey": "selector"})
			callbacksManager.FleetUpdatedCallback(nil, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(2))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetValidateTask))
			Expect(publishedResource.Op).To(Equal(FleetValidateOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))
		})
	})

	When("before is not nil and after is nil", func() {
		It("submits FleetSelectorMatchTask", func() {
			before := CreateTestingFleet(orgId, "before", "image1", &map[string]string{"labelKey": "selector"})
			callbacksManager.FleetUpdatedCallback(before, nil)

			Expect(mockPublisher.publishedResources).To(HaveLen(1))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetValidateOpUpdate))
		})
	})

	When("template is updated", func() {
		It("submits FleetValidateTask and FleetSelectorMatchTask", func() {
			before := CreateTestingFleet(orgId, "before", "image1", &map[string]string{"labelKey": "selector1"})
			after := CreateTestingFleet(orgId, "after", "image2", &map[string]string{"labelKey": "selector2"})
			callbacksManager.FleetUpdatedCallback(before, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(2))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetValidateTask))
			Expect(publishedResource.Op).To(Equal(FleetValidateOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))
		})
	})

	When("selector is updated", func() {
		It("submits FleetSelectorMatchTask", func() {
			before := CreateTestingFleet(orgId, "before", "image1", &map[string]string{"labelKey": "selector1"})
			after := CreateTestingFleet(orgId, "after", "image1", &map[string]string{"labelKey": "selector2"})
			callbacksManager.FleetUpdatedCallback(before, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(1))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))
		})
	})
})

var _ = Describe("DeviceUpdatedCallback", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	When("both before and after are nil", func() {
		It("does nothing", func() {
			callbacksManager.DeviceUpdatedCallback(nil, nil)
			Expect(mockPublisher.publishedResources).To(BeEmpty())
		})
	})

	When("before is nil and after is not nil", func() {
		It("submits FleetRolloutTask, FleetSelectorMatchTask and DeviceRenderTask", func() {
			after := CreateTestingDevice(orgId, "after", &map[string]string{"labelKey": "label1"}, "os1")
			callbacksManager.DeviceUpdatedCallback(nil, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(3))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
			Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))

			publishedResource = mockPublisher.publishedResources[2]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(DeviceRenderTask))
			Expect(publishedResource.Op).To(Equal(DeviceRenderOpUpdate))
		})
	})

	When("before is not nil and after is nil", func() {
		It("submits FleetRolloutTask and FleetSelectorMatchTask", func() {
			before := CreateTestingDevice(orgId, "before", &map[string]string{"labelKey": "label1"}, "os1")
			callbacksManager.DeviceUpdatedCallback(before, nil)

			Expect(mockPublisher.publishedResources).To(HaveLen(2))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
			Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))
		})
	})

	When("labels are updated", func() {
		It("submits FleetRolloutTask, FleetSelectorMatchTask and DeviceRenderTask", func() {
			before := CreateTestingDevice(orgId, "before", &map[string]string{"labelKey": "label1"}, "os1")
			after := CreateTestingDevice(orgId, "after", &map[string]string{"labelKey": "label2"}, "os2")
			callbacksManager.DeviceUpdatedCallback(before, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(3))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
			Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))

			publishedResource = mockPublisher.publishedResources[2]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(DeviceRenderTask))
			Expect(publishedResource.Op).To(Equal(DeviceRenderOpUpdate))
		})
	})

	When("spec is updated", func() {
		It("submits FleetRolloutTask, FleetSelectorMatchTask and DeviceRenderTask", func() {
			before := CreateTestingDevice(orgId, "before", &map[string]string{"labelKey": "label1"}, "os1")
			after := CreateTestingDevice(orgId, "after", &map[string]string{"labelKey": "label2"}, "os2")
			callbacksManager.DeviceUpdatedCallback(before, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(3))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
			Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))

			publishedResource = mockPublisher.publishedResources[2]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(DeviceRenderTask))
			Expect(publishedResource.Op).To(Equal(DeviceRenderOpUpdate))
		})
	})
})

var _ = Describe("FleetSourceUpdated", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	It("submits FleetValidateTask", func() {
		callbacksManager.FleetSourceUpdated(orgId, "name")

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(model.FleetKind))
		Expect(publishedResource.TaskName).To(Equal(FleetValidateTask))
		Expect(publishedResource.Op).To(Equal(FleetValidateOpUpdate))
	})
})

var _ = Describe("DeviceSourceUpdated", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	It("submits DeviceRenderTask", func() {
		callbacksManager.DeviceSourceUpdated(orgId, "name")

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
		Expect(publishedResource.TaskName).To(Equal(DeviceRenderTask))
		Expect(publishedResource.Op).To(Equal(DeviceRenderOpUpdate))
	})
})

var _ = Describe("RepositoryUpdatedCallback", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	It("submits RepositoryUpdatesTask", func() {
		repository := CreateTestingRepository(orgId, "name", "url")
		callbacksManager.RepositoryUpdatedCallback(repository)

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(model.RepositoryKind))
		Expect(publishedResource.TaskName).To(Equal(RepositoryUpdatesTask))
		Expect(publishedResource.Op).To(Equal(RepositoryUpdateOpUpdate))
	})
})

var _ = Describe("AllRepositoriesDeletedCallback", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	It("submits RepositoryUpdatesTask", func() {
		callbacksManager.AllRepositoriesDeletedCallback(orgId)

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(model.RepositoryKind))
		Expect(publishedResource.TaskName).To(Equal(RepositoryUpdatesTask))
		Expect(publishedResource.Op).To(Equal(RepositoryUpdateOpDeleteAll))
	})
})

var _ = Describe("AllFleetsDeletedCallback", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	It("submits FleetSelectorMatchTask", func() {
		callbacksManager.AllFleetsDeletedCallback(orgId)

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(model.FleetKind))
		Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
		Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpDeleteAll))
	})
})

var _ = Describe("AllDevicesDeletedCallback", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	It("submits FleetSelectorMatchTask", func() {
		callbacksManager.AllDevicesDeletedCallback(orgId)

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(model.DeviceKind))
		Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
		Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpDeleteAll))
	})
})

var _ = Describe("TemplateVersionCreatedCallback", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	It("submits TemplateVersionPopulateTask", func() {
		templateVersion := CreateTestingTemplateVersion(orgId, "name", "template")
		callbacksManager.TemplateVersionCreatedCallback(templateVersion)

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(model.TemplateVersionKind))
		Expect(publishedResource.TaskName).To(Equal(TemplateVersionPopulateTask))
		Expect(publishedResource.Op).To(Equal(TemplateVersionPopulateOpCreated))
	})
})

var _ = Describe("TemplateVersionValidatedCallback", func() {
	BeforeEach(func() {
		mockPublisher = &MockPublisher{}
		callbacksManager = NewCallbackManager(mockPublisher, flightlog.InitLogs())
		orgId = uuid.New()
	})

	It("submits FleetRolloutTask", func() {
		templateVersion := CreateTestingTemplateVersion(orgId, "name", "template")
		callbacksManager.TemplateVersionValidatedCallback(templateVersion)

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(model.FleetKind))
		Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
		Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))
	})
})

func CreateTestingFleet(orgId uuid.UUID, name string, templateImage string, selector *map[string]string) *model.Fleet {
	resource := api.Fleet{
		ApiVersion: "v1",
		Kind:       model.FleetKind,
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: selector,
		},
		Spec: api.FleetSpec{
			Selector: &api.LabelSelector{
				MatchLabels: selector,
			},
			Template: struct {
				Metadata *api.ObjectMeta `json:"metadata,omitempty"`
				Spec     api.DeviceSpec  `json:"spec"`
			}{
				Spec: api.DeviceSpec{
					Os: &api.DeviceOSSpec{
						Image: templateImage,
					},
				},
			},
		},
		Status: nil,
	}

	fleet, err := model.NewFleetFromApiResource(&resource)
	Expect(err).ToNot(HaveOccurred())

	fleet.OrgID = orgId

	return fleet
}

func CreateTestingDevice(orgId uuid.UUID, name string, labels *map[string]string, spec string) *model.Device {
	resource := api.Device{
		ApiVersion: "v1",
		Kind:       model.DeviceKind,
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: labels,
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOSSpec{Image: spec},
		},
		Status: nil,
	}

	device, err := model.NewDeviceFromApiResource(&resource)
	Expect(err).ToNot(HaveOccurred())

	device.OrgID = orgId

	return device
}

func CreateTestingTemplateVersion(orgId uuid.UUID, name string, template string) *model.TemplateVersion {
	resource := api.TemplateVersion{
		ApiVersion: "v1",
		Kind:       model.TemplateVersionKind,
		Metadata: api.ObjectMeta{
			Name: &name,
		},
		Spec: api.TemplateVersionSpec{
			Fleet: template,
		},
		Status: nil,
	}

	templateVersion, err := model.NewTemplateVersionFromApiResource(&resource)
	Expect(err).ToNot(HaveOccurred())

	templateVersion.OrgID = orgId

	return templateVersion
}

func CreateTestingRepository(orgId uuid.UUID, name string, url string) *model.Repository {
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url: url,
	})
	Expect(err).ToNot(HaveOccurred())

	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name: &name,
		},
		Spec:   spec,
		Status: nil,
	}

	repository, err := model.NewRepositoryFromApiResource(&resource)
	Expect(err).ToNot(HaveOccurred())

	repository.OrgID = orgId

	return repository
}
