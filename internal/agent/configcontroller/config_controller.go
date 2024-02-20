package configcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/client"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/export"
	"github.com/flightctl/flightctl/internal/agent/observe"
	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	// maxUpdateBackoff is the maximum time to react to a change as we back off
	// in the face of errors.
	maxUpdateBackoff = 60 * time.Second
	// updateDelay is the time to wait before we react to change.
	updateDelay = 5 * time.Second
)

type ConfigController struct {
	caFilePath           string
	device               *device.Device
	deviceWriter         *device.Writer
	deviceStatusExporter export.DeviceStatus
	deviceObserver       *observe.Device
	queue                workqueue.RateLimitingInterface

	enrollmentClient        *client.Enrollment
	enrollmentVerifyBackoff wait.Backoff
	enrollmentEndpoint      string

	managementClient       *client.Management
	managementEndpoint     string
	managementCertFilePath string
	agentKeyFilePath       string

	// The device fingerprint
	enrollmentCSR []byte
	// The log prefix used for testing
	logPrefix string
}

func New(
	device *device.Device,
	enrollmentClient *client.Enrollment,
	enrollmentEndpoint string,
	managementEndpoint string,
	caFilePath string,
	managementCertFilePath string,
	agentKeyFilePath string,
	deviceWriter *device.Writer,
	deviceStatusExporter export.DeviceStatus,
	deviceObserver *observe.Device,
	enrollmentCSR []byte,
	logPrefix string,
) *ConfigController {

	enrollmentVerifyBackoff := wait.Backoff{
		Cap:      3 * time.Minute,
		Duration: 10 * time.Second,
		Factor:   1.5,
		Steps:    24,
	}

	c := &ConfigController{
		enrollmentClient:        enrollmentClient,
		enrollmentVerifyBackoff: enrollmentVerifyBackoff,
		enrollmentEndpoint:      enrollmentEndpoint,
		device:                  device,
		deviceWriter:            deviceWriter,
		deviceStatusExporter:    deviceStatusExporter,
		deviceObserver:          deviceObserver,
		caFilePath:              caFilePath,
		managementEndpoint:      managementEndpoint,
		managementCertFilePath:  managementCertFilePath,
		agentKeyFilePath:        agentKeyFilePath,
		enrollmentCSR:           enrollmentCSR,
		logPrefix:               logPrefix,
	}

	c.queue = workqueue.NewNamedRateLimitingQueue(workqueue.NewMaxOfRateLimiter(
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(updateDelay), 1)},
		workqueue.NewItemExponentialFailureRateLimiter(1*time.Second, maxUpdateBackoff)), "device-config")

	return c
}

func (c *ConfigController) sync(ctx context.Context, device *v1alpha1.Device) error {
	deviceStatus := c.deviceStatusExporter.Get(ctx)
	// ensure the device is enrolled
	if err := c.ensureDeviceEnrollment(ctx, device); err != nil {
		klog.Warningf("%s enrollment did not succeed: %v", c.logPrefix, err)
		return err
	}

	var conditions []v1alpha1.Condition
	// post enrollment update status
	deviceCondition := v1alpha1.Condition{
		Type:   "Enrolled",
		Status: v1alpha1.ConditionStatusTrue,
	}
	conditions = append(conditions, deviceCondition)
	deviceStatus.Conditions = &conditions
	// update the device status once if enrolled
	if !c.isDeviceEnrolled() {
		_, updateErr := c.managementClient.UpdateDeviceStatus(ctx, *device.Metadata.Name, deviceStatus)
		if updateErr != nil {
			klog.Errorf("%sfailed to update device status: %v", c.logPrefix, updateErr)
			if !c.isDeviceEnrolled() {
				return updateErr
			}
		}
	}

	// ensure the device is configured
	if err := c.ensureConfig(ctx, device); err != nil {
		klog.Errorf("%s configuration did not succeed: %v", c.logPrefix, err)
		errMsg := err.Error()

		// TODO: better status
		condition := v1alpha1.Condition{
			Type:    "Configured",
			Status:  v1alpha1.ConditionStatusFalse,
			Message: &errMsg,
		}
		conditions = append(conditions, condition)
		_, updateErr := c.managementClient.UpdateDeviceStatus(ctx, *device.Metadata.Name, deviceStatus)
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
	deviceStatus.Conditions = &conditions

	_, updateErr := c.managementClient.UpdateDeviceStatus(ctx, *device.Metadata.Name, deviceStatus)
	if updateErr != nil {
		klog.Errorf("%sfailed to update device status: %v", c.logPrefix, updateErr)
		return updateErr
	}

	return nil
}

func (c *ConfigController) Run(ctx context.Context) {
	klog.Infof("%sstarting device config controller", c.logPrefix)
	defer klog.Infof("%sstopping device config controller", c.logPrefix)

	err := wait.PollInfinite(time.Second, func() (bool, error) {
		if c.deviceStatusExporter.HasSynced(ctx) {
			return true, nil
		}
		return false, nil
	})

	if err != nil {
		klog.Errorf("%sfailed to sync device status: %v", c.logPrefix, err)
		return
	}

	go wait.UntilWithContext(ctx, c.inform, time.Minute)
	go wait.UntilWithContext(ctx, c.worker, time.Second)

	<-ctx.Done()
}

type Ignition struct {
	Raw  json.RawMessage `json:"inline"`
	Name string          `json:"name"`
}

func (c *ConfigController) ensureConfig(_ context.Context, device *v1alpha1.Device) error {
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

func (c *ConfigController) inform(ctx context.Context) {
	observedDevice := c.deviceObserver.Get(ctx)
	existingDevice := c.device.Get(ctx)
	if !equality.Semantic.DeepEqual(existingDevice.Spec, observedDevice.Spec) {
		klog.V(4).Infof("%s device changed, syncing", c.logPrefix)
	}

	// add regardless of change let the queue handle the rest
	c.queue.AddRateLimited(observedDevice)
}

func (c *ConfigController) worker(ctx context.Context) {
	for c.processNext(ctx) {
	}
}

func (c *ConfigController) processNext(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.sync(ctx, key.(*v1alpha1.Device))
	c.handleErr(err, key)

	return true
}

func (c *ConfigController) handleErr(err error, key interface{}) {
	if err == nil {
		// work is done
		c.queue.Forget(key)
		return
	}

	klog.V(2).Infof("Error syncing device %v (retries %d): %v", key, c.queue.NumRequeues(key), err)
	c.queue.AddRateLimited(key)
}
