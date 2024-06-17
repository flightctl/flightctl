package tasks

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
)

type TimeGetter func() time.Time
type DeviceLivenessChecker struct {
	log            logrus.FieldLogger
	store          store.Store
	getCurrentTime TimeGetter
}

func NewDeviceLivenessChecker(taskManager TaskManager) *DeviceLivenessChecker {
	return &DeviceLivenessChecker{
		log:            taskManager.log,
		store:          taskManager.store,
		getCurrentTime: time.Now,
	}
}

func (d *DeviceLivenessChecker) OverrideTimeGetterForTesting(timeGetter TimeGetter) {
	d.getCurrentTime = timeGetter
}

func (d *DeviceLivenessChecker) Poll() {
	reqid.OverridePrefix("device-liveness-checker")
	requestID := reqid.NextRequestID()
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, d.log)

	log.Info("Running DeviceLivenessChecker Polling")

	now := d.getCurrentTime()
	devices, err := d.store.Device().ListExpiredHeartbeats(ctx, now)
	if err != nil {
		log.Errorf("error fetching expired devices: %s", err)
		return
	}

	for i := range *devices {
		device := (*devices)[i]
		if device.Spec == nil || device.Spec.Data.Settings == nil || device.Status == nil || device.Status.Data.LastSeenAt == nil {
			continue
		}

		lastSeen, err := time.Parse(time.RFC3339, *device.Status.Data.LastSeenAt)
		if err != nil {
			d.log.Errorf("failed parsing last seen timestamp for device %s/%s: %v", device.OrgID, device.Name, err)
			continue
		}

		conditionsToSet := []api.Condition{}

		err = d.setHeartbeatConditionsIfNecessary(&device, device.Spec.Data.Settings.HeartbeatWarningTime, now, lastSeen, api.DeviceHeartbeatWarning, &conditionsToSet)
		if err != nil {
			d.log.Errorf("failed setting HeartbeatWarningTime for device %s/%s: %v", device.OrgID, device.Name, err)
			continue
		}
		err = d.setHeartbeatConditionsIfNecessary(&device, device.Spec.Data.Settings.HeartbeatErrorTime, now, lastSeen, api.DeviceHeartbeatError, &conditionsToSet)
		if err != nil {
			d.log.Errorf("failed setting HeartbeatErrorTime for device %s/%s: %v", device.OrgID, device.Name, err)
			continue
		}

		if len(conditionsToSet) > 0 {
			err = d.store.Device().SetServiceConditions(ctx, device.OrgID, device.Name, conditionsToSet)
			if err != nil {
				d.log.Errorf("failed setting heartbeat conditions for device %s/%s: %v", device.OrgID, device.Name, err)
			}
		}
	}
}

func (d *DeviceLivenessChecker) setHeartbeatConditionsIfNecessary(device *model.Device, expirationDurationStr *string, now time.Time, lastSeen time.Time, expirationType api.ConditionType, conditionsToSet *[]api.Condition) error {
	if expirationDurationStr == nil {
		return nil
	}

	expirationDuration, err := time.ParseDuration(*expirationDurationStr)
	if err != nil {
		return fmt.Errorf("failed parsing device expiration: %w", err)
	}

	expiredTime := lastSeen.Add(expirationDuration).Sub(now)
	if expiredTime < 0 {
		condition := api.Condition{
			Type:    expirationType,
			Status:  api.ConditionStatusTrue,
			Reason:  util.StrToPtr("Heartbeat expired"),
			Message: util.StrToPtr("Heartbeat expired"),
		}

		if device.ServiceConditions == nil {
			device.ServiceConditions = model.MakeJSONField(model.ServiceConditions{})
		}
		if device.ServiceConditions.Data.Conditions == nil {
			device.ServiceConditions.Data.Conditions = &[]api.Condition{}
		}
		changed := api.SetStatusCondition(device.ServiceConditions.Data.Conditions, condition)
		if changed {
			d.log.Infof("Setting device %s/%s condition %s because heartbeat expired %s ago", device.OrgID, device.Name, expirationType, expiredTime.Abs().Truncate(time.Second))
			*conditionsToSet = append(*conditionsToSet, condition)
		}
	}

	return nil
}
