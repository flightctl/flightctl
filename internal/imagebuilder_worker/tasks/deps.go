package tasks

import (
	"context"
	"errors"
	"net/http"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

// CatalogItems is the catalog.Service surface the imagebuilder worker needs.
type CatalogItems interface {
	GetCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*coredomain.CatalogItem, coredomain.Status)
	CreateCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, item coredomain.CatalogItem) (*coredomain.CatalogItem, coredomain.Status)
	ReplaceCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, item coredomain.CatalogItem, enforceOwnership bool) (*coredomain.CatalogItem, coredomain.Status)
}

// RepositoryLookup is the repository.Service surface the imagebuilder worker needs.
type RepositoryLookup interface {
	GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*coredomain.Repository, coredomain.Status)
}

// OrganizationLister is the organization.Service surface the worker needs for org sweeps.
type OrganizationLister interface {
	List(ctx context.Context, listParams store.ListParams) ([]*model.Organization, error)
}

func statusToErr(status coredomain.Status) error {
	if status.Code >= 200 && status.Code < 300 {
		return nil
	}
	switch status.Code {
	case http.StatusNotFound:
		return flterrors.ErrResourceNotFound
	case http.StatusConflict:
		return flterrors.ErrDuplicateName
	default:
		if status.Message != "" {
			return errors.New(status.Message)
		}
		return errors.New(http.StatusText(int(status.Code)))
	}
}
