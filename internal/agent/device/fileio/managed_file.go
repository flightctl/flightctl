package fileio

import (
	"bytes"
	"fmt"
	"os"
	"syscall"

	"github.com/ccoveille/go-safecast"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
)

type managedFile struct {
	ign3types.File
	exists   bool
	size     int64
	perms    os.FileMode
	uid      int
	gid      int
	contents []byte
	writer   Writer
}

func newManagedFile(f ign3types.File, writer Writer) (ManagedFile, error) {
	mf := &managedFile{
		File:   f,
		writer: writer,
	}
	if err := mf.initExistingFileMetadata(); err != nil {
		return nil, err
	}
	return mf, nil
}

// initExistingFileMetadata initializes the exists and size fields of the on disk managedFile.
func (m *managedFile) initExistingFileMetadata() error {
	path := m.writer.PathFor(m.Path())
	fileInfo, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fileInfo.IsDir() {
		return fmt.Errorf("%w: %s", errors.ErrPathIsDir, path)
	}
	m.exists = true
	m.size = fileInfo.Size()
	return nil
}

func (m *managedFile) decodeFile() error {
	if m.contents != nil {
		return nil
	}
	contents, err := decodeIgnitionFileContents(m.Contents.Source, m.Contents.Compression)
	if err != nil {
		return err
	}
	m.contents = contents

	m.uid, m.gid, err = getFileOwnership(m.File)
	if err != nil {
		return fmt.Errorf("failed to retrieve file ownership for file %q: %w", m.Path(), err)
	}

	m.perms, err = intToFileMode(m.Mode)
	if err != nil {
		return fmt.Errorf("failed to retrieve file permissions for file %q: %w", m.Path(), err)
	}

	return nil
}

func (m *managedFile) isUpToDate() (bool, error) {
	if err := m.decodeFile(); err != nil {
		return false, err
	}
	currentContent, err := os.ReadFile(m.writer.PathFor(m.Path()))
	if err != nil {
		return false, err
	}
	if !bytes.Equal(currentContent, m.contents) {
		return false, nil
	}

	fileInfo, err := os.Stat(m.writer.PathFor(m.Path()))
	if err != nil {
		return false, err
	}
	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("failed to retrieve UID and GID")
	}

	uid, err := safecast.ToUint32(m.uid)
	if err != nil {
		return false, err
	}

	gid, err := safecast.ToUint32(m.gid)
	if err != nil {
		return false, err
	}

	// compare file ownership
	if stat.Uid != uid || stat.Gid != gid {
		return false, nil
	}

	// compare file permissions
	if fileInfo.Mode().Perm() != m.perms.Perm() {
		return false, nil
	}

	return true, nil
}

func (m *managedFile) Path() string {
	return m.File.Path
}

func (m *managedFile) Exists() (bool, error) {
	return m.exists, nil
}

func (m *managedFile) IsUpToDate() (bool, error) {
	if err := m.decodeFile(); err != nil {
		return false, err
	}
	if m.exists && m.size == int64(len(m.contents)) {
		isUpToDate, err := m.isUpToDate()
		if err != nil {
			return false, err
		}
		if isUpToDate {
			return true, nil
		}
	}
	return false, nil
}

func (m *managedFile) Write() error {
	if err := m.decodeFile(); err != nil {
		return err
	}

	mode, err := intToFileMode(m.Mode)
	if err != nil {
		return fmt.Errorf("failed to retrieve file permissions for file %q: %w", m.Path(), err)
	}

	// set chown if file information is provided
	uid, gid, err := getFileOwnership(m.File)
	if err != nil {
		return fmt.Errorf("failed to retrieve file ownership for file %q: %w", m.Path(), err)
	}

	return m.writer.WriteFile(m.Path(), m.contents, mode, WithGid(gid), WithUid(uid))
}

func intToFileMode(i *int) (os.FileMode, error) {
	mode := DefaultFilePermissions
	if i != nil {
		filemode, err := safecast.ToUint32(*i)
		if err != nil {
			return 0, err
		}

		// Go stores setuid/setgid/sticky differently, so we
		// strip them off and then add them back
		mode = os.FileMode(filemode).Perm()
		if *i&0o1000 != 0 {
			mode = mode | os.ModeSticky
		}
		if *i&0o2000 != 0 {
			mode = mode | os.ModeSetgid
		}
		if *i&0o4000 != 0 {
			mode = mode | os.ModeSetuid
		}
	}
	return mode, nil
}
