package tasks

import (
	"context"
	"errors"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
)

const ItemsPerPage = 1000

var (
	ErrUnknownConfigName      = errors.New("failed to find configuration item name")
	ErrUnknownApplicationType = errors.New("unknown application type")
)

func getOwnerFleet(device *api.Device) (string, bool, error) {
	if device.Metadata.Owner == nil {
		return "", true, nil
	}

	ownerType, ownerName, err := util.GetResourceOwner(device.Metadata.Owner)
	if err != nil {
		return "", false, err
	}

	if ownerType != api.FleetKind {
		return "", false, nil
	}

	return ownerName, true, nil
}

func getRepository(ctx context.Context, serviceHandler *service.ServiceHandler, name string) (*api.Repository, error) {
	response, err := serviceHandler.ReadRepository(ctx, server.ReadRepositoryRequestObject{Name: name})
	if err != nil {
		return nil, fmt.Errorf("failed getting repository: %w", err)
	}
	var repository api.Repository
	switch resp := response.(type) {
	case server.ReadRepository200JSONResponse:
		repository = api.Repository(resp)
	default:
		return nil, fmt.Errorf("failed getting repository: %s", server.PrintResponse(resp))
	}
	return &repository, nil
}

func listRepositories(ctx context.Context, serviceHandler *service.ServiceHandler, params api.ListRepositoriesParams) (*api.RepositoryList, error) {
	response, err := serviceHandler.ListRepositories(ctx, server.ListRepositoriesRequestObject{Params: params})
	if err != nil {
		return nil, fmt.Errorf("failed fetching repositories: %w", err)
	}
	var repoList api.RepositoryList
	switch resp := response.(type) {
	case server.ListRepositories200JSONResponse:
		repoList = api.RepositoryList(resp)
	default:
		return nil, fmt.Errorf("failed fetching repositories: %s", server.PrintResponse(resp))
	}
	return &repoList, nil
}
