package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// GitServerConfig holds configuration for the git server. Callers get it from infra (e.g. auxiliary).
type GitServerConfig struct {
	Host string
	Port int
	User string
}

// runGitServerSSHCommand executes a command on the git server via SSH using key authentication.
// keyPath must be the path to the git SSH private key (caller gets it from infra, e.g. auxiliary.Get(ctx).GetGitSSHPrivateKeyPath()).
func (h *Harness) runGitServerSSHCommand(config GitServerConfig, keyPath util.SSHPrivateKeyPath, command string) error {
	if keyPath == "" {
		return fmt.Errorf("SSH private key path is required for git server SSH commands")
	}

	// #nosec G204 -- This is test code with controlled inputs from GitServerConfig
	sshCmd := exec.Command("ssh",
		"-i", string(keyPath),
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

// runGitCommands executes a sequence of git commands in the specified working directory.
// keyPath must be the path to the git SSH private key (caller gets it from infra).
func (h *Harness) runGitCommands(workDir string, keyPath util.SSHPrivateKeyPath, gitCmds [][]string) error {
	if keyPath == "" {
		return fmt.Errorf("SSH private key path is required for git commands")
	}

	gitEnv := append(os.Environ(),
		fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o PasswordAuthentication=no -o BatchMode=yes", string(keyPath)),
		"GIT_AUTHOR_NAME=Test Harness",
		"GIT_AUTHOR_EMAIL=test@flightctl.dev",
		"GIT_COMMITTER_NAME=Test Harness",
		"GIT_COMMITTER_EMAIL=test@flightctl.dev",
	)

	for _, gitCmd := range gitCmds {
		args := gitCmd
		if len(args) >= 2 && args[0] == "git" && args[1] == "commit" {
			args = append([]string{"git", "-c", "commit.gpgsign=false"}, args[1:]...)
		}
		// #nosec G204 -- This is test code with controlled git commands
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = workDir
		cmd.Env = gitEnv

		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to execute git command %v: %w, output: %s", gitCmd, err, string(output))
		}
	}
	return nil
}

// CreateGitRepositoryOnServer creates a new Git repository on the e2e git server.
// Callers pass config and keyPath from infra (e.g. auxiliary).
func (h *Harness) CreateGitRepositoryOnServer(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	createCmd := fmt.Sprintf("create-repo '%s'", repoName)
	err := h.runGitServerSSHCommand(config, keyPath, createCmd)
	if err != nil {
		return fmt.Errorf("failed to create git repository %s: %w", repoName, err)
	}

	// Store the repository name for cleanup
	h.gitRepos[repoName] = fmt.Sprintf("ssh://%s@%s/home/user/repos/%s.git",
		config.User, net.JoinHostPort(config.Host, strconv.Itoa(config.Port)), repoName)

	logrus.Infof("Created git repository: %s on git server", repoName)
	return nil
}

// DeleteGitRepositoryOnServer deletes a Git repository from the e2e git server.
func (h *Harness) DeleteGitRepositoryOnServer(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	deleteCmd := fmt.Sprintf("delete-repo '%s'", repoName)
	err := h.runGitServerSSHCommand(config, keyPath, deleteCmd)
	if err != nil {
		return fmt.Errorf("failed to delete git repository %s: %w", repoName, err)
	}

	// Remove from our tracking
	delete(h.gitRepos, repoName)

	logrus.Infof("Deleted git repository: %s from git server", repoName)
	return nil
}

// CloneGitRepositoryFromServer clones a repository from the git server to a local working directory.
func (h *Harness) CloneGitRepositoryFromServer(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName, localPath string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if localPath == "" {
		return fmt.Errorf("local path cannot be empty")
	}

	if keyPath == "" {
		return fmt.Errorf("SSH private key path is required to clone from git server")
	}

	repoURL := fmt.Sprintf("ssh://%s@%s/home/user/repos/%s.git",
		config.User, net.JoinHostPort(config.Host, strconv.Itoa(config.Port)), repoName)

	// Create parent directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	// #nosec G204 -- This is test code with controlled inputs from GitServerConfig
	cloneCmd := exec.Command("git", "clone", repoURL, localPath)
	cloneCmd.Env = append(os.Environ(),
		fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o PasswordAuthentication=no -o BatchMode=yes", string(keyPath)))

	if output, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to clone repository %s to %s: %w, output: %s", repoURL, localPath, err, string(output))
	}

	logrus.Infof("Cloned git repository %s to %s", repoName, localPath)
	return nil
}

// pushToGitServerRepo is a helper that clones a repo, calls prepareContent to set up files,
// then commits and pushes changes.
// sanitizeFilePath cleans filePath, rejects absolute paths, and verifies the
// result stays inside workDir. Returns the full path and cleaned relative path.
func sanitizeFilePath(workDir, filePath string) (fullPath, relPath string, err error) {
	cleaned := filepath.Clean(filePath)
	if filepath.IsAbs(cleaned) {
		return "", "", fmt.Errorf("file path %q must be relative", filePath)
	}
	full := filepath.Join(workDir, cleaned)
	rel, err := filepath.Rel(workDir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", fmt.Errorf("file path %q escapes work directory", filePath)
	}
	return full, rel, nil
}

func (h *Harness) pushToGitServerRepo(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName, addPath, commitMessage string, prepareContent func(workDir string) error) error {
	workDir := filepath.Join(h.gitWorkDir, "temp-"+uuid.New().String())
	defer os.RemoveAll(workDir)

	if err := h.CloneGitRepositoryFromServer(config, keyPath, repoName, workDir); err != nil {
		return fmt.Errorf("failed to clone repository for push: %w", err)
	}

	if err := prepareContent(workDir); err != nil {
		return err
	}

	return h.runGitCommands(workDir, keyPath, [][]string{
		{"git", "add", addPath},
		{"git", "commit", "-m", commitMessage},
		{"git", "branch", "-M", "main"},
		{"git", "push", "origin", "main"},
	})
}

// PushContentToGitServerRepo pushes content to a git repository on the server.
func (h *Harness) PushContentToGitServerRepo(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName, filePath, content, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Add content via test harness"
	}

	err := h.pushToGitServerRepo(config, keyPath, repoName, filePath, commitMessage, func(workDir string) error {
		fullFilePath, _, err := sanitizeFilePath(workDir, filePath)
		if err != nil {
			return err
		}
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

// CreateGitBranchOnServer creates a new branch from main in a git repository on the server.
func (h *Harness) CreateGitBranchOnServer(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName, branchName string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if branchName == "" {
		return fmt.Errorf("branch name cannot be empty")
	}

	workDir := filepath.Join(h.gitWorkDir, "temp-"+uuid.New().String())
	defer os.RemoveAll(workDir)

	if err := h.CloneGitRepositoryFromServer(config, keyPath, repoName, workDir); err != nil {
		return fmt.Errorf("failed to clone repository: %w", err)
	}

	err := h.runGitCommands(workDir, keyPath, [][]string{
		{"git", "fetch", "origin", "main"},
		{"git", "checkout", "-B", branchName, "origin/main"},
		{"git", "push", "-u", "origin", branchName},
	})
	if err != nil {
		return fmt.Errorf("failed to create branch %s: %w", branchName, err)
	}

	logrus.Infof("Created branch %s in git repository %s", branchName, repoName)
	return nil
}

// PushContentToGitServerRepoBranch pushes content to a specific branch of a git repository on the server.
func (h *Harness) PushContentToGitServerRepoBranch(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName, branch, filePath, content, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if branch == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Add content via test harness"
	}

	workDir := filepath.Join(h.gitWorkDir, "temp-"+uuid.New().String())
	defer os.RemoveAll(workDir)

	if err := h.CloneGitRepositoryFromServer(config, keyPath, repoName, workDir); err != nil {
		return fmt.Errorf("failed to clone repository for push: %w", err)
	}

	if err := h.runGitCommands(workDir, keyPath, [][]string{
		{"git", "checkout", branch},
	}); err != nil {
		return fmt.Errorf("failed to checkout branch %s: %w", branch, err)
	}

	fullFilePath, cleanedPath, err := sanitizeFilePath(workDir, filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullFilePath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for file: %w", err)
	}
	if err := os.WriteFile(fullFilePath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write content to file: %w", err)
	}

	if err := h.runGitCommands(workDir, keyPath, [][]string{
		{"git", "add", cleanedPath},
		{"git", "commit", "-m", commitMessage},
		{"git", "push", "origin", branch},
	}); err != nil {
		return err
	}

	logrus.Infof("Pushed content to git repository %s branch %s, file: %s", repoName, branch, filePath)
	return nil
}

// PushContentToGitServerRepoFromPath reads content from a local file or directory and pushes it to a git repository on the server.
func (h *Harness) PushContentToGitServerRepoFromPath(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName, sourcePath, commitMessage string) error {
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

	err = h.pushToGitServerRepo(config, keyPath, repoName, ".", commitMessage, func(workDir string) error {
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

// CreateGitRepository creates a Repository resource pointing to the git server repository.
func (h *Harness) CreateGitRepository(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName string, repositorySpec domain.RepositorySpec) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	if err := h.CreateGitRepositoryOnServer(config, keyPath, repoName); err != nil {
		return fmt.Errorf("failed to create git repository on server: %w", err)
	}

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
		if cleanupErr := h.DeleteGitRepositoryOnServer(config, keyPath, repoName); cleanupErr != nil {
			logrus.Errorf("failed to delete git repository %s: %v", repoName, cleanupErr)
		}
		return fmt.Errorf("failed to create Repository resource: %w", err)
	}

	logrus.Infof("Created Repository resource %s", repoName)
	return nil
}

// UpdateGitServerRepository updates content in an existing git repository working directory.
func (h *Harness) UpdateGitServerRepository(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName, filePath, content, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Update content via test harness"
	}

	return h.PushContentToGitServerRepo(config, keyPath, repoName, filePath, content, commitMessage)
}

// CommitAndPushGitRepo commits all changes in a local git working directory and pushes to the remote.
// keyPath must be the path to the git SSH private key (caller gets it from infra).
func (h *Harness) CommitAndPushGitRepo(workDir string, keyPath util.SSHPrivateKeyPath, commitMessage string) error {
	if workDir == "" {
		return fmt.Errorf("working directory cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Update via test harness"
	}

	gitDir := filepath.Join(workDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return fmt.Errorf("working directory %s is not a git repository", workDir)
	}

	err := h.runGitCommands(workDir, keyPath, [][]string{
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

// Git SSH keys: callers get the key path from infra (e.g. auxiliary.Get(ctx).GetGitSSHPrivateKeyPath()) and pass it into harness methods that need it.

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

// SetupTemplatedGitRepoFromDir creates a git repo, clones it, populates with templated files, and pushes.
func (h *Harness) SetupTemplatedGitRepoFromDir(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName, sourceDir string, data interface{}) (string, error) {
	err := h.CreateGitRepositoryOnServer(config, keyPath, repoName)
	if err != nil {
		return "", fmt.Errorf("failed to create git repository: %w", err)
	}

	workDir, err := os.MkdirTemp("", "resourcesync-test-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	err = h.CloneGitRepositoryFromServer(config, keyPath, repoName, workDir)
	if err != nil {
		return "", fmt.Errorf("failed to clone git repository: %w", err)
	}

	err = writeTemplatedFilesToDir(sourceDir, workDir, data)
	if err != nil {
		return "", fmt.Errorf("failed to write templated files: %w", err)
	}

	err = h.CommitAndPushGitRepo(workDir, keyPath, "Add initial fleet files")
	if err != nil {
		return "", fmt.Errorf("failed to commit and push: %w", err)
	}

	return workDir, nil
}

// VerifyDeviceGitConfigPath fetches the device and asserts that the named git config's resolved path matches expectedPath.
func (h *Harness) VerifyDeviceGitConfigPath(deviceId, configName, expectedPath string) error {
	device, err := h.GetDevice(deviceId)
	if err != nil {
		return fmt.Errorf("failed to get device %s: %w", deviceId, err)
	}
	gitConfig, err := h.GetDeviceGitConfig(device, configName)
	if err != nil {
		return fmt.Errorf("failed to get git config %s for device %s: %w", configName, deviceId, err)
	}
	if gitConfig.GitRef.Path != expectedPath {
		return fmt.Errorf("expected git config path %q, got %q", expectedPath, gitConfig.GitRef.Path)
	}
	return nil
}

// ReadFileFromDevice reads a file from the device via SSH and returns its content.
func (h *Harness) ReadFileFromDevice(filePath string) (string, error) {
	stdout, err := h.VM.RunSSH([]string{"cat", filePath}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to read %s from device: %w", filePath, err)
	}
	return stdout.String(), nil
}

// GetRemoteHeadSHA returns the HEAD commit SHA of a remote git repository on the E2E git server.
func (h *Harness) GetRemoteHeadSHA(config GitServerConfig, keyPath util.SSHPrivateKeyPath, repoName, branch string) (string, error) {
	if keyPath == "" {
		return "", fmt.Errorf("ssh key path cannot be empty")
	}
	if repoName == "" {
		return "", fmt.Errorf("repository name cannot be empty")
	}
	if branch == "" {
		branch = "main"
	}

	gitEnv := append(os.Environ(),
		fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o PasswordAuthentication=no -o BatchMode=yes -o LogLevel=ERROR", string(keyPath)),
	)

	repoURL := fmt.Sprintf("ssh://%s@%s/home/user/repos/%s.git",
		config.User, net.JoinHostPort(config.Host, strconv.Itoa(config.Port)), repoName)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", repoURL, fmt.Sprintf("refs/heads/%s", branch))
	cmd.Env = gitEnv

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("timed out querying remote HEAD for branch %s: %w", branch, ctx.Err())
		}
		return "", fmt.Errorf("failed to get remote HEAD SHA: %w, output: %s", err, string(output))
	}

	parts := strings.Fields(string(output))
	if len(parts) == 0 {
		return "", fmt.Errorf("no output from git ls-remote for branch %s", branch)
	}
	return parts[0], nil
}

// PushTemplatedFilesToGitRepo updates an existing git repo with templated files, commits and pushes.
func (h *Harness) PushTemplatedFilesToGitRepo(workDir string, keyPath util.SSHPrivateKeyPath, sourceDir string, data interface{}) error {
	err := writeTemplatedFilesToDir(sourceDir, workDir, data)
	if err != nil {
		return fmt.Errorf("failed to write templated files: %w", err)
	}

	err = h.CommitAndPushGitRepo(workDir, keyPath, "")
	if err != nil {
		return fmt.Errorf("failed to commit and push: %w", err)
	}

	return nil
}

// GitRepoSetupOpts holds options for SetupGitRepoWithContent.
type GitRepoSetupOpts struct {
	GitServer     GitServerConfig
	SSHKeyPath    util.SSHPrivateKeyPath
	SSHKeyContent util.SSHPrivateKeyContent
	InternalHost  string
	InternalPort  int
	RepoName      string
	FilePath      string
	Content       string
	CommitMsg     string
	// Timeout for waiting for the Repository to become accessible. Defaults to 2 minutes.
	AccessTimeout time.Duration
	// Polling interval for accessibility check. Defaults to 5 seconds.
	AccessInterval time.Duration
}

// SetupGitRepoWithContent creates a git repo on the server, pushes initial content,
// registers a Repository resource with SSH credentials, and waits for it to become accessible.
func (h *Harness) SetupGitRepoWithContent(opts GitRepoSetupOpts) error {
	if opts.AccessTimeout == 0 {
		opts.AccessTimeout = 2 * time.Minute
	}
	if opts.AccessInterval == 0 {
		opts.AccessInterval = 5 * time.Second
	}

	if err := h.CreateGitRepositoryOnServer(opts.GitServer, opts.SSHKeyPath, opts.RepoName); err != nil {
		return fmt.Errorf("create git repo on server: %w", err)
	}

	if err := h.PushContentToGitServerRepo(opts.GitServer, opts.SSHKeyPath, opts.RepoName,
		opts.FilePath, opts.Content, opts.CommitMsg); err != nil {
		return fmt.Errorf("push initial content: %w", err)
	}

	if err := h.CreateRepositoryWithValidE2ECredentials(opts.InternalHost, opts.InternalPort,
		opts.RepoName, opts.SSHKeyContent); err != nil {
		return fmt.Errorf("create Repository resource: %w", err)
	}

	if err := h.WaitForRepositoryAccessible(opts.RepoName, opts.AccessTimeout, opts.AccessInterval); err != nil {
		return fmt.Errorf("wait for repository accessible: %w", err)
	}

	return nil
}
