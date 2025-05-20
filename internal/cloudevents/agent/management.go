package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	agentclient "github.com/flightctl/flightctl/internal/agent/client"
	apiclient "github.com/flightctl/flightctl/internal/api/client/agent"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/cloudevents/util"
	"github.com/flightctl/flightctl/internal/cloudevents/wrapper"
	"github.com/flightctl/flightctl/pkg/log"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/clients/statushash"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/options/grpc"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

var _ agentclient.Management = (*management)(nil)

type management struct {
	client     generic.CloudEventsClient[*wrapper.Device]
	cache      *deviceCache
	log        *log.PrefixLogger
	deviceName string
}

// NewManagement returns a management based on gRPC pub/sub
func NewManagement(ctx context.Context, config *client.Config, deviceName string, log *log.PrefixLogger) (agentclient.Management, error) {
	endpoint, tlsConfig, err := util.GRPCConfig(config)
	if err != nil {
		return nil, err
	}

	grpcOptions := &grpc.GRPCOptions{
		Dialer: &grpc.GRPCDialer{
			URL: endpoint,
			KeepAliveOptions: grpc.KeepAliveOptions{
				Enable:              true,
				Time:                30 * time.Second,
				Timeout:             10 * time.Second,
				PermitWithoutStream: true,
			},
			TLSConfig: tlsConfig,
		},
	}

	options, err := generic.BuildCloudEventsAgentOptions(
		grpcOptions,
		deviceName,
		fmt.Sprintf("%s-agent", deviceName),
	)
	if err != nil {
		return nil, err
	}

	cache := NewDeviceCache(log, deviceName)
	cloudEventsClient, err := generic.NewCloudEventAgentClient(
		ctx,
		options,
		NewDeviceLister(cache),
		statushash.StatusHash,
		NewDeviceCodec(),
	)
	if err != nil {
		return nil, err
	}

	// subscribe to the gRPC server to receive device spec updates
	cloudEventsClient.Subscribe(ctx, func(action types.ResourceAction, obj *wrapper.Device) error {
		log.Infof("receive the device %s spec update (%s)", *obj.Metadata.Name, obj.Version())
		return cache.Upsert(obj.Device)
	})

	// start a go routine to handle agent reconnect signal
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-cloudEventsClient.ReconnectedChan():
				// received a agent reconnected signal, resync the device for this agent
				if err := cloudEventsClient.Resync(ctx, types.SourceAll); err != nil {
					log.Errorf("failed to send resync request, %v", err)
				}
			}
		}
	}()

	// send the first device sync request
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := cloudEventsClient.Resync(ctx, types.SourceAll); err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to publish device sync request: %v", err)
	}

	return &management{client: cloudEventsClient, cache: cache, log: log, deviceName: deviceName}, nil
}

// UpdateDeviceStatus publish the status of the device via gRPC.
func (m *management) UpdateDeviceStatus(ctx context.Context, name string, device v1alpha1.Device, rcb ...apiclient.RequestEditorFn) error {
	m.log.Infof("publish device %s status to gRPC server", name)

	current, existing := m.cache.Get(name)
	if !existing {
		return errors.NewNotFound(schema.GroupResource{Resource: "device"}, name)
	}

	device.Metadata = current.Metadata

	evtType := types.CloudEventsType{
		CloudEventsDataType: wrapper.DeviceEventDataType,
		SubResource:         types.SubResourceStatus,
		Action:              "update",
	}
	if err := m.client.Publish(ctx, evtType, &wrapper.Device{Device: &device}); err != nil {
		return err
	}

	m.cache.UpdateStatus(device)

	return nil
}

// GetRenderedDevice returns the latest received rendered device spec for the given device
func (m *management) GetRenderedDevice(ctx context.Context, name string, params *v1alpha1.GetRenderedDeviceParams, rcb ...apiclient.RequestEditorFn) (*v1alpha1.Device, int, error) {
	m.log.Infof("get rendered device %s from cache", name)

	device, existing := m.cache.Get(name)
	if !existing {
		return nil, http.StatusNotFound, nil
	}

	return device, http.StatusOK, nil
}

func (m *management) PatchDeviceStatus(ctx context.Context, name string, patch v1alpha1.PatchRequest, rcb ...apiclient.RequestEditorFn) error {
	return errors.NewMethodNotSupported(schema.GroupResource{Resource: "device"}, "patch")
}
