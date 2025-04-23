package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const retryIterations = 10

type CreateOrUpdateMode string

func ErrorFromGormError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, gorm.ErrForeignKeyViolated):
		return flterrors.ErrResourceNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
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

type ListQueryOption func(*listQuery)

func WithSelectorResolver(resolver selector.Resolver) ListQueryOption {
	return func(q *listQuery) {
		q.resolver = resolver
	}
}

func WithSortDirective(sortDirective *string) ListQueryOption {
	return func(q *listQuery) {
		q.sortDirective = sortDirective
	}
}

type listQuery struct {
	dest          any
	resolver      selector.Resolver
	sortDirective *string
}

func ListQuery(dest any, opts ...ListQueryOption) *listQuery {
	q := &listQuery{dest: dest}

	for _, opt := range opts {
		opt(q)
	}

	// Set resolver if not provided
	if q.resolver == nil {
		resolver, err := selector.SelectorFieldResolver(q.dest)
		if err != nil {
			q.resolver = selector.EmptyResolver{}
		} else {
			q.resolver = resolver
		}
	}
	return q
}

func (lq *listQuery) BuildNoOrder(ctx context.Context, db *gorm.DB, orgId uuid.UUID, listParams ListParams) (*gorm.DB, error) {
	query := db.Model(lq.dest)

	query = query.Where(
		fmt.Sprintf("%s = ?", lq.resolveOrDefault(
			selector.NewHiddenSelectorName("metadata.orgid"), "org_id")), orgId)

	if listParams.FieldSelector != nil {
		q, p, err := listParams.FieldSelector.Parse(ctx, lq.resolver)
		if err != nil {
			return nil, err
		}
		query = query.Where(q, p...)
	}

	if listParams.LabelSelector != nil {
		q, p, err := listParams.LabelSelector.Parse(ctx,
			selector.NewHiddenSelectorName("metadata.labels"), lq.resolver)
		if err != nil {
			return nil, err
		}
		query = query.Where(q, p...)
	}

	if listParams.AnnotationSelector != nil {
		q, p, err := listParams.AnnotationSelector.Parse(ctx,
			selector.NewHiddenSelectorName("metadata.annotations"), lq.resolver)
		if err != nil {
			return nil, err
		}
		query = query.Where(q, p...)
	}

	return query, nil
}

func (lq *listQuery) Build(ctx context.Context, db *gorm.DB, orgId uuid.UUID, listParams ListParams) (*gorm.DB, error) {
	query, err := lq.BuildNoOrder(ctx, db, orgId, listParams)
	if err != nil {
		return nil, err
	}
	sortDirective := "name"
	if lq.sortDirective != nil {
		sortDirective = *lq.sortDirective
	}
	return query.Order(sortDirective), nil
}

func (lq *listQuery) resolveOrDefault(sn selector.SelectorName, d string) string {
	r, err := lq.resolver.ResolveFields(sn)
	if err != nil {
		return d
	}
	if len(r) > 0 && r[0] != nil {
		return r[0].FieldName
	}
	return d
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
