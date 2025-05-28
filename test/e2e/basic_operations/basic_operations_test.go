package basic_operations

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBasicOperations(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Basic Operations E2E Suite")
}

var _ = Describe("Basic Operations", Label("sanity"), func() {
	const createdResource = "201 Created"

	var (
		harness *e2e.Harness
	)

	DescribeTable("Create a resource from example file",
		func(resourceType string, fileName string, extractResourceNameFromExampleFile func(*e2e.Harness, string) (string, error)) {
			name, err := extractResourceNameFromExampleFile(harness, fileName)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(name).ShouldNot(BeEmpty(), fmt.Sprintf("Resource name should not be empty for %s", fileName))

			Expect(resources.ExpectNotExistWithName(harness, resourceType, name)).To(Succeed())

			output, err := resources.ApplyFromExampleFile(harness, fileName)
			Expect(err).ShouldNot(HaveOccurred())

			matched, err := regexp.MatchString(createdResource, output)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(matched).To(BeTrue(), fmt.Sprintf("Expected output to match pattern '%s'", createdResource))

			response, err := resources.Delete(harness, resourceType, name)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(response).Should(BeEmpty(), fmt.Sprintf("Resource deletion response should be empty for %s", fileName))
		},
		Entry("Create a device from example file", util.Device, "device.yaml", extractDeviceNameFromExampleFile),
		Entry("Create a fleet from example file", util.Fleet, "fleet.yaml", extractFleetNameFromExampleFile),
		Entry("Create a repository from example file", util.Repository, "repository-flightctl.yaml", extractRepositoryNameFromExampleFile),
	)
})

func extractDeviceNameFromExampleFile(harness *e2e.Harness, deviceFileName string) (string, error) {
	device := harness.GetDeviceByYaml(util.GetTestExamplesYamlPath(deviceFileName))
	if device.Metadata.Name == nil {
		return "", fmt.Errorf("device name should not be empty")
	}
	return strings.TrimSpace(*device.Metadata.Name), nil
}

func extractFleetNameFromExampleFile(harness *e2e.Harness, fleetFileName string) (string, error) {
	fleet := harness.GetFleetByYaml(util.GetTestExamplesYamlPath(fleetFileName))
	if fleet.Metadata.Name == nil {
		return "", fmt.Errorf("fleet name should not be empty")
	}
	return strings.TrimSpace(*fleet.Metadata.Name), nil
}

func extractRepositoryNameFromExampleFile(harness *e2e.Harness, repositoryFileName string) (string, error) {
	repository := harness.GetRepositoryByYaml(util.GetTestExamplesYamlPath(repositoryFileName))
	if repository.Metadata.Name == nil {
		return "", fmt.Errorf("repository name should not be empty")
	}
	return strings.TrimSpace(*repository.Metadata.Name), nil
}
