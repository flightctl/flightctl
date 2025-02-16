package tasks_client

import (
	"encoding/json"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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
			callbacksManager.FleetUpdatedCallback(orgId, nil, nil)
			Expect(mockPublisher.publishedResources).To(BeEmpty())
		})
	})

	When("before is nil and after is not nil", func() {
		It("submits FleetValidateTask and FleetSelectorMatchTask", func() {
			after := CreateTestingFleet("after", "image1", &map[string]string{"labelKey": "selector"})
			callbacksManager.FleetUpdatedCallback(orgId, nil, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(2))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetValidateTask))
			Expect(publishedResource.Op).To(Equal(FleetValidateOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))
		})
	})

	When("before is not nil and after is nil", func() {
		It("submits FleetSelectorMatchTask", func() {
			before := CreateTestingFleet("before", "image1", &map[string]string{"labelKey": "selector"})
			callbacksManager.FleetUpdatedCallback(orgId, before, nil)

			Expect(mockPublisher.publishedResources).To(HaveLen(1))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetValidateOpUpdate))
		})
	})

	When("template is updated", func() {
		It("submits FleetValidateTask and FleetSelectorMatchTask", func() {
			before := CreateTestingFleet("before", "image1", &map[string]string{"labelKey": "selector1"})
			after := CreateTestingFleet("after", "image2", &map[string]string{"labelKey": "selector2"})
			callbacksManager.FleetUpdatedCallback(orgId, before, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(2))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetValidateTask))
			Expect(publishedResource.Op).To(Equal(FleetValidateOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.FleetKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))
		})
	})

	When("selector is updated", func() {
		It("submits FleetSelectorMatchTask", func() {
			before := CreateTestingFleet("before", "image1", &map[string]string{"labelKey": "selector1"})
			after := CreateTestingFleet("after", "image1", &map[string]string{"labelKey": "selector2"})
			callbacksManager.FleetUpdatedCallback(orgId, before, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(1))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.FleetKind))
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
			callbacksManager.DeviceUpdatedCallback(orgId, nil, nil)
			Expect(mockPublisher.publishedResources).To(BeEmpty())
		})
	})

	When("before is nil and after is not nil", func() {
		It("submits FleetRolloutTask, FleetSelectorMatchTask and DeviceRenderTask", func() {
			after := CreateTestingDevice("after", &map[string]string{"labelKey": "label1"}, "os1")
			callbacksManager.DeviceUpdatedCallback(orgId, nil, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(3))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
			Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))

			publishedResource = mockPublisher.publishedResources[2]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(DeviceRenderTask))
			Expect(publishedResource.Op).To(Equal(DeviceRenderOpUpdate))
		})
	})

	When("before is not nil and after is nil", func() {
		It("submits FleetRolloutTask and FleetSelectorMatchTask", func() {
			before := CreateTestingDevice("before", &map[string]string{"labelKey": "label1"}, "os1")
			callbacksManager.DeviceUpdatedCallback(orgId, before, nil)

			Expect(mockPublisher.publishedResources).To(HaveLen(2))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
			Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))
		})
	})

	When("labels are updated", func() {
		It("submits FleetRolloutTask, FleetSelectorMatchTask and DeviceRenderTask", func() {
			before := CreateTestingDevice("before", &map[string]string{"labelKey": "label1"}, "os1")
			after := CreateTestingDevice("after", &map[string]string{"labelKey": "label2"}, "os2")
			callbacksManager.DeviceUpdatedCallback(orgId, before, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(3))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
			Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))

			publishedResource = mockPublisher.publishedResources[2]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(DeviceRenderTask))
			Expect(publishedResource.Op).To(Equal(DeviceRenderOpUpdate))
		})
	})

	When("spec is updated", func() {
		It("submits FleetRolloutTask, FleetSelectorMatchTask and DeviceRenderTask", func() {
			before := CreateTestingDevice("before", &map[string]string{"labelKey": "label1"}, "os1")
			after := CreateTestingDevice("after", &map[string]string{"labelKey": "label2"}, "os2")
			callbacksManager.DeviceUpdatedCallback(orgId, before, after)

			Expect(mockPublisher.publishedResources).To(HaveLen(3))

			publishedResource := mockPublisher.publishedResources[0]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
			Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))

			publishedResource = mockPublisher.publishedResources[1]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
			Expect(publishedResource.TaskName).To(Equal(FleetSelectorMatchTask))
			Expect(publishedResource.Op).To(Equal(FleetSelectorMatchOpUpdate))

			publishedResource = mockPublisher.publishedResources[2]
			Expect(publishedResource.OrgID).To(Equal(orgId))
			Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
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
		Expect(publishedResource.Kind).To(Equal(api.FleetKind))
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
		Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
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
		repository := CreateTestingRepository("name", "url")
		callbacksManager.RepositoryUpdatedCallback(orgId, nil, repository)

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(api.RepositoryKind))
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
		Expect(publishedResource.Kind).To(Equal(api.RepositoryKind))
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
		Expect(publishedResource.Kind).To(Equal(api.FleetKind))
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
		Expect(publishedResource.Kind).To(Equal(api.DeviceKind))
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

	It("submits FleetRolloutTask", func() {
		templateVersion := CreateTestingTemplateVersion("name", "template")
		callbacksManager.TemplateVersionCreatedCallback(orgId, nil, templateVersion)

		Expect(mockPublisher.publishedResources).To(HaveLen(1))

		publishedResource := mockPublisher.publishedResources[0]
		Expect(publishedResource.OrgID).To(Equal(orgId))
		Expect(publishedResource.Kind).To(Equal(api.FleetKind))
		Expect(publishedResource.TaskName).To(Equal(FleetRolloutTask))
		Expect(publishedResource.Op).To(Equal(FleetRolloutOpUpdate))
	})
})

func CreateTestingFleet(name string, templateImage string, selector *map[string]string) *api.Fleet {
	return &api.Fleet{
		ApiVersion: "v1",
		Kind:       api.FleetKind,
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
					Os: &api.DeviceOsSpec{
						Image: templateImage,
					},
				},
			},
		},
		Status: nil,
	}
}

func CreateTestingDevice(name string, labels *map[string]string, spec string) *api.Device {
	return &api.Device{
		ApiVersion: "v1",
		Kind:       api.DeviceKind,
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: labels,
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOsSpec{Image: spec},
		},
		Status: nil,
	}
}

func CreateTestingTemplateVersion(name string, template string) *api.TemplateVersion {
	return &api.TemplateVersion{
		ApiVersion: "v1",
		Kind:       api.TemplateVersionKind,
		Metadata: api.ObjectMeta{
			Name: &name,
		},
		Spec: api.TemplateVersionSpec{
			Fleet: template,
		},
		Status: nil,
	}
}

func CreateTestingRepository(name string, url string) *api.Repository {
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url: url,
	})
	Expect(err).ToNot(HaveOccurred())

	return &api.Repository{
		Metadata: api.ObjectMeta{
			Name: &name,
		},
		Spec:   spec,
		Status: nil,
	}
}
