package fileio

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
)

type managedFile struct {
	ign3types.File
	initialized bool
	exists      bool
	size        int64
	contents    []byte
	rootDir     string
}

func newManagedFile(f ign3types.File, rootDir string) ManagedFile {
	return &managedFile{
		File:    f,
		rootDir: rootDir,
	}
}

func (m *managedFile) initMetadata() error {
	if m.initialized {
		return nil
	}
	fileInfo, err := os.Stat(m.Path())
	if err != nil {
		if os.IsNotExist(err) {
			m.initialized = true
			return nil
		}
		return err
	}
	if fileInfo.IsDir() {
		return fmt.Errorf("provided path %q is a directory", m.Path())
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
	currentContent, err := os.ReadFile(m.Path())
	if err != nil {
		return false, err
	}
	return bytes.Equal(currentContent, m.contents), nil
}

func (m *managedFile) Path() string {
	return m.File.Path
}

func (m *managedFile) Exists() (bool, error) {
	if err := m.initMetadata(); err != nil {
		return false, err
	}
	return m.exists, nil
}

func (m *managedFile) IsUpToDate() (bool, error) {
	if err := m.initMetadata(); err != nil {
		return false, err
	}
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
	uid, gid, err := getFileOwnership(m.File, len(m.rootDir) > 0)
	if err != nil {
		return fmt.Errorf("failed to retrieve file ownership for file %q: %w", m.Path(), err)
	}

	// TODO: implement createOrigFile
	// if err := createOrigFile(file.Path, file.Path); err != nil {
	// 	return err
	// }
	if err := writeFileAtomically(filepath.Join(m.rootDir, m.Path()), m.contents, defaultDirectoryPermissions, mode, uid, gid); err != nil {
		return err
	}
	return nil
}
