package resourcesync_test

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	PollingInterval = 5 * time.Second
	// Default resource sync task runtime is 2 minutes, but is configurable
	// via periodic tasks config.  For gh ci e2e runs the interval is set to 5s,
	// but we set the timeout at 3 minutesto ensure this test can run w/ default config.
	// in other environments
	PollingTimeout = 3 * time.Minute
	BranchName     = "main"

	// Images used in test data files
	Fedora42Image = "quay.io/fedora/fedora-bootc:42"
	Fedora43Image = "quay.io/fedora/fedora-bootc:43"
)

// testContext holds shared test state across test cases
type testContext struct {
	harness          *e2e.Harness
	testID           string
	repoName         string
	repoDir          string
	resourceSyncName string
	ownerFilter      string
	listParams       v1beta1.ListFleetsParams
}

var _ = Describe("ResourceSync success cases", func() {
	var tc *testContext

	BeforeEach(func() {
		tc = &testContext{}
		tc.harness = e2e.GetWorkerHarness()

		tc.testID = tc.harness.GetTestIDFromContext()
		tc.repoName = fmt.Sprintf("e2e-repo-%s", tc.testID)
		tc.resourceSyncName = fmt.Sprintf("e2e-resourcesync-%s", tc.testID)

		repoDir, err := tc.harness.SetupTemplatedGitRepoFromDir(
			tc.repoName,
			e2e.GetTestDataPath("valid_fleet_files"),
			templateData{TestID: tc.testID},
		)
		Expect(err).ToNot(HaveOccurred())
		tc.repoDir = repoDir

		err = tc.harness.CreateRepositoryWithValidE2ECredentials(tc.repoName)
		Expect(err).ToNot(HaveOccurred())

		// Define owner filter for querying fleets created by this ResourceSync
		tc.ownerFilter = fmt.Sprintf("metadata.owner=ResourceSync/%s", tc.resourceSyncName)
		tc.listParams = v1beta1.ListFleetsParams{
			FieldSelector: lo.ToPtr(tc.ownerFilter),
		}

		// Verify fleets do NOT exist before creating ResourceSync
		fleetsResp, err := tc.harness.Client.ListFleetsWithResponse(tc.harness.Context, &tc.listParams)
		Expect(err).ToNot(HaveOccurred())
		Expect(fleetsResp.JSON200).ToNot(BeNil())
		Expect(fleetsResp.JSON200.Items).To(BeEmpty(), "Expected no fleets owned by ResourceSync before creation")

		// Create a ResourceSync pointing to the repository
		resourceSyncSpec := v1beta1.ResourceSyncSpec{
			Repository:     tc.repoName,
			TargetRevision: BranchName,
			Path:           "/",
		}
		err = tc.harness.CreateResourceSync(tc.resourceSyncName, tc.repoName, resourceSyncSpec)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		// Clean up the local working directory
		if tc != nil && tc.repoDir != "" {
			os.RemoveAll(tc.repoDir)
		}
	})

	It("Performs synchronization of fleets from repository files", Label("75522", "sanity"), func() {
		By("creating fleets from repository files")
		tc.harness.WaitForFleetCount(&tc.listParams, 3, PollingTimeout, PollingInterval)

		fleetsResp, err := tc.harness.Client.ListFleetsWithResponse(tc.harness.Context, &tc.listParams)
		Expect(err).ToNot(HaveOccurred())
		Expect(fleetsResp.JSON200).ToNot(BeNil())
		Expect(fleetsResp.JSON200.Items).To(HaveLen(3), "Expected 3 fleets to be created by ResourceSync")

		for _, fleet := range fleetsResp.JSON200.Items {
			Expect(*fleet.Metadata.Owner).To(Equal(fmt.Sprintf("ResourceSync/%s", tc.resourceSyncName)))
			Expect(fleet.Spec.Template.Spec.Os.Image).To(Equal(Fedora42Image))
			Expect(*fleet.Metadata.Name).To(ContainSubstring(tc.testID))
		}

		By("updating fleet when repository file is modified")
		// Get fleet-1 and verify its current image
		fleet1Name := fmt.Sprintf("resourcesync-test-fleet-1-%s", tc.testID)
		fleet1, err := tc.harness.GetFleet(fleet1Name)
		Expect(err).ToNot(HaveOccurred())
		Expect(getFleetImage(fleet1)).To(Equal(Fedora42Image), fmt.Sprintf("Expected initial image to be %s", Fedora42Image))

		// Modify fleet-1.yaml in the working directory with a new image (uses updated_fleet_files template)
		err = tc.harness.PushTemplatedFilesToGitRepo(tc.repoName, e2e.GetTestDataPath("updated_fleet_files"), tc.repoDir, templateData{TestID: tc.testID})
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() (string, error) {
			fleet, err := tc.harness.GetFleet(fleet1Name)
			if err != nil {
				return "", err
			}
			return getFleetImage(fleet), nil
		}, PollingTimeout, PollingInterval).Should(Equal(Fedora43Image),
			"Expected fleet-1 image to be updated to "+Fedora43Image)

		By("deleting fleet when repository file is removed")
		// Verify fleet-3 exists
		fleet3Name := fmt.Sprintf("resourcesync-test-fleet-3-%s", tc.testID)
		Expect(tc.harness.FleetExists(fleet3Name)).To(BeTrue(), "Expected fleet-3 to exist before deletion")

		// Delete fleet-3.yaml from the working directory
		err = os.Remove(filepath.Join(tc.repoDir, "fleet-3.yaml"))
		Expect(err).ToNot(HaveOccurred())

		err = tc.harness.CommitAndPushGitRepo(tc.repoDir, "Remove fleet-3")
		Expect(err).ToNot(HaveOccurred())

		tc.harness.WaitForFleetCount(&tc.listParams, 2, PollingTimeout, PollingInterval)

		Expect(tc.harness.FleetExists(fleet3Name)).To(BeFalse(), "Expected fleet-3 to be deleted")
	})
})

var _ = Describe("ResourceSync Failure Cases", func() {
	var tc *testContext

	BeforeEach(func() {
		tc = &testContext{}
		tc.harness = e2e.GetWorkerHarness()
		tc.testID = tc.harness.GetTestIDFromContext()
	})

	AfterEach(func() {
		// Clean up the local working directory
		if tc != nil && tc.repoDir != "" {
			os.RemoveAll(tc.repoDir)
		}
	})

	It("Handles failure scenarios", Label("87156", "sanity"), func() {
		By("failing to sync when repository credentials are invalid")
		{
			repoName := fmt.Sprintf("e2e-repo-invalid-creds-%s", tc.testID)
			resourceSyncName := fmt.Sprintf("e2e-resourcesync-invalid-creds-%s", tc.testID)

			repoDir, err := tc.harness.SetupTemplatedGitRepoFromDir(
				repoName,
				e2e.GetTestDataPath("valid_fleet_files"),
				templateData{TestID: tc.testID},
			)
			Expect(err).ToNot(HaveOccurred())
			defer os.RemoveAll(repoDir)

			// Create a Repository resource with INVALID SSH credentials (garbage key)
			repoURL, err := tc.harness.GetInternalGitRepoURL(repoName)
			Expect(err).ToNot(HaveOccurred())
			invalidSSHKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nINVALID_KEY_DATA\n-----END OPENSSH PRIVATE KEY-----"
			err = tc.harness.CreateRepositoryWithSSHCredentials(repoName, repoURL, invalidSSHKey)
			Expect(err).ToNot(HaveOccurred())

			ownerFilter := fmt.Sprintf("metadata.owner=ResourceSync/%s", resourceSyncName)
			listParams := v1beta1.ListFleetsParams{
				FieldSelector: lo.ToPtr(ownerFilter),
			}

			err = tc.harness.CreateResourceSyncForRepo(resourceSyncName, repoName, BranchName)
			Expect(err).ToNot(HaveOccurred())

			// Wait for the Accessible condition to become False
			tc.harness.WaitForResourceSyncCondition(resourceSyncName, v1beta1.ConditionTypeResourceSyncAccessible, v1beta1.ConditionStatusFalse, PollingTimeout, PollingInterval)

			// Verify no fleets were created
			fleetsResp, err := tc.harness.Client.ListFleetsWithResponse(tc.harness.Context, &listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(fleetsResp.JSON200).ToNot(BeNil())
			Expect(fleetsResp.JSON200.Items).To(BeEmpty(), "Expected no fleets to be created when repository is inaccessible")
		}

		By("failing to parse invalid fleet YAML")
		{
			scenarioTestID := tc.testID + "-invalid-yaml"
			repoName := fmt.Sprintf("e2e-repo-invalid-yaml-%s", tc.testID)
			resourceSyncName := fmt.Sprintf("e2e-resourcesync-invalid-yaml-%s", tc.testID)

			repoDir, err := tc.harness.SetupTemplatedGitRepoFromDir(
				repoName,
				e2e.GetTestDataPath("valid_fleet_files"),
				templateData{TestID: scenarioTestID},
			)
			Expect(err).ToNot(HaveOccurred())
			defer os.RemoveAll(repoDir)

			err = tc.harness.CreateRepositoryWithValidE2ECredentials(repoName)
			Expect(err).ToNot(HaveOccurred())

			ownerFilter := fmt.Sprintf("metadata.owner=ResourceSync/%s", resourceSyncName)
			listParams := v1beta1.ListFleetsParams{
				FieldSelector: lo.ToPtr(ownerFilter),
			}

			err = tc.harness.CreateResourceSyncForRepo(resourceSyncName, repoName, BranchName)
			Expect(err).ToNot(HaveOccurred())

			tc.harness.WaitForFleetCount(&listParams, 3, PollingTimeout, PollingInterval)

			// Push an invalid fleet YAML file
			err = tc.harness.PushTemplatedFilesToGitRepo(
				repoName,
				e2e.GetTestDataPath("invalid_fleet_files"),
				repoDir,
				templateData{TestID: scenarioTestID},
			)
			Expect(err).ToNot(HaveOccurred())

			// Wait for the ResourceParsed condition to become False
			tc.harness.WaitForResourceSyncCondition(resourceSyncName, v1beta1.ConditionTypeResourceSyncResourceParsed, v1beta1.ConditionStatusFalse, PollingTimeout, PollingInterval)

			// Verify the original 3 fleets still exist (not deleted due to parse error)
			fleetsResp, err := tc.harness.Client.ListFleetsWithResponse(tc.harness.Context, &listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(fleetsResp.JSON200).ToNot(BeNil())
			Expect(fleetsResp.JSON200.Items).To(HaveLen(3), "Expected original 3 fleets to remain unchanged after parse error")
		}

		By("failing to sync when fleet name conflicts with another ResourceSync")
		{
			scenarioTestID := tc.testID + "-conflict"
			repoName := fmt.Sprintf("e2e-repo-conflict-%s", tc.testID)
			resourceSyncName := fmt.Sprintf("e2e-resourcesync-conflict-%s", tc.testID)

			repoDir, err := tc.harness.SetupTemplatedGitRepoFromDir(
				repoName,
				e2e.GetTestDataPath("valid_fleet_files"),
				templateData{TestID: scenarioTestID},
			)
			Expect(err).ToNot(HaveOccurred())
			defer os.RemoveAll(repoDir)

			// Create a valid repository and ResourceSync
			err = tc.harness.CreateRepositoryWithValidE2ECredentials(repoName)
			Expect(err).ToNot(HaveOccurred())

			err = tc.harness.CreateResourceSyncForRepo(resourceSyncName, repoName, BranchName)
			Expect(err).ToNot(HaveOccurred())

			ownerFilter := fmt.Sprintf("metadata.owner=ResourceSync/%s", resourceSyncName)
			listParams := v1beta1.ListFleetsParams{
				FieldSelector: lo.ToPtr(ownerFilter),
			}
			tc.harness.WaitForFleetCount(&listParams, 3, PollingTimeout, PollingInterval)

			// Setup a second ResourceSync (rs2) with the SAME fleet name
			rs2Name := fmt.Sprintf("e2e-resourcesync-conflict-2-%s", tc.testID)
			repo2Name := fmt.Sprintf("e2e-repo-conflict-2-%s", tc.testID)

			repoDir2, err := tc.harness.SetupTemplatedGitRepoFromDir(
				repo2Name,
				e2e.GetTestDataPath("conflict_fleet_files"),
				templateData{TestID: scenarioTestID},
			)
			Expect(err).ToNot(HaveOccurred())
			defer os.RemoveAll(repoDir2)

			err = tc.harness.CreateRepositoryWithValidE2ECredentials(repo2Name)
			Expect(err).ToNot(HaveOccurred())

			err = tc.harness.CreateResourceSyncForRepo(rs2Name, repo2Name, BranchName)
			Expect(err).ToNot(HaveOccurred())

			// Wait for rs2's Synced condition to become False due to conflict
			tc.harness.WaitForResourceSyncCondition(rs2Name, v1beta1.ConditionTypeResourceSyncSynced, v1beta1.ConditionStatusFalse, PollingTimeout, PollingInterval)

			condMessage, err := tc.harness.GetResourceSyncConditionMessage(rs2Name, v1beta1.ConditionTypeResourceSyncSynced)
			Expect(err).ToNot(HaveOccurred())
			Expect(condMessage).To(ContainSubstring("conflict"), "Expected error message to contain conflict")

			// Verify the fleet is still owned by the original ResourceSync
			fleet1Name := fmt.Sprintf("resourcesync-test-fleet-1-%s", scenarioTestID)
			fleet1, err := tc.harness.GetFleet(fleet1Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet1.Metadata.Owner).ToNot(BeNil())
			Expect(*fleet1.Metadata.Owner).To(Equal(fmt.Sprintf("ResourceSync/%s", resourceSyncName)), "Expected fleet to still be owned by the original ResourceSync")
		}
	})
})

type templateData struct {
	TestID string
}

func getFleetImage(fleet *v1beta1.Fleet) string {
	if fleet.Spec.Template.Spec.Os == nil {
		return ""
	}
	return fleet.Spec.Template.Spec.Os.Image
}
