package field_selectors

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
)

func filteringDevicesWithFieldSelectorAndOperator(harness *e2e.Harness, fieldSelector string, operator string, fieldValue string) (string, []string, error) {
	return filteringResourcesWithFieldSelectorAndOperator(harness, resources.Devices, fieldSelector, operator, fieldValue)
}

func filteringFleetsWithFieldSelectorAndOperator(harness *e2e.Harness, fieldSelector string, operator string, fieldValue string) (string, []string, error) {
	return filteringResourcesWithFieldSelectorAndOperator(harness, resources.Fleets, fieldSelector, operator, fieldValue)
}

func filteringRepositoriesWithFieldSelectorAndOperator(harness *e2e.Harness, fieldSelector string, operator string, fieldValue string) (string, []string, error) {
	return filteringResourcesWithFieldSelectorAndOperator(harness, resources.Repositories, fieldSelector, operator, fieldValue)
}

func filteringResourcesWithFieldSelectorAndOperator(harness *e2e.Harness, resourceType string, fieldSelector string, operator string, fieldValue string) (string, []string, error) {
	fieldSelectorOperator, err := resources.ToFieldSelectorOperator(operator)
	if err != nil {
		return "", nil, err
	}
	response, err := resources.FilterWithFieldValueCondition(harness, resourceType, fieldSelector, fieldSelectorOperator, fieldValue)

	var supportedFields []string
	if err != nil && strings.Contains(response, resources.UnknownOrUnsupportedSelectorError) {
		supportedFields, err = extractSupportedFields(response)
	}
	if err != nil && strings.Contains(response, resources.FailedToResolveOperatorError) {
		err = nil
	}
	if err != nil && strings.Contains(response, resources.InvalidFieldSelectorSyntax) {
		err = nil
	}
	return response, supportedFields, err
}

func extractSupportedFields(output string) ([]string, error) {
	re := regexp.MustCompile(`Supported selectors are: \[(.*?)]`)
	matches := re.FindStringSubmatch(output)
	if len(matches) < 2 {
		return nil, fmt.Errorf("no supported selectors found in the output")
	}
	fields := strings.Split(matches[1], " ")
	return fields, nil
}

func filterDevicesWithCreationTimeDuringCurrentYear(harness *e2e.Harness, fieldName string) (string, error) {
	return resources.FilterWithCreationTimeDuringCurrentYear(harness, resources.Devices, fieldName)
}

func filterFleetsWithCreationTimeDuringCurrentYear(harness *e2e.Harness, fieldName string) (string, error) {
	return resources.FilterWithCreationTimeDuringCurrentYear(harness, resources.Fleets, fieldName)
}

func filterRepositoriesWithCreationTimeDuringCurrentYear(harness *e2e.Harness, fieldName string) (string, error) {
	return resources.FilterWithCreationTimeDuringCurrentYear(harness, resources.Repositories, fieldName)
}

func responseShouldContainExpectedDevices(response string, err error, count int) error {
	return resources.SomeRowsAreListedInResponse(response, err, count)
}

func responseShouldContainExpectedFleets(response string, err error, count int) error {
	return resources.SomeRowsAreListedInResponse(response, err, count)
}

func responseShouldContainExpectedRepositories(response string, err error, count int) error {
	return resources.SomeRowsAreListedInResponse(response, err, count)
}

func createDevicesWithNamePrefixAndFleet(harness *e2e.Harness, count int, namePrefix string, fleetName string, devices *[]*api.Device) error {
	if count <= 0 {
		return fmt.Errorf("count should be greater than 0")
	}
	if namePrefix == "" {
		return fmt.Errorf("name prefix cannot be empty")
	}
	if fleetName == "" {
		return fmt.Errorf("fleet name cannot be empty")
	}
	for i := 0; i < count; i++ {
		deviceName := fmt.Sprintf("%s%d", namePrefix, i)
		device, err := resources.CreateDevice(harness, deviceName, &map[string]string{"fleet": fleetName})
		if err != nil {
			return fmt.Errorf("failed to create device '%s': %w", deviceName, err)
		}
		*devices = append(*devices, device)
	}
	return nil
}

func createFleet(harness *e2e.Harness, name string, templateImage string, fleets *[]*api.Fleet) error {
	fleet, err := resources.CreateFleet(harness, name, templateImage, &map[string]string{"fleet": name})
	if err != nil {
		return fmt.Errorf("failed to create fleet '%s': %w", name, err)
	}
	*fleets = append(*fleets, fleet)
	time.Sleep(500 * time.Millisecond) // This sleep allows async binding between fleet to devices to complete

	return nil
}

func createFleetsWithNamePrefix(harness *e2e.Harness, count int, namePrefix string, templateImage string, fleets *[]*api.Fleet) error {
	if count <= 0 {
		return fmt.Errorf("count should be greater than 0")
	}
	if namePrefix == "" {
		return fmt.Errorf("name prefix cannot be empty")
	}
	for i := 0; i < count; i++ {
		fleetName := fmt.Sprintf("%s%d", namePrefix, i)
		fleet, err := resources.CreateFleet(harness, fleetName, templateImage, &map[string]string{"fleet": fleetName})
		if err == nil {
			*fleets = append(*fleets, fleet)
		}
	}
	return nil
}

func createRepositoriesWithNamePrefix(harness *e2e.Harness, count int, namePrefix string, repoUrl string, repositories *[]*api.Repository) error {
	if count <= 0 {
		return fmt.Errorf("count should be greater than 0")
	}
	if namePrefix == "" {
		return fmt.Errorf("name prefix cannot be empty")
	}
	for i := 0; i < count; i++ {
		repositoryName := fmt.Sprintf("%s%d", namePrefix, i)
		repository, err := resources.CreateRepository(harness, repositoryName, repoUrl)
		if err == nil {
			*repositories = append(*repositories, repository)
		}
	}
	return nil
}

func devicesAreListed(harness *e2e.Harness, count int) error {
	listedDevices, err := resources.ListAll(harness, resources.Devices)
	return resources.SomeRowsAreListedInResponse(listedDevices, err, count)
}

func fleetsAreListed(harness *e2e.Harness, count int) error {
	listedFleets, err := resources.ListAll(harness, resources.Fleets)
	return resources.SomeRowsAreListedInResponse(listedFleets, err, count)
}

func repositoriesAreListed(harness *e2e.Harness, count int) error {
	listedRepositories, err := resources.ListAll(harness, resources.Repositories)
	return resources.SomeRowsAreListedInResponse(listedRepositories, err, count)
}

func contains(slice []string, item string) bool {
	i := sort.SearchStrings(slice, item)
	return i < len(slice) && slice[i] == item
}
