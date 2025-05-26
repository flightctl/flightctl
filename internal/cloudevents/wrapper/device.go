package wrapper

import (
	flightctl "github.com/flightctl/flightctl/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubetypes "k8s.io/apimachinery/pkg/types"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

var DeviceEventDataType = types.CloudEventsDataType{
	Group:    "io.flightctl",
	Version:  "v1alpha1",
	Resource: "devices",
}

var DeviceEventType = types.CloudEventsType{
	CloudEventsDataType: DeviceEventDataType,
}

var _ generic.ResourceObject = &Device{}

// Device wraps a device object to provide the functions that are required by ResourceObject interface
type Device struct {
	*flightctl.Device
}

func (d *Device) GetUID() kubetypes.UID {
	return kubetypes.UID(*d.Metadata.Name)
}

func (d *Device) GetResourceVersion() string {
	return d.Version()
}

func (d *Device) GetDeletionTimestamp() *metav1.Time {
	if d.Metadata.DeletionTimestamp == nil {
		return nil
	}

	return &metav1.Time{Time: *d.Metadata.DeletionTimestamp}
}
