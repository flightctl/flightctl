package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/sirupsen/logrus"
)

func scaleFleetNames(prefix string, count int) []string {
	names := make([]string, count)
	for i := 0; i < count; i++ {
		names[i] = fmt.Sprintf("%s-%02d", prefix, i)
	}
	return names
}

func createScaleFleets(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses, prefix string, count int) ([]string, error) {
	names := scaleFleetNames(prefix, count)
	for _, name := range names {
		response, err := serviceClient.GetFleetWithResponse(ctx, name, &v1beta1.GetFleetParams{})
		if err == nil && response.HTTPResponse != nil && response.HTTPResponse.StatusCode == http.StatusOK {
			log.Infof("Fleet %s already exists, skipping creation", name)
			continue
		}

		fleet, err := newScaleFleet(name)
		if err != nil {
			return nil, fmt.Errorf("building fleet %s: %w", name, err)
		}
		fleetJSON, err := json.Marshal(fleet)
		if err != nil {
			return nil, fmt.Errorf("marshaling fleet %s: %w", name, err)
		}
		createResponse, err := serviceClient.ReplaceFleetWithBodyWithResponse(ctx, name, "application/json", bytes.NewReader(fleetJSON))
		if err != nil {
			return nil, fmt.Errorf("creating fleet %s: %w", name, err)
		}
		if createResponse.StatusCode() < http.StatusOK || createResponse.StatusCode() >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("creating fleet %s: status %d, body: %s", name, createResponse.StatusCode(), string(createResponse.Body))
		}
		log.Infof("Successfully created fleet: %s", name)
	}
	return names, nil
}

func newScaleFleet(name string) (*v1beta1.Fleet, error) {
	mode := 0644
	content := fmt.Sprintf("This system is managed by flightctl (fleet %s).", name)
	var configItem v1beta1.ConfigProviderSpec
	if err := configItem.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{
		Name: "motd-update",
		Inline: []v1beta1.FileSpec{
			{
				Path:    "/etc/motd",
				Content: content,
				Mode:    &mode,
			},
		},
	}); err != nil {
		return nil, err
	}

	matchLabels := map[string]string{"fleet": name}
	templateLabels := map[string]string{"fleet": name}
	return &v1beta1.Fleet{
		ApiVersion: v1beta1.ApiVersion("flightctl.io/v1beta1"),
		Kind:       v1beta1.FleetKind,
		Metadata: v1beta1.ObjectMeta{
			Name: &name,
		},
		Spec: v1beta1.FleetSpec{
			Selector: &v1beta1.LabelSelector{
				MatchLabels: &matchLabels,
			},
			Template: struct {
				Metadata *v1beta1.ObjectMeta `json:"metadata,omitempty"`
				Spec     v1beta1.DeviceSpec  `json:"spec"`
			}{
				Metadata: &v1beta1.ObjectMeta{
					Labels: &templateLabels,
				},
				Spec: v1beta1.DeviceSpec{
					Config: &[]v1beta1.ConfigProviderSpec{configItem},
				},
			},
		},
	}, nil
}
