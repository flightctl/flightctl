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
	query = StatusFilterSelectionQuery(query, listParams.Filter)
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

// StatusFilterSelectionQuery takes a GORM DB query and a map of search parameters. To search for a key-value pair in the
// in a JSON object use the key to reflect location in the JSON data and the value to reflect the value to search for.
// example map[string]string{"config.summary.status": "UpToDate"} will search config.summary.status for for the value "UpToDate".
// To search for multiple values in the same field, separate the values with a comma.
func StatusFilterSelectionQuery(query *gorm.DB, fieldMap map[string][]string) *gorm.DB {
	queryStr, args := createQueryFromFilterMap(fieldMap)
	if len(queryStr) > 0 {
		query = query.Where(queryStr, args...)
	}

	return query
}

func createQueryFromFilterMap(fieldMap map[string][]string) (string, []interface{}) {
	var queryParams []string
	var args []interface{}

	for key, values := range fieldMap {
		if key == "" || values == nil || len(values) == 0 {
			continue
		}

		for _, val := range values {
			val = strings.TrimSpace(val)
			if val == "" {
				continue
			}
			params := createParamsFromKey(key)
			queryParams = append(queryParams, fmt.Sprintf("%s = ?", params))
			args = append(args, val)
		}
	}

	var query string
	// join all query conditions with 'OR'
	if len(queryParams) > 0 {
		query = strings.Join(queryParams, " OR ")
	}

	return query, args
}

func createParamsFromKey(key string) string {
	parts := strings.Split(key, ".")
	params := "status"
	for i, part := range parts {
		if i == len(parts)-1 {
			// prefix last part with the ->> operator for JSONB fetching text
			params += fmt.Sprintf(" ->> '%s'", part)
		} else {
			// prefix intermediate parts with the -> operator for JSONB
			params += fmt.Sprintf(" -> '%s'", part)
		}
	}
	return params
}
