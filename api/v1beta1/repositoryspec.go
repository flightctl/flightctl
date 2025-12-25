package v1beta1

import (
	"bytes"
	"encoding/json"
	"errors"
)

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

// FromSshRepoSpec overwrites the union data with the provided SshRepoSpec.
// SshRepoSpec is a structural variant of GenericRepoSpec (both use type: git),
// distinguished by the presence of sshConfig.
func (t *RepositorySpec) FromSshRepoSpec(v SshRepoSpec) error {
	v.Type = Git // SSH repos use type: git
	b, err := json.Marshal(v)
	t.union = b
	return err
}

func (t RepositorySpec) GetOciRepoSpec() (OciRepoSpec, error) {
	var body OciRepoSpec
	err := t.getRepoSpec(&body)
	return body, err
}

// loose decoder is fine here as all repo specs have `repo` field
func (t RepositorySpec) GetRepoURL() (string, error) {
	genericRepo, err := t.AsGenericRepoSpec()
	if err != nil {
		return "", err
	}
	return genericRepo.Url, nil
}
