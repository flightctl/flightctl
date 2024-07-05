package util

import (
	"context"
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

func CreateTestDevice(ctx context.Context, deviceStore store.Device, orgId uuid.UUID, name string, owner *string, tv *string, labels *map[string]string) {
	deviceStatus := api.NewDeviceStatus()
	deviceStatus.Os.Image = "quay.io/flightctl/test-osimage:latest"
	resource := api.Device{
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: labels,
			Owner:  owner,
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOSSpec{
				Image: "os",
			},
		},
		Status: &deviceStatus,
	}

	if tv != nil {
		rv := *tv
		annotations := map[string]string{
			model.DeviceAnnotationTemplateVersion: rv,
		}
		resource.Metadata.Annotations = &annotations
		deviceStatus.Config.RenderedVersion = rv
	}

	callback := store.DeviceStoreCallback(func(before *model.Device, after *model.Device) {})
	_, err := deviceStore.Create(ctx, orgId, &resource, callback)
	if err != nil {
		log.Fatalf("creating device: %v", err)
	}
}

func CreateTestDevices(ctx context.Context, numDevices int, deviceStore store.Device, orgId uuid.UUID, owner *string, sameVals bool) {
	for i := 1; i <= numDevices; i++ {
		labels := map[string]string{"key": fmt.Sprintf("value-%d", i), "otherkey": "othervalue", "version": fmt.Sprintf("%d", i)}
		if sameVals {
			labels["key"] = "value"
			labels["version"] = "1"
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

func CreateTestFleets(ctx context.Context, numFleets int, fleetStore store.Fleet, orgId uuid.UUID, namePrefix string, sameVals bool, owner *string) {
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
	err = tvStore.UpdateStatus(ctx, orgId, tv, &valid, callback)
	return err
}

func CreateTestTemplateVersions(ctx context.Context, numTemplateVersions int, tvStore store.TemplateVersion, orgId uuid.UUID, fleet string) error {
	for i := 1; i <= numTemplateVersions; i++ {
		err := CreateTestTemplateVersion(ctx, tvStore, orgId, fleet, fmt.Sprintf("1.0.%d", i), "myimage", true)
		if err != nil {
			return err
		}
	}
	return nil
}

func CreateRepositories(ctx context.Context, numRepositories int, storeInst store.Store, orgId uuid.UUID) error {
	for i := 1; i <= numRepositories; i++ {
		spec := api.RepositorySpec{}
		err := spec.FromGitGenericRepoSpec(api.GitGenericRepoSpec{
			Repo: "myrepo",
		})
		if err != nil {
			return err
		}
		resource := api.Repository{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("myrepository-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: spec,
		}

		callback := store.RepositoryStoreCallback(func(*model.Repository) {})
		_, err = storeInst.Repository().Create(ctx, orgId, &resource, callback)
		if err != nil {
			return err
		}
	}
	return nil
}
