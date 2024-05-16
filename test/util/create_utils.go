package util

import (
	"context"
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/bootimage"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

func CreateTestDevice(ctx context.Context, deviceStore store.Device, orgId uuid.UUID, name string, owner *string, tv *string, labels *map[string]string) {
	resource := api.Device{
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: labels,
			Owner:  owner,
		},
		Spec: &api.DeviceSpec{
			TemplateVersion: tv,
			Os: &api.DeviceOSSpec{
				Image: "os",
			},
		},
	}

	callback := store.DeviceStoreCallback(func(before *model.Device, after *model.Device) {})
	_, err := deviceStore.Create(ctx, orgId, &resource, callback)
	if err != nil {
		log.Fatalf("creating device: %v", err)
	}
}

func CreateTestDevices(numDevices int, ctx context.Context, deviceStore store.Device, orgId uuid.UUID, owner *string, sameVals bool) {
	for i := 1; i <= numDevices; i++ {
		labels := map[string]string{"key": fmt.Sprintf("value-%d", i), "otherkey": "othervalue"}
		if sameVals {
			labels["key"] = "value"
		}

		CreateTestDevice(ctx, deviceStore, orgId, fmt.Sprintf("mydevice-%d", i), owner, nil, &labels)
	}
}

func CreateTestFleet(ctx context.Context, fleetStore store.Fleet, orgId uuid.UUID, name string, selector *map[string]string, owner *string) {
	resource := api.Fleet{
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: selector,
			Owner:  owner,
		},
		Spec: api.FleetSpec{},
	}

	if selector != nil {
		resource.Spec.Selector = &api.LabelSelector{MatchLabels: *selector}
	}
	callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {})
	_, err := fleetStore.Create(ctx, orgId, &resource, callback)
	if err != nil {
		log.Fatalf("creating fleet: %v", err)
	}
}

func CreateTestFleets(numFleets int, ctx context.Context, fleetStore store.Fleet, orgId uuid.UUID, namePrefix string, sameVals bool, owner *string) {
	for i := 1; i <= numFleets; i++ {
		selector := map[string]string{"key": fmt.Sprintf("value-%d", i)}
		if sameVals {
			selector["key"] = "value"
		}
		CreateTestFleet(ctx, fleetStore, orgId, fmt.Sprintf("%s-%d", namePrefix, i), &selector, owner)
	}
}

func CreateTestTemplateVersion(ctx context.Context, tvStore store.TemplateVersion, orgId uuid.UUID, fleet, name, osImage string, valid bool) error {
	owner := util.SetResourceOwner(model.FleetKind, fleet)
	resource := api.TemplateVersion{
		Metadata: api.ObjectMeta{
			Name:  &name,
			Owner: owner,
		},
		Spec: api.TemplateVersionSpec{
			Fleet: fleet,
		},
	}

	callback := store.TemplateVersionStoreCallback(func(tv *model.TemplateVersion) {})
	tv, err := tvStore.Create(ctx, orgId, &resource, callback)
	if err != nil {
		return err
	}

	tv.Status = &api.TemplateVersionStatus{}
	tv.Status.Os = &api.DeviceOSSpec{Image: osImage}
	err = tvStore.UpdateStatusAndConfig(ctx, orgId, tv, &valid, util.StrToPtr("rendered config"), callback)
	return err
}

func CreateTestTemplateVersions(numTemplateVersions int, ctx context.Context, tvStore store.TemplateVersion, orgId uuid.UUID, fleet string) error {
	for i := 1; i <= numTemplateVersions; i++ {
		err := CreateTestTemplateVersion(ctx, tvStore, orgId, fleet, fmt.Sprintf("1.0.%d", i), "myimage", true)
		if err != nil {
			return err
		}
	}
	return nil
}

func CreateTestImageManagerBootedStatus(image string) *bootimage.HostStatus {
	return &bootimage.HostStatus{
		Booted: bootimage.BootEntry{
			Image: bootimage.ImageStatus{
				Image: bootimage.ImageReference{
					Image: image,
				},
			},
		},
	}
}
