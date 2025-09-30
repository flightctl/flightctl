package git_server

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Git server basic operations", Label("git-server", "sanity"), func() {
	It("creates a repo, pushes a file, clones and verifies content", func() {
		// Get harness
		h := e2e.GetWorkerHarness()

		// Unique names via test-id
		repoName := "repo-" + h.GetTestIDFromContext()
		fileRelPath := "README.md"
		fileContent := "hello from flightctl e2e\n"

		// Build the repository spec pointing to the e2e git server
		cfg := h.GetGitServerConfig()
		repoURL := fmt.Sprintf("ssh://%s@%s:%d/home/user/repos/%s.git", cfg.User, cfg.Host, cfg.Port, repoName)
		spec := v1alpha1.RepositorySpec{}
		Expect(spec.FromGenericRepoSpec(v1alpha1.GenericRepoSpec{Type: v1alpha1.Git, Url: repoURL})).To(Succeed())

		// Create the bare repo on server and the Repository resource (with test labels)
		Expect(h.CreateGitRepositoryOnServer(repoName)).To(Succeed())
		meta := v1alpha1.ObjectMeta{Name: &repoName}
		Expect(h.CreateRepository(spec, meta)).To(Succeed())
		DeferCleanup(func() { _ = h.DeleteGitRepositoryOnServer(repoName) })

		// Push content into the repo
		Expect(h.PushContentToGitServerRepo(repoName, fileRelPath, fileContent, "initial commit")).To(Succeed())

		// Clone locally to verify
		workDir := GinkgoT().TempDir()
		clonePath := filepath.Join(workDir, "clone")
		Expect(h.CloneGitRepositoryFromServer(repoName, clonePath)).To(Succeed())

		data, err := os.ReadFile(filepath.Join(clonePath, fileRelPath))
		Expect(err).ToNot(HaveOccurred())
		Expect(string(data)).To(Equal(fileContent))
	})
})
