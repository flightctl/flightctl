package v1beta1

import (
	"encoding/json"
	"strings"
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
	require.Empty(t, reasonSchema.Value.Pattern)
	require.Zero(t, reasonSchema.Value.MinLength)
	require.Nil(t, reasonSchema.Value.MaxLength)
	require.Empty(t, reasonSchema.Value.AllOf)
	require.Empty(t, reasonSchema.Value.AnyOf)
	require.Empty(t, reasonSchema.Value.OneOf)
	require.Nil(t, reasonSchema.Value.Not)
	require.IsType(t, "", Condition{}.Reason)
	require.IsType(t, "", ConditionBase{}.Reason)
	conditionSchema := spec.Components.Schemas["Condition"].Value
	require.Len(t, conditionSchema.AllOf, 2)
	require.Equal(t, "#/components/schemas/ConditionBase", conditionSchema.AllOf[0].Ref)
	require.NotContains(t, conditionSchema.Properties, "reason")
	require.NotContains(t, conditionSchema.AllOf[1].Value.Properties, "reason")

	for _, conditionType := range spec.Components.Schemas["ConditionType"].Value.Enum {
		t.Run("When reading "+conditionType.(string)+" it should preserve an unknown reason", func(t *testing.T) {
			for _, reason := range []string{"FutureReasonNotInAnyCatalog", "", "future reason / new-producer"} {
				condition := Condition{
					Type: ConditionType(conditionType.(string)), Status: ConditionStatusFalse,
					LastTransitionTime: time.Now().UTC(), Reason: reason, Message: "Future condition",
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
				require.Equal(t, reason, decoded.Reason)
				var decodedBase ConditionBase
				require.NoError(t, json.Unmarshal(encoded, &decodedBase))
				require.Equal(t, reason, decodedBase.Reason)
			}
		})
	}
}

func TestConditionReasonCatalogMappings(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	mappings := []struct {
		resource      string
		conditionType ConditionType
		catalog       string
	}{
		{"EnrollmentRequest", ConditionTypeEnrollmentRequestApproved, "EnrollmentApprovalConditionReason"},
		{"EnrollmentRequest", ConditionTypeEnrollmentRequestTPMVerified, "TpmConditionReason"},
		{"CertificateSigningRequest", ConditionTypeCertificateSigningRequestApproved, "EnrollmentApprovalConditionReason"},
		{"CertificateSigningRequest", ConditionTypeCertificateSigningRequestDenied, "EnrollmentApprovalConditionReason"},
		{"CertificateSigningRequest", ConditionTypeCertificateSigningRequestFailed, "CertificateSigningRequestFailureConditionReason"},
		{"CertificateSigningRequest", ConditionTypeCertificateSigningRequestTPMVerified, "TpmConditionReason"},
		{"Repository", ConditionTypeRepositoryAccessible, "RepositoryConditionReason"},
		{"ResourceSync", ConditionTypeResourceSyncAccessible, "ResourceSyncConditionReason"},
		{"ResourceSync", ConditionTypeResourceSyncResourceParsed, "ResourceSyncConditionReason"},
		{"ResourceSync", ConditionTypeResourceSyncSynced, "ResourceSyncConditionReason"},
		{"LabelSyncMapping", ConditionTypeLabelSyncMappingReady, "LabelSyncMappingConditionReason"},
		{"Fleet", ConditionTypeFleetValid, "ValidationConditionReason"},
		{"Fleet", ConditionTypeFleetRolloutInProgress, "FleetRolloutConditionReason"},
		{"Fleet", ConditionTypeFleetDeltaPreparing, "DeltaPreparingConditionReason"},
		{"Device", ConditionTypeDeviceUpdating, "DeviceUpdateConditionReason"},
		{"Device", ConditionTypeDeviceSpecValid, "ValidationConditionReason"},
		{"Device", ConditionTypeDeviceMultipleOwners, "DeviceOwnershipConditionReason"},
		{"Device", ConditionTypeDeviceLabelsSynced, "DeviceLabelsConditionReason"},
		{"Device", ConditionTypeDeviceDecommissioning, "DeviceDecommissionConditionReason"},
		{"Device", ConditionTypeDeviceDeltaPreparing, "DeltaPreparingConditionReason"},
		{"Device", ConditionTypeDeviceEnrollmentHooks, "EnrollmentHooksConditionReason"},
		{"EnrollmentHookPolicy", ConditionType("Ready"), "EnrollmentHookPolicyConditionReason"},
	}
	coveredConditions := make(map[ConditionType]bool)
	coveredCatalogs := make(map[string]bool)
	for _, mapping := range mappings {
		t.Run("When reading "+mapping.resource+" "+string(mapping.conditionType)+" it should document its reason catalog", func(t *testing.T) {
			catalog := spec.Components.Schemas[mapping.catalog]
			require.NotNil(t, catalog)
			require.Contains(t, catalog.Value.Description, mapping.resource)
			require.Contains(t, catalog.Value.Description, string(mapping.conditionType))
		})
		coveredConditions[mapping.conditionType] = true
		coveredCatalogs[mapping.catalog] = true
	}
	for _, conditionType := range spec.Components.Schemas["ConditionType"].Value.Enum {
		require.True(t, coveredConditions[ConditionType(conditionType.(string))], "missing condition mapping for %s", conditionType)
	}
	for name := range spec.Components.Schemas {
		if strings.HasSuffix(name, "ConditionReason") {
			require.True(t, coveredCatalogs[name], "missing resource mapping for %s", name)
		}
	}
}
