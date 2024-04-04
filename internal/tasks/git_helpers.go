package tasks

import (
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
