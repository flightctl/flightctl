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

func (t RepositorySpec) GetGitGenericRepoSpec() (GitGenericRepoSpec, error) {
	var body GitGenericRepoSpec
	err := t.getRepoSpec(&body)
	return body, err
}

func (t RepositorySpec) GetGitHttpRepoSpec() (GitHttpRepoSpec, error) {
	var body GitHttpRepoSpec
	err := t.getRepoSpec(&body)
	return body, err
}

func (t RepositorySpec) GetGitSshRepoSpec() (GitSshRepoSpec, error) {
	var body GitSshRepoSpec
	err := t.getRepoSpec(&body)
	return body, err
}

// loose decoder is fine here as all repo specs have `repo` field
func (t RepositorySpec) GetRepoURL() (string, error) {
	genericRepo, err := t.AsGitGenericRepoSpec()
	if err != nil {
		return "", err
	}
	return genericRepo.Repo, nil
}
