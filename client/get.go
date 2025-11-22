package client

import (
	"context"
	"fmt"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
)

// GetDevice fetches a device by name from the FlightCtl API.
// Returns the API response including HTTP status code.
func GetDevice(ctx context.Context, client *apiclient.ClientWithResponses, name string) (*apiclient.GetDeviceResponse, error) {
	response, err := client.GetDeviceWithResponse(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("getting device %s: %w", name, err)
	}

	return response, nil
}
