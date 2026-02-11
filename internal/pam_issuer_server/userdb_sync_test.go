//go:build linux

package pam_issuer_server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestRunUserDBSync_emptyDirsReturnError(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := RunUserDBSync(ctx, log, "", "/etc")
	if events != nil || err != ErrInvalidSyncDirs {
		t.Errorf("empty userdbDir: got (events=%v, err=%v), want (nil, ErrInvalidSyncDirs)", events != nil, err)
	}

	events2, err2 := RunUserDBSync(ctx, log, "/userdb", "")
	if events2 != nil || err2 != ErrInvalidSyncDirs {
		t.Errorf("empty etcDir: got (events=%v, err=%v), want (nil, ErrInvalidSyncDirs)", events2 != nil, err2)
	}
}

func TestRunUserDBSync_missingUserdbDirReturnsError(t *testing.T) {
	dir := t.TempDir()
	userdbDir := filepath.Join(dir, "nonexistent")
	etcDir := filepath.Join(dir, "etc")
	_ = os.MkdirAll(etcDir, 0755)

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := RunUserDBSync(ctx, log, userdbDir, etcDir)
	if err != ErrUserDBDirInvalid {
		t.Errorf("missing userdb dir: got err %v, want ErrUserDBDirInvalid", err)
	}
	if events != nil {
		t.Errorf("missing userdb dir: got non-nil channel")
	}
	cancel()
}

func TestRunUserDBSync_copyInAndCopyBack(t *testing.T) {
	dir := t.TempDir()
	userdbDir := filepath.Join(dir, "userdb")
	etcDir := filepath.Join(dir, "etc")
	if err := os.MkdirAll(userdbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(etcDir, 0755); err != nil {
		t.Fatal(err)
	}

	passwdContent := []byte("root:x:0:0:root:/root:/bin/bash\n")
	if err := os.WriteFile(filepath.Join(userdbDir, "passwd"), passwdContent, 0600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"group", "shadow", "gshadow"} {
		if err := os.WriteFile(filepath.Join(userdbDir, f), []byte(""), 0600); err != nil {
			t.Fatal(err)
		}
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := RunUserDBSync(ctx, log, userdbDir, etcDir)
	if err != nil {
		t.Fatalf("RunUserDBSync: %v", err)
	}

	if got := <-events; got.Kind != SyncCopyInDone {
		t.Fatalf("after start: got %q, want %q", got.Kind, SyncCopyInDone)
	}
	if got := <-events; got.Kind != SyncWatcherStarted {
		t.Fatalf("after copy-in: got %q, want %q", got.Kind, SyncWatcherStarted)
	}

	// Copy-in: etc should have passwd from userdb
	etcPasswd := filepath.Join(etcDir, "passwd")
	data, err := os.ReadFile(etcPasswd)
	if err != nil {
		t.Fatalf("copy-in: read etc/passwd: %v", err)
	}
	if string(data) != string(passwdContent) {
		t.Errorf("copy-in: etc/passwd = %q, want %q", data, passwdContent)
	}

	// Change etc/passwd using atomic write-then-rename, which is how Linux
	// tools (useradd, usermod, chpasswd, etc.) actually modify these files.
	newContent := []byte("root:x:0:0:root:/root:/bin/bash\nadmin:x:1000:1000:admin:/home/admin:/bin/bash\n")
	tmpPasswd := etcPasswd + "+"
	if err := os.WriteFile(tmpPasswd, newContent, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpPasswd, etcPasswd); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-events:
		if got.Kind != SyncCopyBackDone {
			t.Errorf("after write: got %q, want %q", got.Kind, SyncCopyBackDone)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for copy_back_done")
	}

	userdbPasswd := filepath.Join(userdbDir, "passwd")
	data, err = os.ReadFile(userdbPasswd)
	if err != nil {
		t.Fatalf("copy-back: read userdb/passwd: %v", err)
	}
	if string(data) != string(newContent) {
		t.Errorf("copy-back: userdb/passwd = %q, want %q", data, newContent)
	}

	cancel()
}

func TestRunUserDBSync_emptyUserdbStartsWatcher(t *testing.T) {
	// Fresh install: empty userdb dir; copy-in skips missing files, watcher starts so copy-back can populate volume.
	dir := t.TempDir()
	userdbDir := filepath.Join(dir, "userdb")
	etcDir := filepath.Join(dir, "etc")
	if err := os.MkdirAll(userdbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(etcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etcDir, "passwd"), []byte("root:x:0:0::/root:/bin/bash\n"), 0600); err != nil {
		t.Fatal(err)
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := RunUserDBSync(ctx, log, userdbDir, etcDir)
	if err != nil {
		t.Fatalf("RunUserDBSync: %v", err)
	}

	if got := <-events; got.Kind != SyncCopyInDone {
		t.Fatalf("empty userdb: got %q, want SyncCopyInDone", got.Kind)
	}
	if got := <-events; got.Kind != SyncWatcherStarted {
		t.Fatalf("empty userdb: got %q, want SyncWatcherStarted", got.Kind)
	}
	cancel()
}
