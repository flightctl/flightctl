package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

// Config controller is responsible for ensuring the device configuration is reconciled
// against the device spec.
type Controller struct {
	deviceReadWriter fileio.ReadWriter
	dataDir          string
	log              *log.PrefixLogger
}

// NewController creates a new config controller. dataDir is the agent data
// directory under which the managed-directory ownership manifest is persisted.
func NewController(
	deviceReadWriter fileio.ReadWriter,
	dataDir string,
	log *log.PrefixLogger,
) *Controller {
	return &Controller{
		deviceReadWriter: deviceReadWriter,
		dataDir:          dataDir,
		log:              log,
	}
}

func (c *Controller) Sync(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	c.log.Debug("Syncing device configuration")
	defer c.log.Debug("Finished syncing device configuration")

	desiredFiles, err := ProviderSpecToFiles(desired.Config)
	if err != nil {
		return fmt.Errorf("%w: %w", deviceerrors.ErrConvertDesiredConfigToFiles, err)
	}

	currentFiles, err := ProviderSpecToFiles(current.Config)
	if err != nil {
		return fmt.Errorf("%w: %w", deviceerrors.ErrConvertCurrentConfigToFiles, err)
	}

	return c.ensureConfigFiles(currentFiles, desiredFiles)
}

func computeRemoval(currentFileList, desiredFileList []v1beta1.FileSpec) []string {
	desiredFiles := getFilePaths(desiredFileList)
	result := []string{}
	desiredMap := make(map[string]bool)

	for _, file := range desiredFiles {
		desiredMap[file] = true
	}

	currentFiles := getFilePaths(currentFileList)
	for _, file := range currentFiles {
		if !desiredMap[file] && len(file) > 0 {
			result = append(result, file)
		}
	}

	return result
}

func (c *Controller) ensureConfigFiles(currentFiles, desiredFiles []v1beta1.FileSpec) error {
	// owned is the set of directories the agent created for managed files. It
	// gates empty-directory cleanup so the agent only removes directories it
	// created, and is persisted across reconciles (and reboots).
	owned := c.loadManagedDirectories()
	dirty := false

	removed, err := c.removeObsoleteFiles(currentFiles, desiredFiles, owned)
	if err != nil {
		return fmt.Errorf("%w: %w", deviceerrors.ErrFailedToRemoveObsoleteFiles, err)
	}
	dirty = dirty || removed

	if len(desiredFiles) == 0 {
		c.log.Debug("No config files to write")
	} else {
		// Record the directories the agent is about to create before writing, so
		// they become eligible for future cleanup.
		if c.recordNewDirectories(desiredFiles, owned) {
			dirty = true
		}
		if err := c.writeFiles(desiredFiles); err != nil {
			c.log.Warnf("Writing config files failed: %+v", err)
			return fmt.Errorf("failed to apply configuration: %w", err)
		}
	}

	if dirty {
		if err := c.saveManagedDirectories(owned); err != nil {
			// Persisting ownership is best-effort: a failure only means some
			// directories may not be cleaned up later, never a failed sync.
			c.log.Warnf("Failed to persist managed directory ownership: %v", err)
		}
	}
	return nil
}

// removeObsoleteFiles removes files that are present in the currentFiles but not
// in the desiredFiles, then prunes any now-empty directories the agent created.
// It reports whether the owned-directory set changed.
func (c *Controller) removeObsoleteFiles(currentFiles, desiredFiles []v1beta1.FileSpec, owned map[string]bool) (bool, error) {
	removeFiles := computeRemoval(currentFiles, desiredFiles)
	for _, file := range removeFiles {
		if len(file) == 0 {
			continue
		}
		c.log.Debugf("Deleting file: %s", file)
		if err := c.deviceReadWriter.RemoveFile(file); err != nil {
			return false, fmt.Errorf("%w %w: %w", deviceerrors.ErrDeletingFilesFailed, deviceerrors.WithElement(file), err)
		}
	}
	// Remove empty managed directories left after obsolete files are deleted.
	changed := c.removeEmptyDirs(removeFiles, desiredFiles, owned)
	return changed, nil
}

// removeEmptyDirs removes directories left empty after obsolete files were
// deleted. It walks up the parent directories of each removed file (deepest
// first) and removes each one that is now empty, but only if the agent created
// it (owned); directories the agent did not create — pre-existing, package- or
// user-owned directories that merely held a managed file — are never removed.
// Directories that still hold desired files are preserved, as are any that still
// contain other content (RemoveEmptyDir is a no-op on non-empty directories).
// Top-level and relative paths are excluded by isCleanupCandidate. Cleanup
// failures are logged, never fatal; a directory whose removal errors keeps its
// ownership so the next reconcile retries. Ownership is otherwise relinquished
// after the best-effort attempt — whether the directory was removed or preserved
// — so it reports whether owned changed.
func (c *Controller) removeEmptyDirs(removedFiles []string, desiredFiles []v1beta1.FileSpec, owned map[string]bool) bool {
	// Preserve every directory a desired file lives under: those directories are
	// needed by the subsequent write step and must not be pruned.
	keep := make(map[string]bool)
	for _, file := range getFilePaths(desiredFiles) {
		for dir := filepath.Dir(file); isCleanupCandidate(dir); dir = filepath.Dir(dir) {
			keep[dir] = true
		}
	}

	// Collect the unique set of candidate directories from the removed files.
	candidates := make(map[string]bool)
	for _, file := range removedFiles {
		if len(file) == 0 {
			continue
		}
		for dir := filepath.Dir(file); isCleanupCandidate(dir); dir = filepath.Dir(dir) {
			candidates[dir] = true
		}
	}

	// Remove deepest directories first so that parents become empty (and thus
	// removable) only after their now-empty children are gone. Only directories
	// the agent created (owned) and not needed by a desired file are eligible.
	dirs := make([]string, 0, len(candidates))
	for dir := range candidates {
		if keep[dir] || !owned[dir] {
			continue
		}
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		di, dj := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/")
		if di != dj {
			return di > dj
		}
		// Deterministic ordering for siblings at the same depth.
		return dirs[i] < dirs[j]
	})

	changed := false
	for _, dir := range dirs {
		removed, err := c.deviceReadWriter.RemoveEmptyDir(dir)
		if err != nil {
			// Keep ownership so the next reconcile retries the removal.
			c.log.Warnf("Failed to remove empty directory %s: %v", dir, err)
			continue
		}
		if removed {
			c.log.Debugf("Removed empty directory: %s", dir)
		}
		// Relinquish ownership after the best-effort attempt: a directory that was
		// preserved (it still holds unmanaged content, or is a symlink) is no
		// longer tracked either way, so cleanup never revisits it.
		delete(owned, dir)
		changed = true
	}
	return changed
}

// isCleanupCandidate reports whether dir may be considered for empty-directory
// cleanup. The root and any of its direct children (top-level directories such
// as /etc, /var, /media, /lib64) are never removed: pruning a top-level
// directory is surprising and outside the scope of tidying up config-managed
// files, and an allowlist of well-known roots would inevitably miss some.
// Empty and relative paths are also excluded; requiring an absolute path guards
// against traversal so a rendered path such as "../etc" can never be joined
// with the writer root and removed.
func isCleanupCandidate(dir string) bool {
	if dir == "" || dir == "." {
		return false
	}
	cleaned := filepath.Clean(dir)
	if !filepath.IsAbs(cleaned) {
		return false
	}
	// Never remove the root or a top-level directory (a direct child of "/").
	return filepath.Dir(cleaned) != "/"
}

func (c *Controller) writeFiles(files []v1beta1.FileSpec) error {
	for _, file := range files {
		managedFile, err := c.deviceReadWriter.CreateManagedFile(file)
		if err != nil {
			return fmt.Errorf("creating managed file %w: %w", deviceerrors.WithElement(file.Path), err)
		}
		upToDate, err := managedFile.IsUpToDate()
		if err != nil {
			return fmt.Errorf("checking if file is up to date %w: %w", deviceerrors.WithElement(file.Path), err)
		}
		if upToDate {
			continue
		}
		if _, err = managedFile.Exists(); err != nil {
			return fmt.Errorf("checking if file exists %w: %w", deviceerrors.WithElement(file.Path), err)
		}
		if err := managedFile.Write(); err != nil {
			c.log.Warnf("Failed to write file %s: %v", file.Path, err)
			// in order to create clearer error in status in case we fail in temp file creation
			// we don't want to return temp filename but rather change the error message to return given file path
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				return fmt.Errorf("write file %w: %w", deviceerrors.WithElement(file.Path), pathErr.Err)
			}
			return fmt.Errorf("write file %w: %w", deviceerrors.WithElement(file.Path), err)
		}
	}
	return nil
}

func getFilePaths(currentFileList []v1beta1.FileSpec) []string {
	result := make([]string, len(currentFileList))
	for i, f := range currentFileList {
		result[i] = f.Path
	}
	return result
}

// ProviderSpecToFiles converts a list of ConfigProviderSpecs to a list of FileSpecs.
func ProviderSpecToFiles(configs *[]v1beta1.ConfigProviderSpec) ([]v1beta1.FileSpec, error) {
	if configs == nil || len(*configs) == 0 {
		return []v1beta1.FileSpec{}, nil
	}

	configItem := (*configs)[0]
	desiredProvider, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to convert config to inline config: %w", err)
	}

	return desiredProvider.Inline, nil
}

func FilesToProviderSpec(files []v1beta1.FileSpec) (*[]v1beta1.ConfigProviderSpec, error) {
	var provider v1beta1.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{
		Inline: files,
	})
	if err != nil {
		return nil, fmt.Errorf("inline config: %w", err)
	}
	return &[]v1beta1.ConfigProviderSpec{provider}, nil
}
