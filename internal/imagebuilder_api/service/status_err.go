package service

import (
	"context"
	"errors"
	"net/http"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/google/uuid"
)

// CatalogLookup is the catalog.Service surface ImagePromotion needs.
type CatalogLookup interface {
	GetCatalog(ctx context.Context, orgId uuid.UUID, name string) (*coredomain.Catalog, coredomain.Status)
	GetCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*coredomain.CatalogItem, coredomain.Status)
}

// RepositoryLookup is the repository.Service surface ImageBuild/ImageExport need.
type RepositoryLookup interface {
	GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*coredomain.Repository, coredomain.Status)
}

// statusToErr maps a core service domain.Status into an error suitable for
// existing imagebuilder error checks (errors.Is with flterrors).
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
