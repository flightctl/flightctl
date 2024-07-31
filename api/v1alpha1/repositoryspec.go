package v1alpha1

import (
	"bytes"
	"encoding/json"
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
