package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// GitServerConfig holds configuration for the git server. Callers get it from infra (e.g. satellite).
type GitServerConfig struct {
	Host string
	Port int
	User string
}

// runGitServerSSHCommand executes a command on the git server via SSH using key authentication.
// sshPrivateKeyPath: path to private key; if empty, util.GetSSHPrivateKeyPath() is used.
func (h *Harness) runGitServerSSHCommand(config GitServerConfig, sshPrivateKeyPath, command string) error {
	keyPath := sshPrivateKeyPath
	if keyPath == "" {
		var err error
		keyPath, err = util.GetSSHPrivateKeyPath()
		if err != nil {
			return fmt.Errorf("failed to get SSH key path: %w", err)
		}
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

// runGitCommands executes a sequence of git commands in the specified working directory.
// sshPrivateKeyPath: if empty, util.GetSSHPrivateKeyPath() is used.
func (h *Harness) runGitCommands(workDir, sshPrivateKeyPath string, gitCmds [][]string) error {
	keyPath := sshPrivateKeyPath
	if keyPath == "" {
		var err error
		keyPath, err = util.GetSSHPrivateKeyPath()
		if err != nil {
			return fmt.Errorf("failed to get SSH key path: %w", err)
		}
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

// CreateGitRepositoryOnServer creates a new Git repository on the e2e git server.
// Callers pass config and sshPrivateKeyPath from infra/util.
func (h *Harness) CreateGitRepositoryOnServer(config GitServerConfig, sshPrivateKeyPath, repoName string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	createCmd := fmt.Sprintf("create-repo %s", repoName)
	err := h.runGitServerSSHCommand(config, sshPrivateKeyPath, createCmd)
	if err != nil {
		return fmt.Errorf("failed to create git repository %s: %w", repoName, err)
	}

	// Store the repository name for cleanup
	h.gitRepos[repoName] = fmt.Sprintf("ssh://%s@%s:%d/home/user/repos/%s.git",
		config.User, config.Host, config.Port, repoName)

	logrus.Infof("Created git repository: %s on git server", repoName)
	return nil
}

// DeleteGitRepositoryOnServer deletes a Git repository from the e2e git server.
func (h *Harness) DeleteGitRepositoryOnServer(config GitServerConfig, sshPrivateKeyPath, repoName string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	deleteCmd := fmt.Sprintf("delete-repo %s", repoName)
	err := h.runGitServerSSHCommand(config, sshPrivateKeyPath, deleteCmd)
	if err != nil {
		return fmt.Errorf("failed to delete git repository %s: %w", repoName, err)
	}

	// Remove from our tracking
	delete(h.gitRepos, repoName)

	logrus.Infof("Deleted git repository: %s from git server", repoName)
	return nil
}

// CloneGitRepositoryFromServer clones a repository from the git server to a local working directory.
func (h *Harness) CloneGitRepositoryFromServer(config GitServerConfig, sshPrivateKeyPath, repoName, localPath string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if localPath == "" {
		return fmt.Errorf("local path cannot be empty")
	}

	keyPath := sshPrivateKeyPath
	if keyPath == "" {
		var err error
		keyPath, err = util.GetSSHPrivateKeyPath()
		if err != nil {
			return fmt.Errorf("failed to get SSH key path: %w", err)
		}
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
// then commits and pushes changes.
func (h *Harness) pushToGitServerRepo(config GitServerConfig, sshPrivateKeyPath, repoName, addPath, commitMessage string, prepareContent func(workDir string) error) error {
	workDir := filepath.Join(h.gitWorkDir, "temp-"+uuid.New().String())
	defer os.RemoveAll(workDir)

	if err := h.CloneGitRepositoryFromServer(config, sshPrivateKeyPath, repoName, workDir); err != nil {
		return fmt.Errorf("failed to clone repository for push: %w", err)
	}

	if err := prepareContent(workDir); err != nil {
		return err
	}

	return h.runGitCommands(workDir, sshPrivateKeyPath, [][]string{
		{"git", "add", addPath},
		{"git", "commit", "-m", commitMessage},
		{"git", "branch", "-M", "main"},
		{"git", "push", "origin", "main"},
	})
}

// PushContentToGitServerRepo pushes content to a git repository on the server.
func (h *Harness) PushContentToGitServerRepo(config GitServerConfig, sshPrivateKeyPath, repoName, filePath, content, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Add content via test harness"
	}

	err := h.pushToGitServerRepo(config, sshPrivateKeyPath, repoName, filePath, commitMessage, func(workDir string) error {
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
func (h *Harness) PushContentToGitServerRepoFromPath(config GitServerConfig, sshPrivateKeyPath, repoName, sourcePath, commitMessage string) error {
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

	err = h.pushToGitServerRepo(config, sshPrivateKeyPath, repoName, ".", commitMessage, func(workDir string) error {
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
func (h *Harness) CreateGitRepository(config GitServerConfig, sshPrivateKeyPath, repoName string, repositorySpec domain.RepositorySpec) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	if err := h.CreateGitRepositoryOnServer(config, sshPrivateKeyPath, repoName); err != nil {
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
		if cleanupErr := h.DeleteGitRepositoryOnServer(config, sshPrivateKeyPath, repoName); cleanupErr != nil {
			logrus.Errorf("failed to delete git repository %s: %v", repoName, cleanupErr)
		}
		return fmt.Errorf("failed to create Repository resource: %w", err)
	}

	logrus.Infof("Created Repository resource %s", repoName)
	return nil
}

// UpdateGitServerRepository updates content in an existing git repository working directory.
func (h *Harness) UpdateGitServerRepository(config GitServerConfig, sshPrivateKeyPath, repoName, filePath, content, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Update content via test harness"
	}

	return h.PushContentToGitServerRepo(config, sshPrivateKeyPath, repoName, filePath, content, commitMessage)
}

// CommitAndPushGitRepo commits all changes in a local git working directory and pushes to the remote.
// sshPrivateKeyPath: if empty, util.GetSSHPrivateKeyPath() is used.
func (h *Harness) CommitAndPushGitRepo(workDir, sshPrivateKeyPath, commitMessage string) error {
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

	err := h.runGitCommands(workDir, sshPrivateKeyPath, [][]string{
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

// GetSSHPublicKeyPath and GetSSHPrivateKeyPath live in test/util (sshkeys.go); use util.GetSSHPublicKeyPath / util.GetSSHPrivateKeyPath.

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
func (h *Harness) SetupTemplatedGitRepoFromDir(config GitServerConfig, sshPrivateKeyPath, repoName, sourceDir string, data interface{}) (string, error) {
	err := h.CreateGitRepositoryOnServer(config, sshPrivateKeyPath, repoName)
	if err != nil {
		return "", fmt.Errorf("failed to create git repository: %w", err)
	}

	workDir, err := os.MkdirTemp("", "resourcesync-test-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	err = h.CloneGitRepositoryFromServer(config, sshPrivateKeyPath, repoName, workDir)
	if err != nil {
		return "", fmt.Errorf("failed to clone git repository: %w", err)
	}

	err = writeTemplatedFilesToDir(sourceDir, workDir, data)
	if err != nil {
		return "", fmt.Errorf("failed to write templated files: %w", err)
	}

	err = h.CommitAndPushGitRepo(workDir, sshPrivateKeyPath, "Add initial fleet files")
	if err != nil {
		return "", fmt.Errorf("failed to commit and push: %w", err)
	}

	return workDir, nil
}

// PushTemplatedFilesToGitRepo updates an existing git repo with templated files, commits and pushes.
func (h *Harness) PushTemplatedFilesToGitRepo(workDir, sshPrivateKeyPath, sourceDir string, data interface{}) error {
	err := writeTemplatedFilesToDir(sourceDir, workDir, data)
	if err != nil {
		return fmt.Errorf("failed to write templated files: %w", err)
	}

	err = h.CommitAndPushGitRepo(workDir, sshPrivateKeyPath, "")
	if err != nil {
		return fmt.Errorf("failed to commit and push: %w", err)
	}

	return nil
}
