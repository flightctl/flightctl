package tasks

import (
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
)

const (
	accessibleConditionType    = "Accessible"
	resourceParseConditionType = "ResourceParse"
	syncedConditionType        = "Synced"
)

func addRepoNotFoundCondition(resSync *model.ResourceSync, err error) {
	addCondition(resSync, accessibleConditionType, "accessible", "repository resource not found", err)
}

func addRepoAccessCondition(resSync *model.ResourceSync, err error) {
	addCondition(resSync, accessibleConditionType, "accessible", "failed to clone repository", err)
}

func addPathAccessCondition(resSync *model.ResourceSync, err error) {
	addCondition(resSync, accessibleConditionType, "accessible", "path not found in repository", err)
}

func addResourceParseCondition(resSync *model.ResourceSync, err error) {
	addCondition(resSync, resourceParseConditionType, "Success", "Fail", err)
}

func addSyncedCondition(resSync *model.ResourceSync, err error) {
	addCondition(resSync, syncedConditionType, "Success", "Fail", err)
}

func addCondition(resSync *model.ResourceSync, conditionType string, okReason string, failReason string, err error) {
	conditions, prevCondition := extractPrevConditionByType(resSync, conditionType)
	timestamp := util.TimeStampStringPtr()
	var lastTransitionTime *string
	if prevCondition != nil {
		lastTransitionTime = prevCondition.LastTransitionTime
	} else {
		lastTransitionTime = timestamp
	}
	condition := api.ResourceSyncCondition{
		Type:               conditionType,
		LastTransitionTime: lastTransitionTime,
	}

	if err == nil {
		condition.Status = api.True
		condition.Reason = util.StrToPtr(okReason)
		condition.Message = util.StrToPtr(okReason)
	} else {
		condition.Status = api.False
		condition.Reason = util.StrToPtr(failReason)
		condition.Message = util.StrToPtr(err.Error())
	}
	conditions = append(conditions, condition)
	if resSync.Status == nil {
		resSync.Status = model.MakeJSONField(api.ResourceSyncStatus{Conditions: &conditions})
	} else {
		resSync.Status.Data.Conditions = &conditions
	}
}

func extractPrevConditionByType(resSync *model.ResourceSync, conditionType string) ([]api.ResourceSyncCondition, *api.ResourceSyncCondition) {
	if resSync.Status == nil {
		resSync.Status = &model.JSONField[api.ResourceSyncStatus]{
			Data: api.ResourceSyncStatus{
				Conditions: &[]api.ResourceSyncCondition{},
			},
		}
		return []api.ResourceSyncCondition{}, nil
	}
	if resSync.Status.Data.Conditions == nil {
		return []api.ResourceSyncCondition{}, nil
	}
	conditions := make([]api.ResourceSyncCondition, len(*resSync.Status.Data.Conditions))
	copy(conditions, *resSync.Status.Data.Conditions)
	for i, c := range *resSync.Status.Data.Conditions {
		if c.Type == conditionType {
			conditions = append(conditions[:i], conditions[i+1:]...)
			return conditions, &c
		}
	}
	return conditions, nil
}
