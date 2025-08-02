package basic_operations

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	suiteCtx context.Context
	harness  *e2e.Harness
)

func TestBasicOperations(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Basic Operations E2E Suite")
}

var _ = Describe("Basic Operations", Label("integration", "82220"), func() {
	const createdResource = "201 Created"

	DescribeTable("Create a resource from example file",
		func(resourceType string, fileName string, extractResourceNameFromExampleFile func(*e2e.Harness, string) (string, error)) {
			// Generate unique test ID for this test
			testID := harness.GetTestIDFromContext()

			// Create unique YAML file for this test
			uniqueYAML, err := testutil.CreateUniqueYAMLFile(fileName, testID)
			Expect(err).ToNot(HaveOccurred())
			defer testutil.CleanupTempYAMLFile(uniqueYAML)

			name, err := extractResourceNameFromExampleFile(harness, uniqueYAML)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(name).ShouldNot(BeEmpty(), fmt.Sprintf("Resource name should not be empty for %s", fileName))

			Expect(resources.ExpectNotExistWithName(harness, resourceType, name)).To(Succeed())

			output, err := harness.ManageResource(util.ApplyAction, uniqueYAML)
			Expect(err).ShouldNot(HaveOccurred())

			matched, err := regexp.MatchString(createdResource, output)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(matched).To(BeTrue(), fmt.Sprintf("Expected output to match pattern '%s'", createdResource))

			response, err := resources.Delete(harness, resourceType, name)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(response).Should(MatchRegexp(fmt.Sprintf("Deletion request for %s \"%s\" completed\n", resourceType, name)),
				fmt.Sprintf("Resource deletion response should match 'Deletion request for %s <name> completed' pattern for %s", resourceType, fileName))
		},
		Entry("Create a device from example file", util.Device, "device.yaml", extractDeviceNameFromExampleFile),
		Entry("Create a fleet from example file", util.Fleet, "fleet.yaml", extractFleetNameFromExampleFile),
		Entry("Create a repository from example file", util.Repository, "repository-flightctl.yaml", extractRepositoryNameFromExampleFile),
	)
})

func extractDeviceNameFromExampleFile(harness *e2e.Harness, deviceFileName string) (string, error) {
	device := harness.GetDeviceByYaml(deviceFileName)
	if device.Metadata.Name == nil {
		return "", fmt.Errorf("device name should not be empty")
	}
	return strings.TrimSpace(*device.Metadata.Name), nil
}

func extractFleetNameFromExampleFile(harness *e2e.Harness, fleetFileName string) (string, error) {
	fleet := harness.GetFleetByYaml(fleetFileName)
	if fleet.Metadata.Name == nil {
		return "", fmt.Errorf("fleet name should not be empty")
	}
	return strings.TrimSpace(*fleet.Metadata.Name), nil
}

func extractRepositoryNameFromExampleFile(harness *e2e.Harness, repositoryFileName string) (string, error) {
	repository := harness.GetRepositoryByYaml(repositoryFileName)
	if repository.Metadata.Name == nil {
		return "", fmt.Errorf("repository name should not be empty")
	}
	return strings.TrimSpace(*repository.Metadata.Name), nil
}
