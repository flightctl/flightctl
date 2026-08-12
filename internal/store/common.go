package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/store/storeutil"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ErrMutateSkipWrite tells a store Mutate implementation to return success
// without persisting, when a handler's apply closure determines there is
// nothing to write.
var ErrMutateSkipWrite = errors.New("mutate skip write")

const retryIterations = 10

// ResourceMutation is the unit GenericStore.Mutate applies and persists.
// It may be richer than A (e.g. device rendered_* side channel).
// Resource is nil on the create path until apply assigns it.
type ResourceMutation[A any] interface {
	Resource() *A
	SetResource(*A)
	Clone() (ResourceMutation[A], error)
}

// Mutation is a ResourceMutation that only carries the API resource.
type Mutation[A any] struct {
	resource *A
}

func (m *Mutation[A]) Resource() *A { return m.resource }

func (m *Mutation[A]) SetResource(resource *A) { m.resource = resource }

func (m *Mutation[A]) Clone() (ResourceMutation[A], error) {
	cloned, err := CloneJSON(m.resource)
	if err != nil {
		return nil, err
	}
	return &Mutation[A]{resource: cloned}, nil
}

// RequireExisting returns ErrResourceNotFound when Resource is nil (create path).
func (m *Mutation[A]) RequireExisting() error {
	if m.resource == nil {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

// ApplyFunc mutates m on every Mutate attempt.
type ApplyFunc[A any] func(m ResourceMutation[A]) error

// MutateHooks customizes wrap and persist for a resource type.
// GenericStore.Mutate owns only the load/apply/retry control loop.
type MutateHooks[A any] struct {
	// Wrap turns a loaded API object into the typed mutation. Wrap(nil) is the create path.
	// Required.
	Wrap func(*A) ResourceMutation[A]
	// Load fetches the current resource when previous is unset / on retry.
	// Return (nil, nil) when not found (create path). When nil, GenericStore uses loadByName.
	Load func(ctx context.Context, orgId uuid.UUID, name string) (*A, error)
	// PersistCreate inserts after apply on the create path.
	// Defaults to GenericStore.Create on m.Resource().
	PersistCreate func(ctx context.Context, orgId uuid.UUID, m ResourceMutation[A]) (*A, error)
	// PersistUpdate writes after apply on the update path. Required.
	// Returns retry=true on conflict / deadlock so Mutate can reload and retry.
	PersistUpdate func(ctx context.Context, orgId uuid.UUID, name string, before *A, m ResourceMutation[A]) (retry bool, err error)
}

// CloneJSON deep-copies v via JSON marshal/unmarshal.
// Fields tagged json:"-" are not preserved.
func CloneJSON[A any](v *A) (*A, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out A
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetryUpdate runs fn until it returns retry=false or retryIterations is exhausted.
func RetryUpdate(fn func() (bool, error)) error {
	var err error
	for i := 0; i < retryIterations; i++ {
		retry, fnErr := fn()
		err = fnErr
		if !retry {
			return err
		}
	}
	return err
}

// AuthProvider database constraint names
const (
	ConstraintAuthProviderOIDCUnique   = "idx_authproviders_oidc_unique"
	ConstraintAuthProviderOAuth2Unique = "idx_authproviders_oauth2_unique"
)

type CreateOrUpdateMode string

type EventCallbackCaller func(ctx context.Context, callbackEvent EventCallback, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)

type EventCallback func(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error)

// ErrorFromGormError translates well-known gorm errors into domain-specific errors.
// Delegated to storeutil so that packages outside internal/store can reuse the logic.
func ErrorFromGormError(err error) error {
	return storeutil.ErrorFromGormError(err)
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

type listQuery struct {
	dest     any
	resolver selector.Resolver
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

func getSortColumns(listParams ListParams) ([]SortColumn, SortOrder, string) {
	order := SortAsc
	if listParams.SortOrder != nil {
		order = *listParams.SortOrder
	}
	op := map[SortOrder]string{SortAsc: ">=", SortDesc: "<="}[order]

	columns := listParams.SortColumns
	if len(columns) == 0 {
		columns = []SortColumn{SortByName}
	}

	return columns, order, op
}

func (lq *listQuery) Build(ctx context.Context, db *gorm.DB, orgId uuid.UUID, listParams ListParams) (*gorm.DB, error) {
	query, err := lq.BuildNoOrder(ctx, db, orgId, listParams)
	if err != nil {
		return nil, err
	}

	columns, order, _ := getSortColumns(listParams)
	orderExprs := lo.Map(columns, func(col SortColumn, _ int) string {
		return fmt.Sprintf("%s %s", col, order)
	})

	return query.Order(strings.Join(orderExprs, ", ")), nil
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

func AddPaginationToQuery(query *gorm.DB, limit int, cont *Continue, listParams ListParams) *gorm.DB {
	if limit == 0 {
		return query
	}

	query = query.Limit(limit)
	if cont == nil {
		return query
	}

	columns, _, op := getSortColumns(listParams)
	if len(columns) == 1 {
		return query.Where(
			fmt.Sprintf("%s %s ?", columns[0], op),
			cont.Names[0],
		)
	}

	// Multi-column tuple comparison
	columnExpr := fmt.Sprintf("(%s)", strings.Join(lo.Map(columns, func(col SortColumn, _ int) string {
		return string(col)
	}), ", "))

	placeholderExpr := strings.TrimRight(strings.Repeat("?, ", len(columns)), ", ")
	return query.Where(
		fmt.Sprintf("%s %s (%s)", columnExpr, op, placeholderExpr),
		lo.ToAnySlice(cont.Names)...,
	)
}

func CountRemainingItems(query *gorm.DB, nextValues []string, listParams ListParams) int64 {
	var count int64

	columns, _, op := getSortColumns(listParams)
	if len(columns) != len(nextValues) {
		return 0
	}

	columnExpr := fmt.Sprintf("(%s)", strings.Join(lo.Map(columns, func(c SortColumn, _ int) string {
		return string(c)
	}), ", "))

	placeholderExpr := strings.TrimRight(strings.Repeat("?, ", len(columns)), ", ")
	query = query.Where(
		fmt.Sprintf("%s %s (%s)", columnExpr, op, placeholderExpr),
		lo.ToAnySlice(nextValues)...,
	)

	query.Count(&count)
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

func retryCreateOrUpdate[A any](fn func() (*A, *A, bool, bool, error)) (*A, *A, bool, error) {
	var (
		a, b    *A
		created bool
		err     error
	)
	err = RetryUpdate(func() (bool, error) {
		var retry bool
		a, b, created, retry, err = fn()
		return retry, err
	})
	return a, b, created, err
}

// ApplyObjectMetaUpdate copies labels/annotations/owner from resource onto current,
// preserving nil fields unless listed in fieldsToUnset. Generation and ResourceVersion
// are server-managed and are not copied; callers that need RV preconditions should use
// CheckResourceVersionConflict (or equivalent) before applying.
func ApplyObjectMetaUpdate(current, resource *domain.ObjectMeta, fieldsToUnset []string) {
	if current == nil || resource == nil {
		return
	}
	unset := func(field string) bool { return lo.Contains(fieldsToUnset, field) }
	if resource.Labels != nil || unset("labels") {
		current.Labels = resource.Labels
	}
	if resource.Annotations != nil || unset("annotations") {
		current.Annotations = resource.Annotations
	}
	if resource.Owner != nil || unset("owner") {
		current.Owner = resource.Owner
	}
}

// MergeAnnotations merges annotations and applies deleteKeys.
func MergeAnnotations(current *map[string]string, annotations map[string]string, deleteKeys []string) map[string]string {
	merged := util.MergeLabels(util.EnsureMap(lo.FromPtr(current)), annotations)
	for _, deleteKey := range deleteKeys {
		delete(merged, deleteKey)
	}
	return merged
}

// Call callback if provided (but don't fail the operation if callback fails)
// with panic recovery to prevent callback failures from affecting the main operation
func SafeEventCallback(log logrus.FieldLogger, callback func()) {
	if callback == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("Event Callback panicked: %v\nBacktrace:\n%s", r, string(debug.Stack()))
		}
	}()
	callback()
}

func CallEventCallback(resourceKind domain.ResourceKind, log logrus.FieldLogger) func(ctx context.Context, callbackEvent EventCallback, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	return func(ctx context.Context, callbackEvent EventCallback, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
		if callbackEvent == nil {
			return
		}

		SafeEventCallback(log, func() {
			callbackEvent(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
		})
	}
}
