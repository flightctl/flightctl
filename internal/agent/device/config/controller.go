package config

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/client"
	"github.com/flightctl/flightctl/internal/agent/device/writer"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

type Controller struct {
	deviceWriter            *writer.Writer
	enrollmentClient        *client.Enrollment
	managementClient        *client.Management
	enrollmentVerifyBackoff wait.Backoff
	enrollmentEndpoint      string

	caFilePath             string
	managementEndpoint     string
	managementCertFilePath string
	agentKeyFilePath       string

	enrollmentCSR []byte
	// The log prefix used for testing
	logPrefix string
}

func NewController(
	enrollmentClient *client.Enrollment,
	enrollmentEndpoint string,
	managementEndpoint string,
	caFilePath string,
	managementCertFilePath string,
	agentKeyFilePath string,
	deviceWriter *writer.Writer,
	enrollmentCSR []byte,
	logPrefix string,
) *Controller {

	enrollmentVerifyBackoff := wait.Backoff{
		Cap:      3 * time.Minute,
		Duration: 10 * time.Second,
		Factor:   1.5,
		Steps:    24,
	}

	c := &Controller{
		enrollmentClient:        enrollmentClient,
		enrollmentVerifyBackoff: enrollmentVerifyBackoff,
		enrollmentEndpoint:      enrollmentEndpoint,
		caFilePath:              caFilePath,
		managementEndpoint:      managementEndpoint,
		managementCertFilePath:  managementCertFilePath,
		agentKeyFilePath:        agentKeyFilePath,
		enrollmentCSR:           enrollmentCSR,
		logPrefix:               logPrefix,
		deviceWriter:            deviceWriter,
	}

	return c
}

func (c *Controller) Sync(ctx context.Context, device v1alpha1.Device) error {
	// ensure the device is bootstrapped
	if err := c.ensureBootstrap(ctx, &device); err != nil {
		klog.Warningf("%s bootstrap failed: %v", c.logPrefix, err)
		return err
	}

	var conditions []v1alpha1.Condition
	// post enrollment update status
	deviceCondition := v1alpha1.Condition{
		Type:   "Enrolled",
		Status: v1alpha1.ConditionStatusTrue,
	}
	conditions = append(conditions, deviceCondition)
	device.Status.Conditions = &conditions

	if !c.isBootstrapComplete() {
		_, updateErr := c.managementClient.UpdateDeviceStatus(ctx, *device.Metadata.Name, *device.Status)
		if updateErr != nil {
			klog.Errorf("%sfailed to update device status: %v", c.logPrefix, updateErr)
			return updateErr
		}
	}

	// ensure the device is configured
	if err := c.ensureConfig(ctx, &device); err != nil {
		klog.Errorf("%s configuration did not succeed: %v", c.logPrefix, err)
		errMsg := err.Error()

		// TODO: better status
		condition := v1alpha1.Condition{
			Type:    "Configured",
			Status:  v1alpha1.ConditionStatusFalse,
			Message: &errMsg,
		}
		conditions = append(conditions, condition)
		_, updateErr := c.managementClient.UpdateDeviceStatus(ctx, *device.Metadata.Name, *device.Status)
		if updateErr != nil {
			klog.Errorf("%sfailed to update device status: %v", c.logPrefix, updateErr)
			return updateErr
		}
		return err
	}

	// TODO: status should be more informative
	// post configuration update status
	configCondition := v1alpha1.Condition{
		Type:   "Configured",
		Status: v1alpha1.ConditionStatusTrue,
	}
	conditions = append(conditions, configCondition)
	device.Status.Conditions = &conditions

	_, updateErr := c.managementClient.UpdateDeviceStatus(ctx, *device.Metadata.Name, *device.Status)
	if updateErr != nil {
		klog.Errorf("%sfailed to update device status: %v", c.logPrefix, updateErr)
		return updateErr
	}

	return nil
}

type Ignition struct {
	Raw  json.RawMessage `json:"inline"`
	Name string          `json:"name"`
}

func (c *Controller) ensureConfig(_ context.Context, device *v1alpha1.Device) error {
	if device.Spec.Config == nil {
		klog.V(4).Infof("%s device config is nil", c.logPrefix)
		return nil
	}

	for _, config := range *device.Spec.Config {
		configBytes, err := json.Marshal(config)
		if err != nil {
			return fmt.Errorf("marshalling config failed: %w", err)
		}

		var ignition Ignition
		err = json.Unmarshal(configBytes, &ignition)
		if err != nil {
			return fmt.Errorf("unmarshalling config failed: %w", err)
		}

		ignitionConfig, err := ParseAndConvertConfig(ignition.Raw)
		if err != nil {
			return fmt.Errorf("parsing and converting config failed: %w", err)
		}

		err = c.deviceWriter.WriteIgnitionFiles(ignitionConfig.Storage.Files...)
		if err != nil {
			return fmt.Errorf("writing ignition files failed: %w", err)
		}
	}

	return nil
}
