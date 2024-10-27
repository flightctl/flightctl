package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
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

type listQuery struct {
	dest any
}

func ListQuery(model any) *listQuery {
	return &listQuery{dest: model}
}

func (lq *listQuery) Build(ctx context.Context, db *gorm.DB, orgId uuid.UUID, listParams ListParams) (*gorm.DB, error) {
	var err error
	query := db.Model(lq.dest)
	query = query.Where("org_id = ?", orgId)

	var invertLabels bool
	if listParams.InvertLabels != nil && *listParams.InvertLabels {
		invertLabels = true
	}
	query = LabelSelectionQuery(query, listParams.Labels, invertLabels)
	if query, err = LabelMatchExpressionsQuery(query, listParams.LabelMatchExpressions); err != nil {
		return nil, err
	}
	if query, err = AnnotationsMatchExpressionsQuery(query, listParams.AnnotationsMatchExpressions); err != nil {
		return nil, err
	}

	query = FieldFilterSelectionQuery(query, listParams.Filter)

	queryStr, args := createOrQuery("owner", listParams.Owners)
	if len(queryStr) > 0 {
		query = query.Where(queryStr, args...)
	}

	if listParams.FleetName != nil {
		query = query.Where("fleet_name = ?", *listParams.FleetName)
	}

	if listParams.FieldSelector != nil {
		fs, err := selector.NewFieldSelector(lq.dest)
		if err != nil {
			return nil, err
		}

		q, p, err := fs.Parse(ctx, listParams.FieldSelector)
		if err != nil {
			return nil, err
		}
		query = query.Where(q, p...)
	}

	resolver, err := selector.SelectorFieldResolver(lq.dest)
	if err != nil {
		return nil, err
	}

	if listParams.SortBy != nil {
		// Resolve name from the SortBy field, which might correspond to multiple fields.
		fields, err := resolver.ResolveNames(listParams.SortBy.FieldName)
		if err != nil {
			return nil, err
		}
		for _, name := range fields {
			query = query.Order(fmt.Sprintf("%s %s", createParamsFromKey(name),
				strings.ToLower(string(listParams.SortBy.Order))))
		}
	}

	return query, nil
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

func matchExpressionQueryAndArgs(matchExpression api.MatchExpression, colName string) (string, []any) {
	var params []string
	var args []any

	for _, val := range *matchExpression.Values {
		params = append(params, "?")
		args = append(args, matchExpression.Key+"="+val)
	}

	return fmt.Sprintf("%s && ARRAY[%s]", colName, strings.Join(params, ",")), args
}

func matchInQuery(query *gorm.DB, matchExpression api.MatchExpression, colName string) *gorm.DB {
	whereStr, args := matchExpressionQueryAndArgs(matchExpression, colName)
	return query.Where(whereStr, args...)
}

func matchNotInQuery(query *gorm.DB, matchExpression api.MatchExpression, colName string) *gorm.DB {
	whereStr, args := matchExpressionQueryAndArgs(matchExpression, colName)
	return query.Not(whereStr, args...)
}

func existsQuery(colName string) string {
	return fmt.Sprintf("exists (select 1 from unnest(%s) as element where element like ?)", colName)
}

func matchExistsQuery(query *gorm.DB, matchExpression api.MatchExpression, colName string) *gorm.DB {
	return query.Where(existsQuery(colName), matchExpression.Key+"=%")
}

func matchDoesNotExistQuery(query *gorm.DB, matchExpression api.MatchExpression, colName string) *gorm.DB {
	return query.Not(existsQuery(colName), matchExpression.Key+"=%")
}

func matchExpressionQuery(query *gorm.DB, matchExpression api.MatchExpression, colName string) (*gorm.DB, error) {
	switch matchExpression.Operator {
	case api.In:
		return matchInQuery(query, matchExpression, colName), nil
	case api.NotIn:
		return matchNotInQuery(query, matchExpression, colName), nil
	case api.Exists:
		return matchExistsQuery(query, matchExpression, colName), nil
	case api.DoesNotExist:
		return matchDoesNotExistQuery(query, matchExpression, colName), nil
	default:
		return nil, fmt.Errorf("unexpected operator %s", matchExpression.Operator)
	}
}

func matchExpressionsQuery(query *gorm.DB, matchExpressions api.MatchExpressions, colName string) (*gorm.DB, error) {
	var err error
	for _, me := range matchExpressions {
		if query, err = matchExpressionQuery(query, me, colName); err != nil {
			return nil, err
		}
	}
	return query, nil

}

func LabelMatchExpressionsQuery(query *gorm.DB, matchExpressions api.MatchExpressions) (*gorm.DB, error) {
	return matchExpressionsQuery(query, matchExpressions, "labels")
}

func AnnotationsMatchExpressionsQuery(query *gorm.DB, matchExpressions api.MatchExpressions) (*gorm.DB, error) {
	return matchExpressionsQuery(query, matchExpressions, "annotations")
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
