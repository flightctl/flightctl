package store

import (
	"context"
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

func CreateTestDevices(numDevices int, ctx context.Context, deviceStore service.DeviceStore, orgId uuid.UUID, sameVals bool) {
	for i := 1; i <= numDevices; i++ {
		resource := api.Device{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("mydevice-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i), "otherkey": "othervalue"},
			},
			Spec: api.DeviceSpec{
				Os: &api.DeviceOSSpec{
					Image: "myimage",
				},
			},
		}
		if sameVals {
			(*resource.Metadata.Labels)["key"] = "value"
		}

		_, err := deviceStore.Create(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("creating device: %v", err)
		}
	}
}

func CreateTestFleets(numFleets int, ctx context.Context, fleetStore service.FleetStore, orgId uuid.UUID, namePrefix string, sameVals bool) {
	for i := 1; i <= numFleets; i++ {
		resource := api.Fleet{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("%s-%d", namePrefix, i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.FleetSpec{
				Selector: &api.LabelSelector{
					MatchLabels: map[string]string{"key": fmt.Sprintf("value-%d", i)},
				},
			},
		}
		if sameVals {
			resource.Spec.Selector.MatchLabels["key"] = "value"
			(*resource.Metadata.Labels)["key"] = "value"
		}

		_, err := fleetStore.Create(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("creating fleet: %v", err)
		}
	}
}
