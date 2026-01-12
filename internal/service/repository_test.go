package service

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func verifyRepoPatchFailed(require *require.Assertions, status api.Status) {
	require.Equal(statusBadRequestCode, status.Code)
}

func newOciAuth(username, password string) *api.OciAuth {
	auth := &api.OciAuth{}
	_ = auth.FromDockerAuth(api.DockerAuth{
		Username: username,
		Password: password,
	})
	return auth
}

func testRepositoryPatch(require *require.Assertions, patch api.PatchRequest) (*api.Repository, api.Repository, api.Status) {
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  "foo",
		Type: "git",
	})
	require.NoError(err)
	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: spec,
	}

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	ctx := context.Background()
	_, err = serviceHandler.store.Repository().Create(ctx, store.NullOrgId, &repository, nil)
	require.NoError(err)
	resp, status := serviceHandler.PatchRepository(ctx, store.NullOrgId, "foo", patch)
	require.NotEqual(statusFailedCode, status.Code)
	return resp, repository, status
}
func TestRepositoryPatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchSpec(t *testing.T) {
	require := require.New(t)
	pr := api.PatchRequest{
		{Op: "remove", Path: "/spec"},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, orig, status := testRepositoryPatch(require, pr)
	orig.Metadata.Labels = &addLabels
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, orig, status = testRepositoryPatch(require, pr)
	orig.Metadata.Labels = &map[string]string{}
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)
}

func TestRepositoryNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	ctx := context.Background()
	_, err := serviceHandler.store.Repository().Create(ctx, store.NullOrgId, &api.Repository{
		Metadata: api.ObjectMeta{Name: lo.ToPtr("foo")},
	}, nil)
	require.NoError(err)
	_, status := serviceHandler.PatchRepository(ctx, store.NullOrgId, "bar", pr)
	require.Equal(statusNotFoundCode, status.Code)
	event, _ := serviceHandler.store.Event().List(context.Background(), store.NullOrgId, store.ListParams{})
	require.Len(event.Items, 0)
}

func createRepository(ctx context.Context, r store.Repository, orgId uuid.UUID, name string, labels *map[string]string) error {
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url: "myrepourl",
	})
	if err != nil {
		return err
	}
	resource := api.Repository{
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: labels,
		},
		Spec: spec,
	}

	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	_, err = r.Create(ctx, orgId, &resource, callback)
	return err
}

func setAccessCondition(ctx context.Context, orgId uuid.UUID, repository *api.Repository, err error, h ServiceHandler) error {
	if repository.Status == nil {
		repository.Status = &api.RepositoryStatus{Conditions: []api.Condition{}}
	}
	if repository.Status.Conditions == nil {
		repository.Status.Conditions = []api.Condition{}
	}
	_, status := h.ReplaceRepositoryStatusByError(ctx, orgId, lo.FromPtr(repository.Metadata.Name), *repository, err)

	return ApiStatusToErr(status)
}

func TestRepoTester_SetAccessCondition(t *testing.T) {
	require := require.New(t)

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	r := serviceHandler.store.Repository()
	ctx := context.Background()
	orgId := store.NullOrgId

	err := createRepository(ctx, r, orgId, "nil-to-ok", &map[string]string{"status": "OK"})
	require.NoError(err)

	err = createRepository(ctx, r, orgId, "ok-to-ok", &map[string]string{"status": "OK"})
	require.NoError(err)
	repo, err := r.Get(ctx, orgId, "ok-to-ok")
	require.NoError(err)

	err = setAccessCondition(ctx, orgId, repo, err, serviceHandler)
	require.NoError(err)
}

func testOciRepositoryPatch(require *require.Assertions, patch api.PatchRequest) (*api.Repository, api.Repository, api.Status) {
	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(api.OciRepoSpec{
		Registry: "quay.io",
		Type:     "oci",
		OciAuth:  newOciAuth("myuser", "mypassword"),
	})
	require.NoError(err)
	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("oci-repo"),
			Labels: &map[string]string{"type": "oci"},
		},
		Spec: spec,
	}

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	ctx := context.Background()
	_, err = serviceHandler.store.Repository().Create(ctx, store.NullOrgId, &repository, nil)
	require.NoError(err)
	resp, status := serviceHandler.PatchRepository(ctx, store.NullOrgId, "oci-repo", patch)
	require.NotEqual(statusFailedCode, status.Code)
	return resp, repository, status
}

func TestOciRepositoryPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"type": "oci", "env": "prod"}
	var value interface{} = "prod"
	pr := api.PatchRequest{
		{Op: "add", Path: "/metadata/labels/env", Value: &value},
	}

	resp, orig, status := testOciRepositoryPatch(require, pr)
	orig.Metadata.Labels = &addLabels
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)
}

func TestOciRepositoryCreate(t *testing.T) {
	require := require.New(t)

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	ctx := context.Background()

	// Test creating OCI repository with credentials
	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(api.OciRepoSpec{
		Registry: "quay.io",
		Type:     "oci",
		OciAuth:  newOciAuth("myuser", "mypassword"),
	})
	require.NoError(err)

	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("test-oci-repo"),
		},
		Spec: spec,
	}

	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)
	require.NotNil(resp)
	require.Equal("test-oci-repo", *resp.Metadata.Name)

	// Verify we can retrieve it
	retrieved, err := serviceHandler.store.Repository().Get(ctx, store.NullOrgId, "test-oci-repo")
	require.NoError(err)
	require.NotNil(retrieved)

	// Verify the OCI spec is preserved
	ociSpec, err := retrieved.Spec.GetOciRepoSpec()
	require.NoError(err)
	require.Equal("quay.io", ociSpec.Registry)
	require.Equal(api.RepoSpecTypeOci, ociSpec.Type)
	require.NotNil(ociSpec.OciAuth)
	dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
	require.NoError(err)
	require.Equal("myuser", dockerAuth.Username)
	require.Equal("mypassword", dockerAuth.Password)
}

func TestOciRepositoryCreateWithoutCredentials(t *testing.T) {
	require := require.New(t)

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
	ctx := context.Background()

	// Test creating OCI repository without credentials (public registry)
	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(api.OciRepoSpec{
		Registry: "registry.redhat.io",
		Type:     "oci",
	})
	require.NoError(err)

	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("public-oci-repo"),
		},
		Spec: spec,
	}

	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)
	require.NotNil(resp)

	// Verify the OCI spec without credentials
	ociSpec, err := resp.Spec.GetOciRepoSpec()
	require.NoError(err)
	require.Equal("registry.redhat.io", ociSpec.Registry)
	require.Nil(ociSpec.OciAuth)
}

// createServiceHandler creates a ServiceHandler with a TestStore for testing
func createServiceHandler() ServiceHandler {
	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	return ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
}

// Git Repository (GenericRepoSpec) CRUD Tests

func TestGitRepositoryCreate(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  "https://github.com/flightctl/flightctl.git",
		Type: api.RepoSpecTypeGit,
	})
	require.NoError(err)

	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("test-git-repo"),
			Labels: &map[string]string{"type": "git"},
		},
		Spec: spec,
	}

	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)
	require.NotNil(resp)
	require.Equal("test-git-repo", *resp.Metadata.Name)

	// Verify we can retrieve it
	retrieved, err := serviceHandler.store.Repository().Get(ctx, store.NullOrgId, "test-git-repo")
	require.NoError(err)
	require.NotNil(retrieved)

	// Verify the spec is preserved
	genericSpec, err := retrieved.Spec.GetGenericRepoSpec()
	require.NoError(err)
	require.Equal("https://github.com/flightctl/flightctl.git", genericSpec.Url)
	require.Equal(api.RepoSpecTypeGit, genericSpec.Type)
}

func TestGitRepositoryGet(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// Create a repository first
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  "https://github.com/flightctl/flightctl.git",
		Type: api.RepoSpecTypeGit,
	})
	require.NoError(err)

	repository := api.Repository{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("get-test-repo"),
		},
		Spec: spec,
	}

	_, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)

	// Get the repository
	retrieved, status := serviceHandler.GetRepository(ctx, store.NullOrgId, "get-test-repo")
	require.Equal(int32(200), status.Code)
	require.NotNil(retrieved)
	require.Equal("get-test-repo", *retrieved.Metadata.Name)
}

func TestGitRepositoryGetNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// Try to get a non-existent repository
	_, status := serviceHandler.GetRepository(ctx, store.NullOrgId, "non-existent-repo")
	require.Equal(int32(404), status.Code)
}

func TestGitRepositoryReplace(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// Create a repository first
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  "https://github.com/original/repo.git",
		Type: api.RepoSpecTypeGit,
	})
	require.NoError(err)

	repository := api.Repository{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("replace-test-repo"),
		},
		Spec: spec,
	}

	_, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)

	// Replace with updated URL
	err = spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  "https://github.com/updated/repo.git",
		Type: api.RepoSpecTypeGit,
	})
	require.NoError(err)
	repository.Spec = spec

	resp, status := serviceHandler.ReplaceRepository(ctx, store.NullOrgId, "replace-test-repo", repository)
	require.Equal(int32(200), status.Code)
	require.NotNil(resp)

	// Verify the update
	genericSpec, err := resp.Spec.GetGenericRepoSpec()
	require.NoError(err)
	require.Equal("https://github.com/updated/repo.git", genericSpec.Url)
}

func TestGitRepositoryDelete(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// Create a repository first
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  "https://github.com/flightctl/flightctl.git",
		Type: api.RepoSpecTypeGit,
	})
	require.NoError(err)

	repository := api.Repository{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("delete-test-repo"),
		},
		Spec: spec,
	}

	_, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)

	// Delete the repository
	status = serviceHandler.DeleteRepository(ctx, store.NullOrgId, "delete-test-repo")
	require.Equal(int32(200), status.Code)

	// Verify it's deleted
	_, status = serviceHandler.GetRepository(ctx, store.NullOrgId, "delete-test-repo")
	require.Equal(int32(404), status.Code)
}

// SSH Repository (SshRepoSpec) CRUD Tests

func TestSshRepositoryCreate(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// Valid base64-encoded private key (base64 of "ssh-rsa AAAA...")
	privateKey := "c3NoLXJzYSBBQUFBQg=="
	passphrase := "mysecretpassphrase"
	skipVerify := true

	spec := api.RepositorySpec{}
	err := spec.FromSshRepoSpec(api.SshRepoSpec{
		Url:  "git@github.com:flightctl/flightctl.git",
		Type: api.RepoSpecTypeGit,
		SshConfig: api.SshConfig{
			SshPrivateKey:          &privateKey,
			PrivateKeyPassphrase:   &passphrase,
			SkipServerVerification: &skipVerify,
		},
	})
	require.NoError(err)

	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("test-ssh-repo"),
			Labels: &map[string]string{"type": "ssh"},
		},
		Spec: spec,
	}

	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)
	require.NotNil(resp)
	require.Equal("test-ssh-repo", *resp.Metadata.Name)

	// Verify we can retrieve it
	retrieved, err := serviceHandler.store.Repository().Get(ctx, store.NullOrgId, "test-ssh-repo")
	require.NoError(err)
	require.NotNil(retrieved)

	// Verify the SSH spec is preserved
	sshSpec, err := retrieved.Spec.GetSshRepoSpec()
	require.NoError(err)
	require.Equal("git@github.com:flightctl/flightctl.git", sshSpec.Url)
	require.Equal(api.RepoSpecTypeGit, sshSpec.Type)
	require.NotNil(sshSpec.SshConfig.SshPrivateKey)
	require.Equal(privateKey, *sshSpec.SshConfig.SshPrivateKey)
	require.NotNil(sshSpec.SshConfig.PrivateKeyPassphrase)
	require.Equal(passphrase, *sshSpec.SshConfig.PrivateKeyPassphrase)
	require.NotNil(sshSpec.SshConfig.SkipServerVerification)
	require.True(*sshSpec.SshConfig.SkipServerVerification)
}

func TestSshRepositoryCreateWithoutPassphrase(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// Valid base64-encoded private key
	privateKey := "c3NoLXJzYSBBQUFBQg=="

	spec := api.RepositorySpec{}
	err := spec.FromSshRepoSpec(api.SshRepoSpec{
		Url:  "git@gitlab.com:myorg/myrepo.git",
		Type: api.RepoSpecTypeGit,
		SshConfig: api.SshConfig{
			SshPrivateKey: &privateKey,
		},
	})
	require.NoError(err)

	repository := api.Repository{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("ssh-no-passphrase"),
		},
		Spec: spec,
	}

	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)
	require.NotNil(resp)

	// Verify the SSH spec without passphrase
	sshSpec, err := resp.Spec.GetSshRepoSpec()
	require.NoError(err)
	require.Equal("git@gitlab.com:myorg/myrepo.git", sshSpec.Url)
	require.NotNil(sshSpec.SshConfig.SshPrivateKey)
	require.Nil(sshSpec.SshConfig.PrivateKeyPassphrase)
}

// HTTP Repository (HttpRepoSpec) CRUD Tests

func TestHttpRepositoryCreate(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	username := "httpuser"
	password := "httppassword"
	skipVerify := true

	spec := api.RepositorySpec{}
	err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Url:  "https://github.com/flightctl/flightctl.git",
		Type: api.RepoSpecTypeHttp,
		HttpConfig: api.HttpConfig{
			Username:               &username,
			Password:               &password,
			SkipServerVerification: &skipVerify,
		},
	})
	require.NoError(err)

	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("test-http-repo"),
			Labels: &map[string]string{"type": "http"},
		},
		Spec: spec,
	}

	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)
	require.NotNil(resp)
	require.Equal("test-http-repo", *resp.Metadata.Name)

	// Verify we can retrieve it
	retrieved, err := serviceHandler.store.Repository().Get(ctx, store.NullOrgId, "test-http-repo")
	require.NoError(err)
	require.NotNil(retrieved)

	// Verify the HTTP spec is preserved
	httpSpec, err := retrieved.Spec.GetHttpRepoSpec()
	require.NoError(err)
	require.Equal("https://github.com/flightctl/flightctl.git", httpSpec.Url)
	require.Equal(api.RepoSpecTypeHttp, httpSpec.Type)
	require.NotNil(httpSpec.HttpConfig.Username)
	require.Equal(username, *httpSpec.HttpConfig.Username)
	require.NotNil(httpSpec.HttpConfig.Password)
	require.Equal(password, *httpSpec.HttpConfig.Password)
	require.NotNil(httpSpec.HttpConfig.SkipServerVerification)
	require.True(*httpSpec.HttpConfig.SkipServerVerification)
}

func TestHttpRepositoryCreateWithToken(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// JWT format token (three base64url-encoded parts separated by dots)
	token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c" //nolint:gosec

	spec := api.RepositorySpec{}
	err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Url:  "https://github.com/flightctl/flightctl.git",
		Type: api.RepoSpecTypeHttp,
		HttpConfig: api.HttpConfig{
			Token: &token,
		},
	})
	require.NoError(err)

	repository := api.Repository{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("http-token-repo"),
		},
		Spec: spec,
	}

	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)
	require.NotNil(resp)

	// Verify the HTTP spec with token
	httpSpec, err := resp.Spec.GetHttpRepoSpec()
	require.NoError(err)
	require.NotNil(httpSpec.HttpConfig.Token)
	require.Equal(token, *httpSpec.HttpConfig.Token)
	require.Nil(httpSpec.HttpConfig.Username)
	require.Nil(httpSpec.HttpConfig.Password)
}

func TestHttpRepositoryCreateWithTLS(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// Valid base64-encoded certificate/key data
	caCrt := "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCg=="
	tlsCrt := "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCg=="
	tlsKey := "LS0tLS1CRUdJTiBQUklWQVRFIEtFWS0tLS0tCg=="

	spec := api.RepositorySpec{}
	err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Url:  "https://private.git.server/repo.git",
		Type: api.RepoSpecTypeHttp,
		HttpConfig: api.HttpConfig{
			CaCrt:  &caCrt,
			TlsCrt: &tlsCrt,
			TlsKey: &tlsKey,
		},
	})
	require.NoError(err)

	repository := api.Repository{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("http-tls-repo"),
		},
		Spec: spec,
	}

	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)
	require.NotNil(resp)

	// Verify the HTTP spec with TLS config
	httpSpec, err := resp.Spec.GetHttpRepoSpec()
	require.NoError(err)
	require.NotNil(httpSpec.HttpConfig.CaCrt)
	require.Equal(caCrt, *httpSpec.HttpConfig.CaCrt)
	require.NotNil(httpSpec.HttpConfig.TlsCrt)
	require.Equal(tlsCrt, *httpSpec.HttpConfig.TlsCrt)
	require.NotNil(httpSpec.HttpConfig.TlsKey)
	require.Equal(tlsKey, *httpSpec.HttpConfig.TlsKey)
}

func TestHttpRepositoryReplace(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// Create initial HTTP repository with username/password
	username := "originaluser"
	password := "originalpass"
	spec := api.RepositorySpec{}
	err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Url:  "https://github.com/original/repo.git",
		Type: api.RepoSpecTypeHttp,
		HttpConfig: api.HttpConfig{
			Username: &username,
			Password: &password,
		},
	})
	require.NoError(err)

	repository := api.Repository{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("http-replace-test"),
		},
		Spec: spec,
	}

	_, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)

	// Replace with new values
	newUsername := "updateduser"
	newPassword := "updatedpass"
	err = spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Url:  "https://github.com/updated/repo.git",
		Type: api.RepoSpecTypeHttp,
		HttpConfig: api.HttpConfig{
			Username: &newUsername,
			Password: &newPassword,
		},
	})
	require.NoError(err)
	repository.Spec = spec

	resp, status := serviceHandler.ReplaceRepository(ctx, store.NullOrgId, "http-replace-test", repository)
	require.Equal(int32(200), status.Code)
	require.NotNil(resp)

	// Verify the update
	httpSpec, err := resp.Spec.GetHttpRepoSpec()
	require.NoError(err)
	require.Equal("https://github.com/updated/repo.git", httpSpec.Url)
	require.NotNil(httpSpec.HttpConfig.Username)
	require.Equal(newUsername, *httpSpec.HttpConfig.Username)
}

func TestHttpRepositoryDelete(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	serviceHandler := createServiceHandler()

	// Create HTTP repository with username/password
	username := "deleteuser"
	password := "deletepass"
	spec := api.RepositorySpec{}
	err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Url:  "https://github.com/delete/repo.git",
		Type: api.RepoSpecTypeHttp,
		HttpConfig: api.HttpConfig{
			Username: &username,
			Password: &password,
		},
	})
	require.NoError(err)

	repository := api.Repository{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("http-delete-test"),
		},
		Spec: spec,
	}

	_, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, repository)
	require.Equal(int32(201), status.Code)

	// Verify it exists
	_, status = serviceHandler.GetRepository(ctx, store.NullOrgId, "http-delete-test")
	require.Equal(int32(200), status.Code)

	// Delete the repository
	status = serviceHandler.DeleteRepository(ctx, store.NullOrgId, "http-delete-test")
	require.Equal(int32(200), status.Code)

	// Verify it no longer exists
	_, status = serviceHandler.GetRepository(ctx, store.NullOrgId, "http-delete-test")
	require.Equal(int32(404), status.Code)
}
