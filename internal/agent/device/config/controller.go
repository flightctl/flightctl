package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/writer"
	"github.com/flightctl/flightctl/internal/client"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

// Config controller is responsible for ensuring the device configuration is reconciled
// against the device spec.
type Controller struct {
	deviceName              string
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

// NewController creates a new config controller.
func NewController(
	deviceName string,
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

	return &Controller{
		deviceName:              deviceName,
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
}

func (c *Controller) Sync(ctx context.Context, device v1alpha1.Device) error {
	klog.V(4).Infof("%s syncing device configuration", c.logPrefix)
	defer klog.V(4).Infof("%s finished syncing device configuration", c.logPrefix)

	// ensure the device is bootstrapped
	if err := c.ensureBootstrap(ctx, &device); err != nil {
		klog.Warningf("%s bootstrap failed: %v", c.logPrefix, err)
		return err
	}

	// ensure the device configuration is reconciled
	if err := c.ensureConfig(ctx, &device); err != nil {
		updateErr := c.updateStatus(ctx, &device, err.Error())
		if updateErr != nil {
			klog.Warningf("%s failed to update device status: %v", c.logPrefix, updateErr)
		}
		return err
	}

	return c.updateStatus(ctx, &device, "")
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

func (c *Controller) updateStatus(ctx context.Context, device *v1alpha1.Device, errMsg string) error {
	var conditions []v1alpha1.Condition

	// client certs don not exist prior to enrollment so we can assume this is
	// always true.
	conditions = append(conditions, v1alpha1.Condition{
		Type:   v1alpha1.EnrollmentRequestApproved,
		Status: v1alpha1.ConditionStatusTrue,
	})

	status := v1alpha1.ConditionStatusTrue
	var message *string
	if len(errMsg) > 0 {
		status = v1alpha1.ConditionStatusFalse
		message = &errMsg
	}

	syncCondition := v1alpha1.Condition{
		Type:   v1alpha1.ResourceSyncSynced,
		Status: status,
	}
	if message != nil {
		syncCondition.Message = message
	}
	conditions = append(conditions, syncCondition)
	device.Status.Conditions = &conditions

	buf := &bytes.Buffer{}
	err := json.NewEncoder(buf).Encode(device)
	if err != nil {
		return fmt.Errorf("encoding device failed: %w", err)
	}

	return c.managementClient.UpdateDeviceStatus(ctx, *device.Metadata.Name, buf)
}
