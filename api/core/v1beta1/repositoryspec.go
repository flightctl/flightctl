package v1beta1

import "fmt"

// GetRepoURL returns the repository URL. For OCI repositories, it returns the registry hostname.
func (t RepositorySpec) GetRepoURL() (string, error) {
	repoType, err := t.Discriminator()
	if err != nil {
		return "", fmt.Errorf("failed to determine repository type: %w", err)
	}

	switch repoType {
	case string(OciRepoSpecTypeOci):
		ociSpec, err := t.AsOciRepoSpec()
		if err != nil {
			return "", fmt.Errorf("failed to decode OCI repository spec: %w", err)
		}
		return ociSpec.Registry, nil
	case string(GitRepoSpecTypeGit):
		gitSpec, err := t.AsGitRepoSpec()
		if err != nil {
			return "", fmt.Errorf("failed to decode Git repository spec: %w", err)
		}
		return gitSpec.Url, nil
	case string(HttpRepoSpecTypeHttp):
		httpSpec, err := t.AsHttpRepoSpec()
		if err != nil {
			return "", fmt.Errorf("failed to decode HTTP repository spec: %w", err)
		}
		return httpSpec.Url, nil
	default:
		return "", fmt.Errorf("unknown repository type: %s", repoType)
	}
}
