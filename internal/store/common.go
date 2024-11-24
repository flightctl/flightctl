package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
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
	case gorm.ErrRecordNotFound, gorm.ErrForeignKeyViolated:
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

type list struct {
	dest any
}

type listQuery struct {
	dest   any
	query  *gorm.DB
	params ListParams
}

func List(model any) *list {
	return &list{dest: model}
}

func (l *list) Build(ctx context.Context, db *gorm.DB, orgId uuid.UUID, listParams ListParams) (*listQuery, error) {
	var err error
	query := db.Model(l.dest)
	query.Where("org_id = ?", orgId)

	var invertLabels bool
	if listParams.InvertLabels != nil && *listParams.InvertLabels {
		invertLabels = true
	}
	LabelSelectionQuery(query, listParams.Labels, invertLabels)
	if query, err = LabelMatchExpressionsQuery(query, listParams.LabelMatchExpressions); err != nil {
		return nil, err
	}
	if query, err = AnnotationsMatchExpressionsQuery(query, listParams.AnnotationsMatchExpressions); err != nil {
		return nil, err
	}

	FieldFilterSelectionQuery(query, listParams.Filter)

	queryStr, args := createOrQuery("owner", listParams.Owners)
	if len(queryStr) > 0 {
		query.Where(queryStr, args...)
	}

	if listParams.FleetName != nil {
		query.Where("fleet_name = ?", *listParams.FleetName)
	}

	if listParams.FieldSelector != nil {
		fs, err := selector.NewFieldSelector(l.dest)
		if err != nil {
			return nil, err
		}

		q, p, err := fs.Parse(ctx, listParams.FieldSelector)
		if err != nil {
			return nil, err
		}
		query.Where(q, p...)
	}

	if listParams.Continue != nil {
		sortable, ok := l.dest.(model.Sortable)
		if !ok {
			return nil, fmt.Errorf("resource type %T is not sortable", l.dest)
		}

		sortSelectors := selector.NewSelectorFieldNameSet().Add("metadata.name")
		if listParams.SortBy != nil {
			sortSelectors.Add(listParams.SortBy.FieldName)
		}

		if sortSelectors.Size() != len(listParams.Continue.Cursor) {
			return nil, fmt.Errorf(
				"mismatch between continue cursor size (%d) and sort parameter size (%d)",
				len(listParams.Continue.Cursor), sortSelectors.Size(),
			)
		}

		var requirements []selector.SortRequirement
		for _, cursor := range listParams.Continue.Cursor {
			if !sortSelectors.Contains(cursor.Name) || !sortable.SortableSelectors().Contains(cursor.Name) {
				return nil, fmt.Errorf(
					"invalid selector '%s' in continue cursor; not part of the sort parameters",
					cursor.Name,
				)
			}
			requirements = append(requirements, cursor)
		}

		sortSelector, err := selector.NewSortSelector(l.dest)
		if err != nil {
			return nil, fmt.Errorf("error creating sort selector: %w", err)
		}

		q, v, err := sortSelector.Parse(ctx, requirements...)
		if err != nil {
			return nil, fmt.Errorf("error parsing sort requirements: %w", err)
		}
		query.Where(q, v...)
	}

	if listParams.SortBy != nil {
		sortable, ok := l.dest.(model.Sortable)
		if !ok {
			return nil, fmt.Errorf("resource type %T is not sortable", l.dest)
		}

		if !sortable.SortableSelectors().Contains(listParams.SortBy.FieldName) {
			supportedFields := sortable.SortableSelectors().List()
			sort.Slice(supportedFields, func(i, j int) bool {
				return supportedFields[i] < supportedFields[j]
			})

			return nil, selector.NewSelectorError(flterrors.ErrFieldSelectorUnknownSelector,
				fmt.Errorf("invalid sort selector '%s'; not supported for the resource. Supported selectors are: %v",
					listParams.SortBy.FieldName, supportedFields))
		}

		resolver, err := selector.SelectorFieldResolver(l.dest)
		if err != nil {
			return nil, fmt.Errorf("error resolving selector fields: %w", err)
		}

		fields, err := resolver.ResolveNames(listParams.SortBy.FieldName)
		if err != nil {
			return nil, fmt.Errorf("error resolving field names for sort selector '%s': %w", listParams.SortBy.FieldName, err)
		}

		switch len(fields) {
		case 0:
			return nil, fmt.Errorf("no fields resolved for sort selector '%s'", listParams.SortBy.FieldName)
		case 1:
			if fields[0] != "name" {
				query.Order(fmt.Sprintf("%s %s", createParamsFromKey(fields[0]),
					strings.ToLower(string(listParams.SortBy.Order)))).Order("name")
			} else {
				query.Order(fmt.Sprintf("%s %s", createParamsFromKey(fields[0]),
					strings.ToLower(string(listParams.SortBy.Order))))
			}
		default:
			return nil, fmt.Errorf("multiple fields resolved for sort selector '%s'; expected exactly one", listParams.SortBy.FieldName)
		}
	} else {
		query.Order("name")
	}

	return &listQuery{
		dest:   l.dest,
		query:  query,
		params: listParams,
	}, nil
}

func (lq *listQuery) Query() *gorm.DB {
	return lq.query
}

func (lq *listQuery) Limit(limit int) *listQuery {
	if limit > 0 {
		lq.query.Limit(limit)
	}
	return lq
}

func (lq *listQuery) Find(ctx context.Context, dest any) (*Continue, error) {
	// Validate that dest is a pointer to a slice
	destValue := reflect.ValueOf(dest)
	if destValue.Kind() != reflect.Ptr || destValue.Elem().Kind() != reflect.Slice {
		return nil, fmt.Errorf("dest must be a pointer to a slice")
	}

	// Perform the database query
	res := lq.query.WithContext(ctx).Find(dest)
	if res.Error != nil {
		return nil, res.Error
	}

	// Handle Sortable resources if applicable
	if _, ok := lq.dest.(model.Sortable); ok && res.RowsAffected > 0 {
		// Ensure the slice is not empty
		sliceValue := destValue.Elem()
		if sliceValue.Len() == 0 {
			return nil, fmt.Errorf("query returned rows, but slice is empty")
		}

		// Get the last item in the slice
		lastItem := sliceValue.Index(sliceValue.Len() - 1)
		if lastItem.Kind() != reflect.Ptr {
			// Use Addr() to get the address of the last item
			lastItem = lastItem.Addr()
		}

		sortable, ok := lastItem.Interface().(model.Sortable)
		if !ok {
			return nil, fmt.Errorf("result is not sortable")
		}

		// Create a SortSelector
		sortSelector, err := selector.NewSortSelector(lq.dest)
		if err != nil {
			return nil, err
		}

		// Prepare SortRequirements
		var requirements []selector.SortRequirement
		if lq.params.SortBy != nil {
			if lq.params.SortBy.FieldName != "metadata.name" {
				requirements = []selector.SortRequirement{
					NewResourceSelector(lq.params.SortBy.FieldName, selector.SortOrder(lq.params.SortBy.Order), sortable),
					NewResourceSelector("metadata.name", selector.Ascending, sortable),
				}
			} else {
				requirements = []selector.SortRequirement{
					NewResourceSelector(lq.params.SortBy.FieldName, selector.SortOrder(lq.params.SortBy.Order), sortable),
				}
			}
		} else {
			requirements = []selector.SortRequirement{
				NewResourceSelector("metadata.name", selector.Ascending, sortable),
			}
		}

		// Parse the SortSelector
		q, v, err := sortSelector.Parse(ctx, requirements...)
		if err != nil {
			return nil, err
		}

		// Count remaining items
		var remain int64
		res = lq.query.WithContext(ctx).Where(q, v...).Count(&remain)
		if res.Error != nil {
			return nil, res.Error
		}

		if remain > 0 {
			// Create the ListContinue response
			cont := &Continue{
				Version: CurrentContinueVersion,
				Count:   remain,
			}

			// Build the cursor selectors
			cont.Cursor = make([]*CursorSelector, len(requirements))
			for i, req := range requirements {
				cont.Cursor[i] = &CursorSelector{
					Name:      req.By(),
					Val:       req.Value(),
					SortOrder: req.Order(),
				}
			}
			return cont, nil
		}
	}
	return nil, nil
}

type ResourceSelector struct {
	Name  selector.SelectorName
	order selector.SortOrder
	dest  model.Sortable
}

func NewResourceSelector(name selector.SelectorName, order selector.SortOrder, dest model.Sortable) *ResourceSelector {
	return &ResourceSelector{name, order, dest}
}

func (rs *ResourceSelector) By() selector.SelectorName {
	return rs.Name
}
func (rs *ResourceSelector) Value() any {
	return rs.dest.ValueOf(rs.Name)
}

func (rs *ResourceSelector) Order() selector.SortOrder {
	return rs.order
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
