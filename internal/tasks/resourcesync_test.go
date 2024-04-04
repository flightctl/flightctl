package tasks

import (
	"fmt"
	"os"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/stretchr/testify/require"
)

func TestIsValidFile_invalid(t *testing.T) {
	require := require.New(t)

	require.False(isValidFile("something"))
	require.False(isValidFile("something.pdf"))
}

func TestIsValidFile_valid(t *testing.T) {
	require := require.New(t)

	for _, ext := range validFileExtensions {
		require.True(isValidFile(fmt.Sprintf("file.%s", ext)))
	}
}

func TestFleetDelta(t *testing.T) {
	require := require.New(t)

	owner := util.SetResourceOwner(model.ResourceSyncKind, "foo")
	ownedFleets := []api.Fleet{
		{
			Metadata: api.ObjectMeta{
				Name:  util.StrToPtr("fleet-1"),
				Owner: owner,
			},
		},
		{
			Metadata: api.ObjectMeta{
				Name:  util.StrToPtr("fleet-2"),
				Owner: owner,
			},
		},
	}
	newFleets := []*api.Fleet{
		&ownedFleets[1],
	}

	delta := fleetsDelta(ownedFleets, newFleets)
	require.Len(delta, 1)
	require.Equal(delta[0], "fleet-1")

}
func TestParseAndValidate_already_in_sync(t *testing.T) {
	require := require.New(t)
	rs := testResourceSync()
	repo := testRepo()
	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	// Patch the status so we are already in sync
	rs.Status.Data.ObservedCommit = &gitRepoCommit
	rs.Status.Data.ObservedGeneration = util.Int64ToPtr(1)

	// Already in sync with hash
	rm, err := rsTask.parseAndValidateResources(&rs, &repo, testCloneEmptyGitRepo)
	require.NoError(err)
	require.Nil(rm)
}

func TestParseAndValidate_no_files(t *testing.T) {
	require := require.New(t)
	rs := testResourceSync()
	repo := testRepo()
	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	// Empty folder
	_, err := rsTask.parseAndValidateResources(&rs, &repo, testCloneEmptyGitRepo)
	require.Error(err)
}

func TestParseAndValidate_unsupportedFiles(t *testing.T) {
	require := require.New(t)
	rs := testResourceSync()
	repo := testRepo()
	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	_, err := rsTask.parseAndValidateResources(&rs, &repo, testCloneUnsupportedGitRepo)
	require.Error(err)
}

func TestParseAndValidate_singleFile(t *testing.T) {
	require := require.New(t)
	rs := testResourceSync()
	repo := testRepo()
	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	rs.Spec.Data.Path = util.StrToPtr("/examples/fleet.yaml")
	resources, err := rsTask.parseAndValidateResources(&rs, &repo, testCloneUnsupportedGitRepo)
	require.NoError(err)
	require.Len(resources, 1)
	require.Equal(resources[0]["kind"], model.FleetKind)
}

func TestExtractResourceFromFile(t *testing.T) {
	require := require.New(t)

	memfs := memfs.New()
	writeCopy(memfs, "../../examples/fleet.yaml", "/fleet.yaml")

	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	genericResources, err := rsTask.extractResourcesFromFile(memfs, "/fleet.yaml")
	require.NoError(err)
	require.Len(genericResources, 1)
	require.Equal(genericResources[0]["kind"], model.FleetKind)
}

func TestExtractResourceFromDir(t *testing.T) {
	require := require.New(t)

	memfs := memfs.New()
	require.NoError(memfs.MkdirAll("/fleets", 0666))
	writeCopy(memfs, "../../examples/fleet.yaml", "/fleets/fleet.yaml")
	writeCopy(memfs, "../../examples/fleet-b.yaml", "/fleets/fleet-b.yaml")

	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	genericResources, err := rsTask.extractResourcesFromDir(memfs, "/fleets/")
	require.NoError(err)
	require.Len(genericResources, 2)
	require.Equal(genericResources[0]["kind"], model.FleetKind)
	require.Equal(genericResources[1]["kind"], model.FleetKind)

}

func TestExtractResourceFromFile_incompatible(t *testing.T) {
	require := require.New(t)

	memfs := memfs.New()
	writeCopy(memfs, "../../examples/device.yaml", "/device.yaml")

	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	_, err := rsTask.extractResourcesFromFile(memfs, "/device.yaml")
	require.Error(err)
}

func TestParseFleet(t *testing.T) {
	require := require.New(t)

	memfs := memfs.New()
	writeCopy(memfs, "../../examples/fleet.yaml", "/fleet.yaml")

	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	genericResources, err := rsTask.extractResourcesFromFile(memfs, "/fleet.yaml")
	require.NoError(err)
	require.Len(genericResources, 1)

	owner := util.SetResourceOwner(model.ResourceSyncKind, "foo")
	fleets, err := rsTask.parseFleets(genericResources, owner)
	require.NoError(err)
	require.Len(fleets, 1)
	require.Equal(fleets[0].Kind, model.FleetKind)
	require.Equal(*fleets[0].Metadata.Name, "default")
	require.Equal(fleets[0].Spec.Selector.MatchLabels["fleet"], "default")
	require.NotNil(fleets[0].Metadata.Owner)
	require.Equal(*fleets[0].Metadata.Owner, *owner)
}

func TestParseFleet_invalid(t *testing.T) {
	require := require.New(t)

	memfs := memfs.New()
	writeCopy(memfs, "../../examples/fleet.yaml", "/fleet.yaml")

	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	genericResources, err := rsTask.extractResourcesFromFile(memfs, "/fleet.yaml")
	require.NoError(err)
	require.Len(genericResources, 1)
	genericResources[0]["kind"] = "NotValid"

	owner := util.SetResourceOwner(model.ResourceSyncKind, "foo")
	_, err = rsTask.parseFleets(genericResources, owner)
	require.Error(err)
}

func TestParseFleet_multiple(t *testing.T) {
	require := require.New(t)

	memfs := memfs.New()
	require.NoError(memfs.MkdirAll("/fleets", 0666))
	writeCopy(memfs, "../../examples/fleet.yaml", "/fleets/fleet.yaml")
	writeCopy(memfs, "../../examples/fleet-b.yaml", "/fleets/fleet-b.yaml")

	rsTask := NewResourceSync(TaskManager{log: flightlog.InitLogs()})

	genericResources, err := rsTask.extractResourcesFromDir(memfs, "/fleets")
	require.NoError(err)
	require.Len(genericResources, 2)

	owner := util.SetResourceOwner(model.ResourceSyncKind, "foo")
	fleets, err := rsTask.parseFleets(genericResources, owner)
	require.NoError(err)
	require.Len(fleets, 2)

}

func testResourceSync() model.ResourceSync {
	return model.ResourceSync{
		Resource: model.Resource{
			Generation: util.Int64ToPtr(1),
			Name:       *util.StrToPtr("rs"),
		},
		Spec: &model.JSONField[api.ResourceSyncSpec]{
			Data: api.ResourceSyncSpec{
				Repository: util.StrToPtr("demoRepo"),
				Path:       util.StrToPtr("/examples"),
			},
		},
		Status: &model.JSONField[api.ResourceSyncStatus]{
			Data: api.ResourceSyncStatus{
				Conditions: &[]api.Condition{},
			},
		},
	}
}

func testRepo() model.Repository {
	return model.Repository{
		Spec: &model.JSONField[api.RepositorySpec]{
			Data: api.RepositorySpec{
				// This is contacting a GIT repo, we should either mock it, or move it to E2E eventually
				// where we setup a local test git repo we could control (i.e. https://github.com/rockstorm101/git-server-docker)
				Repo: util.StrToPtr("https://github.com/flightctl/flightctl"),
			},
		},
	}
}

var gitRepoCommit = "abcdef012"

func testCloneEmptyGitRepo(_ *model.Repository, _ *string, _ *int) (billy.Filesystem, string, error) {
	memfs := memfs.New()

	return memfs, gitRepoCommit, nil
}

func testCloneUnsupportedGitRepo(_ *model.Repository, _ *string, _ *int) (billy.Filesystem, string, error) {
	memfs := memfs.New()
	_ = memfs.MkdirAll("/examples", 0666)

	writeCopy(memfs, "../../examples/fleet.yaml", "/examples/fleet.yaml")
	writeCopy(memfs, "../../examples/enrollmentrequest.yaml", "/examples/enrollmentrequest.yaml")

	return memfs, gitRepoCommit, nil
}

func writeCopy(fs billy.Filesystem, localPath, path string) {
	f, err := fs.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	data, err := os.ReadFile(localPath)
	if err != nil {
		panic(err)
	}

	_, err = f.Write(data)
	if err != nil {
		panic(err)
	}
}
