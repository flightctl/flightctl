package git

import (
	"os"
	"path/filepath"
	"time"

	"github.com/argoproj/argo-cd/v2/reposerver/askpass"
	reposervercache "github.com/argoproj/argo-cd/v2/reposerver/cache"
	"github.com/argoproj/argo-cd/v2/util/cache"
	"github.com/flightctl/flightctl/internal/configprovider/git/repository"
)

type GitConfigProvider struct {
	repoService *repository.Service
}

func NewGitConfigProvider(cache *cache.Cache, repoCacheExpiration time.Duration, revisionCacheExpiration time.Duration) *GitConfigProvider {
	gitCredsStore := askpass.NewServer()
	repoCache := reposervercache.NewCache(cache, repoCacheExpiration, revisionCacheExpiration)
	service := repository.NewService(repoCache, gitCredsStore, filepath.Join(os.TempDir(), "_flightctl-repo"))
	return &GitConfigProvider{
		repoService: service,
	}
}
