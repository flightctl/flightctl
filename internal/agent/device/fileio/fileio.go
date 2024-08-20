package fileio

import (
	"io/fs"
	"os"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
)

type ManagedFile interface {
	Path() string
	Exists() (bool, error)
	IsUpToDate() (bool, error)
	Write() error
}

type Writer interface {
	SetRootdir(path string)
	WriteFileBytes(name string, data []byte, perm os.FileMode) error
	WriteFile(name string, data []byte, perm fs.FileMode) error
	RemoveFile(file string) error
	CreateManagedFile(file ign3types.File) ManagedFile
}
