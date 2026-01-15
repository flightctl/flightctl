package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// RepositoryConverter converts between v1beta1 API types and domain types for Repository resources.
type RepositoryConverter interface {
	ToDomain(apiv1beta1.Repository) domain.Repository
	FromDomain(*domain.Repository) *apiv1beta1.Repository
	ListFromDomain(*domain.RepositoryList) *apiv1beta1.RepositoryList

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListRepositoriesParams) domain.ListRepositoriesParams
}

type repositoryConverter struct{}

// NewRepositoryConverter creates a new RepositoryConverter.
func NewRepositoryConverter() RepositoryConverter {
	return &repositoryConverter{}
}

func (c *repositoryConverter) ToDomain(r apiv1beta1.Repository) domain.Repository {
	return r
}

func (c *repositoryConverter) FromDomain(r *domain.Repository) *apiv1beta1.Repository {
	return r
}

func (c *repositoryConverter) ListFromDomain(l *domain.RepositoryList) *apiv1beta1.RepositoryList {
	return l
}

func (c *repositoryConverter) ListParamsToDomain(p apiv1beta1.ListRepositoriesParams) domain.ListRepositoriesParams {
	return p
}
