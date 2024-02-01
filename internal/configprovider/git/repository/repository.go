// adapted from https://github.com/argoproj/argo-cd/blob/master/reposerver/repository/

package repository

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	// "github.com/argoproj/argo-cd/reposerver/apiclient"
	"github.com/argoproj/argo-cd/v2/reposerver/cache"
	"github.com/argoproj/argo-cd/v2/util/git"
	argoio "github.com/argoproj/argo-cd/v2/util/io"
	gogit "github.com/go-git/go-git/v5"
	"github.com/labstack/gommon/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

type Service struct {
	gitCredsStore git.CredsStore
	rootDir       string
	gitRepoPaths  argoio.TempPaths
	// chartPaths                argoio.TempPaths
	gitRepoInitializer func(rootPath string) io.Closer
	repoLock           *repositoryLock
	cache              *cache.Cache
	// parallelismLimitSemaphore *semaphore.Weighted
	// metricsServer             *metrics.MetricsServer
	// resourceTracking          argo.ResourceTracking
	newGitClient func(rawRepoURL string, root string, creds git.Creds, insecure bool, enableLfs bool, proxy string, opts ...git.ClientOpts) (git.Client, error)
	// now                       func() time.Time
}

// NewService returns a new instance of the Manifest service
func NewService(cache *cache.Cache, gitCredsStore git.CredsStore, rootDir string) *Service {
	repoLock := NewRepositoryLock()
	gitRandomizedPaths := argoio.NewRandomizedTempPaths(rootDir)
	return &Service{
		// parallelismLimitSemaphore: parallelismLimitSemaphore,
		repoLock: repoLock,
		cache:    cache,
		// metricsServer:             metricsServer,
		newGitClient: git.NewClientExt,
		// resourceTracking:          resourceTracking,
		// newHelmClient: func(repoURL string, creds helm.Creds, enableOci bool, proxy string, opts ...helm.ClientOpts) helm.Client {
		// 	return helm.NewClientWithLock(repoURL, creds, sync.NewKeyLock(), enableOci, proxy, opts...)
		// },
		// initConstants:      initConstants,
		// now:                time.Now,
		gitCredsStore:      gitCredsStore,
		gitRepoPaths:       gitRandomizedPaths,
		gitRepoInitializer: directoryPermissionInitializer,
		rootDir:            rootDir,
	}
}

func (s *Service) Init() error {
	_, err := os.Stat(s.rootDir)
	if os.IsNotExist(err) {
		return os.MkdirAll(s.rootDir, 0300)
	}
	if err == nil {
		// give itself read permissions to list previously written directories
		err = os.Chmod(s.rootDir, 0700)
	}
	var dirEntries []fs.DirEntry
	if err == nil {
		dirEntries, err = os.ReadDir(s.rootDir)
	}
	if err != nil {
		klog.Errorf("Failed to restore cloned repositories paths: %v", err)
		return nil
	}

	for _, file := range dirEntries {
		if !file.IsDir() {
			continue
		}
		fullPath := filepath.Join(s.rootDir, file.Name())
		closer := s.gitRepoInitializer(fullPath)
		if repo, err := gogit.PlainOpen(fullPath); err == nil {
			if remotes, err := repo.Remotes(); err == nil && len(remotes) > 0 && len(remotes[0].Config().URLs) > 0 {
				s.gitRepoPaths.Add(git.NormalizeGitURL(remotes[0].Config().URLs[0]), fullPath)
			}
		}
		argoio.Close(closer)
	}
	// remove read permissions since no-one should be able to list the directories
	return os.Chmod(s.rootDir, 0300)
}

// ListRefs List a subset of the refs (currently, branches and tags) of a git repo
func (s *Service) ListRefs(ctx context.Context, repo *v1alpha1.Repository) (*git.Refs, error) {
	gitClient, err := s.newClient(repo)
	if err != nil {
		return nil, fmt.Errorf("error creating git client: %w", err)
	}

	// s.metricsServer.IncPendingRepoRequest(q.Repo.Repo)
	// defer s.metricsServer.DecPendingRepoRequest(q.Repo.Repo)

	return gitClient.LsRefs()
}

func (s *Service) newClient(repo *v1alpha1.Repository, opts ...git.ClientOpts) (git.Client, error) {
	repoPath, err := s.gitRepoPaths.GetPath(git.NormalizeGitURL(repo.Repo))
	if err != nil {
		return nil, err
	}
	// opts = append(opts, git.WithEventHandlers(metrics.NewGitClientEventHandlers(s.metricsServer)))
	return s.newGitClient(repo.Repo, repoPath, repo.GetGitCreds(s.gitCredsStore), repo.IsInsecure(), repo.EnableLFS, repo.Proxy, opts...)
}

// newClientResolveRevision is a helper to perform the common task of instantiating a git client
// and resolving a revision to a commit SHA
func (s *Service) newClientResolveRevision(repo *v1alpha1.Repository, revision string, opts ...git.ClientOpts) (git.Client, string, error) {
	gitClient, err := s.newClient(repo, opts...)
	if err != nil {
		return nil, "", err
	}
	commitSHA, err := gitClient.LsRemote(revision)
	if err != nil {
		return nil, "", err
	}
	return gitClient, commitSHA, nil
}

// directoryPermissionInitializer ensures the directory has read/write/execute permissions and returns
// a function that can be used to remove all permissions.
func directoryPermissionInitializer(rootPath string) io.Closer {
	if _, err := os.Stat(rootPath); err == nil {
		if err := os.Chmod(rootPath, 0700); err != nil {
			klog.Errorf("Failed to restore read/write/execute permissions on %s: %v", rootPath, err)
		} else {
			klog.Infof("Successfully restored read/write/execute permissions on %s", rootPath)
		}
	}

	return argoio.NewCloser(func() error {
		if err := os.Chmod(rootPath, 0000); err != nil {
			klog.Errorf("Failed to remove permissions on %s: %v", rootPath, err)
		} else {
			klog.Infof("Successfully removed permissions on %s", rootPath)
		}
		return nil
	})
}

// checkoutRevision is a convenience function to initialize a repo, fetch, and checkout a revision
// Returns the 40 character commit SHA after the checkout has been performed
// nolint:unparam
func (s *Service) checkoutRevision(gitClient git.Client, revision string, submoduleEnabled bool) (io.Closer, error) {
	closer := s.gitRepoInitializer(gitClient.Root())
	return closer, checkoutRevision(gitClient, revision, submoduleEnabled)
}

func checkoutRevision(gitClient git.Client, revision string, submoduleEnabled bool) error {
	err := gitClient.Init()
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to initialize git repo: %v", err)
	}

	// Fetching with no revision first. Fetching with an explicit version can cause repo bloat. https://github.com/argoproj/argo-cd/issues/8845
	err = gitClient.Fetch("")
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to fetch default: %v", err)
	}

	err = gitClient.Checkout(revision, submoduleEnabled)
	if err != nil {
		// When fetching with no revision, only refs/heads/* and refs/remotes/origin/* are fetched. If checkout fails
		// for the given revision, try explicitly fetching it.
		log.Infof("Failed to checkout revision %s: %v", revision, err)
		log.Infof("Fallback to fetching specific revision %s. ref might not have been in the default refspec fetched.", revision)

		err = gitClient.Fetch(revision)
		if err != nil {
			return status.Errorf(codes.Internal, "Failed to checkout revision %s: %v", revision, err)
		}

		err = gitClient.Checkout("FETCH_HEAD", submoduleEnabled)
		if err != nil {
			return status.Errorf(codes.Internal, "Failed to checkout FETCH_HEAD: %v", err)
		}
	}

	return err
}

func (s *Service) TestRepository(ctx context.Context, repo *v1alpha1.Repository) error {
	// per Type doc, "git" should be assumed if empty or absent
	if repo.Type == "" {
		repo.Type = "git"
	}

	switch repo.Type {
	case "git":
		return git.TestRepo(repo.Repo, repo.GetGitCreds(s.gitCredsStore), repo.IsInsecure(), repo.IsLFSEnabled(), repo.Proxy)
	default:
		return fmt.Errorf("error testing repository connectivity: repo type is not 'git' but %s", repo.Type)
	}
}

func (s *Service) GetGitFiles(_ context.Context, repo *v1alpha1.Repository, revision string, gitPath string, enableNewGitFileGlobbing bool, submoduleEnabled bool) (map[string][]byte, error) {
	if gitPath == "" {
		gitPath = "."
	}

	if repo == nil {
		return nil, status.Error(codes.InvalidArgument, "must pass a valid repo")
	}

	gitClient, revision, err := s.newClientResolveRevision(repo, revision, git.WithCache(s.cache, true))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to resolve git revision %s: %v", revision, err)
	}

	// check the cache and return the results if present
	if cachedFiles, err := s.cache.GetGitFiles(repo.Repo, revision, gitPath); err == nil {
		log.Debugf("cache hit for repo: %s revision: %s pattern: %s", repo.Repo, revision, gitPath)
		return cachedFiles, nil
	}

	// s.metricsServer.IncPendingRepoRequest(repo.Repo)
	// defer s.metricsServer.DecPendingRepoRequest(repo.Repo)

	// cache miss, generate the results
	closer, err := s.repoLock.Lock(gitClient.Root(), revision, true, func() (io.Closer, error) {
		return s.checkoutRevision(gitClient, revision, submoduleEnabled)
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to checkout git repo %s with revision %s pattern %s: %v", repo.Repo, revision, gitPath, err)
	}
	defer argoio.Close(closer)

	gitFiles, err := gitClient.LsFiles(gitPath, enableNewGitFileGlobbing)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to list files. repo %s with revision %s pattern %s: %v", repo.Repo, revision, gitPath, err)
	}
	log.Debugf("listed %d git files from %s under %s", len(gitFiles), repo.Repo, gitPath)

	res := make(map[string][]byte)
	for _, filePath := range gitFiles {
		fileContents, err := os.ReadFile(filepath.Join(gitClient.Root(), filePath))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "unable to read files. repo %s with revision %s pattern %s: %v", repo.Repo, revision, gitPath, err)
		}
		res[filePath] = fileContents
	}

	err = s.cache.SetGitFiles(repo.Repo, revision, gitPath, res)
	if err != nil {
		log.Warnf("error caching git files for repo %s with revision %s pattern %s: %v", repo.Repo, revision, gitPath, err)
	}

	return res, nil
}

func (s *Service) GetGitDirectories(_ context.Context, repo *v1alpha1.Repository, revision string, submoduleEnabled bool) ([]string, error) {
	if repo == nil {
		return nil, status.Error(codes.InvalidArgument, "must pass a valid repo")
	}

	gitClient, revision, err := s.newClientResolveRevision(repo, revision, git.WithCache(s.cache, true))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to resolve git revision %s: %v", revision, err)
	}

	// check the cache and return the results if present
	if cachedPaths, err := s.cache.GetGitDirectories(repo.Repo, revision); err == nil {
		log.Debugf("cache hit for repo: %s revision: %s", repo.Repo, revision)
		return cachedPaths, nil
	}

	// s.metricsServer.IncPendingRepoRequest(repo.Repo)
	// defer s.metricsServer.DecPendingRepoRequest(repo.Repo)

	// cache miss, generate the results
	closer, err := s.repoLock.Lock(gitClient.Root(), revision, true, func() (io.Closer, error) {
		return s.checkoutRevision(gitClient, revision, submoduleEnabled)
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to checkout git repo %s with revision %s: %v", repo.Repo, revision, err)
	}
	defer argoio.Close(closer)

	repoRoot := gitClient.Root()
	var paths []string
	if err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, fnErr error) error {
		if fnErr != nil {
			return fmt.Errorf("error walking the file tree: %w", fnErr)
		}
		if !entry.IsDir() { // Skip files: directories only
			return nil
		}

		fname := entry.Name()
		if strings.HasPrefix(fname, ".") { // Skip all folders starts with "."
			return filepath.SkipDir
		}

		relativePath, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return fmt.Errorf("error constructing relative repo path: %w", err)
		}

		if relativePath == "." { // Exclude '.' from results
			return nil
		}

		paths = append(paths, relativePath)

		return nil
	}); err != nil {
		return nil, err
	}

	log.Debugf("found %d git paths from %s", len(paths), repo.Repo)
	err = s.cache.SetGitDirectories(repo.Repo, revision, paths)
	if err != nil {
		log.Warnf("error caching git directories for repo %s with revision %s: %v", repo.Repo, revision, err)
	}

	return paths, nil
}
