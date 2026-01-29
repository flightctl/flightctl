package e2e

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

func (h *Harness) GetRepository(repositoryName string) (*v1beta1.Repository, error) {
	response, err := h.Client.GetRepositoryWithResponse(h.Context, repositoryName)
	if err != nil {
		return nil, fmt.Errorf("failed to get repository: %w", err)
	}
	if response == nil {
		return nil, fmt.Errorf("repository response is nil")
	}
	if response.JSON200 != nil {
		return response.JSON200, nil
	}
	status := 0
	if response.HTTPResponse != nil {
		status = response.HTTPResponse.StatusCode
	}
	body := string(response.Body)
	return nil, fmt.Errorf("failed to get repository %q: status=%d body=%s", repositoryName, status, body)
}

// GetInternalGitRepoURL returns the internal cluster URL for a git repository on the E2E git server.
// This URL is used by services running inside the cluster (e.g., ResourceSync periodic task).
func (h *Harness) GetInternalGitRepoURL(repoName string) (string, error) {
	gitConfig, err := h.GetGitServerConfig()
	if err != nil {
		return "", fmt.Errorf("failed to get git server config: %w", err)
	}
	// Use the internal cluster URL and port since services run inside the cluster.
	gitServerInternalHost := "e2e-git-server.flightctl-e2e.svc.cluster.local"
	gitServerInternalPort := 3222
	return fmt.Sprintf("%s@%s:%d:/home/user/repos/%s.git",
		gitConfig.User, gitServerInternalHost, gitServerInternalPort, repoName), nil
}

// CreateRepositoryWithSSHCredentials creates a Repository resource with SSH credentials
func (h *Harness) CreateRepositoryWithSSHCredentials(repoName, repoURL, sshPrivateKey string) error {
	sshPrivateKeyBase64 := base64.StdEncoding.EncodeToString([]byte(sshPrivateKey))

	repoSpec := v1beta1.RepositorySpec{}
	err := repoSpec.FromGitRepoSpec(v1beta1.GitRepoSpec{
		Url:  repoURL,
		Type: v1beta1.GitRepoSpecTypeGit,
		SshConfig: &v1beta1.SshConfig{
			SshPrivateKey:          lo.ToPtr(sshPrivateKeyBase64),
			SkipServerVerification: lo.ToPtr(true),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create SSH repo spec: %w", err)
	}

	repository := v1beta1.Repository{
		ApiVersion: v1beta1.RepositoryAPIVersion,
		Kind:       v1beta1.RepositoryKind,
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(repoName),
		},
		Spec: repoSpec,
	}

	resp, err := h.Client.CreateRepositoryWithResponse(h.Context, repository)
	if err != nil {
		return fmt.Errorf("failed to create repository: %w", err)
	}
	if resp.StatusCode() != 201 {
		return fmt.Errorf("expected 201 Created, got %d: %s", resp.StatusCode(), string(resp.Body))
	}
	return nil
}

// CreateRepositoryWithValidE2ECredentials creates a Repository resource using the E2E SSH key
// and the internal cluster URL for the git server.
func (h *Harness) CreateRepositoryWithValidE2ECredentials(repoName string) error {
	repoURL, err := h.GetInternalGitRepoURL(repoName)
	if err != nil {
		return fmt.Errorf("failed to get internal git repo URL: %w", err)
	}
	sshPrivateKey, err := GetSSHPrivateKey()
	if err != nil {
		return fmt.Errorf("failed to read SSH private key: %w", err)
	}
	return h.CreateRepositoryWithSSHCredentials(repoName, repoURL, sshPrivateKey)
}

// WaitForRepositoryAccessible waits for a repository to have its Accessible condition set to True.
func (h *Harness) WaitForRepositoryAccessible(name string, timeout, polling time.Duration) error {
	GinkgoWriter.Printf("Waiting for Repository %s to be accessible (timeout: %v, polling: %v)\n", name, timeout, polling)

	Eventually(func() (bool, error) {
		repo, err := h.GetRepository(name)
		if err != nil {
			return false, err
		}

		if repo.Status == nil || len(repo.Status.Conditions) == 0 {
			GinkgoWriter.Printf("Repository %s has no status/conditions yet\n", name)
			return false, nil
		}

		for _, cond := range repo.Status.Conditions {
			if cond.Type == v1beta1.ConditionTypeRepositoryAccessible {
				if cond.Status == v1beta1.ConditionStatusTrue {
					GinkgoWriter.Printf("Repository %s is accessible\n", name)
					return true, nil
				}
				GinkgoWriter.Printf("Repository %s Accessible condition: status=%s reason=%s message=%s\n",
					name, cond.Status, cond.Reason, cond.Message)
				return false, nil
			}
		}

		GinkgoWriter.Printf("Repository %s has no Accessible condition yet\n", name)
		return false, nil
	}, timeout, polling).Should(BeTrue(),
		fmt.Sprintf("Repository %s should become accessible", name))

	return nil
}

// IsRepositoryAccessible checks if a repository has its Accessible condition set to True.
func (h *Harness) IsRepositoryAccessible(name string) (bool, error) {
	repo, err := h.GetRepository(name)
	if err != nil {
		return false, err
	}

	if repo.Status == nil || len(repo.Status.Conditions) == 0 {
		return false, nil
	}

	for _, cond := range repo.Status.Conditions {
		if cond.Type == v1beta1.ConditionTypeRepositoryAccessible {
			return cond.Status == v1beta1.ConditionStatusTrue, nil
		}
	}

	return false, nil
}

// WaitForRepositoryNotAccessible waits for a repository to have its Accessible condition set to False.
func (h *Harness) WaitForRepositoryNotAccessible(name string, timeout, polling time.Duration) error {
	GinkgoWriter.Printf("Waiting for Repository %s to become not accessible (timeout: %v, polling: %v)\n", name, timeout, polling)

	Eventually(func() (bool, error) {
		repo, err := h.GetRepository(name)
		if err != nil {
			return false, err
		}

		if repo.Status == nil || len(repo.Status.Conditions) == 0 {
			GinkgoWriter.Printf("Repository %s has no status/conditions yet\n", name)
			return false, nil
		}

		for _, cond := range repo.Status.Conditions {
			if cond.Type == v1beta1.ConditionTypeRepositoryAccessible {
				if cond.Status == v1beta1.ConditionStatusFalse {
					GinkgoWriter.Printf("Repository %s is not accessible: %s\n", name, cond.Message)
					return true, nil
				}
				GinkgoWriter.Printf("Repository %s Accessible condition: status=%s\n", name, cond.Status)
				return false, nil
			}
		}

		return false, nil
	}, timeout, polling).Should(BeTrue(),
		fmt.Sprintf("Repository %s should become not accessible", name))

	return nil
}

// UpdateOCIRepositoryRegistry updates an OCI repository's registry URL.
func (h *Harness) UpdateOCIRepositoryRegistry(name, newRegistry string, skipTLSVerify bool) error {
	// Get the existing repository
	repo, err := h.GetRepository(name)
	if err != nil {
		return fmt.Errorf("failed to get repository: %w", err)
	}

	// Get existing OCI spec
	ociSpec, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		return fmt.Errorf("failed to get OCI spec: %w", err)
	}

	// Update the registry
	ociSpec.Registry = newRegistry
	if skipTLSVerify {
		ociSpec.SkipServerVerification = lo.ToPtr(true)
	}

	// Create new spec
	newSpec := v1beta1.RepositorySpec{}
	if err := newSpec.FromOciRepoSpec(ociSpec); err != nil {
		return fmt.Errorf("failed to create new spec: %w", err)
	}

	// Update the repository
	repo.Spec = newSpec

	resp, err := h.Client.ReplaceRepositoryWithResponse(h.Context, name, *repo)
	if err != nil {
		return fmt.Errorf("failed to replace repository: %w", err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("expected 200 OK, got %d: %s", resp.StatusCode(), string(resp.Body))
	}

	GinkgoWriter.Printf("Updated repository %s registry to %s\n", name, newRegistry)
	return nil
}

// ListRepositories lists all repositories.
func (h *Harness) ListRepositories() (*v1beta1.RepositoryList, error) {
	resp, err := h.Client.ListRepositoriesWithResponse(h.Context, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list repositories: %w", err)
	}
	if resp.JSON200 != nil {
		return resp.JSON200, nil
	}
	status := 0
	if resp.HTTPResponse != nil {
		status = resp.HTTPResponse.StatusCode
	}
	return nil, fmt.Errorf("failed to list repositories: status=%d body=%s", status, string(resp.Body))
}

// CreateHTTPRepository creates an HTTP repository.
func (h *Harness) CreateHTTPRepository(name, url string, username, password *string) (*v1beta1.Repository, error) {
	httpSpec := v1beta1.HttpRepoSpec{
		Url:  url,
		Type: v1beta1.HttpRepoSpecTypeHttp,
	}

	if username != nil || password != nil {
		httpSpec.HttpConfig = &v1beta1.HttpConfig{
			Username: username,
			Password: password,
		}
	}

	spec := v1beta1.RepositorySpec{}
	if err := spec.FromHttpRepoSpec(httpSpec); err != nil {
		return nil, fmt.Errorf("failed to create HTTP repo spec: %w", err)
	}

	repository := v1beta1.Repository{
		ApiVersion: v1beta1.RepositoryAPIVersion,
		Kind:       v1beta1.RepositoryKind,
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
	}

	h.addTestLabelToRepositoryMetadata(&repository.Metadata)

	resp, err := h.Client.CreateRepositoryWithResponse(h.Context, repository)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP repository: %w", err)
	}
	if resp.StatusCode() != 201 {
		return nil, fmt.Errorf("expected 201 Created, got %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return resp.JSON201, nil
}

// CreateGitRepositoryNoAuth creates a Git repository without authentication.
func (h *Harness) CreateGitRepositoryNoAuth(name, url string) (*v1beta1.Repository, error) {
	gitSpec := v1beta1.GitRepoSpec{
		Url:  url,
		Type: v1beta1.GitRepoSpecTypeGit,
	}

	spec := v1beta1.RepositorySpec{}
	if err := spec.FromGitRepoSpec(gitSpec); err != nil {
		return nil, fmt.Errorf("failed to create Git repo spec: %w", err)
	}

	repository := v1beta1.Repository{
		ApiVersion: v1beta1.RepositoryAPIVersion,
		Kind:       v1beta1.RepositoryKind,
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
	}

	h.addTestLabelToRepositoryMetadata(&repository.Metadata)

	resp, err := h.Client.CreateRepositoryWithResponse(h.Context, repository)
	if err != nil {
		return nil, fmt.Errorf("failed to create Git repository: %w", err)
	}
	if resp.StatusCode() != 201 {
		return nil, fmt.Errorf("expected 201 Created, got %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return resp.JSON201, nil
}

// addTestLabelToRepositoryMetadata adds test labels to repository metadata.
func (h *Harness) addTestLabelToRepositoryMetadata(metadata *v1beta1.ObjectMeta) {
	h.SetLabelsForRepositoryMetadata(metadata, nil)
}
