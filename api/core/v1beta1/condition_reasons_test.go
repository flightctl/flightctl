package v1beta1

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKnownConditionReasonCatalogs(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	catalogs := map[string][]string{
		"EnrollmentApprovalConditionReason":               {"Approved", "ManuallyApproved", "Denied"},
		"CertificateSigningRequestFailureConditionReason": {"SigningFailed"},
		"TpmConditionReason":                              {TPMVerificationFailedReason, "TPMVerificationSucceeded", TPMChallengeRequiredReason, TPMChallengeFailedReason, TPMChallengeSucceededReason},
		"RepositoryConditionReason":                       {"Accessible", "Inaccessible"},
		"ResourceSyncConditionReason":                     {"accessible", "repository resource not found", "failed to clone repository", "path not found in repository", "success", "fail", ResourceSyncNewHashDetectedReason},
		"ValidationConditionReason":                       {"Valid", "Invalid"},
		"FleetRolloutConditionReason":                     {RolloutInactiveReason, RolloutActiveReason, RolloutSuspendedReason, RolloutWaitingReason},
		"DeviceUpdateConditionReason":                     {DeviceConditionBootstrapReason, string(UpdateStatePreparing), string(UpdateStateReadyToUpdate), string(UpdateStateApplyingUpdate), string(UpdateStateRebooting), string(UpdateStateUpdated), string(UpdateStateCanceled), string(UpdateStateError), string(UpdateStateRollingBack), string(UpdateStateRetrying)},
		"DeviceDecommissionConditionReason":               {string(DecommissionStateStarted), string(DecommissionStateComplete), string(DecommissionStateError)},
		"DeviceOwnershipConditionReason":                  {"MultipleOwners"},
		"DeviceLabelsConditionReason":                     {"Reconciled", "ReconciliationFailed"},
		"LabelSyncMappingConditionReason":                 {"Pending", "Degraded", "Success"},
		"DeltaPreparingConditionReason":                   {"DeltaGenerationInProgress"},
		"EnrollmentHooksConditionReason":                  {EnrollmentHooksReasonNotifyPending, EnrollmentHooksReasonPending, EnrollmentHooksReasonFailed, EnrollmentHooksReasonSucceeded, EnrollmentHooksReasonContinued, EnrollmentHooksReasonManualOverride},
		"EnrollmentHookPolicyConditionReason":             {"ValidationPassed"},
	}
	for name, expected := range catalogs {
		t.Run("When reading "+name+" it should catalog existing reasons", func(t *testing.T) {
			catalog := spec.Components.Schemas[name]
			require.NotNil(t, catalog)
			require.True(t, catalog.Value.Type.Is("string"))
			actual := make([]string, 0, len(catalog.Value.Enum))
			for _, value := range catalog.Value.Enum {
				reason, ok := value.(string)
				require.True(t, ok)
				actual = append(actual, reason)
			}
			require.ElementsMatch(t, expected, actual)
			require.Contains(t, catalog.Value.Description, "Non-exhaustive")
			require.Contains(t, catalog.Value.Description, "unknown reason strings")
		})
	}
	require.Equal(t, TPMChallengeRequiredReason, string(TpmConditionReasonChallengeRequired))
}

func TestConditionReasonsRemainOpen(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	reasonSchema := spec.Components.Schemas["ConditionBase"].Value.Properties["reason"]
	require.Empty(t, reasonSchema.Ref)
	require.True(t, reasonSchema.Value.Type.Is("string"))
	require.Empty(t, reasonSchema.Value.Enum)

	for _, conditionType := range spec.Components.Schemas["ConditionType"].Value.Enum {
		t.Run("When reading "+conditionType.(string)+" it should preserve an unknown reason", func(t *testing.T) {
			condition := Condition{
				Type: ConditionType(conditionType.(string)), Status: ConditionStatusFalse,
				LastTransitionTime: time.Now().UTC(), Reason: "FutureReasonNotInAnyCatalog", Message: "Future condition",
			}
			encoded, err := json.Marshal(condition)
			require.NoError(t, err)
			var value map[string]interface{}
			require.NoError(t, json.Unmarshal(encoded, &value))
			for _, name := range []string{"ConditionBase", "Condition"} {
				require.NoError(t, spec.Components.Schemas[name].Value.VisitJSON(value), name)
			}
			var decoded Condition
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			require.Equal(t, condition.Reason, decoded.Reason)
		})
	}
}
