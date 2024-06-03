package tasks

import (
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	gitplumbing "github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitmemory "github.com/go-git/go-git/v5/storage/memory"
)

// a function to clone a git repo, for mockable unit testing
type cloneGitRepoFunc func(repo *model.Repository, revision *string, depth *int) (billy.Filesystem, string, error)

func CloneGitRepo(repo *model.Repository, revision *string, depth *int) (billy.Filesystem, string, error) {
	storage := gitmemory.NewStorage()
	mfs := memfs.New()
	opts := &git.CloneOptions{
		URL: *repo.Spec.Data.Repo,
	}
	if depth != nil {
		opts.Depth = *depth
	}
	if repo.Spec.Data.Username != nil && repo.Spec.Data.Password != nil {
		opts.Auth = &githttp.BasicAuth{
			Username: *repo.Spec.Data.Username,
			Password: *repo.Spec.Data.Password,
		}
	}
	if revision != nil {
		opts.ReferenceName = gitplumbing.ReferenceName(*revision)
	}
	gitRepo, err := git.Clone(storage, mfs, opts)
	if err != nil {
		return nil, "", err
	}
	head, err := gitRepo.Head()
	if err != nil {
		return nil, "", err
	}
	hash := head.Hash().String()
	return mfs, hash, nil
}

func ConvertFileSystemToIgnition(mfs billy.Filesystem, path string) (*config_latest_types.Config, error) {
	fileInfo, err := mfs.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed accessing path %s: %w", path, err)
	}
	ignitionConfig, _, _ := config_latest.ParseCompatibleVersion([]byte("{\"ignition\": {\"version\": \"3.4.0\"}"))

	if fileInfo.IsDir() {
		files, err := mfs.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("failed reading directory %s: %w", path, err)
		}
		err = addGitDirToIgnitionConfig(mfs, path, "/", files, &ignitionConfig)
		if err != nil {
			return nil, fmt.Errorf("failed converting directory %s to ignition: %w", path, err)
		}
	} else {
		err = addGitFileToIgnitionConfig(mfs, path, "/", fileInfo, &ignitionConfig)
		if err != nil {
			return nil, fmt.Errorf("failed converting file %s to ignition: %w", path, err)
		}
	}

	return &ignitionConfig, nil
}

func addGitDirToIgnitionConfig(mfs billy.Filesystem, fullPrefix, ignPrefix string, fileInfos []fs.FileInfo, ignitionConfig *config_latest_types.Config) error {
	for _, fileInfo := range fileInfos {
		if fileInfo.IsDir() {
			subdirFiles, err := mfs.ReadDir(filepath.Join(fullPrefix, fileInfo.Name()))
			if err != nil {
				return fmt.Errorf("failed reading directory %s: %w", fileInfo.Name(), err)
			}
			// recursion!
			err = addGitDirToIgnitionConfig(mfs, filepath.Join(fullPrefix, fileInfo.Name()), filepath.Join(ignPrefix, fileInfo.Name()), subdirFiles, ignitionConfig)
			if err != nil {
				return err
			}
		} else {
			err := addGitFileToIgnitionConfig(mfs, filepath.Join(fullPrefix, fileInfo.Name()), filepath.Join(ignPrefix, fileInfo.Name()), fileInfo, ignitionConfig)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func addGitFileToIgnitionConfig(mfs billy.Filesystem, fullPath, ignPath string, fileInfo fs.FileInfo, ignitionConfig *config_latest_types.Config) error {
	openFile, err := mfs.Open(fullPath)
	if err != nil {
		return err
	}
	defer openFile.Close()

	fileContents, err := io.ReadAll(openFile)
	if err != nil {
		return err
	}

	setFileInIgnition(ignitionConfig, ignPath, fileContents, int(fileInfo.Mode()), true)
	return nil
}

func setFileInIgnition(ignitionConfig *config_latest_types.Config, filePath string, fileBytes []byte, mode int, overwrite bool) {
	fileContents := "data:text/plain;charset=utf-8;base64," + base64.StdEncoding.EncodeToString(fileBytes)
	rootUser := "root"
	file := config_latest_types.File{
		Node: config_latest_types.Node{
			Path:      filePath,
			Overwrite: &overwrite,
			Group:     config_latest_types.NodeGroup{},
			User:      config_latest_types.NodeUser{Name: &rootUser},
		},
		FileEmbedded1: config_latest_types.FileEmbedded1{
			Append: []config_latest_types.Resource{},
			Contents: config_latest_types.Resource{
				Source: &fileContents,
			},
			Mode: &mode,
		},
	}
	ignitionConfig.Storage.Files = append(ignitionConfig.Storage.Files, file)
}
