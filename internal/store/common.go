package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

const retryIterations = 10

type CreateOrUpdateMode string

func ErrorFromGormError(err error) error {
	switch err {
	case nil:
		return nil
	case gorm.ErrRecordNotFound:
		return flterrors.ErrResourceNotFound
	case gorm.ErrDuplicatedKey:
		return flterrors.ErrDuplicateName
	default:
		return err
	}
}

type StatusCount struct {
	Category      string
	StatusSummary string
	Count         int64
}

type StatusCountList []StatusCount

func (s StatusCountList) List(status string) map[string]int64 {
	res := make(map[string]int64)

	for _, sType := range s {
		if strings.EqualFold(sType.Category, status) {
			res[sType.StatusSummary] += sType.Count
		}
	}
	return res
}

const (
	ModeCreateOnly     CreateOrUpdateMode = "create-only"
	ModeUpdateOnly     CreateOrUpdateMode = "update-only"
	ModeCreateOrUpdate CreateOrUpdateMode = "create-or-update"
)

func BuildBaseListQuery(query *gorm.DB, orgId uuid.UUID, listParams ListParams) *gorm.DB {
	query = query.Where("org_id = ?", orgId).Order("name")
	invertLabels := false
	if listParams.InvertLabels != nil && *listParams.InvertLabels {
		invertLabels = true
	}
	query = LabelSelectionQuery(query, listParams.Labels, invertLabels)
	query = FieldFilterSelectionQuery(query, listParams.Filter)

	queryStr, args := createOrQuery("owner", listParams.Owners)
	if len(queryStr) > 0 {
		query = query.Where(queryStr, args...)
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

func CountStatusList(ctx context.Context, query *gorm.DB, status ...string) (StatusCountList, error) {
	var statusCounts StatusCountList
	var statusQueries []string
	var params []interface{}

	baseQuery := query.Select("status")
	params = append(params, baseQuery)

	statusQuery := `
	SELECT
		(?) AS category,
		%s AS status_summary,
		COUNT(*) AS count
	FROM data
	GROUP BY status_summary`

	for _, field := range status {
		statusQueries = append(statusQueries, fmt.Sprintf(statusQuery, createParamsFromKey(field)))
		params = append(params, field)
	}

	// Combine the device query (with Common Table Expression) and the status queries
	queryAggregate := fmt.Sprintf(`
		WITH data AS (?)
		%s`, strings.Join(statusQueries, " UNION ALL "))

	if err := query.WithContext(ctx).Raw(queryAggregate, params...).Scan(&statusCounts).Error; err != nil {
		return nil, ErrorFromGormError(err)
	}

	return statusCounts, nil
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

	if resource.Generation != nil {
		ret = append(ret, "generation")
	}

	if resource.ResourceVersion != nil {
		ret = append(ret, "resource_version")
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

// FieldFilterSelectionQuery takes a GORM DB query and a map of search parameters. To search for a key-value pair in the
// in a JSON object use the key to reflect location in the JSON data and the value to reflect the value to search for.
// example map[string]string{"status.config.summary.status": "UpToDate"} will search status.config.summary.status for the
// value "UpToDate".
// To search for multiple values in the same field, separate the values with a comma.
func FieldFilterSelectionQuery(query *gorm.DB, fieldMap map[string][]string) *gorm.DB {
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

		orQuery, queryArgs := createOrQuery(createParamsFromKey(key), values)
		if len(orQuery) > 0 {
			queryParams = append(queryParams, orQuery)
			args = append(args, queryArgs...)
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
	params := ""
	for i, part := range parts {
		if i == 0 {
			params += part
		} else if i == len(parts)-1 {
			// prefix last part with the ->> operator for JSONB fetching text
			params += fmt.Sprintf(" ->> '%s'", part)
		} else {
			// prefix intermediate parts with the -> operator for JSONB
			params += fmt.Sprintf(" -> '%s'", part)
		}
	}
	return params
}

// createOrQuery can return empty `queryStr`/`args` (ie if `key` or `values` params are empty).
// The caller is expected to check the size of `queryStr`/`args` before constructing a GORM query.
func createOrQuery(key string, values []string) (string, []interface{}) {
	var queryStr string
	var queryParams []string
	var args []interface{}

	if key == "" {
		return queryStr, args
	}

	for _, val := range values {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		queryParams = append(queryParams, fmt.Sprintf("%s = ?", key))
		args = append(args, val)
	}

	if len(queryParams) > 0 {
		queryStr = strings.Join(queryParams, " OR ")
	}
	return queryStr, args
}

func getExistingRecord[R any](db *gorm.DB, name string, orgId uuid.UUID) (*R, error) {
	var existingRecord R
	if err := db.Where("name = ? and org_id = ?", name, orgId).First(&existingRecord).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, ErrorFromGormError(err)
	}
	return &existingRecord, nil
}

func retryCreateOrUpdate[A any](fn func() (*A, bool, bool, error)) (*A, bool, error) {
	var (
		a              *A
		created, retry bool
		err            error
	)
	i := 0
	for a, created, retry, err = fn(); retry && i < retryIterations; a, created, retry, err = fn() {
		i++
	}
	return a, created, err
}

func retryUpdate(fn func() (bool, error)) error {
	var (
		retry bool
		err   error
	)
	i := 0
	for retry, err = fn(); retry && i < retryIterations; retry, err = fn() {
		i++
	}
	return err
}

func configsAreEqual(c1, c2 *[]api.DeviceSpec_Config_Item) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 api.DeviceSpec_Config_Item, item2 api.DeviceSpec_Config_Item) bool {
		value1, err := item1.ValueByDiscriminator()
		if err != nil {
			return false
		}
		value2, err := item2.ValueByDiscriminator()
		if err != nil {
			return false
		}
		return reflect.DeepEqual(value1, value2)
	})
}

func applicationsAreEqual(c1, c2 *[]api.ApplicationSpec) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 api.ApplicationSpec, item2 api.ApplicationSpec) bool {
		type1, err := item1.Type()
		if err != nil {
			return false
		}
		type2, err := item2.Type()
		if err != nil {
			return false
		}

		if type1 != type2 {
			return false
		}
		switch type1 {
		case string(api.ImageApplicationProviderType):
			imageSpec1, err := item1.AsImageApplicationProvider()
			if err != nil {
				return false
			}
			imageSpec2, err := item2.AsImageApplicationProvider()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(imageSpec1, imageSpec2)
		default:
			return false
		}
	})
}

func resourcesAreEqual(c1, c2 *[]api.ResourceMonitor) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 api.ResourceMonitor, item2 api.ResourceMonitor) bool {
		value1, err := item1.ValueByDiscriminator()
		if err != nil {
			return false
		}
		value2, err := item2.ValueByDiscriminator()
		if err != nil {
			return false
		}
		return reflect.DeepEqual(value1, value2)
	})
}

func DeviceSpecsAreEqual(d1, d2 api.DeviceSpec) bool {
	// Check OS
	if !reflect.DeepEqual(d1.Os, d2.Os) {
		return false
	}

	// Check Config
	if !configsAreEqual(d1.Config, d2.Config) {
		return false
	}

	// Check Hooks
	if !reflect.DeepEqual(d1.Hooks, d2.Hooks) {
		return false
	}

	// Check Applications
	if !applicationsAreEqual(d1.Applications, d2.Applications) {
		return false
	}

	// Check Containers
	if !reflect.DeepEqual(d1.Containers, d2.Containers) {
		return false
	}

	// Check Systemd
	if !reflect.DeepEqual(d1.Systemd, d2.Systemd) {
		return false
	}

	// Check Resources
	if !resourcesAreEqual(d1.Resources, d2.Resources) {
		return false
	}

	return true
}

func FleetSpecsAreEqual(f1, f2 api.FleetSpec) bool {
	if !reflect.DeepEqual(f1.Selector, f2.Selector) {
		return false
	}

	if !reflect.DeepEqual(f1.Template.Metadata, f2.Template.Metadata) {
		return false
	}

	return DeviceSpecsAreEqual(f1.Template.Spec, f2.Template.Spec)
}
