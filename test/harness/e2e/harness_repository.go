package e2e

import (
	"encoding/base64"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
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
