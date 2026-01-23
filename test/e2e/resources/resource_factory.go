// Package resources provides utilities for creating and managing FlightCtl resources in e2e tests.
package resources

import (
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	"sigs.k8s.io/yaml"
)

const (
	created = "created"
)

// createMultiple is a generic helper for batch resource creation.
// It handles the common pattern of creating multiple resources with indexed names.
func createMultiple(harness *e2e.Harness, count int, namePrefix string, createFn func(name string) error) ([]string, error) {
	names := make([]string, count)
	testID := harness.GetTestIDFromContext()

	for i := 0; i < count; i++ {
		names[i] = fmt.Sprintf("%s-%d-%s", namePrefix, i+1, testID)
		if err := createFn(names[i]); err != nil {
			return names[:i], fmt.Errorf("failed to create resource %s: %w", names[i], err)
		}
	}
	return names, nil
}

// CreateDevice creates a single device with the given name and labels.
func CreateDevice(harness *e2e.Harness, name string, labels *map[string]string) (*api.Device, error) {
	device := &api.Device{
		ApiVersion: api.DeviceAPIVersion,
		Kind:       api.DeviceKind,
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: labels,
		},
	}

	setLabelsOrDefault(harness.SetLabelsForDeviceMetadata, &device.Metadata, labels)

	yamlStr, err := marshalToString(device)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal device: %w", err)
	}

	return device, applyToCreateFromText(harness, yamlStr)
}

// CreateDevices creates multiple devices with names formatted as "{namePrefix}-{index}-{testID}".
// Returns the list of created device names.
func CreateDevices(harness *e2e.Harness, count int, namePrefix string, labels *map[string]string) ([]string, error) {
	return createMultiple(harness, count, namePrefix, func(name string) error {
		_, err := CreateDevice(harness, name, labels)
		return err
	})
}

// DeleteDevices deletes multiple devices.
func DeleteDevices(harness *e2e.Harness, devices []*api.Device) error {
	for _, device := range devices {
		if _, err := Delete(harness, Devices, *device.Metadata.Name); err != nil {
			return err
		}
	}
	return nil
}

// CreateFleet creates a single fleet with the given name, template image, and labels.
func CreateFleet(harness *e2e.Harness, name, templateImage string, labels *map[string]string) (*api.Fleet, error) {
	// Ensure selector has valid matchLabels (empty map if nil) to pass API validation
	selectorLabels := labels
	if selectorLabels == nil {
		empty := map[string]string{}
		selectorLabels = &empty
	}

	fleet := &api.Fleet{
		ApiVersion: api.FleetAPIVersion,
		Kind:       api.FleetKind,
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: labels,
		},
		Spec: api.FleetSpec{
			Selector: &api.LabelSelector{MatchLabels: selectorLabels},
			Template: struct {
				Metadata *api.ObjectMeta `json:"metadata,omitempty"`
				Spec     api.DeviceSpec  `json:"spec"`
			}{
				Spec: api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: templateImage},
				},
			},
		},
	}

	setLabelsOrDefault(harness.SetLabelsForFleetMetadata, &fleet.Metadata, labels)

	yamlStr, err := marshalToString(fleet)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal fleet: %w", err)
	}
	return fleet, applyToCreateFromText(harness, yamlStr)
}

// CreateFleets creates multiple fleets with names formatted as "{namePrefix}-{index}-{testID}".
// Returns the list of created fleet names.
func CreateFleets(harness *e2e.Harness, count int, namePrefix, templateImage string, labels *map[string]string) ([]string, error) {
	return createMultiple(harness, count, namePrefix, func(name string) error {
		_, err := CreateFleet(harness, name, templateImage, labels)
		return err
	})
}

// DeleteFleets deletes multiple fleets by name.
func DeleteFleets(harness *e2e.Harness, fleets []*api.Fleet) error {
	for _, fleet := range fleets {
		if _, err := Delete(harness, Fleets, *fleet.Metadata.Name); err != nil {
			return err
		}
	}
	return nil
}

// CreateRepository creates a single git repository with the given name, URL, and labels.
func CreateRepository(harness *e2e.Harness, name, url string, labels *map[string]string) (*api.Repository, error) {
	spec := api.RepositorySpec{}
	if err := spec.FromGenericRepoSpec(api.GenericRepoSpec{Url: url, Type: api.RepoSpecTypeGit}); err != nil {
		return nil, fmt.Errorf("failed to create repo spec: %w", err)
	}

	repository := &api.Repository{
		ApiVersion: api.RepositoryAPIVersion,
		Kind:       api.RepositoryKind,
		Metadata:   api.ObjectMeta{Name: &name},
		Spec:       spec,
	}

	setLabelsOrDefault(harness.SetLabelsForRepositoryMetadata, &repository.Metadata, labels)

	yamlStr, err := marshalToString(repository)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal repository: %w", err)
	}
	return repository, applyToCreateFromText(harness, yamlStr)
}

// CreateRepositories creates multiple repositories with names formatted as "{namePrefix}-{index}-{testID}".
// Returns the list of created repository names.
func CreateRepositories(harness *e2e.Harness, count int, namePrefix, url string, labels *map[string]string) ([]string, error) {
	return createMultiple(harness, count, namePrefix, func(name string) error {
		_, err := CreateRepository(harness, name, url, labels)
		return err
	})
}

// DeleteRepositories deletes multiple repositories by name.
func DeleteRepositories(harness *e2e.Harness, repositories []*api.Repository) error {
	for _, repository := range repositories {
		if _, err := Delete(harness, Repositories, *repository.Metadata.Name); err != nil {
			return err
		}
	}
	return nil
}

// setLabelsOrDefault applies labels to metadata, using an empty map if labels is nil.
// This ensures test-id labels are always preserved.
func setLabelsOrDefault(setFn func(*api.ObjectMeta, map[string]string), metadata *api.ObjectMeta, labels *map[string]string) {
	if labels != nil {
		setFn(metadata, *labels)
	} else {
		setFn(metadata, map[string]string{})
	}
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
		GinkgoWriter.Printf("%s\n", output)
	}
	return err
}

// Delete removes a resource by kind and name.
func Delete(harness *e2e.Harness, resourceKind, name string) (string, error) {
	return harness.CLI("delete", fmt.Sprintf("%s/%s", resourceKind, name))
}

// DeleteAll removes all provided devices, fleets, and repositories.
func DeleteAll(harness *e2e.Harness, devices []*api.Device, fleets []*api.Fleet, repositories []*api.Repository) error {
	if err := DeleteDevices(harness, devices); err != nil {
		return err
	}
	if err := DeleteFleets(harness, fleets); err != nil {
		return err
	}
	return DeleteRepositories(harness, repositories)
}
