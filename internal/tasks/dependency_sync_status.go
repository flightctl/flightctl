package tasks

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var credentialPattern = regexp.MustCompile(`(?i)(password|token|secret|bearer|authorization)[=:\s]+\S+`)

// RefreshFleetDependencySyncStatus recomputes the DependenciesSynced condition
// and DependencySync status block for a single fleet. Called after each
// reconcile by the sync controllers with only the affected fleet name.
func RefreshFleetDependencySyncStatus(ctx context.Context, svc service.Service, log logrus.FieldLogger, orgId uuid.UUID, fleetName string, informerConnected *bool) {
	refsWithState, st := svc.ListDependencyRefsWithSyncState(ctx, orgId, &fleetName, nil)
	if st.Code != http.StatusOK {
		log.WithField("fleet", fleetName).Errorf("failed listing refs with sync state: %s", st.Message)
		return
	}

	if len(refsWithState) == 0 {
		condition := domain.Condition{
			Type:    domain.ConditionTypeFleetDependenciesSynced,
			Status:  domain.ConditionStatusTrue,
			Reason:  "NoDependencies",
			Message: "No external dependencies configured.",
		}
		if st := svc.UpdateFleetDependencySyncStatus(ctx, orgId, fleetName, []domain.Condition{condition}, nil); st.Code != http.StatusOK {
			log.WithField("fleet", fleetName).Errorf("failed clearing fleet dependency sync status: %s", st.Message)
		}
		return
	}

	condition, syncStatus := computeStatus(refsWithState, informerConnected, domain.ConditionTypeFleetDependenciesSynced)

	if st := svc.UpdateFleetDependencySyncStatus(ctx, orgId, fleetName, []domain.Condition{condition}, syncStatus); st.Code != http.StatusOK {
		log.WithField("fleet", fleetName).Errorf("failed updating fleet dependency sync status: %s", st.Message)
	}
}

// RefreshDeviceDependencySyncStatus recomputes the DependenciesSynced condition
// and DependencySync status block for a single device. Called after each
// reconcile by the sync controllers with only the affected device name.
func RefreshDeviceDependencySyncStatus(ctx context.Context, svc service.Service, log logrus.FieldLogger, orgId uuid.UUID, deviceName string, informerConnected *bool) {
	refsWithState, st := svc.ListDependencyRefsWithSyncState(ctx, orgId, nil, &deviceName)
	if st.Code != http.StatusOK {
		log.WithField("device", deviceName).Errorf("failed listing refs with sync state: %s", st.Message)
		return
	}

	if len(refsWithState) == 0 {
		condition := domain.Condition{
			Type:    domain.ConditionTypeDeviceDependenciesSynced,
			Status:  domain.ConditionStatusTrue,
			Reason:  "NoDependencies",
			Message: "No external dependencies configured.",
		}
		if st := svc.SetDeviceDependencySyncStatus(ctx, orgId, deviceName, []domain.Condition{condition}, nil); st.Code != http.StatusOK {
			log.WithField("device", deviceName).Errorf("failed clearing device dependency sync status: %s", st.Message)
		}
		return
	}

	condition, syncStatus := computeStatus(refsWithState, informerConnected, domain.ConditionTypeDeviceDependenciesSynced)

	if st := svc.SetDeviceDependencySyncStatus(ctx, orgId, deviceName, []domain.Condition{condition}, syncStatus); st.Code != http.StatusOK {
		log.WithField("device", deviceName).Errorf("failed updating device dependency sync status: %s", st.Message)
	}
}

// computeStatus derives the DependenciesSynced condition and DependencySyncStatus
// from the joined dependency refs + sync state rows.
func computeStatus(refs []model.DependencyRefWithSyncState, informerConnected *bool, conditionType domain.ConditionType) (domain.Condition, *domain.DependencySyncStatus) {
	var configRefs []domain.DependencySyncConfigRefStatus
	hasSecretRefs := false
	anyFailed := false
	var failedMessages []string

	var latestProbeTime *time.Time
	var latestSuccessfulProbeTime *time.Time

	for _, ref := range refs {
		isSecret := ref.RefType == "secret"
		if isSecret {
			hasSecretRefs = true
		}

		refStatus := domain.DependencySyncConfigRefStatusSynced
		var message string

		if ref.ProbeStatus != nil {
			switch *ref.ProbeStatus {
			case string(domain.DependencySyncConfigRefStatusProbeFailed):
				refStatus = domain.DependencySyncConfigRefStatusProbeFailed
				anyFailed = true
				if ref.ProbeMessage != nil {
					message = *ref.ProbeMessage
					failedMessages = append(failedMessages, message)
				}
			case string(domain.DependencySyncConfigRefStatusSynced):
				refStatus = domain.DependencySyncConfigRefStatusSynced
			default:
				refStatus = domain.DependencySyncConfigRefStatusProbeFailed
				message = fmt.Sprintf("unexpected probe status: %s", *ref.ProbeStatus)
				anyFailed = true
				failedMessages = append(failedMessages, message)
			}
		} else if ref.Fingerprint == nil {
			refStatus = domain.DependencySyncConfigRefStatusSynced
			message = "awaiting first probe"
		}

		if ref.LastCheckedAt != nil {
			if latestProbeTime == nil || ref.LastCheckedAt.After(*latestProbeTime) {
				latestProbeTime = ref.LastCheckedAt
			}
			if refStatus == domain.DependencySyncConfigRefStatusSynced {
				if latestSuccessfulProbeTime == nil || ref.LastCheckedAt.After(*latestSuccessfulProbeTime) {
					latestSuccessfulProbeTime = ref.LastCheckedAt
				}
			}
		}

		// Override secret refs when informer is disconnected
		if isSecret && informerConnected != nil && !*informerConnected {
			refStatus = domain.DependencySyncConfigRefStatusSecretWatchDisconnected
			message = "secret informer disconnected"
		}

		cfgRef := domain.DependencySyncConfigRefStatus{
			ConfigProviderName: ref.ConfigProviderName,
			Status:             refStatus,
			Fingerprint:        ref.Fingerprint,
			LastProbeTime:      ref.LastCheckedAt,
			LastUpdatedAt:      ref.LastChangeAt,
		}
		if message != "" {
			cfgRef.Message = lo.ToPtr(message)
		}
		configRefs = append(configRefs, cfgRef)
	}

	condition := buildCondition(conditionType, configRefs, hasSecretRefs, anyFailed, strings.Join(failedMessages, "; "), informerConnected)

	syncStatus := &domain.DependencySyncStatus{
		ConfigRefs:              &configRefs,
		LastProbeTime:           latestProbeTime,
		LastSuccessfulProbeTime: latestSuccessfulProbeTime,
	}

	return condition, syncStatus
}

func buildCondition(conditionType domain.ConditionType, configRefs []domain.DependencySyncConfigRefStatus, hasSecretRefs, anyFailed bool, failedMessage string, informerConnected *bool) domain.Condition {
	condition := domain.Condition{
		Type: conditionType,
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
	if err == nil {
		return ""
	}
	msg := err.Error()
	return credentialPattern.ReplaceAllString(msg, "$1=[REDACTED]")
}
