package server

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

func NewDeviceCodec() *deviceCodec {
	return &deviceCodec{}
}

func (c *deviceCodec) EventDataType() types.CloudEventsDataType {
	return wrapper.DeviceEventDataType
}

// Encode devices to cloudevents
func (c *deviceCodec) Encode(source string, eventType types.CloudEventsType, device *wrapper.Device) (*cloudevents.Event, error) {
	if eventType.CloudEventsDataType != wrapper.DeviceEventDataType {
		return nil, fmt.Errorf("unsupported cloudevents data type %s", eventType.CloudEventsDataType)
	}

	resourceVersion, err := strconv.Atoi(device.Version())
	if err != nil {
		return nil, fmt.Errorf("failed to convert resource version %s to int: %v", *device.Metadata.ResourceVersion, err)
	}

	evt := types.NewEventBuilder(source, eventType).
		WithClusterName(*device.Metadata.Name).
		WithResourceID(*device.Metadata.Name).
		WithResourceVersion(int64(resourceVersion)).
		NewEvent()

	metaJson, err := json.Marshal(device.Metadata)
	if err != nil {
		return nil, err
	}
	evt.SetExtension("metadata", string(metaJson))

	if err := evt.SetData(cloudevents.ApplicationJSON, device.Spec); err != nil {
		return nil, fmt.Errorf("failed to encode manifestwork status to a cloudevent: %v", err)
	}

	return &evt, nil
}

// Decode cloudevents to devices
func (c *deviceCodec) Decode(evt *cloudevents.Event) (*wrapper.Device, error) {
	eventType, err := types.ParseCloudEventsType(evt.Type())
	if err != nil {
		return nil, fmt.Errorf("failed to parse cloud event type %s, %v", evt.Type(), err)
	}

	if eventType.CloudEventsDataType != wrapper.DeviceEventDataType {
		return nil, fmt.Errorf("unsupported cloudevents data type %s", eventType.CloudEventsDataType)
	}

	metaJson, err := cloudeventstypes.ToString(evt.Context.GetExtensions()["metadata"])
	if err != nil {
		return nil, fmt.Errorf("failed to get metadata extension: %v", err)
	}

	metadata := v1alpha1.ObjectMeta{}
	if err := json.Unmarshal([]byte(metaJson), &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata extension: %v", err)
	}

	device := &v1alpha1.Device{Metadata: metadata}

	status := &v1alpha1.DeviceStatus{}
	if err := evt.DataAs(status); err != nil {
		return nil, fmt.Errorf("failed to unmarshal event data %s, %v", string(evt.Data()), err)
	}
	device.Status = status

	return &wrapper.Device{Device: device}, nil
}
