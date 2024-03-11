package device

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/lthibault/jitterbug"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/klog/v2"
)

// Agent is responsible for managing the configuration and status of the device.
type Agent struct {
	name                   string
	device                 v1alpha1.Device
	deviceStatus           v1alpha1.DeviceStatus
	deviceStatusCollector  *status.Collector
	managementClient       *client.Management
	managementEndpoint     string
	managementCertFilePath string
	agentKeyFilePath       string
	caCertFilePath         string
	logPrefix              string
	fetchSpecInterval      time.Duration
	fetchStatusInterval    time.Duration
	configController       *config.Controller
	isBootstrapComplete    bool
}

// NewAgent creates a new device agent.
func NewAgent(
	name string,
	fetchSpecInterval time.Duration,
	fetchStatusInterval time.Duration,
	caCertFilePath string,
	managementCertFilePath string,
	agentKeyFilePath string,
	managementEndpoint string,
	tpm *tpm.TPM,
	executor executer.Executer,
	logPrefix string,
	configController *config.Controller,
) *Agent {
	return &Agent{
		name:                   name,
		fetchSpecInterval:      fetchSpecInterval,
		fetchStatusInterval:    fetchStatusInterval,
		caCertFilePath:         caCertFilePath,
		managementCertFilePath: managementCertFilePath,
		agentKeyFilePath:       agentKeyFilePath,
		managementEndpoint:     managementEndpoint,
		logPrefix:              logPrefix,
		configController:       configController,
		device: v1alpha1.Device{
			ApiVersion: "v1alpha1",
			Kind:       "Device",
			Status:     &v1alpha1.DeviceStatus{},
			Metadata: v1alpha1.ObjectMeta{
				Name: &name,
			},
		},
		deviceStatusCollector: status.NewCollector(tpm, executor),
	}
}

type AgentGetter interface {
	Get(ctx context.Context) (*v1alpha1.Device, error)
}

// Run starts the device agent reconciliation loop.
func (a *Agent) Run(ctx context.Context) error {
	fetchSpecTicker := jitterbug.New(a.fetchSpecInterval, &jitterbug.Norm{Stdev: util.CreateRandomJitterDuration(20, time.Millisecond), Mean: 0})
	defer fetchSpecTicker.Stop()
	fetchStatusTicker := jitterbug.New(a.fetchStatusInterval, &jitterbug.Norm{Stdev: util.CreateRandomJitterDuration(20, time.Millisecond), Mean: 0})
	defer fetchStatusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-fetchSpecTicker.C:
			klog.V(4).Infof("%s fetching device spec", a.logPrefix)
			if err := a.ensureDevice(ctx); err != nil {
				klog.Errorf("%sfailed to ensure device: %v", a.logPrefix, err)
			}
		case <-fetchStatusTicker.C:
			klog.V(4).Infof("%s fetching device status", a.logPrefix)
			if err := a.ensureDeviceStatus(ctx); err != nil {
				klog.Errorf("%sfailed to ensure device status: %v", a.logPrefix, err)
			}
			//TODO: do we require a periodic Sync()?
		}
	}
}

func (a *Agent) ensureDevice(ctx context.Context) error {
	existingDevice := a.Get(ctx)
	if !a.isBootstrapComplete {
		// sync with the default bootstrap device
		if err := a.configController.Sync(ctx, existingDevice); err != nil {
			return err
		}
		klog.Infof("%s bootstrap complete", a.logPrefix)
		a.isBootstrapComplete = true
		return nil
	}

	if err := a.ensureClient(); err != nil {
		return err
	}
	newDevice, err := a.managementClient.GetDevice(ctx, a.name)
	if err != nil {
		return err
	}
	if equality.Semantic.DeepEqual(existingDevice.Spec, newDevice.Spec) {
		return nil
	}
	// ensure retry on failure and set the new device only if successful.
	if err := a.configController.Sync(ctx, *newDevice); err != nil {
		return err
	}

	a.set(*newDevice)
	return nil
}

func (a *Agent) ensureDeviceStatus(ctx context.Context) error {
	err := a.ensureClient()
	if err != nil {
		return err
	}
	a.deviceStatus, err = a.deviceStatusCollector.Get(ctx)
	return err
}

func (a *Agent) ensureClient() error {
	if a.managementClient != nil {
		return nil
	}
	managementHTTPClient, err := client.NewWithResponses(a.managementEndpoint, a.caCertFilePath, a.managementCertFilePath, a.agentKeyFilePath)
	if err != nil {
		return err
	}
	a.managementClient = client.NewManagement(managementHTTPClient)
	return nil
}

// Get returns the most recently observed device.
func (a *Agent) Get(ctx context.Context) v1alpha1.Device {
	return a.device
}

func (a *Agent) set(device v1alpha1.Device) {
	device.Status = &a.deviceStatus
	a.device = device
}
