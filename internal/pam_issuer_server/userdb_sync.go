//go:build linux

package pam_issuer_server

import (
	"context"
	"errors"
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

// SyncEventKind is the type of a userdb sync event.
type SyncEventKind string

const (
	SyncSkipped        SyncEventKind = "skipped"         // empty dirs or userdb dir missing/not a dir
	SyncCopyInDone     SyncEventKind = "copy_in_done"    // copy from userdb to etc finished
	SyncWatcherStarted SyncEventKind = "watcher_started" // fsnotify watch on etcDir active
	SyncCopyBackDone   SyncEventKind = "copy_back_done"  // copy from etc to userdb finished (after a change)
	SyncError          SyncEventKind = "error"           // runtime failure (e.g. watcher); Err is set
)

// SyncEvent is emitted on the returned channel for observability and testing.
// For SyncError, Err is set. For SyncCopyBackDone, File is the base name of the file copied.
type SyncEvent struct {
	Kind SyncEventKind
	Err  error
	File string
}

// ErrUserDBDirInvalid is returned when userdbDir is missing or not a directory.
var ErrUserDBDirInvalid = errors.New("userdb dir missing or not a directory")

// ErrInvalidSyncDirs is returned when userdbDir or etcDir is empty.
var ErrInvalidSyncDirs = errors.New("userdbDir and etcDir are required")

func sendSyncEvent(events chan<- SyncEvent, e SyncEvent) {
	select {
	case events <- e:
	default:
		// Caller not reading: don't block the sync loop
	}
}

// RunUserDBSync copies userdb from userdbDir into etcDir on start, then watches etcDir
// and copies the four userdb files back to userdbDir whenever they change (so changes
// from groupadd/useradd/usermod/chpasswd persist). Run until ctx is done.
// Initial validation (empty dirs, userdbDir missing or not a directory) is done
// synchronously and returns an error; runtime failures (e.g. watcher) are sent as
// SyncError on the channel. On success returns (events, nil); the sync runs in a
// goroutine and the channel is closed when it stops.
func RunUserDBSync(ctx context.Context, log logrus.FieldLogger, userdbDir, etcDir string) (<-chan SyncEvent, error) {
	if userdbDir == "" || etcDir == "" {
		log.Warnf("userdb sync: %v", ErrInvalidSyncDirs)
		return nil, ErrInvalidSyncDirs
	}
	if fi, err := os.Stat(userdbDir); err != nil || !fi.IsDir() {
		log.Warnf("userdb sync: %v", ErrUserDBDirInvalid)
		return nil, ErrUserDBDirInvalid
	}
	events := make(chan SyncEvent, 32)
	go func() {
		defer close(events)
		runUserDBSync(ctx, log, userdbDir, etcDir, events)
	}()
	return events, nil
}

func runUserDBSync(ctx context.Context, log logrus.FieldLogger, userdbDir, etcDir string, events chan<- SyncEvent) {
	copyUserdbDir(log, userdbDir, etcDir)
	sendSyncEvent(events, SyncEvent{Kind: SyncCopyInDone})
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Warnf("userdb fsnotify watcher: %v (sync disabled)", err)
		sendSyncEvent(events, SyncEvent{Kind: SyncError, Err: err})
		return
	}
	defer watcher.Close()
	if err := watcher.Add(etcDir); err != nil {
		log.Warnf("userdb watch %s: %v (sync disabled)", etcDir, err)
		sendSyncEvent(events, SyncEvent{Kind: SyncError, Err: err})
		return
	}
	sendSyncEvent(events, SyncEvent{Kind: SyncWatcherStarted})
	log.Infof("userdb sync: watching %s, persisting to %s", etcDir, userdbDir)
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-watcher.Errors:
			log.Warnf("userdb watcher error: %v", err)
			continue
		case e := <-watcher.Events:
			if !isUserDBFile(e.Name) {
				continue
			}
			if e.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			dst := filepath.Join(userdbDir, filepath.Base(e.Name))
			if err := copyFile(e.Name, dst); err != nil {
				log.Warnf("userdb sync copy-back: %v", err)
				sendSyncEvent(events, SyncEvent{Kind: SyncError, Err: err})
				continue
			}
			log.Debugf("userdb sync: copied %s -> %s", e.Name, dst)
			sendSyncEvent(events, SyncEvent{Kind: SyncCopyBackDone, File: filepath.Base(e.Name)})
		}
	}
}

func isUserDBFile(name string) bool {
	base := filepath.Base(name)
	for _, f := range userdbFiles {
		if base == f {
			return true
		}
	}
	return false
}

func copyUserdbDir(log logrus.FieldLogger, srcDir, dstDir string) {
	for _, f := range userdbFiles {
		src := filepath.Join(srcDir, f)
		dst := filepath.Join(dstDir, f)
		if err := copyFile(src, dst); err != nil {
			log.Debugf("userdb sync: skip %s: %v", src, err)
			continue
		}
		log.Debugf("userdb sync: copied %s -> %s", src, dst)
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeUserDBFile(dst, data)
}

func writeUserDBFile(dst string, data []byte) error {
	mode := os.FileMode(0644)
	if base := filepath.Base(dst); base == userdbShadow || base == userdbGshadow {
		mode = 0600
	}
	return os.WriteFile(dst, data, mode)
}
