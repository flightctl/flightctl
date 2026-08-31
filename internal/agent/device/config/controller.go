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
	deviceWriter fileio.Writer
	log          *log.PrefixLogger
}

// NewController creates a new config controller.
func NewController(
	deviceWriter fileio.Writer,
	log *log.PrefixLogger,
) *Controller {
	return &Controller{
		deviceWriter: deviceWriter,
		log:          log,
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
	if err := c.removeObsoleteFiles(currentFiles, desiredFiles); err != nil {
		return fmt.Errorf("%w: %w", deviceerrors.ErrFailedToRemoveObsoleteFiles, err)
	}

	if len(desiredFiles) == 0 {
		c.log.Debug("No config files to write")
		// no files to write
		return nil
	}

	if err := c.writeFiles(desiredFiles); err != nil {
		c.log.Warnf("Writing config files failed: %+v", err)
		return fmt.Errorf("failed to apply configuration: %w", err)
	}
	return nil
}

// removeObsoleteFiles removes files that are present in the currentFiles but not in the desiredFiles.
func (c *Controller) removeObsoleteFiles(currentFiles, desiredFiles []v1beta1.FileSpec) error {
	removeFiles := computeRemoval(currentFiles, desiredFiles)
	for _, file := range removeFiles {
		if len(file) == 0 {
			continue
		}
		c.log.Debugf("Deleting file: %s", file)
		if err := c.deviceWriter.RemoveFile(file); err != nil {
			return fmt.Errorf("%w %w: %w", deviceerrors.ErrDeletingFilesFailed, deviceerrors.WithElement(file), err)
		}
	}
	// Fix for EDM-4892: removing the files is not enough. A removed host
	// configuration source (e.g. a git repo populating
	// /etc/microshift/manifests.d/) also creates directories to hold those
	// files; without cleaning them up the device is left with a stale, empty
	// directory tree that reads as "content still present" after the source is
	// removed. This is best-effort cleanup and never fails the reconcile.
	c.removeEmptyDirs(removeFiles, desiredFiles)
	return nil
}

// protectedDirs are directories we never remove during empty-directory cleanup,
// even if they end up empty. They are well-known system roots; removing them
// would be surprising and outside the scope of tidying up config-managed files.
var protectedDirs = map[string]bool{
	"/":     true,
	"/etc":  true,
	"/var":  true,
	"/usr":  true,
	"/opt":  true,
	"/run":  true,
	"/home": true,
	"/root": true,
	"/boot": true,
	"/tmp":  true,
	"/srv":  true,
	"/mnt":  true,
	"/dev":  true,
	"/proc": true,
	"/sys":  true,
	"/lib":  true,
	"/bin":  true,
	"/sbin": true,
}

// removeEmptyDirs removes directories left empty after obsolete files were
// deleted. It walks up the parent directories of each removed file (deepest
// first) and removes each one that is now empty, stopping at protected system
// roots. Directories that still hold desired files are preserved, as are any
// directories that still contain other content (RemoveEmptyDir is a no-op on
// non-empty directories). Cleanup failures are logged, never fatal.
func (c *Controller) removeEmptyDirs(removedFiles []string, desiredFiles []v1beta1.FileSpec) {
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
	// removable) only after their now-empty children are gone.
	dirs := make([]string, 0, len(candidates))
	for dir := range candidates {
		if keep[dir] {
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

	for _, dir := range dirs {
		if err := c.deviceWriter.RemoveEmptyDir(dir); err != nil {
			c.log.Warnf("Failed to remove empty directory %s: %v", dir, err)
			continue
		}
		c.log.Debugf("Removed empty directory: %s", dir)
	}
}

// isCleanupCandidate reports whether dir may be considered for empty-directory
// cleanup. Empty, relative, and protected system directories are excluded.
// Requiring an absolute path guards against traversal: a rendered path such as
// "../etc" must never be joined with the writer root and removed.
func isCleanupCandidate(dir string) bool {
	if dir == "" || dir == "." {
		return false
	}
	cleaned := filepath.Clean(dir)
	if !filepath.IsAbs(cleaned) {
		return false
	}
	return !protectedDirs[cleaned]
}

func (c *Controller) writeFiles(files []v1beta1.FileSpec) error {
	for _, file := range files {
		managedFile, err := c.deviceWriter.CreateManagedFile(file)
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
