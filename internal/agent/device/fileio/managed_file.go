package fileio

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
)

var ErrPathIsDir = errors.New("provided path is a directory")

type managedFile struct {
	ign3types.File
	initialized bool
	exists      bool
	size        int64
	contents    []byte
	writer      Writer
}

func newManagedFile(f ign3types.File, writer Writer) (ManagedFile, error) {
	mf := &managedFile{
		File:   f,
		writer: writer,
	}
	if err := mf.initMetadata(); err != nil {
		return nil, err
	}
	return mf, nil
}

func (m *managedFile) initMetadata() error {
	if m.initialized {
		return nil
	}
	path := m.writer.PathFor(m.Path())
	fileInfo, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			m.initialized = true
			return nil
		}
		return err
	}
	if fileInfo.IsDir() {
		return fmt.Errorf("%w: %s", ErrPathIsDir, path)
	}
	m.exists = true
	m.size = fileInfo.Size()
	m.initialized = true
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
	return nil
}

func (m *managedFile) isContentUpToDate() (bool, error) {
	if err := m.decodeFile(); err != nil {
		return false, err
	}
	currentContent, err := os.ReadFile(m.writer.PathFor(m.Path()))
	if err != nil {
		return false, err
	}
	return bytes.Equal(currentContent, m.contents), nil
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
		contentUpToDate, err := m.isContentUpToDate()
		if err != nil {
			return false, err
		}
		if contentUpToDate {
			return true, nil
		}
	}
	return false, nil
}

func (m *managedFile) Write() error {
	if err := m.decodeFile(); err != nil {
		return err
	}

	mode := DefaultFilePermissions
	if m.Mode != nil {
		mode = os.FileMode(*m.Mode)
	}

	// set chown if file information is provided
	uid, gid, err := getFileOwnership(m.File)
	if err != nil {
		return fmt.Errorf("failed to retrieve file ownership for file %q: %w", m.Path(), err)
	}

	return m.writer.WriteFile(m.Path(), m.contents, mode, WithGid(gid), WithUid(uid))
}
