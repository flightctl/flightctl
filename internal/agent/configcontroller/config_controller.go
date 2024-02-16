package configcontroller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/client"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/deviceexporter"
)

const (
	// name of the CA bundle file
	caBundleFile = "ca.crt"
	// name of the agent's key file
	agentKeyFile = "agent.key"
	// name of the enrollment certificate file
	enrollmentCertFile = "client-enrollment.crt"
	// name of the enrollment key file
	enrollmentKeyFile = "client-enrollment.key"
	// name of the client certificate file
	clientCertFile = "client.crt"
)

type ConfigController struct {
	caFilePath   string
	device       *v1alpha1.Device
	deviceWriter *device.Writer
	// The device exporter manager manages the device status exporters
	deviceManager *deviceexporter.Manager

	enrollmentClient        *client.Enrollment
	enrollmentVerifyBackoff wait.Backoff
	enrollmentEndpoint      string

	managementClient       *client.Management
	managementEndpoint     string
	managementCertFilePath string
	managementKeyFilePath  string

	// The device fingerprint
	enrollmentCSR []byte
	// The log prefix used for testing
	logPrefix string
	// The directory to write the certificate to
	certDir string
}

func New(
	device *v1alpha1.Device,
	enrollmentClient *client.Enrollment,
	enrollmentEndpoint string,
	managementEndpoint string,
	caFilePath string,
	managementCertFilePath string,
	managementKeyFilePath string,
	deviceWriter *device.Writer,
	deviceManager *deviceexporter.Manager,
	enrollmentCSR []byte,
	logPrefix string,
) *ConfigController {

	enrollmentVerifyBackoff := wait.Backoff{
		Cap:      3 * time.Minute,
		Duration: 10 * time.Second,
		Factor:   1.5,
		Steps:    24,
	}

	return &ConfigController{
		enrollmentClient:        enrollmentClient,
		enrollmentVerifyBackoff: enrollmentVerifyBackoff,
		enrollmentEndpoint:      enrollmentEndpoint,
		device:                  device,
		deviceWriter:            deviceWriter,
		deviceManager:           deviceManager,
		caFilePath:              caFilePath,
		managementEndpoint:      managementEndpoint,
		managementCertFilePath:  managementCertFilePath,
		managementKeyFilePath:   managementKeyFilePath,
		enrollmentCSR:           enrollmentCSR,
		logPrefix:               logPrefix,
	}
}

func (c *ConfigController) Run(ctx context.Context, workers int) {
	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					existingDevice := c.device
					newDevice, err := c.managementClient.GetDevice(ctx, *c.device.Metadata.Name)
					if err != nil {
						klog.Errorf("%sfailed to get device: %v", c.logPrefix, err)
						continue
					}
					if equality.Semantic.DeepEqual(existingDevice, newDevice) {
						time.Sleep(10 * time.Second) //constant
						continue
					}
					if err := c.sync(ctx, newDevice); err != nil {
						klog.Errorf("%sfailed to sync: %v", c.logPrefix, err)
					}
					// c.SetDevice(device)
				}
			}
		}()
	}
}

func (c *ConfigController) sync(ctx context.Context, device *v1alpha1.Device) error {
	deviceStatus := c.deviceManager.GetStatus()

	// ensure the device is enrolled
	if err := c.ensureDeviceEnrollment(ctx, device); err != nil {
		klog.Errorf("%s enrollment did not succeed: %v", c.logPrefix, err)
		return err
	}

	// post enrollment we can update status
	condition := v1alpha1.DeviceCondition{
		Type:   "Enrolled",
		Status: v1alpha1.True,
	}
	deviceStatus.Conditions = &[]v1alpha1.DeviceCondition{condition}
	_, updateErr := c.managementClient.UpdateDeviceStatus(ctx, *device.Metadata.Name, deviceStatus)
	if updateErr != nil {
		klog.Errorf("%sfailed to update device status: %v", c.logPrefix, updateErr)
		return updateErr
	}

	// ensure the device is configured
	if err := c.ensureConfig(ctx, device); err != nil {

	}

	return nil
}
