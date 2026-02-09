package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"text/template"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// sshKeyCacheData holds the cached SSH private key path
type sshKeyCacheData struct {
	path string
	once sync.Once
	err  error
}

var sshKeyCache sshKeyCacheData

// GetGitServerConfig returns the configuration for the e2e git server.
// The git server runs as a testcontainer managed by SatelliteServices.
func (h *Harness) GetGitServerConfig() (GitServerConfig, error) {
	satellite := infra.GetInfra(h.Context)

	if satellite.GitServerHost == "" || satellite.GitServerPort == 0 {
		return GitServerConfig{}, fmt.Errorf("git server not started")
	}

	return GitServerConfig{
		Host: satellite.GitServerHost,
		Port: satellite.GitServerPort,
		User: "user",
	}, nil
}

// GitServerConfig holds configuration for the git server
type GitServerConfig struct {
	Host string
	Port int
	User string
}

// runGitServerSSHCommand executes a command on the git server via SSH using key authentication
func (h *Harness) runGitServerSSHCommand(config GitServerConfig, command string) error {
	keyPath, err := GetSSHPrivateKeyPath()
	if err != nil {
		return fmt.Errorf("failed to get SSH key path: %w", err)
	}

	// #nosec G204 -- This is test code with controlled inputs from GitServerConfig
	sshCmd := exec.Command("ssh",
		"-i", keyPath,
		"-p", fmt.Sprintf("%d", config.Port),
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "PasswordAuthentication=no",
		"-o", "BatchMode=yes",
		"-o", "LogLevel=ERROR",
		fmt.Sprintf("%s@%s", config.User, config.Host),
		command)

	output, err := sshCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("SSH command failed: %w, output: %s", err, string(output))
	}

	logrus.Debugf("SSH command executed successfully: %s", command)
	return nil
}

// runGitCommands executes a sequence of git commands in the specified working directory
// with proper authentication and author configuration.
func (h *Harness) runGitCommands(workDir string, gitCmds [][]string) error {
	keyPath, err := GetSSHPrivateKeyPath()
	if err != nil {
		return fmt.Errorf("failed to get SSH key path: %w", err)
	}

	gitEnv := append(os.Environ(),
		fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o PasswordAuthentication=no -o BatchMode=yes", keyPath),
		"GIT_AUTHOR_NAME=Test Harness",
		"GIT_AUTHOR_EMAIL=test@flightctl.dev",
		"GIT_COMMITTER_NAME=Test Harness",
		"GIT_COMMITTER_EMAIL=test@flightctl.dev",
	)

	for _, gitCmd := range gitCmds {
		// #nosec G204 -- This is test code with controlled git commands
		cmd := exec.Command(gitCmd[0], gitCmd[1:]...)
		cmd.Dir = workDir
		cmd.Env = gitEnv

		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to execute git command %v: %w, output: %s", gitCmd, err, string(output))
		}
	}
	return nil
}

// CreateGitRepositoryOnServer creates a new Git repository on the e2e git server
func (h *Harness) CreateGitRepositoryOnServer(repoName string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	config, err := h.GetGitServerConfig()
	if err != nil {
		return fmt.Errorf("failed to get git server config: %w", err)
	}

	// Use SSH to create the repository on the git server
	createCmd := fmt.Sprintf("create-repo %s", repoName)
	err = h.runGitServerSSHCommand(config, createCmd)
	if err != nil {
		return fmt.Errorf("failed to create git repository %s: %w", repoName, err)
	}

	// Store the repository name for cleanup
	h.gitRepos[repoName] = fmt.Sprintf("ssh://%s@%s:%d/home/user/repos/%s.git",
		config.User, config.Host, config.Port, repoName)

	logrus.Infof("Created git repository: %s on git server", repoName)
	return nil
}

// DeleteGitRepositoryOnServer deletes a Git repository from the e2e git server
func (h *Harness) DeleteGitRepositoryOnServer(repoName string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	config, err := h.GetGitServerConfig()
	if err != nil {
		return fmt.Errorf("failed to get git server config: %w", err)
	}

	// Use SSH to delete the repository on the git server
	deleteCmd := fmt.Sprintf("delete-repo %s", repoName)
	err = h.runGitServerSSHCommand(config, deleteCmd)
	if err != nil {
		return fmt.Errorf("failed to delete git repository %s: %w", repoName, err)
	}

	// Remove from our tracking
	delete(h.gitRepos, repoName)

	logrus.Infof("Deleted git repository: %s from git server", repoName)
	return nil
}

// CloneGitRepositoryFromServer clones a repository from the git server to a local working directory
func (h *Harness) CloneGitRepositoryFromServer(repoName, localPath string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if localPath == "" {
		return fmt.Errorf("local path cannot be empty")
	}

	config, err := h.GetGitServerConfig()
	if err != nil {
		return fmt.Errorf("failed to get git server config: %w", err)
	}

	keyPath, err := GetSSHPrivateKeyPath()
	if err != nil {
		return fmt.Errorf("failed to get SSH key path: %w", err)
	}

	repoURL := fmt.Sprintf("ssh://%s@%s:%d/home/user/repos/%s.git",
		config.User, config.Host, config.Port, repoName)

	// Create parent directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	// #nosec G204 -- This is test code with controlled inputs from GitServerConfig
	cloneCmd := exec.Command("git", "clone", repoURL, localPath)
	cloneCmd.Env = append(os.Environ(),
		fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o PasswordAuthentication=no -o BatchMode=yes", keyPath))

	if output, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to clone repository %s to %s: %w, output: %s", repoURL, localPath, err, string(output))
	}

	logrus.Infof("Cloned git repository %s to %s", repoName, localPath)
	return nil
}

// pushToGitServerRepo is a helper that clones a repo, calls prepareContent to set up files,
// then commits and pushes changes. The addPath is the path to pass to git add.
func (h *Harness) pushToGitServerRepo(repoName, addPath, commitMessage string, prepareContent func(workDir string) error) error {
	// Create a temporary working directory
	workDir := filepath.Join(h.gitWorkDir, "temp-"+uuid.New().String())
	defer os.RemoveAll(workDir)

	// Clone the repository
	if err := h.CloneGitRepositoryFromServer(repoName, workDir); err != nil {
		return fmt.Errorf("failed to clone repository for push: %w", err)
	}

	// Prepare content in the working directory
	if err := prepareContent(workDir); err != nil {
		return err
	}

	return h.runGitCommands(workDir, [][]string{
		{"git", "add", addPath},
		{"git", "commit", "-m", commitMessage},
		{"git", "branch", "-M", "main"},
		{"git", "push", "origin", "main"},
	})
}

// PushContentToGitServerRepo pushes content to a git repository on the server
func (h *Harness) PushContentToGitServerRepo(repoName, filePath, content, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Add content via test harness"
	}

	err := h.pushToGitServerRepo(repoName, filePath, commitMessage, func(workDir string) error {
		fullFilePath := filepath.Join(workDir, filePath)
		if err := os.MkdirAll(filepath.Dir(fullFilePath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for file: %w", err)
		}
		if err := os.WriteFile(fullFilePath, []byte(content), 0600); err != nil {
			return fmt.Errorf("failed to write content to file: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	logrus.Infof("Pushed content to git repository %s, file: %s", repoName, filePath)
	return nil
}

// PushContentToGitServerRepoFromPath reads content from a local file or directory and pushes it to a git repository on the server.
// If sourcePath is a directory, all files within it are copied to the repository root.
// If sourcePath is a file, it is copied to the repository with its base name.
func (h *Harness) PushContentToGitServerRepoFromPath(repoName, sourcePath, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if sourcePath == "" {
		return fmt.Errorf("source path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Add content via test harness"
	}

	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to stat source path: %w", err)
	}

	err = h.pushToGitServerRepo(repoName, ".", commitMessage, func(workDir string) error {
		if sourceInfo.IsDir() {
			// Copy all files from source directory to workDir
			return filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				relPath, err := filepath.Rel(sourcePath, path)
				if err != nil {
					return err
				}
				destPath := filepath.Join(workDir, relPath)

				if info.IsDir() {
					return os.MkdirAll(destPath, 0755)
				}

				content, err := os.ReadFile(path)
				if err != nil {
					return fmt.Errorf("failed to read file %s: %w", path, err)
				}
				if err := os.WriteFile(destPath, content, 0600); err != nil {
					return fmt.Errorf("failed to write file %s: %w", destPath, err)
				}
				return nil
			})
		}
		// Single file - copy to workDir with its base name
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			return fmt.Errorf("failed to read source file: %w", err)
		}
		destPath := filepath.Join(workDir, filepath.Base(sourcePath))
		if err := os.WriteFile(destPath, content, 0600); err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	logrus.Infof("Pushed content from path %s to git repository %s", sourcePath, repoName)
	return nil
}

// CreateRepository creates a Repository resource pointing to the git server repository
func (h *Harness) CreateGitRepository(repoName string, repositorySpec domain.RepositorySpec) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	// First create the git repository on the server
	if err := h.CreateGitRepositoryOnServer(repoName); err != nil {
		return fmt.Errorf("failed to create git repository on server: %w", err)
	}

	// Create the Repository resource
	repository := domain.Repository{
		ApiVersion: domain.RepositoryAPIVersion,
		Kind:       domain.RepositoryKind,
		Metadata: domain.ObjectMeta{
			Name: &repoName,
		},
		Spec: repositorySpec,
	}

	_, err := h.Client.CreateRepositoryWithResponse(h.Context, repository)
	if err != nil {
		// Clean up the git repository if Repository resource creation fails
		if cleanupErr := h.DeleteGitRepositoryOnServer(repoName); cleanupErr != nil {
			logrus.Errorf("failed to delete git repository %s: %v", repoName, cleanupErr)
		}
		return fmt.Errorf("failed to create Repository resource: %w", err)
	}

	logrus.Infof("Created Repository resource %s", repoName)
	return nil
}

// UpdateGitServerRepository updates content in an existing git repository working directory
func (h *Harness) UpdateGitServerRepository(repoName, filePath, content, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Update content via test harness"
	}

	return h.PushContentToGitServerRepo(repoName, filePath, content, commitMessage)
}

// CommitAndPushGitRepo commits all changes in a local git working directory and pushes to the remote.
// This uses `git add -A` to stage all changes (additions, modifications, deletions),
// then commits and pushes. The workDir must be a cloned git repository.
// This method mirrors real git workflows where you manipulate files locally then push.
func (h *Harness) CommitAndPushGitRepo(workDir, commitMessage string) error {
	if workDir == "" {
		return fmt.Errorf("working directory cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Update via test harness"
	}

	// Verify the directory exists and is a git repository
	gitDir := filepath.Join(workDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return fmt.Errorf("working directory %s is not a git repository", workDir)
	}

	// Use git add -A to stage all changes including deletions
	// Use git branch -M main to ensure we're on main branch (handles empty repos)
	// Use git push -u origin main to set upstream and push
	err := h.runGitCommands(workDir, [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", commitMessage},
		{"git", "branch", "-M", "main"},
		{"git", "push", "-u", "origin", "main"},
	})
	if err != nil {
		return err
	}

	logrus.Infof("Committed and pushed changes from %s", workDir)
	return nil
}

// getTestDataPath returns the path to a file/directory in the testdata folder.
// Note: ginkgo runs tests from the test package directory (e.g. resourcesync test runs from test/e2e/resourcesync/).
func GetTestDataPath(relativePath string) string {
	return filepath.Join("testdata", relativePath)
}

// GetSSHPrivateKeyPath returns the path to the SSH private key file.
// Delegates to the infra package which manages the git server testcontainer.
func GetSSHPrivateKeyPath() (string, error) {
	sshKeyCache.once.Do(func() {
		sshKeyCache.path, sshKeyCache.err = infra.GetSSHPrivateKeyPath()
	})
	return sshKeyCache.path, sshKeyCache.err
}

// GetSSHPrivateKey returns the SSH private key content.
func GetSSHPrivateKey() (string, error) {
	keyPath, err := GetSSHPrivateKeyPath()
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read SSH private key from %s: %w", keyPath, err)
	}
	return string(content), nil
}

// writeTemplatedFilesToDir is a helper that
// 1. reads template files from sourceDir
// 2. applies Go templating with the provided data
// 3. writes the results to destDir.
func writeTemplatedFilesToDir(sourceDir, destDir string, data interface{}) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", sourceDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(sourceDir, entry.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", filePath, err)
		}

		tmpl, err := template.New(entry.Name()).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", entry.Name(), err)
		}

		destPath := filepath.Join(destDir, entry.Name())
		destFile, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("failed to create file %s: %w", destPath, err)
		}

		err = tmpl.Execute(destFile, data)
		destFile.Close()
		if err != nil {
			return fmt.Errorf("failed to execute template %s: %w", entry.Name(), err)
		}
	}

	return nil
}

// SetupTemplatedGitRepoFromDir creates a git repo, clones it, populates with templated files, and pushes
func (h *Harness) SetupTemplatedGitRepoFromDir(repoName, sourceDir string, data interface{}) (string, error) {
	err := h.CreateGitRepositoryOnServer(repoName)
	if err != nil {
		return "", fmt.Errorf("failed to create git repository: %w", err)
	}

	workDir, err := os.MkdirTemp("", "resourcesync-test-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	err = h.CloneGitRepositoryFromServer(repoName, workDir)
	if err != nil {
		return "", fmt.Errorf("failed to clone git repository: %w", err)
	}

	err = writeTemplatedFilesToDir(sourceDir, workDir, data)
	if err != nil {
		return "", fmt.Errorf("failed to write templated files: %w", err)
	}

	err = h.CommitAndPushGitRepo(workDir, "Add initial fleet files")
	if err != nil {
		return "", fmt.Errorf("failed to commit and push: %w", err)
	}

	return workDir, nil
}

// PushTemplatedFilesToGitRepo updates an existing git repo with templated files, commits and pushes
func (h *Harness) PushTemplatedFilesToGitRepo(repoName, sourceDir, workDir string, data interface{}) error {
	err := writeTemplatedFilesToDir(sourceDir, workDir, data)
	if err != nil {
		return fmt.Errorf("failed to write templated files: %w", err)
	}

	err = h.CommitAndPushGitRepo(workDir, "")
	if err != nil {
		return fmt.Errorf("failed to commit and push: %w", err)
	}

	return nil
}
