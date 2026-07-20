package authprovider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Store interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, authProvider *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, error)
	Update(ctx context.Context, orgId uuid.UUID, authProvider *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, authProvider *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.AuthProvider, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.AuthProviderList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, error)
	GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*domain.AuthProvider, error)
	GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*domain.AuthProvider, error)

	// Used by domain metrics
	Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error)
	CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]store.CountByOrgResult, error)

	// ListAll lists all auth providers without org filtering
	ListAll(ctx context.Context, listParams store.ListParams) (*domain.AuthProviderList, error)
}

type AuthProviderStore struct {
	dbHandler           *gorm.DB
	log                 logrus.FieldLogger
	genericStore        *store.GenericStore[*model.AuthProvider, model.AuthProvider, domain.AuthProvider, domain.AuthProviderList]
	eventCallbackCaller store.EventCallbackCaller
}

// Make sure we conform to the Store interface
var _ Store = (*AuthProviderStore)(nil)

func NewAuthProviderStore(db *gorm.DB, log logrus.FieldLogger) Store {
	genericStore := store.NewGenericStore[*model.AuthProvider, model.AuthProvider, domain.AuthProvider, domain.AuthProviderList](
		db,
		log,
		model.NewAuthProviderFromApiResource,
		(*model.AuthProvider).ToApiResource,
		model.AuthProvidersToApiResource,
	)

	return &AuthProviderStore{
		dbHandler:           db,
		log:                 log,
		genericStore:        genericStore,
		eventCallbackCaller: store.CallEventCallback(domain.AuthProviderKind, log),
	}
}

func (s *AuthProviderStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.AuthProvider{}); err != nil {
		return err
	}

	// Create unique partial index for OIDC providers (issuer + clientId)
	// This ensures no duplicate OIDC providers across all organizations
	if err := s.createOIDCUniqueIndex(db); err != nil {
		return err
	}

	// Create unique partial index for OAuth2 providers (userinfoUrl + clientId)
	// This ensures no duplicate OAuth2 providers across all organizations
	if err := s.createOAuth2UniqueIndex(db); err != nil {
		return err
	}

	return nil
}

func (s *AuthProviderStore) createOIDCUniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.AuthProvider{}, store.ConstraintAuthProviderOIDCUnique) {
		if db.Dialector.Name() == "postgres" {
			// Create unique partial index on (spec->>'issuer', spec->>'clientId')
			// Only for OIDC providers (where providerType = 'oidc')
			return db.Exec(`
				CREATE UNIQUE INDEX ` + store.ConstraintAuthProviderOIDCUnique + `
				ON auth_providers ((spec->>'issuer'), (spec->>'clientId'))
				WHERE spec->>'providerType' = 'oidc'
			`).Error
		}
	}
	return nil
}

func (s *AuthProviderStore) createOAuth2UniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.AuthProvider{}, store.ConstraintAuthProviderOAuth2Unique) {
		if db.Dialector.Name() == "postgres" {
			// Create unique partial index on (spec->>'userinfoUrl', spec->>'clientId')
			// Only for OAuth2 providers (where providerType = 'oauth2')
			return db.Exec(`
				CREATE UNIQUE INDEX ` + store.ConstraintAuthProviderOAuth2Unique + `
				ON auth_providers ((spec->>'userinfoUrl'), (spec->>'clientId'))
				WHERE spec->>'providerType' = 'oauth2'
			`).Error
		}
	}
	return nil
}

func (s *AuthProviderStore) Create(ctx context.Context, orgId uuid.UUID, resource *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, error) {
	provider, err := s.genericStore.Create(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, provider, true, err)
	return provider, err
}

func (s *AuthProviderStore) Update(ctx context.Context, orgId uuid.UUID, resource *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, error) {
	newProvider, oldProvider, err := s.genericStore.Update(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldProvider, newProvider, false, err)
	return newProvider, err
}

func (s *AuthProviderStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, bool, error) {
	newProvider, oldProvider, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldProvider, newProvider, created, err)
	return newProvider, created, err
}

func (s *AuthProviderStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.AuthProvider, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *AuthProviderStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.AuthProviderList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *AuthProviderStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *AuthProviderStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error {
	deleted, err := s.genericStore.Delete(ctx, model.AuthProvider{Resource: model.Resource{OrgID: orgId, Name: name}})
	if deleted && eventCallback != nil {
		s.eventCallbackCaller(ctx, eventCallback, orgId, name, nil, nil, false, nil)
	}
	return err
}

func (s *AuthProviderStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}

func (s *AuthProviderStore) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	query, err := store.ListQuery(&model.AuthProvider{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return 0, err
	}
	var authProvidersCount int64
	if err := query.Count(&authProvidersCount).Error; err != nil {
		return 0, store.ErrorFromGormError(err)
	}
	return authProvidersCount, nil
}

func (s *AuthProviderStore) CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]store.CountByOrgResult, error) {
	var query *gorm.DB
	var err error

	if orgId != nil {
		query, err = store.ListQuery(&model.AuthProvider{}).BuildNoOrder(ctx, s.getDB(ctx), *orgId, store.ListParams{})
	} else {
		// When orgId is nil, we don't filter by org_id
		query = s.getDB(ctx).Model(&model.AuthProvider{})
	}

	if err != nil {
		return nil, err
	}

	var results []store.CountByOrgResult
	if err := query.Select("org_id, COUNT(*) as count").Group("org_id").Scan(&results).Error; err != nil {
		return nil, store.ErrorFromGormError(err)
	}

	return results, nil
}

func (s *AuthProviderStore) ListAll(ctx context.Context, listParams store.ListParams) (*domain.AuthProviderList, error) {
	var resourceList []model.AuthProvider
	var nextContinue *string
	var numRemaining *int64

	// Build query without org filtering
	query := s.getDB(ctx).Model(&model.AuthProvider{})

	// Apply field selector if present
	if listParams.FieldSelector != nil {
		q, p, err := listParams.FieldSelector.Parse(ctx, nil)
		if err != nil {
			return nil, err
		}
		query = query.Where(q, p...)
	}

	// Apply label selector if present
	if listParams.LabelSelector != nil {
		q, p, err := listParams.LabelSelector.Parse(ctx, selector.NewHiddenSelectorName("metadata.labels"), nil)
		if err != nil {
			return nil, err
		}
		query = query.Where(q, p...)
	}

	// Apply annotation selector if present
	if listParams.AnnotationSelector != nil {
		q, p, err := listParams.AnnotationSelector.Parse(ctx, selector.NewHiddenSelectorName("metadata.annotations"), nil)
		if err != nil {
			return nil, err
		}
		query = query.Where(q, p...)
	}

	// Apply sorting
	columns, order, _ := getSortColumns(listParams)
	orderExprs := lo.Map(columns, func(col store.SortColumn, _ int) string {
		return fmt.Sprintf("%s %s", col, order)
	})
	if len(orderExprs) > 0 {
		query = query.Order(strings.Join(orderExprs, ", "))
	}

	// Apply pagination if limit is set
	if listParams.Limit > 0 {
		query = store.AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue, listParams)
	}

	// Execute query
	result := query.Find(&resourceList)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}

	// Handle pagination continuation token
	if listParams.Limit > 0 && len(resourceList) > listParams.Limit {
		lastIndex := len(resourceList) - 1
		lastItem := resourceList[lastIndex]
		columns, _, _ := getSortColumns(listParams)

		// Build values for continue token
		continueValues := make([]string, len(columns))
		for i, col := range columns {
			switch col {
			case store.SortByName:
				continueValues[i] = lastItem.GetName()
			case store.SortByCreatedAt:
				continueValues[i] = lastItem.GetTimestamp().Format(time.RFC3339Nano)
			default:
				continueValues[i] = ""
			}
		}

		resourceList = resourceList[:lastIndex]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			// Count remaining items
			countQuery := s.getDB(ctx).Model(&model.AuthProvider{})
			if listParams.FieldSelector != nil {
				q, p, _ := listParams.FieldSelector.Parse(ctx, nil)
				countQuery = countQuery.Where(q, p...)
			}
			if listParams.LabelSelector != nil {
				q, p, _ := listParams.LabelSelector.Parse(ctx, selector.NewHiddenSelectorName("metadata.labels"), nil)
				countQuery = countQuery.Where(q, p...)
			}
			if listParams.AnnotationSelector != nil {
				q, p, _ := listParams.AnnotationSelector.Parse(ctx, selector.NewHiddenSelectorName("metadata.annotations"), nil)
				countQuery = countQuery.Where(q, p...)
			}
			numRemainingVal = store.CountRemainingItems(countQuery, continueValues, listParams)
		}

		nextContinue = store.BuildContinueString(continueValues, numRemainingVal)
		numRemaining = &numRemainingVal
	}

	// Convert to API resources. store.GenericStore does not expose its
	// internal listModelToAPI field across package boundaries, so this calls
	// the same conversion function directly (it is the exact function passed
	// into store.NewGenericStore above).
	apiList, err := model.AuthProvidersToApiResource(resourceList, nextContinue, numRemaining)
	return &apiList, err
}

func (s *AuthProviderStore) GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*domain.AuthProvider, error) {
	query := s.getDB(ctx).Model(&model.AuthProvider{}).Where("org_id = ? AND spec->>'issuer' = ? AND spec->>'clientId' = ?", orgId, issuer, clientId)
	var authProvider model.AuthProvider
	if err := query.First(&authProvider).Error; err != nil {
		return nil, store.ErrorFromGormError(err)
	}
	apiResource, err := authProvider.ToApiResource()
	return apiResource, err
}

func (s *AuthProviderStore) GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*domain.AuthProvider, error) {
	query := s.getDB(ctx).Model(&model.AuthProvider{}).Where("org_id = ? AND spec->>'authorizationUrl' = ?", orgId, authorizationUrl)
	var authProvider model.AuthProvider
	if err := query.First(&authProvider).Error; err != nil {
		return nil, store.ErrorFromGormError(err)
	}
	apiResource, err := authProvider.ToApiResource()
	return apiResource, err
}

// getSortColumns mirrors the unexported helper of the same name in
// internal/store (internal/store/common.go), which is not exported for
// cross-package reuse. Duplicated here since ListAll needs identical
// sort-column resolution logic within this package.
func getSortColumns(listParams store.ListParams) ([]store.SortColumn, store.SortOrder, string) {
	order := store.SortAsc
	if listParams.SortOrder != nil {
		order = *listParams.SortOrder
	}
	op := map[store.SortOrder]string{store.SortAsc: ">=", store.SortDesc: "<="}[order]

	columns := listParams.SortColumns
	if len(columns) == 0 {
		columns = []store.SortColumn{store.SortByName}
	}

	return columns, order, op
}
