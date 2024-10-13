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

func ReturnTestDevice(orgId uuid.UUID, name string, owner *string, tv *string, labels *map[string]string) api.Device {
	deviceStatus := api.NewDeviceStatus()
	deviceStatus.Os.Image = "quay.io/flightctl/test-osimage:latest"

	gitConfig := &api.GitConfigProviderSpec{
		Name: "paramGitConfig",
	}
	gitConfig.GitRef.Path = "path-{{ device.metadata.labels[key] }}"
	gitConfig.GitRef.Repository = "repo"
	gitConfig.GitRef.TargetRevision = "rev"
	gitItem := api.ConfigProviderSpec{}
	_ = gitItem.FromGitConfigProviderSpec(*gitConfig)

	inlineConfig := &api.InlineConfigProviderSpec{
		Name: "paramInlineConfig",
	}
	enc := api.Base64
	inlineConfig.Inline = []api.FileSpec{
		// Unencoded: My version is {{ device.metadata.labels[version] }}
		{Path: "/etc/withparams", ContentEncoding: &enc, Content: "TXkgdmVyc2lvbiBpcyB7eyBkZXZpY2UubWV0YWRhdGEubGFiZWxzW3ZlcnNpb25dIH19"},
	}
	inlineItem := api.ConfigProviderSpec{}
	_ = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)

	httpConfig := &api.HttpConfigProviderSpec{
		Name: "paramHttpConfig",
	}
	httpConfig.HttpRef.Repository = "http-repo"
	httpConfig.HttpRef.FilePath = "http-path-{{ device.metadata.labels[key] }}"
	httpConfig.HttpRef.Suffix = util.StrToPtr("/http-suffix")
	httpItem := api.ConfigProviderSpec{}
	_ = httpItem.FromHttpConfigProviderSpec(*httpConfig)

	resource := api.Device{
		Metadata: api.DeviceMetadata{
			Name:   &name,
			Labels: labels,
			Owner:  owner,
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOSSpec{
				Image: "os",
			},
			Config: &[]api.ConfigProviderSpec{gitItem, inlineItem, httpItem},
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

	return resource
}

func CreateTestDevice(ctx context.Context, deviceStore store.Device, orgId uuid.UUID, name string, owner *string, tv *string, labels *map[string]string) {
	resource := ReturnTestDevice(orgId, name, owner, tv, labels)
	callback := store.DeviceStoreCallback(func(before *model.Device, after *model.Device) {})
	_, _, err := deviceStore.CreateOrUpdate(ctx, orgId, &resource, nil, false, callback)
	if err != nil {
		log.Fatalf("creating device: %v", err)
	}
}

func CreateTestDevices(ctx context.Context, numDevices int, deviceStore store.Device, orgId uuid.UUID, owner *string, sameVals bool) {
	CreateTestDevicesWithOffset(ctx, numDevices, deviceStore, orgId, owner, sameVals, 0)
}

func CreateTestDevicesWithOffset(ctx context.Context, numDevices int, deviceStore store.Device, orgId uuid.UUID, owner *string, sameVals bool, offset int) {
	for i := 1; i <= numDevices; i++ {
		num := i + offset
		labels := map[string]string{"key": fmt.Sprintf("value-%d", num), "otherkey": "othervalue", "version": fmt.Sprintf("%d", num)}
		if sameVals {
			labels["key"] = "value"
			labels["version"] = "1"
		}

		CreateTestDevice(ctx, deviceStore, orgId, fmt.Sprintf("mydevice-%d", num), owner, nil, &labels)
	}
}

func CreateTestFleet(ctx context.Context, fleetStore store.Fleet, orgId uuid.UUID, name string, selector *map[string]string, owner *string) {
	resource := api.Fleet{
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: selector,
			Owner:  owner,
		},
	}

	if selector != nil {
		resource.Spec.Selector = &api.LabelSelector{MatchLabels: selector}
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

func CreateTestTemplateVersion(ctx context.Context, tvStore store.TemplateVersion, orgId uuid.UUID, fleet, name string, status *api.TemplateVersionStatus) error {
	owner := util.SetResourceOwner(model.FleetKind, fleet)
	resource := api.TemplateVersion{
		Metadata: api.ObjectMeta{
			Name:  &name,
			Owner: owner,
		},
		Spec: api.TemplateVersionSpec{
			Fleet: fleet,
		},
		Status: &api.TemplateVersionStatus{},
	}
	if status != nil {
		resource.Status = status
	}

	callback := store.TemplateVersionStoreCallback(func(tv *model.TemplateVersion) {})
	_, err := tvStore.Create(ctx, orgId, &resource, callback)

	return err
}

func CreateTestTemplateVersions(ctx context.Context, numTemplateVersions int, tvStore store.TemplateVersion, orgId uuid.UUID, fleet string) error {
	for i := 1; i <= numTemplateVersions; i++ {
		status := api.TemplateVersionStatus{Os: &api.DeviceOSSpec{Image: "myimage"}}
		err := CreateTestTemplateVersion(ctx, tvStore, orgId, fleet, fmt.Sprintf("1.0.%d", i), &status)
		if err != nil {
			return err
		}
	}
	return nil
}

func CreateRepositories(ctx context.Context, numRepositories int, storeInst store.Store, orgId uuid.UUID) error {
	for i := 1; i <= numRepositories; i++ {
		spec := api.RepositorySpec{}
		err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
			Url: "myrepo",
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
