package server

import (
	"context"
	"fmt"
	"net/http"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/cloudevents/wrapper"
	"github.com/flightctl/flightctl/internal/service"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/server"
	servergrpc "open-cluster-management.io/sdk-go/pkg/cloudevents/server/grpc"
)

const source = "flightctl"

type DeviceService struct {
	log logrus.FieldLogger

	deviceHandler *service.ServiceHandler
	codec         *deviceCodec

	eventHandler server.EventHandler
}

var _ server.Service = &DeviceService{}

// RegisterCloudEventsService creates a device cloudevents service and registers the service
// to a given gRPC server
func RegisterCloudEventsService(log logrus.FieldLogger,
	grpcSvr *grpc.Server, deviceHandler *service.ServiceHandler) *DeviceService {
	broker := servergrpc.NewGRPCBroker(grpcSvr)
	svc := &DeviceService{
		log:           log,
		deviceHandler: deviceHandler,
		codec:         NewDeviceCodec(),
	}

	broker.RegisterService(wrapper.DeviceEventDataType, svc)
	svc.RegisterHandler(broker)

	return svc
}

// Get a device from flightctl database by its name and encode the device to a cloudevent
func (d *DeviceService) Get(ctx context.Context, deviceName string) (*cloudevents.Event, error) {
	device, status := d.deviceHandler.GetDevice(ctx, deviceName)
	if status.Code != http.StatusOK {
		return nil, fmt.Errorf("%s (%s)", status.Message, status.Reason)
	}

	return d.codec.Encode(
		source,
		types.CloudEventsType{CloudEventsDataType: wrapper.DeviceEventDataType},
		&wrapper.Device{Device: device},
	)
}

// List devices from flightctl database and encode the devices to cloudevents
func (d *DeviceService) List(listOpts types.ListOptions) ([]*cloudevents.Event, error) {
	d.log.Infof("list devices %s", listOpts.ClusterName)

	selector := fmt.Sprintf("metadata.name=%s", listOpts.ClusterName)
	devices, status := d.deviceHandler.ListDevices(context.Background(), v1alpha1.ListDevicesParams{
		FieldSelector: &selector,
	}, nil)
	if status.Code != http.StatusOK {
		return nil, fmt.Errorf("%s (%s)", status.Message, status.Reason)
	}

	evts := []*cloudevents.Event{}
	for _, device := range devices.Items {
		evt, err := d.codec.Encode(
			source,
			types.CloudEventsType{CloudEventsDataType: wrapper.DeviceEventDataType},
			&wrapper.Device{Device: &device},
		)
		if err != nil {
			return nil, err
		}

		evts = append(evts, evt)
	}
	return evts, nil
}

// HandleStatusUpdate handles the received device status update
func (d *DeviceService) HandleStatusUpdate(ctx context.Context, evt *cloudevents.Event) error {
	device, err := d.codec.Decode(evt)
	if err != nil {
		return err
	}

	_, status := d.deviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *device.Device)
	if status.Code != http.StatusOK {
		d.log.Errorf("failed to replace device status: %d (%s): %s - %s",
			status.Code, status.Status, status.Message, status.Reason)
		return fmt.Errorf("%s (%s)", status.Message, status.Reason)
	}

	return nil
}

// RegisterHandler register an event handler for this service.
func (d *DeviceService) RegisterHandler(handler server.EventHandler) {
	d.eventHandler = handler
}

// Publish a device to agent
func (d *DeviceService) Publish(ctx context.Context, name string) error {
	return d.eventHandler.OnUpdate(ctx, wrapper.DeviceEventDataType, name)
}
