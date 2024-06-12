package store

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func BuildBaseListQuery(query *gorm.DB, orgId uuid.UUID, listParams ListParams) *gorm.DB {
	query = query.Where("org_id = ?", orgId).Order("name")
	invertLabels := false
	if listParams.InvertLabels != nil && *listParams.InvertLabels {
		invertLabels = true
	}
	query = LabelSelectionQuery(query, listParams.Labels, invertLabels)
	query = ConditionSelectionQuery(query, listParams.Conditions)
	if listParams.Owner != nil {
		query = query.Where("owner = ?", *listParams.Owner)
	}
	if listParams.FleetName != nil {
		query = query.Where("fleet_name = ?", *listParams.FleetName)
	}
	return query
}

func AddPaginationToQuery(query *gorm.DB, limit int, cont *Continue) *gorm.DB {
	if limit == 0 {
		return query
	}
	query = query.Limit(limit)
	if cont != nil {
		query = query.Where("name >= ?", cont.Name)
	}

	return query
}

func CountRemainingItems(query *gorm.DB, lastItemName string) int64 {
	var count int64
	query.Where("name >= ?", lastItemName).Count(&count)
	return count
}

func GetNonNilFieldsFromResource(resource model.Resource) []string {
	ret := []string{}
	if resource.Generation != nil {
		ret = append(ret, "generation")
	}
	if resource.Labels != nil {
		ret = append(ret, "labels")
	}
	if resource.Owner != nil {
		ret = append(ret, "owner")
	}
	if resource.Annotations != nil {
		ret = append(ret, "annotations")
	}

	return ret
}

// LabelSelectionQuery applies a label-based selection query to the given GORM DB query.
// It takes a map of labels and a GORM DB query as input.
// The function returns the modified DB query.
func LabelSelectionQuery(query *gorm.DB, labels map[string]string, inverse bool) *gorm.DB {

	if len(labels) == 0 {
		return query
	}

	arrayLabels := util.LabelMapToArray(&labels)

	// we do this instead of constructing the query string directly because of the Where
	// function implementation, finding a ? in the string will trigger one path, @ in the
	// string will trigger another path that looks for a pre-stored  database query.
	arrayPlaceholders := []string{}
	arrayValues := []interface{}{}
	for _, v := range arrayLabels {
		arrayPlaceholders = append(arrayPlaceholders, "?")
		arrayValues = append(arrayValues, v)
	}

	queryString := fmt.Sprintf("labels @> ARRAY[%s]", strings.Join(arrayPlaceholders, ","))

	if inverse {
		return query.Not(queryString, arrayValues...)
	}
	return query.Where(queryString, arrayValues...)
}

// ConditionSelectionQuery applies a condition-based selection query to the
// given GORM DB query. To query for a condition reason the key should be in the
// format "<type>.<reason>".
func ConditionSelectionQuery(query *gorm.DB, conditions map[string]string) *gorm.DB {
	if len(conditions) == 0 {
		return query
	}

	for conditionType, conditionStatus := range conditions {
		conditionReason := parseConditionReasonFromType(conditionType)
		conditionQuery := `
        EXISTS (
            SELECT 1
            FROM jsonb_array_elements(devices.status->'conditions') AS condition
            WHERE condition->>'type' = ?
			AND condition->>'status' = ?
        `
		if conditionReason != "" {
			conditionQuery += "AND condition->>'reason' = ?"
		}
		conditionQuery += ")"
		if conditionReason != "" {
			query = query.Where(conditionQuery, conditionType, conditionStatus, conditionReason)
		} else {
			query = query.Where(conditionQuery, conditionType, conditionStatus)
		}
	}

	return query
}

// parseConditionReasonFromType extracts the reason from a condition type. The type key syntax is
// expected to be "<type>.<reason>". Where reason is the child of type.
func parseConditionReasonFromType(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}
