package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type AuthProvider interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, authProvider *api.AuthProvider, eventCallback EventCallback) (*api.AuthProvider, error)
	CreateWithFromAPI(ctx context.Context, orgId uuid.UUID, authProvider *api.AuthProvider, fromAPI bool, eventCallback EventCallback) (*api.AuthProvider, error)
	Update(ctx context.Context, orgId uuid.UUID, authProvider *api.AuthProvider, eventCallback EventCallback) (*api.AuthProvider, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, authProvider *api.AuthProvider, eventCallback EventCallback) (*api.AuthProvider, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.AuthProvider, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.AuthProviderList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.AuthProvider, eventCallback EventCallback) (*api.AuthProvider, error)
	GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*api.AuthProvider, error)
	GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*api.AuthProvider, error)

	// Used by domain metrics
	Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error)
	CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]CountByOrgResult, error)

	// ListAll lists all auth providers without org filtering
	ListAll(ctx context.Context, listParams ListParams) (*api.AuthProviderList, error)
}

type AuthProviderStore struct {
	dbHandler           *gorm.DB
	log                 logrus.FieldLogger
	genericStore        *GenericStore[*model.AuthProvider, model.AuthProvider, api.AuthProvider, api.AuthProviderList]
	eventCallbackCaller EventCallbackCaller
}

// Make sure we conform to AuthProvider interface
var _ AuthProvider = (*AuthProviderStore)(nil)

func NewAuthProvider(db *gorm.DB, log logrus.FieldLogger) AuthProvider {
	genericStore := NewGenericStore[*model.AuthProvider, model.AuthProvider, api.AuthProvider, api.AuthProviderList](
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
		eventCallbackCaller: CallEventCallback(api.AuthProviderKind, log),
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
	if !db.Migrator().HasIndex(&model.AuthProvider{}, ConstraintAuthProviderOIDCUnique) {
		if db.Dialector.Name() == "postgres" {
			// Create unique partial index on (spec->>'issuer', spec->>'clientId')
			// Only for OIDC providers (where providerType = 'oidc')
			return db.Exec(`
				CREATE UNIQUE INDEX ` + ConstraintAuthProviderOIDCUnique + ` 
				ON auth_providers ((spec->>'issuer'), (spec->>'clientId'))
				WHERE spec->>'providerType' = 'oidc'
			`).Error
		}
	}
	return nil
}

func (s *AuthProviderStore) createOAuth2UniqueIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.AuthProvider{}, ConstraintAuthProviderOAuth2Unique) {
		if db.Dialector.Name() == "postgres" {
			// Create unique partial index on (spec->>'userinfoUrl', spec->>'clientId')
			// Only for OAuth2 providers (where providerType = 'oauth2')
			return db.Exec(`
				CREATE UNIQUE INDEX ` + ConstraintAuthProviderOAuth2Unique + ` 
				ON auth_providers ((spec->>'userinfoUrl'), (spec->>'clientId'))
				WHERE spec->>'providerType' = 'oauth2'
			`).Error
		}
	}
	return nil
}

func (s *AuthProviderStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.AuthProvider, eventCallback EventCallback) (*api.AuthProvider, error) {
	provider, err := s.genericStore.Create(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, provider, true, err)
	return provider, err
}

func (s *AuthProviderStore) CreateWithFromAPI(ctx context.Context, orgId uuid.UUID, resource *api.AuthProvider, fromAPI bool, eventCallback EventCallback) (*api.AuthProvider, error) {
	provider, _, _, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, fromAPI, func(ctx context.Context, before, after *api.AuthProvider) error {
		// If there's an existing resource, return an error to enforce create-only behavior
		if before != nil {
			return flterrors.ErrDuplicateName
		}
		return nil
	})
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, provider, true, err)
	return provider, err
}

func (s *AuthProviderStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.AuthProvider, eventCallback EventCallback) (*api.AuthProvider, error) {
	newProvider, oldProvider, err := s.genericStore.Update(ctx, orgId, resource, nil, true, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldProvider, newProvider, false, err)
	return newProvider, err
}

func (s *AuthProviderStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.AuthProvider, eventCallback EventCallback) (*api.AuthProvider, bool, error) {
	newProvider, oldProvider, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, true, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldProvider, newProvider, created, err)
	return newProvider, created, err
}

func (s *AuthProviderStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.AuthProvider, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *AuthProviderStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.AuthProviderList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *AuthProviderStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *AuthProviderStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) error {
	deleted, err := s.genericStore.Delete(ctx, model.AuthProvider{Resource: model.Resource{OrgID: orgId, Name: name}})
	if deleted && eventCallback != nil {
		s.eventCallbackCaller(ctx, eventCallback, orgId, name, nil, nil, false, nil)
	}
	return err
}

func (s *AuthProviderStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.AuthProvider, eventCallback EventCallback) (*api.AuthProvider, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}

func (s *AuthProviderStore) Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error) {
	query, err := ListQuery(&model.AuthProvider{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return 0, err
	}
	var authProvidersCount int64
	if err := query.Count(&authProvidersCount).Error; err != nil {
		return 0, ErrorFromGormError(err)
	}
	return authProvidersCount, nil
}

func (s *AuthProviderStore) CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]CountByOrgResult, error) {
	var query *gorm.DB
	var err error

	if orgId != nil {
		query, err = ListQuery(&model.AuthProvider{}).BuildNoOrder(ctx, s.getDB(ctx), *orgId, ListParams{})
	} else {
		// When orgId is nil, we don't filter by org_id
		query = s.getDB(ctx).Model(&model.AuthProvider{})
	}

	if err != nil {
		return nil, err
	}

	var results []CountByOrgResult
	if err := query.Select("org_id, COUNT(*) as count").Group("org_id").Scan(&results).Error; err != nil {
		return nil, ErrorFromGormError(err)
	}

	return results, nil
}

func (s *AuthProviderStore) ListAll(ctx context.Context, listParams ListParams) (*api.AuthProviderList, error) {
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
	orderExprs := lo.Map(columns, func(col SortColumn, _ int) string {
		return fmt.Sprintf("%s %s", col, order)
	})
	if len(orderExprs) > 0 {
		query = query.Order(strings.Join(orderExprs, ", "))
	}

	// Apply pagination if limit is set
	if listParams.Limit > 0 {
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue, listParams)
	}

	// Execute query
	result := query.Find(&resourceList)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
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
			case SortByName:
				continueValues[i] = lastItem.GetName()
			case SortByCreatedAt:
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
			numRemainingVal = CountRemainingItems(countQuery, continueValues, listParams)
		}

		nextContinue = BuildContinueString(continueValues, numRemainingVal)
		numRemaining = &numRemainingVal
	}

	// Convert to API resources
	apiList, err := s.genericStore.listModelToAPI(resourceList, nextContinue, numRemaining)
	return &apiList, err
}

func (s *AuthProviderStore) GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*api.AuthProvider, error) {
	query := s.getDB(ctx).Model(&model.AuthProvider{}).Where("org_id = ? AND spec->>'issuer' = ? AND spec->>'clientId' = ?", orgId, issuer, clientId)
	var authProvider model.AuthProvider
	if err := query.First(&authProvider).Error; err != nil {
		return nil, ErrorFromGormError(err)
	}
	apiResource, err := authProvider.ToApiResource()
	return apiResource, err
}

func (s *AuthProviderStore) GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*api.AuthProvider, error) {
	query := s.getDB(ctx).Model(&model.AuthProvider{}).Where("org_id = ? AND spec->>'authorizationUrl' = ?", orgId, authorizationUrl)
	var authProvider model.AuthProvider
	if err := query.First(&authProvider).Error; err != nil {
		return nil, ErrorFromGormError(err)
	}
	apiResource, err := authProvider.ToApiResource()
	return apiResource, err
}
