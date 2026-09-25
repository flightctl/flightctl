package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

// ManagedDirectoriesFileName is the name of the file, stored under the agent
// data directory, that records the directories the agent created while writing
// managed config files. Only directories in this set are eligible for
// empty-directory cleanup, so the agent never removes a directory it did not
// create (for example a package- or user-owned directory that happens to hold a
// managed file).
const ManagedDirectoriesFileName = "managed-directories.json"

// managedDirectories is the on-disk representation of the agent-created
// directory set. It mirrors the lightweight JSON-manifest pattern used elsewhere
// in the agent (e.g. image pruning's references file) rather than introducing a
// new persistence mechanism.
type managedDirectories struct {
	Timestamp   string   `json:"timestamp"`
	Directories []string `json:"directories"`
}

// managedDirectoriesPath returns the full path to the ownership manifest under
// the agent data directory.
func (c *Controller) managedDirectoriesPath() string {
	return filepath.Join(c.dataDir, ManagedDirectoriesFileName)
}

// loadManagedDirectories reads the agent-created directory set from disk. A
// missing manifest yields an empty set. A read or decode error is logged and
// also yields an empty set: ownership tracking is best-effort and must never
// fail reconciliation, and an empty set is the safe default (nothing is
// eligible for cleanup).
func (c *Controller) loadManagedDirectories() map[string]bool {
	owned := make(map[string]bool)
	data, err := c.deviceReadWriter.ReadFile(c.managedDirectoriesPath())
	if err != nil {
		if !os.IsNotExist(err) {
			c.log.Warnf("Failed to read managed directory manifest: %v", err)
		}
		return owned
	}
	var manifest managedDirectories
	if err := json.Unmarshal(data, &manifest); err != nil {
		c.log.Warnf("Failed to parse managed directory manifest; ignoring it: %v", err)
		return owned
	}
	for _, dir := range manifest.Directories {
		owned[dir] = true
	}
	return owned
}

// saveManagedDirectories persists the agent-created directory set to disk. The
// entries are sorted so the manifest is stable across writes.
func (c *Controller) saveManagedDirectories(owned map[string]bool) error {
	dirs := make([]string, 0, len(owned))
	for dir := range owned {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	data, err := json.Marshal(managedDirectories{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Directories: dirs,
	})
	if err != nil {
		return err
	}
	return c.deviceReadWriter.WriteFile(c.managedDirectoriesPath(), data, fileio.DefaultFilePermissions)
}

// recordNewDirectories records, as agent-created, every cleanup-candidate parent
// directory of the desired files that does not yet exist on disk. It must be
// called before the files are written (which creates those directories via
// MkdirAll): a directory that is absent now but present after the write is one
// the agent created. Directories that already exist are left untracked so
// cleanup never removes pre-existing, non-agent-owned directories. It reports
// whether the owned set changed.
func (c *Controller) recordNewDirectories(desiredFiles []v1beta1.FileSpec, owned map[string]bool) bool {
	changed := false
	for _, file := range getFilePaths(desiredFiles) {
		for dir := filepath.Dir(file); isCleanupCandidate(dir); dir = filepath.Dir(dir) {
			if owned[dir] {
				continue
			}
			exists, err := c.deviceReadWriter.PathExists(dir, fileio.WithSkipContentCheck())
			if err != nil {
				// On an unexpected stat error, err on the side of not claiming
				// ownership: an untracked directory is never removed.
				c.log.Warnf("Failed to check directory %s for ownership tracking: %v", dir, err)
				continue
			}
			if exists {
				continue
			}
			owned[dir] = true
			changed = true
		}
	}
	return changed
}
