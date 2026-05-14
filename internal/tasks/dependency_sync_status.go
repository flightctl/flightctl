package tasks

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var credentialPattern = regexp.MustCompile(`(?i)(password|token|secret|bearer|authorization)[=: ]+\S+`)

// DependencySyncStatusUpdater computes and persists the DependenciesSynced
// condition and DependencySync status block for fleets and devices.
type DependencySyncStatusUpdater struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
}

func NewDependencySyncStatusUpdater(log logrus.FieldLogger, serviceHandler service.Service) *DependencySyncStatusUpdater {
	return &DependencySyncStatusUpdater{
		log:            log,
		serviceHandler: serviceHandler,
	}
}

// UpdateStatusForOrg enumerates all fleets/devices with dependency refs
// in the given org and updates their DependencySyncStatus accordingly.
// informerConnected is nil for git/HTTP callers (no opinion on informer
// state), true/false for secret callers.
func (u *DependencySyncStatusUpdater) UpdateStatusForOrg(ctx context.Context, orgId uuid.UUID, informerConnected *bool) {
	owners, st := u.serviceHandler.ListDistinctDependencyRefOwners(ctx, orgId)
	if st.Code != http.StatusOK {
		u.log.Errorf("failed listing dependency ref owners for org %s: %s", orgId, st.Message)
		return
	}

	for _, owner := range owners {
		if owner.FleetName != "" && owner.DeviceName == "" {
			u.updateFleet(ctx, orgId, owner.FleetName, informerConnected)
		} else if owner.DeviceName != "" {
			u.updateDevice(ctx, orgId, owner.DeviceName, informerConnected)
		}
	}
}

func (u *DependencySyncStatusUpdater) updateFleet(ctx context.Context, orgId uuid.UUID, fleetName string, informerConnected *bool) {
	refsWithState, st := u.serviceHandler.ListDependencyRefsWithSyncState(ctx, orgId, &fleetName, nil)
	if st.Code != http.StatusOK {
		u.log.WithField("fleet", fleetName).Errorf("failed listing refs with sync state: %s", st.Message)
		return
	}
	if len(refsWithState) == 0 {
		return
	}

	condition, syncStatus := computeStatus(refsWithState, informerConnected)

	if st := u.serviceHandler.UpdateFleetDependencySyncStatus(ctx, orgId, fleetName, []domain.Condition{condition}, syncStatus); st.Code != http.StatusOK {
		u.log.WithField("fleet", fleetName).Errorf("failed updating fleet dependency sync status: %s", st.Message)
	}
}

func (u *DependencySyncStatusUpdater) updateDevice(ctx context.Context, orgId uuid.UUID, deviceName string, informerConnected *bool) {
	refsWithState, st := u.serviceHandler.ListDependencyRefsWithSyncState(ctx, orgId, nil, &deviceName)
	if st.Code != http.StatusOK {
		u.log.WithField("device", deviceName).Errorf("failed listing refs with sync state: %s", st.Message)
		return
	}
	if len(refsWithState) == 0 {
		return
	}

	condition, syncStatus := computeStatus(refsWithState, informerConnected)

	if st := u.serviceHandler.SetDeviceDependencySyncStatus(ctx, orgId, deviceName, []domain.Condition{condition}, syncStatus); st.Code != http.StatusOK {
		u.log.WithField("device", deviceName).Errorf("failed updating device dependency sync status: %s", st.Message)
	}
}

// computeStatus derives the DependenciesSynced condition and DependencySyncStatus
// from the joined dependency refs + sync state rows.
func computeStatus(refs []model.DependencyRefWithSyncState, informerConnected *bool) (domain.Condition, *domain.DependencySyncStatus) {
	var configRefs []domain.DependencySyncConfigRefStatus
	hasSecretRefs := false
	anyFailed := false
	failedMessage := ""

	for _, ref := range refs {
		isSecret := ref.RefType == "secret"
		if isSecret {
			hasSecretRefs = true
		}

		refStatus := domain.DependencySyncConfigRefStatusSynced
		var message string
		var lastProbeTime *time.Time

		if ref.ProbeStatus != nil {
			switch *ref.ProbeStatus {
			case "ProbeFailed":
				refStatus = domain.DependencySyncConfigRefStatusProbeFailed
				anyFailed = true
				if ref.ProbeMessage != nil {
					message = *ref.ProbeMessage
					failedMessage = message
				}
			case "Synced":
				refStatus = domain.DependencySyncConfigRefStatusSynced
			}
		} else if ref.Fingerprint == nil {
			refStatus = domain.DependencySyncConfigRefStatusSynced
			message = "awaiting first probe"
		}

		if ref.LastCheckedAt != nil {
			lastProbeTime = ref.LastCheckedAt
		}

		// Override secret refs when informer is disconnected
		if isSecret && informerConnected != nil && !*informerConnected {
			refStatus = domain.DependencySyncConfigRefStatusSecretWatchDisconnected
			message = "secret informer disconnected"
		}

		cfgRef := domain.DependencySyncConfigRefStatus{
			ConfigProviderName: ref.ConfigProviderName,
			Status:             refStatus,
		}
		if message != "" {
			cfgRef.Message = lo.ToPtr(message)
		}
		if lastProbeTime != nil {
			cfgRef.LastProbeTime = lastProbeTime
		}
		configRefs = append(configRefs, cfgRef)
	}

	condition := buildCondition(configRefs, hasSecretRefs, anyFailed, failedMessage, informerConnected)

	syncStatus := &domain.DependencySyncStatus{
		ConfigRefs: &configRefs,
	}

	return condition, syncStatus
}

func buildCondition(configRefs []domain.DependencySyncConfigRefStatus, hasSecretRefs, anyFailed bool, failedMessage string, informerConnected *bool) domain.Condition {
	condition := domain.Condition{
		Type: domain.ConditionTypeFleetDependenciesSynced,
	}

	// Secret watch disconnected takes precedence
	if hasSecretRefs && informerConnected != nil && !*informerConnected {
		condition.Status = domain.ConditionStatusUnknown
		condition.Reason = "SecretWatchDisconnected"
		condition.Message = "Secret informer is disconnected; secret dependency status is unknown."
		return condition
	}

	if anyFailed {
		condition.Status = domain.ConditionStatusFalse
		condition.Reason = "ProbeFailed"
		condition.Message = "One or more dependency probes failed: " + failedMessage
		return condition
	}

	// Check for any non-synced refs
	for _, ref := range configRefs {
		if ref.Status == domain.DependencySyncConfigRefStatusSecretWatchDisconnected {
			condition.Status = domain.ConditionStatusUnknown
			condition.Reason = "SecretWatchDisconnected"
			condition.Message = "Secret informer is disconnected; secret dependency status is unknown."
			return condition
		}
	}

	condition.Status = domain.ConditionStatusTrue
	condition.Reason = "NoDrift"
	condition.Message = "All dependencies are synced."
	return condition
}

// sanitizeError strips credential-like patterns from error messages to
// prevent leaking secrets into events or status fields.
func sanitizeError(err error) string {
	msg := err.Error()
	return credentialPattern.ReplaceAllString(msg, "$1=[REDACTED]")
}
