package service_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/store/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

var _ = Describe("Atomic firmware rollout safeguards", func() {
	var suite *ServiceTestSuite
	const deviceName = "atomic-device"
	const owner = "Fleet/old"
	refs := func(fleet, key string) []model.DependencyRef {
		return []model.DependencyRef{{FleetName: lo.ToPtr(fleet), DeviceName: lo.ToPtr(deviceName), RefType: "git", ResourceKey: key}}
	}
	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		status := api.NewDeviceStatus()
		_, err := suite.DeviceStore.Create(suite.Ctx, suite.OrgID, &api.Device{Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName), Owner: lo.ToPtr(owner), Annotations: &map[string]string{domain.DeviceAnnotationTemplateVersion: "old-version"}}, Spec: &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "os:old"}}, Status: &status}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(dependencyrefstore.NewDependencyRefStore(suite.DB, suite.Log).ReplaceFleetScopedDeviceRefs(suite.Ctx, suite.OrgID, deviceName, refs("old", "old-ref"))).To(Succeed())
	})
	AfterEach(func() { suite.Teardown() })
	write := func() api.Status {
		replacement := refs("old", "new-ref")
		_, status := suite.Device.ReplaceDeviceSpec(suite.Ctx, suite.OrgID, deviceName, lo.ToPtr(owner), api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "os:new"}}, map[string]string{domain.DeviceAnnotationTemplateVersion: "new-version"}, nil, deviceservice.SpecUpdateOptions{FleetDependencyRefs: &replacement})
		return status
	}
	storedRefs := func() []model.DependencyRef {
		var result []model.DependencyRef
		Expect(suite.DB.Where("org_id = ? AND device_name = ?", suite.OrgID, deviceName).Find(&result).Error).To(Succeed())
		return result
	}
	It("When reference persistence fails it should roll back spec and template selection and allow retry", func() {
		const callback = "test:reject-dependency-ref"
		Expect(suite.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "dependency_refs" { // AddError records the failure on the statement.
				_ = tx.AddError(errors.New("injected reference write failure"))
			}
		})).To(Succeed())
		DeferCleanup(func() { Expect(suite.DB.Callback().Create().Remove(callback)).To(Succeed()) })
		Expect(write().Code).To(Equal(int32(http.StatusInternalServerError)))
		current, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, deviceName)
		Expect(err).ToNot(HaveOccurred())
		Expect(current.Spec.Os.Image).To(Equal("os:old"))
		Expect((*current.Metadata.Annotations)[domain.DeviceAnnotationTemplateVersion]).To(Equal("old-version"))
		Expect(storedRefs()).To(HaveLen(1))
		Expect(storedRefs()[0].ResourceKey).To(Equal("old-ref"))
		Expect(suite.DB.Callback().Create().Remove(callback)).To(Succeed())
		Expect(write().Code).To(Equal(int32(http.StatusOK)))
		current, err = suite.DeviceStore.Get(suite.Ctx, suite.OrgID, deviceName)
		Expect(err).ToNot(HaveOccurred())
		Expect(current.Spec.Os.Image).To(Equal("os:new"))
		Expect((*current.Metadata.Annotations)[domain.DeviceAnnotationTemplateVersion]).To(Equal("new-version"))
		Expect(storedRefs()).To(HaveLen(1))
		Expect(storedRefs()[0].ResourceKey).To(Equal("new-ref"))
	})
	It("When a device changes fleets before commit it should retain the new owner's references", func() {
		store := suite.DeviceStore.(*devicestore.DeviceStore)
		concurrent := devicestore.NewDeviceStore(suite.DB, suite.Log)
		var moved atomic.Bool
		store.SetIntegrationTestCreateOrUpdateCallback(func() {
			if !moved.CompareAndSwap(false, true) {
				return
			}
			replacement := refs("new", "new-owner-ref")
			_, _, _, err := concurrent.Mutate(suite.Ctx, suite.OrgID, deviceName, nil, func(m *devicestore.DeviceMutation) error {
				m.Device.Metadata.Owner = lo.ToPtr("Fleet/new")
				m.FleetDependencyRefs = &replacement
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
		})
		DeferCleanup(func() { store.SetIntegrationTestCreateOrUpdateCallback(func() {}) })
		Expect(write().Code).ToNot(Equal(int32(http.StatusOK)))
		Expect(moved.Load()).To(BeTrue())
		current, err := store.Get(suite.Ctx, suite.OrgID, deviceName)
		Expect(err).ToNot(HaveOccurred())
		Expect(lo.FromPtr(current.Metadata.Owner)).To(Equal("Fleet/new"))
		Expect(current.Spec.Os.Image).To(Equal("os:old"))
		Expect(storedRefs()).To(HaveLen(1))
		Expect(storedRefs()[0].ResourceKey).To(Equal("new-owner-ref"))
	})
	It("When rollout repairs dependencies it should clear references left by a former fleet", func() {
		Expect(dependencyrefstore.NewDependencyRefStore(suite.DB, suite.Log).ReplaceFleetScopedDeviceRefs(suite.Ctx, suite.OrgID, deviceName, refs("former", "former-ref"))).To(Succeed())
		Expect(write().Code).To(Equal(int32(http.StatusOK)))
		Expect(storedRefs()).To(HaveLen(1))
		Expect(lo.FromPtr(storedRefs()[0].FleetName)).To(Equal("old"))
	})
	It("When the generation changes before a rendered write it should retain the newer output", func() {
		current, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, deviceName)
		Expect(err).ToNot(HaveOccurred())
		expected := lo.FromPtr(current.Metadata.Generation)
		store := suite.DeviceStore.(*devicestore.DeviceStore)
		concurrent := devicestore.NewDeviceStore(suite.DB, suite.Log)
		var changed atomic.Bool
		var newer model.Device
		store.SetIntegrationTestCreateOrUpdateCallback(func() {
			if !changed.CompareAndSwap(false, true) {
				return
			}
			_, _, _, err := concurrent.Mutate(suite.Ctx, suite.OrgID, deviceName, nil, func(m *devicestore.DeviceMutation) error {
				m.Device.Spec.Os.Image = "os:newer"
				m.Rendered = &devicestore.DeviceRendered{Config: "[]"}
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(suite.DB.Where("org_id = ? AND name = ?", suite.OrgID, deviceName).Take(&newer).Error).To(Succeed())
		})
		DeferCleanup(func() { store.SetIntegrationTestCreateOrUpdateCallback(func() {}) })
		status := suite.Device.UpdateRenderedDevice(context.Background(), suite.OrgID, deviceName, "stale-output", "", "stale-hash", "os:old", nil, false, nil, deviceservice.RenderedDeviceOptions{ExpectedGeneration: &expected})
		Expect(status.Code).To(Equal(int32(http.StatusConflict)))
		var row model.Device
		Expect(suite.DB.Where("org_id = ? AND name = ?", suite.OrgID, deviceName).Take(&row).Error).To(Succeed())
		Expect(newer.RenderedConfig).ToNot(BeNil())
		Expect(row.RenderedConfig).To(Equal(newer.RenderedConfig))
	})
})
