package agent

import (
	"encoding/json"
	"fmt"
	"strconv"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	cloudeventstypes "github.com/cloudevents/sdk-go/v2/types"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/cloudevents/wrapper"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

type deviceCodec struct{}

// NewDeviceCodec returns a codec to encode/decode devices/cloudevents.
func NewDeviceCodec() *deviceCodec {
	return &deviceCodec{}
}

// EventDataType always returns the event data type `io.flightctl.v1alpha1.devices`.
func (c *deviceCodec) EventDataType() types.CloudEventsDataType {
	return wrapper.DeviceEventDataType
}

// Encode devices status to cloudevents.
func (c *deviceCodec) Encode(source string, eventType types.CloudEventsType, device *wrapper.Device) (*cloudevents.Event, error) {
	if eventType.CloudEventsDataType != wrapper.DeviceEventDataType {
		return nil, fmt.Errorf("unsupported cloudevents data type %s", eventType.CloudEventsDataType)
	}

	if device.Metadata.Name == nil {
		return nil, fmt.Errorf("failed to get the name of device, %v", device)
	}

	name := *device.Metadata.Name

	resourceVersion, err := strconv.ParseInt(device.Version(), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse the resourceversion of device %s, %v", name, err)
	}

	evt := types.NewEventBuilder(source, eventType).
		WithResourceID(name).
		WithResourceVersion(resourceVersion).
		WithClusterName(name).
		NewEvent()

	metaJson, err := json.Marshal(device.Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata extension: %v", err)
	}
	evt.SetExtension("metadata", string(metaJson))

	if err := evt.SetData(cloudevents.ApplicationJSON, device.Status); err != nil {
		return nil, fmt.Errorf("failed to encode manifestwork status to a cloudevent: %v", err)
	}

	return &evt, nil
}

// Decode cloudevents to devices.
func (c *deviceCodec) Decode(evt *cloudevents.Event) (*wrapper.Device, error) {
	eventType, err := types.ParseCloudEventsType(evt.Type())
	if err != nil {
		return nil, fmt.Errorf("failed to parse cloud event type %s, %v", evt.Type(), err)
	}

	if eventType.CloudEventsDataType != wrapper.DeviceEventDataType {
		return nil, fmt.Errorf("unsupported cloudevents data type %s", eventType.CloudEventsDataType)
	}

	evtExtensions := evt.Context.GetExtensions()
	metaJson, err := cloudeventstypes.ToString(evtExtensions["metadata"])
	if err != nil {
		return nil, fmt.Errorf("failed to get metadata extension: %v", err)
	}

	metadata := v1alpha1.ObjectMeta{}
	if err := json.Unmarshal([]byte(metaJson), &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata extension: %v", err)
	}

	device := &v1alpha1.Device{
		ApiVersion: "flightctl.io/v1alpha1",
		Kind:       "Device",
		Metadata:   metadata,
	}
	deviceSpec := &v1alpha1.DeviceSpec{}
	if err := evt.DataAs(deviceSpec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal event data %s, %v", string(evt.Data()), err)
	}
	device.Spec = deviceSpec
	return &wrapper.Device{Device: device}, nil
}
