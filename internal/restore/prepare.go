package restore

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/sirupsen/logrus"
)

// PrepareDevices performs post-restoration device preparation: clear KV store,
// update device and enrollment request annotations, add awaiting-reconnection keys,
// and create a system restored event in the store.
func PrepareDevices(ctx context.Context, rs *RestoreStore, kv kvstore.KVStore, log logrus.FieldLogger) (devicesUpdated int64, err error) {
	log.Info("Starting post-restoration device preparation")

	if kv != nil {
		log.Info("Clearing KV store after restoration")
		if err := kv.DeleteAllKeys(ctx); err != nil {
			log.WithError(err).Error("Failed to clear KV store")
			return 0, fmt.Errorf("failed to clear KV store: %w", err)
		}
		log.Info("KV store cleared successfully")
	} else {
		log.Warn("KV store not available, skipping clear")
	}

	log.Info("Updating device annotations and clearing lastSeen timestamps")
	devicesUpdated, err = rs.PrepareDevicesAfterRestore(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to prepare devices after restore")
		return 0, fmt.Errorf("failed to prepare devices after restore: %w", err)
	}

	log.Info("Updating enrollment request annotations for non-approved requests")
	enrollmentRequestsUpdated, err := rs.PrepareEnrollmentRequestsAfterRestore(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to prepare enrollment requests after restore")
		return 0, fmt.Errorf("failed to prepare enrollment requests after restore: %w", err)
	}

	organizations, err := rs.ListOrganizations(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to get organizations for awaiting reconnection keys")
		return 0, fmt.Errorf("failed to get organizations for awaiting reconnection keys: %w", err)
	}
	log.Infof("Adding awaiting reconnection keys for %d organizations", len(organizations))

	awaitingReconnectionKeysAdded := 0
	if kv != nil {
		for _, o := range organizations {
			deviceNames, err := rs.GetAllDeviceNames(ctx, o.ID)
			if err != nil {
				log.WithError(err).Errorf("Failed to get device names for organization %s", o.ID)
				continue
			}
			for _, deviceName := range deviceNames {
				key := kvstore.AwaitingReconnectionKey{
					OrgID:      o.ID,
					DeviceName: deviceName,
				}
				_, err := kv.SetNX(ctx, key.ComposeKey(), []byte("true"))
				if err != nil {
					log.WithError(err).Errorf("Failed to add awaiting reconnection key for device %s in org %s", deviceName, o.ID)
					continue
				}
				awaitingReconnectionKeysAdded++
			}
		}
	}

	log.Infof("Post-restoration device preparation completed successfully. Updated %d devices, %d enrollment requests. Added %d awaiting reconnection keys across %d organizations.",
		devicesUpdated, enrollmentRequestsUpdated, awaitingReconnectionKeysAdded, len(organizations))

	event := domain.GetBaseEvent(ctx,
		domain.SystemKind,
		domain.SystemComponentDB,
		domain.EventReasonSystemRestored,
		fmt.Sprintf("System restored successfully. Updated %d devices for post-restoration preparation.", devicesUpdated),
		nil,
	)
	if event != nil {
		if err := rs.CreateEvent(ctx, org.DefaultID, event); err != nil {
			log.WithError(err).Error("Failed to create system restored event")
		} else {
			log.Info("System restored event created successfully")
		}
	}
	return devicesUpdated, nil
}
