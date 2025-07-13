package resources

import (
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"sigs.k8s.io/yaml"
)

const (
	created = "created"
)

func CreateDevice(harness *e2e.Harness, name string, labels *map[string]string) (*api.Device, error) {
	device := &api.Device{
		ApiVersion: api.DeviceAPIVersion,
		Kind:       api.DeviceKind,
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: labels,
		},
	}

	// Ensure test-id label is preserved
	if labels != nil {
		harness.SetLabelsForDeviceMetadata(&device.Metadata, *labels)
	} else {
		harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{})
	}

	yamlStr, err := marshalToString(device)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal device: %w", err)
	}

	return device, applyToCreateFromText(harness, yamlStr)
}

func DeleteDevices(harness *e2e.Harness, devices []*api.Device) error {
	for _, device := range devices {
		_, err := Delete(harness, Devices, *device.Metadata.Name)
		if err != nil {
			return err
		}
	}
	return nil
}

func CreateFleet(harness *e2e.Harness, name string, templateImage string, labels *map[string]string) (*api.Fleet, error) {
	fleet := &api.Fleet{
		ApiVersion: api.FleetAPIVersion,
		Kind:       api.FleetKind,
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: labels,
		},
		Spec: api.FleetSpec{
			Selector: &api.LabelSelector{
				MatchLabels: labels,
			},
			Template: struct {
				Metadata *api.ObjectMeta `json:"metadata,omitempty"`
				Spec     api.DeviceSpec  `json:"spec"`
			}{
				Spec: api.DeviceSpec{
					Os: &api.DeviceOsSpec{
						Image: templateImage,
					},
				},
			},
		},
	}

	// Ensure test-id label is preserved
	if labels != nil {
		harness.SetLabelsForFleetMetadata(&fleet.Metadata, *labels)
	} else {
		harness.SetLabelsForFleetMetadata(&fleet.Metadata, map[string]string{})
	}

	yamlStr, err := marshalToString(fleet)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal fleet: %w", err)
	}

	return fleet, applyToCreateFromText(harness, yamlStr)
}

func DeleteFleets(harness *e2e.Harness, fleets []*api.Fleet) error {
	for _, fleet := range fleets {
		_, err := Delete(harness, Fleets, *fleet.Metadata.Name)
		if err != nil {
			return err
		}
	}
	return nil
}

func CreateRepository(harness *e2e.Harness, name string, url string) (*api.Repository, error) {
	spec := api.RepositorySpec{}
	specError := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  url,
		Type: api.Git,
	})
	if specError != nil {
		return nil, specError
	}

	repository := &api.Repository{
		ApiVersion: api.RepositoryAPIVersion,
		Kind:       api.RepositoryKind,
		Metadata: api.ObjectMeta{
			Name: &name,
		},
		Spec: spec,
	}

	yamlStr, err := marshalToString(repository)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal repository: %w", err)
	}

	return repository, applyToCreateFromText(harness, yamlStr)
}

func DeleteRepositories(harness *e2e.Harness, repositories []*api.Repository) error {
	for _, repository := range repositories {
		_, err := Delete(harness, Repositories, *repository.Metadata.Name)
		if err != nil {
			return err
		}
	}
	return nil
}

func marshalToString(v interface{}) (string, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("failed to marshal object: %w", err)
	}
	return string(data), nil
}

func applyToCreateFromText(harness *e2e.Harness, text string) error {
	output, err := harness.CLIWithStdin(text, apply, "-f", "-")
	if err == nil && strings.Contains(output, created) {
		fmt.Println(output)
	}
	return err
}

func Delete(harness *e2e.Harness, resourceKind string, name string) (string, error) {
	return harness.CLI("delete", fmt.Sprintf("%s/%s", resourceKind, name))
}

func DeleteAll(harness *e2e.Harness, devices []*api.Device, fleets []*api.Fleet, repositories []*api.Repository) error {
	if err := DeleteDevices(harness, devices); err != nil {
		return err
	}
	if err := DeleteFleets(harness, fleets); err != nil {
		return err
	}
	return DeleteRepositories(harness, repositories)
}
