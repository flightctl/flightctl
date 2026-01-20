package resourcesync_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// getTestDataPath returns the path to a file/directory in the testdata folder.
// Note: ginkgo runs tests from the test package directory (test/e2e/resourcesync/).
func getTestDataPath(relativePath string) string {
	return filepath.Join("testdata", relativePath)
}

// getSSHPrivateKey reads the SSH private key from bin/.ssh/id_rsa.
// Note: ginkgo runs tests from the test package directory (test/e2e/resourcesync/),
// so navigate up to the project root.
func getSSHPrivateKey() (string, error) {
	keyPath := filepath.Join("..", "..", "..", "bin", ".ssh", "id_rsa")
	content, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read SSH private key from %s: %w", keyPath, err)
	}
	return string(content), nil
}

type templateData struct {
	TestID string
}

// writeTemplatedFilesToDir reads template files from sourceDir, applies Go templating with the
// provided data, and writes the results to destDir.
func writeTemplatedFilesToDir(sourceDir, destDir string, data templateData) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", sourceDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(sourceDir, entry.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", filePath, err)
		}

		tmpl, err := template.New(entry.Name()).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", entry.Name(), err)
		}

		destPath := filepath.Join(destDir, entry.Name())
		destFile, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("failed to create file %s: %w", destPath, err)
		}

		err = tmpl.Execute(destFile, data)
		destFile.Close()
		if err != nil {
			return fmt.Errorf("failed to execute template %s: %w", entry.Name(), err)
		}
	}

	return nil
}

// testContext holds shared test state across test cases
type testContext struct {
	harness          *e2e.Harness
	testID           string
	repoName         string
	resourceSyncName string
	workDir          string
	ownerFilter      string
	listParams       domain.ListFleetsParams
}

// waitForFleetCount waits until the number of fleets owned by the ResourceSync matches expectedCount
func (tc *testContext) waitForFleetCount(expectedCount int) {
	Eventually(func() (int, error) {
		resp, err := tc.harness.Client.ListFleetsWithResponse(tc.harness.Context, &tc.listParams)
		if err != nil {
			return 0, err
		}
		if resp.JSON200 == nil {
			return 0, fmt.Errorf("unexpected nil response")
		}
		return len(resp.JSON200.Items), nil
	}, 30*time.Second, 2*time.Second).Should(Equal(expectedCount),
		fmt.Sprintf("Expected %d fleets owned by ResourceSync", expectedCount))
}

func (tc *testContext) getFleetByName(fleetName string) (*domain.Fleet, error) {
	resp, err := tc.harness.Client.GetFleetWithResponse(tc.harness.Context, fleetName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get fleet %s: %w", fleetName, err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("fleet %s not found (status: %d)", fleetName, resp.StatusCode())
	}
	return resp.JSON200, nil
}

func (tc *testContext) fleetExists(fleetName string) bool {
	resp, err := tc.harness.Client.GetFleetWithResponse(tc.harness.Context, fleetName, nil)
	if err != nil {
		return false
	}
	return resp.JSON200 != nil
}

func getFleetImage(fleet *domain.Fleet) string {
	if fleet.Spec.Template.Spec.Os == nil {
		return ""
	}
	return fleet.Spec.Template.Spec.Os.Image
}

func (tc *testContext) getResourceSync(name string) (*domain.ResourceSync, error) {
	resp, err := tc.harness.Client.GetResourceSyncWithResponse(tc.harness.Context, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get ResourceSync %s: %w", name, err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("ResourceSync %s not found (status: %d)", name, resp.StatusCode())
	}
	return resp.JSON200, nil
}

// waitForCondition waits for a ResourceSync condition to reach the expected status
func (tc *testContext) waitForCondition(rsName string, condType domain.ConditionType, expectedStatus domain.ConditionStatus) {
	Eventually(func() (domain.ConditionStatus, error) {
		rs, err := tc.getResourceSync(rsName)
		if err != nil {
			return "", err
		}
		if rs.Status == nil || rs.Status.Conditions == nil {
			return "", fmt.Errorf("ResourceSync %s has no status conditions", rsName)
		}
		cond := domain.FindStatusCondition(rs.Status.Conditions, condType)
		if cond == nil {
			return "", fmt.Errorf("condition %s not found on ResourceSync %s", condType, rsName)
		}
		return cond.Status, nil
	}, 30*time.Second, 2*time.Second).Should(Equal(expectedStatus),
		fmt.Sprintf("Expected ResourceSync %s condition %s to be %s", rsName, condType, expectedStatus))
}

func (tc *testContext) getConditionMessage(rsName string, condType domain.ConditionType) (string, error) {
	rs, err := tc.getResourceSync(rsName)
	if err != nil {
		return "", err
	}
	if rs.Status == nil || rs.Status.Conditions == nil {
		return "", fmt.Errorf("ResourceSync %s has no status conditions", rsName)
	}
	cond := domain.FindStatusCondition(rs.Status.Conditions, condType)
	if cond == nil {
		return "", fmt.Errorf("condition %s not found on ResourceSync %s", condType, rsName)
	}
	return cond.Message, nil
}

func (tc *testContext) createRepositoryWithCredentials(repoName, repoURL, sshPrivateKey string) error {
	sshPrivateKeyBase64 := base64.StdEncoding.EncodeToString([]byte(sshPrivateKey))

	repoSpec := domain.RepositorySpec{}
	err := repoSpec.FromSshRepoSpec(domain.SshRepoSpec{
		Url:  repoURL,
		Type: domain.RepoSpecTypeGit,
		SshConfig: domain.SshConfig{
			SshPrivateKey:          lo.ToPtr(sshPrivateKeyBase64),
			SkipServerVerification: lo.ToPtr(true),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create SSH repo spec: %w", err)
	}

	repository := domain.Repository{
		ApiVersion: domain.RepositoryAPIVersion,
		Kind:       domain.RepositoryKind,
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr(repoName),
		},
		Spec: repoSpec,
	}

	resp, err := tc.harness.Client.CreateRepositoryWithResponse(tc.harness.Context, repository)
	if err != nil {
		return fmt.Errorf("failed to create repository: %w", err)
	}
	if resp.StatusCode() != 201 {
		return fmt.Errorf("expected 201 Created, got %d: %s", resp.StatusCode(), string(resp.Body))
	}
	return nil
}

// setupGitRepoWithFiles creates a git repo, clones it, populates with templated files, and pushes
func (tc *testContext) setupGitRepoWithFiles(sourceDir string) error {
	err := tc.harness.CreateGitRepositoryOnServer(tc.repoName)
	if err != nil {
		return fmt.Errorf("failed to create git repository: %w", err)
	}

	tc.workDir, err = os.MkdirTemp("", "resourcesync-test-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	err = tc.harness.CloneGitRepositoryFromServer(tc.repoName, tc.workDir)
	if err != nil {
		return fmt.Errorf("failed to clone git repository: %w", err)
	}

	err = writeTemplatedFilesToDir(sourceDir, tc.workDir, templateData{TestID: tc.testID})
	if err != nil {
		return fmt.Errorf("failed to write templated files: %w", err)
	}

	err = tc.harness.CommitAndPushGitRepo(tc.workDir, "Add initial fleet files")
	if err != nil {
		return fmt.Errorf("failed to commit and push: %w", err)
	}

	return nil
}

func (tc *testContext) getGitRepoURL(repoName string) string {
	gitConfig := tc.harness.GetGitServerConfig()
	// Use the internal cluster URL since the periodic task runs inside the cluster.
	gitServerInternalHost := "e2e-git-server.flightctl-e2e.svc.cluster.local"
	return fmt.Sprintf("%s@%s:%d:/home/user/repos/%s.git",
		gitConfig.User, gitServerInternalHost, gitConfig.Port, repoName)
}

func (tc *testContext) createRepositoryWithValidCredentials(repoName string) error {
	repoURL := tc.getGitRepoURL(repoName)
	sshPrivateKey, err := getSSHPrivateKey()
	if err != nil {
		return fmt.Errorf("failed to read SSH private key: %w", err)
	}
	return tc.createRepositoryWithCredentials(repoName, repoURL, sshPrivateKey)
}

func (tc *testContext) createResourceSyncForRepo(resourceSyncName, repoName string) error {
	resourceSyncSpec := domain.ResourceSyncSpec{
		Repository:     repoName,
		TargetRevision: "main",
		Path:           "/",
	}
	return tc.harness.CreateResourceSync(resourceSyncName, repoName, resourceSyncSpec)
}

func (tc *testContext) setupOwnerFilter(resourceSyncName string) {
	tc.ownerFilter = fmt.Sprintf("metadata.owner=ResourceSync/%s", resourceSyncName)
	tc.listParams = domain.ListFleetsParams{
		FieldSelector: lo.ToPtr(tc.ownerFilter),
	}
}

func (tc *testContext) getFleetOwner(fleetName string) (string, error) {
	fleet, err := tc.getFleetByName(fleetName)
	if err != nil {
		return "", err
	}
	if fleet.Metadata.Owner == nil {
		return "", nil
	}
	return *fleet.Metadata.Owner, nil
}

var _ = Describe("ResourceSync", func() {
	var tc *testContext

	BeforeEach(func() {
		tc = &testContext{}
		tc.harness = e2e.GetWorkerHarness()

		tc.testID = tc.harness.GetTestIDFromContext()
		tc.repoName = fmt.Sprintf("e2e-repo-%s", tc.testID)
		tc.resourceSyncName = fmt.Sprintf("e2e-resourcesync-%s", tc.testID)

		err := tc.setupGitRepoWithFiles(getTestDataPath("valid_fleet_files"))
		Expect(err).ToNot(HaveOccurred())

		err = tc.createRepositoryWithValidCredentials(tc.repoName)
		Expect(err).ToNot(HaveOccurred())

		// Define owner filter for querying fleets created by this ResourceSync
		tc.ownerFilter = fmt.Sprintf("metadata.owner=ResourceSync/%s", tc.resourceSyncName)
		tc.listParams = domain.ListFleetsParams{
			FieldSelector: lo.ToPtr(tc.ownerFilter),
		}

		// Verify fleets do NOT exist before creating ResourceSync
		fleetsResp, err := tc.harness.Client.ListFleetsWithResponse(tc.harness.Context, &tc.listParams)
		Expect(err).ToNot(HaveOccurred())
		Expect(fleetsResp.JSON200).ToNot(BeNil())
		Expect(fleetsResp.JSON200.Items).To(BeEmpty(), "Expected no fleets owned by ResourceSync before creation")

		// Create a ResourceSync pointing to the repository
		resourceSyncSpec := domain.ResourceSyncSpec{
			Repository:     tc.repoName,
			TargetRevision: "main",
			Path:           "/",
		}
		err = tc.harness.CreateResourceSync(tc.resourceSyncName, tc.repoName, resourceSyncSpec)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		// Clean up the local working directory
		if tc != nil && tc.workDir != "" {
			os.RemoveAll(tc.workDir)
		}
	})

	It("creates fleets from repository files", func() {
		tc.waitForFleetCount(3)

		fleetsResp, err := tc.harness.Client.ListFleetsWithResponse(tc.harness.Context, &tc.listParams)
		Expect(err).ToNot(HaveOccurred())
		Expect(fleetsResp.JSON200).ToNot(BeNil())
		Expect(fleetsResp.JSON200.Items).To(HaveLen(3), "Expected 3 fleets to be created by ResourceSync")

		for _, fleet := range fleetsResp.JSON200.Items {
			Expect(*fleet.Metadata.Owner).To(Equal(fmt.Sprintf("ResourceSync/%s", tc.resourceSyncName)))
			Expect(fleet.Spec.Template.Spec.Os.Image).To(Equal("quay.io/fedora/fedora-bootc:42"))
			Expect(*fleet.Metadata.Name).To(ContainSubstring(tc.testID))
		}
	})

	It("updates fleet when repository file is modified", func() {
		tc.waitForFleetCount(3)

		// Get fleet-1 and verify its current image
		fleet1Name := fmt.Sprintf("resourcesync-test-fleet-1-%s", tc.testID)
		fleet1, err := tc.getFleetByName(fleet1Name)
		Expect(err).ToNot(HaveOccurred())
		Expect(getFleetImage(fleet1)).To(Equal("quay.io/fedora/fedora-bootc:42"), "Expected initial image to be quay.io/fedora/fedora-bootc:42")

		// Modify fleet-1.yaml in the working directory with a new image (uses updated_fleet_files template)
		newImage := "quay.io/fedora/fedora-bootc:43"
		err = writeTemplatedFilesToDir(getTestDataPath("updated_fleet_files"), tc.workDir, templateData{TestID: tc.testID})
		Expect(err).ToNot(HaveOccurred())

		err = tc.harness.CommitAndPushGitRepo(tc.workDir, "Update fleet-1 image to :43 tag")
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() (string, error) {
			fleet, err := tc.getFleetByName(fleet1Name)
			if err != nil {
				return "", err
			}
			return getFleetImage(fleet), nil
		}, 30*time.Second, 2*time.Second).Should(Equal(newImage),
			"Expected fleet-1 image to be updated to "+newImage)
	})

	It("deletes fleet when repository file is removed", func() {
		tc.waitForFleetCount(3)

		// Verify fleet-3 exists
		fleet3Name := fmt.Sprintf("resourcesync-test-fleet-3-%s", tc.testID)
		Expect(tc.fleetExists(fleet3Name)).To(BeTrue(), "Expected fleet-3 to exist before deletion")

		// Delete fleet-3.yaml from the working directory
		err := os.Remove(filepath.Join(tc.workDir, "fleet-3.yaml"))
		Expect(err).ToNot(HaveOccurred())

		err = tc.harness.CommitAndPushGitRepo(tc.workDir, "Remove fleet-3")
		Expect(err).ToNot(HaveOccurred())

		tc.waitForFleetCount(2)

		Expect(tc.fleetExists(fleet3Name)).To(BeFalse(), "Expected fleet-3 to be deleted")
	})
})

var _ = Describe("ResourceSync Failure Cases", func() {
	var tc *testContext

	BeforeEach(func() {
		tc = &testContext{}
		tc.harness = e2e.GetWorkerHarness()

		tc.testID = tc.harness.GetTestIDFromContext()
		tc.repoName = fmt.Sprintf("e2e-repo-%s", tc.testID)
		tc.resourceSyncName = fmt.Sprintf("e2e-resourcesync-%s", tc.testID)
	})

	AfterEach(func() {
		// Clean up the local working directory
		if tc != nil && tc.workDir != "" {
			os.RemoveAll(tc.workDir)
		}
	})

	It("fails to sync when repository credentials are invalid", func() {
		err := tc.setupGitRepoWithFiles(getTestDataPath("valid_fleet_files"))
		Expect(err).ToNot(HaveOccurred())

		// Create a Repository resource with INVALID SSH credentials (garbage key)
		repoURL := tc.getGitRepoURL(tc.repoName)
		invalidSSHKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nINVALID_KEY_DATA\n-----END OPENSSH PRIVATE KEY-----"
		err = tc.createRepositoryWithCredentials(tc.repoName, repoURL, invalidSSHKey)
		Expect(err).ToNot(HaveOccurred())

		tc.setupOwnerFilter(tc.resourceSyncName)

		err = tc.createResourceSyncForRepo(tc.resourceSyncName, tc.repoName)
		Expect(err).ToNot(HaveOccurred())

		// Wait for the Accessible condition to become False
		tc.waitForCondition(tc.resourceSyncName, domain.ConditionTypeResourceSyncAccessible, domain.ConditionStatusFalse)

		// Verify no fleets were created
		fleetsResp, err := tc.harness.Client.ListFleetsWithResponse(tc.harness.Context, &tc.listParams)
		Expect(err).ToNot(HaveOccurred())
		Expect(fleetsResp.JSON200).ToNot(BeNil())
		Expect(fleetsResp.JSON200.Items).To(BeEmpty(), "Expected no fleets to be created when repository is inaccessible")
	})

	It("fails to parse invalid fleet YAML", func() {
		err := tc.setupGitRepoWithFiles(getTestDataPath("valid_fleet_files"))
		Expect(err).ToNot(HaveOccurred())

		err = tc.createRepositoryWithValidCredentials(tc.repoName)
		Expect(err).ToNot(HaveOccurred())

		tc.setupOwnerFilter(tc.resourceSyncName)

		err = tc.createResourceSyncForRepo(tc.resourceSyncName, tc.repoName)
		Expect(err).ToNot(HaveOccurred())

		tc.waitForFleetCount(3)

		// Push an invalid fleet YAML file
		err = writeTemplatedFilesToDir(getTestDataPath("invalid_fleet_files"), tc.workDir, templateData{TestID: tc.testID})
		Expect(err).ToNot(HaveOccurred())

		err = tc.harness.CommitAndPushGitRepo(tc.workDir, "Add invalid fleet file")
		Expect(err).ToNot(HaveOccurred())

		// Wait for the ResourceParsed condition to become False
		tc.waitForCondition(tc.resourceSyncName, domain.ConditionTypeResourceSyncResourceParsed, domain.ConditionStatusFalse)

		// Verify the original 3 fleets still exist (not deleted due to parse error)
		fleetsResp, err := tc.harness.Client.ListFleetsWithResponse(tc.harness.Context, &tc.listParams)
		Expect(err).ToNot(HaveOccurred())
		Expect(fleetsResp.JSON200).ToNot(BeNil())
		Expect(fleetsResp.JSON200.Items).To(HaveLen(3), "Expected original 3 fleets to remain unchanged after parse error")
	})

	It("fails to sync when fleet name conflicts with another ResourceSync", func() {
		// === Setup first ResourceSync (rs1) with a conflict fleet ===
		rs1Name := fmt.Sprintf("e2e-resourcesync-1-%s", tc.testID)
		repo1Name := fmt.Sprintf("e2e-repo-1-%s", tc.testID)
		tc.repoName = repo1Name

		err := tc.setupGitRepoWithFiles(getTestDataPath("conflict_fleet_files"))
		Expect(err).ToNot(HaveOccurred())
		workDir1 := tc.workDir // Save for cleanup

		err = tc.createRepositoryWithValidCredentials(repo1Name)
		Expect(err).ToNot(HaveOccurred())

		err = tc.createResourceSyncForRepo(rs1Name, repo1Name)
		Expect(err).ToNot(HaveOccurred())

		tc.setupOwnerFilter(rs1Name)
		tc.waitForFleetCount(1)

		// Verify the fleet is owned by rs1
		conflictFleetName := fmt.Sprintf("resourcesync-conflict-fleet-%s", tc.testID)
		owner, err := tc.getFleetOwner(conflictFleetName)
		Expect(err).ToNot(HaveOccurred())
		Expect(owner).To(Equal(fmt.Sprintf("ResourceSync/%s", rs1Name)), "Expected fleet to be owned by rs1")

		// === Setup second ResourceSync (rs2) with the SAME fleet name ===
		rs2Name := fmt.Sprintf("e2e-resourcesync-2-%s", tc.testID)
		repo2Name := fmt.Sprintf("e2e-repo-2-%s", tc.testID)
		tc.repoName = repo2Name

		err = tc.setupGitRepoWithFiles(getTestDataPath("conflict_fleet_files"))
		Expect(err).ToNot(HaveOccurred())
		workDir2 := tc.workDir

		err = tc.createRepositoryWithValidCredentials(repo2Name)
		Expect(err).ToNot(HaveOccurred())

		err = tc.createResourceSyncForRepo(rs2Name, repo2Name)
		Expect(err).ToNot(HaveOccurred())

		// Wait for rs2's Synced condition to become False due to conflict
		tc.waitForCondition(rs2Name, domain.ConditionTypeResourceSyncSynced, domain.ConditionStatusFalse)

		condMessage, err := tc.getConditionMessage(rs2Name, domain.ConditionTypeResourceSyncSynced)
		Expect(err).ToNot(HaveOccurred())
		Expect(condMessage).To(ContainSubstring("conflict"), "Expected error message to contain conflict")

		// Verify the fleet is still owned by rs1
		owner, err = tc.getFleetOwner(conflictFleetName)
		Expect(err).ToNot(HaveOccurred())
		Expect(owner).To(Equal(fmt.Sprintf("ResourceSync/%s", rs1Name)), "Expected fleet to still be owned by rs1")

		// Cleanup both work directories
		os.RemoveAll(workDir1)
		os.RemoveAll(workDir2)
		tc.workDir = ""
	})
})
