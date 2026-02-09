//go:build linux

package pam_issuer_server

import (
	"context"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
)

const (
	userdbPasswd  = "passwd"
	userdbGroup   = "group"
	userdbShadow  = "shadow"
	userdbGshadow = "gshadow"
)

var userdbFiles = []string{userdbPasswd, userdbGroup, userdbShadow, userdbGshadow}

// RunUserDBSync copies userdb from userdbDir into etcDir on start, then watches etcDir
// and copies the four userdb files back to userdbDir whenever they change (so changes
// RunUserDBSync copies the tracked user database files from userdbDir into etcDir and keeps them persisted by watching etcDir.
//
// RunUserDBSync performs an initial copy of passwd, group, shadow and gshadow from userdbDir to etcDir, then installs a filesystem
// watcher on etcDir and copies those files back to userdbDir when they are created, written, removed, or renamed. The routine returns
// immediately if either directory string is empty or if userdbDir does not exist or is not a directory. The function runs until ctx
// is cancelled; watcher failures disable syncing and are logged.
func RunUserDBSync(ctx context.Context, log logrus.FieldLogger, userdbDir, etcDir string) {
	if userdbDir == "" || etcDir == "" {
		return
	}
	if fi, err := os.Stat(userdbDir); err != nil || !fi.IsDir() {
		return // no userdb dir: nothing to sync, avoid watcher/copyBack to missing path
	}
	copyIn(log, userdbDir, etcDir)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Warnf("userdb fsnotify watcher: %v (sync disabled)", err)
		return
	}
	defer watcher.Close()
	if err := watcher.Add(etcDir); err != nil {
		log.Warnf("userdb watch %s: %v (sync disabled)", etcDir, err)
		return
	}
	log.Infof("userdb sync: watching %s, persisting to %s", etcDir, userdbDir)
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-watcher.Errors:
			log.Warnf("userdb watcher error: %v", err)
			continue
		case e := <-watcher.Events:
			if !isUserDBFile(e.Name, etcDir) {
				continue
			}
			if e.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			copyBack(log, etcDir, userdbDir)
		}
	}
}

// user database files (passwd, group, shadow, gshadow).
func isUserDBFile(name, etcDir string) bool {
	base := filepath.Base(name)
	for _, f := range userdbFiles {
		if base == f {
			return true
		}
	}
	return false
}

// copyIn copies the tracked user database files from userdbDir into etcDir.
// It attempts to copy each filename listed in userdbFiles; if a source file is
// missing or fails to copy the error is ignored and copying proceeds for the
// remaining files. Successful copies are logged at debug level.
func copyIn(log logrus.FieldLogger, userdbDir, etcDir string) {
	for _, f := range userdbFiles {
		src := filepath.Join(userdbDir, f)
		dst := filepath.Join(etcDir, f)
		if err := copyFile(src, dst); err != nil {
			continue // e.g. userdb/passwd missing: skip, no panic
		}
		log.Debugf("userdb sync: copied %s -> %s", src, dst)
	}
}

// copyBack copies the tracked user database files from etcDir into userdbDir.
// It attempts to update each file in the userdbFiles list and ignores errors for individual files.
// On completion a debug message is logged showing the source and destination directories.
func copyBack(log logrus.FieldLogger, etcDir, userdbDir string) {
	for _, f := range userdbFiles {
		src := filepath.Join(etcDir, f)
		dst := filepath.Join(userdbDir, f)
		if err := copyFile(src, dst); err != nil {
			continue
		}
	}
	log.Debugf("userdb sync: copied %s -> %s", etcDir, userdbDir)
}

// copyFile copies the contents of src to dst, preserving appropriate userdb file permissions.
// It returns any error encountered while reading src or writing dst.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeUserDBFile(dst, data)
}

// writeUserDBFile writes data to dst and sets file permissions to 0644,
// except when the destination base name is "shadow" or "gshadow", in which case it sets permissions to 0600.
// It returns any error encountered while writing the file.
func writeUserDBFile(dst string, data []byte) error {
	mode := os.FileMode(0644)
	if base := filepath.Base(dst); base == userdbShadow || base == userdbGshadow {
		mode = 0600
	}
	return os.WriteFile(dst, data, mode)
}