package v1beta1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Discriminator returns the value of the type discriminator field.
func (t RepositorySpec) Discriminator() (string, error) {
	var disc struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(t.union, &disc); err != nil {
		return "", fmt.Errorf("failed to unmarshal discriminator: %w", err)
	}
	if disc.Type == "" {
		return "", errors.New("discriminator field 'type' is missing or empty")
	}
	return disc.Type, nil
}

// oapi-codegen generates AsGitHttpRepoSpec function but the generated function
// is not using strict decoder.
// The loose decoder settings may decode GitSshRepoSpec as GitHttpRepoSpec and vice-versa.
func (t RepositorySpec) getRepoSpec(body any) error {
	decoder := json.NewDecoder(bytes.NewReader(t.union))
	decoder.DisallowUnknownFields()
	return decoder.Decode(body)
}

func (t RepositorySpec) GetGenericRepoSpec() (GenericRepoSpec, error) {
	var body GenericRepoSpec
	err := t.getRepoSpec(&body)
	return body, err
}

func (t RepositorySpec) GetHttpRepoSpec() (HttpRepoSpec, error) {
	var body HttpRepoSpec
	err := t.getRepoSpec(&body)
	return body, err
}

func (t RepositorySpec) GetSshRepoSpec() (SshRepoSpec, error) {
	var body SshRepoSpec
	if err := t.getRepoSpec(&body); err != nil {
		return body, err
	}
	// Validate that sshConfig is actually present (not just an empty struct from decoding GenericRepoSpec)
	if body.SshConfig.SshPrivateKey == nil && body.SshConfig.PrivateKeyPassphrase == nil && body.SshConfig.SkipServerVerification == nil {
		return body, errors.New("not an SSH repository spec: sshConfig is empty")
	}
	return body, nil
}

func (t RepositorySpec) GetOciRepoSpec() (OciRepoSpec, error) {
	var body OciRepoSpec
	err := t.getRepoSpec(&body)
	return body, err
}

// GetRepoURL returns the repository URL. For OCI repositories, it returns the registry hostname.
func (t RepositorySpec) GetRepoURL() (string, error) {
	// Check if it's an OCI repository - if so, return the registry hostname
	ociSpec, err := t.GetOciRepoSpec()
	if err == nil {
		return ociSpec.Registry, nil
	}

	// For other repository types, get the URL from GenericRepoSpec
	genericRepo, err := t.AsGenericRepoSpec()
	if err != nil {
		return "", err
	}
	return genericRepo.Url, nil
}
