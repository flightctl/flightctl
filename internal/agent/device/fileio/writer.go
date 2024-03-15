package fileio

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/renameio"
	"github.com/vincent-petithory/dataurl"
	"k8s.io/klog/v2"
)

const (
	// defaultDirectoryPermissions houses the default mode to use when no directory permissions are provided
	defaultDirectoryPermissions os.FileMode = 0o755
	// defaultFilePermissions houses the default mode to use when no file permissions are provided
	DefaultFilePermissions os.FileMode = 0o644
)

// Writer is responsible for writing files to the device
type Writer struct {
	// rootDir is the root directory for the device writer useful for testing
	rootDir string
}

// New creates a new writer
func NewWriter() *Writer {
	return &Writer{}
}

// SetRootdir sets the root directory for the writer, useful for testing
func (w *Writer) SetRootdir(path string) {
	w.rootDir = path
}

// WriteIgnitionFiles writes the provided files to the device
func (w *Writer) WriteIgnitionFiles(files ...ign3types.File) error {
	var testMode bool
	if len(w.rootDir) > 0 {
		testMode = true
	}
	for _, file := range files {
		decodedContents, err := DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
		if err != nil {
			return fmt.Errorf("could not decode file %q: %w", file.Path, err)
		}
		mode := DefaultFilePermissions
		if file.Mode != nil {
			mode = os.FileMode(*file.Mode)
		}
		// set chown if file information is provided
		uid, gid, err := getFileOwnership(file, testMode)
		if err != nil {
			return fmt.Errorf("failed to retrieve file ownership for file %q: %w", file.Path, err)
		}

		// TODO: implement createOrigFile
		// if err := createOrigFile(file.Path, file.Path); err != nil {
		// 	return err
		// }
		if err := writeFileAtomically(w.rootDir+file.Path, decodedContents, defaultDirectoryPermissions, mode, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// WriteFile writes the provided data to the file at the path with the provided permissions
func (w *Writer) WriteFile(name string, data []byte, perm fs.FileMode) error {
	// TODO: rethink how we are persisting files
	// convert to ign file so we can use the atomic writer we can do this more directly in future
	return w.WriteIgnitionFiles(NewIgnFileBytes(name, data, perm))
}

// writeFileAtomically uses the renameio package to provide atomic file writing, we can't use renameio.WriteFile
// directly since we need to 1) Chown 2) go through a buffer since files provided can be big
func writeFileAtomically(fpath string, b []byte, dirMode, fileMode os.FileMode, uid, gid int) error {
	dir := filepath.Dir(fpath)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}
	t, err := renameio.TempFile(dir, fpath)
	if err != nil {
		return err
	}
	defer func() {
		_ = t.Cleanup()
	}()
	// Set permissions before writing data, in case the data is sensitive.
	if err := t.Chmod(fileMode); err != nil {
		return err
	}
	w := bufio.NewWriter(t)
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if uid != -1 && gid != -1 {
		if err := t.Chown(uid, gid); err != nil {
			return err
		}
	}
	return t.CloseAtomicallyReplace()
}

// This is essentially ResolveNodeUidAndGid() from Ignition; XXX should dedupe
// In testMode the permissions will be defined user
func getFileOwnership(file ign3types.File, testMode bool) (int, int, error) {
	if testMode {
		// use local user
		currentUser, err := user.Current()
		if err != nil {
			return 0, 0, fmt.Errorf("failed retrieving current user: %w", err)
		}
		gid, err := strconv.Atoi(currentUser.Gid)
		if err != nil {
			return 0, 0, fmt.Errorf("failed converting GID to int: %w", err)
		}
		uid, err := strconv.Atoi(currentUser.Uid)
		if err != nil {
			return 0, 0, fmt.Errorf("failed converting UID to int: %w", err)
		}
		return uid, gid, nil
	}

	uid, gid := 0, 0 // default to root
	var err error    // create default error var
	if file.User.ID != nil {
		uid = *file.User.ID
	} else if file.User.Name != nil && *file.User.Name != "" {
		uid, err = lookupUID(*file.User.Name)
		if err != nil {
			return uid, gid, err
		}
	}

	if file.Group.ID != nil {
		gid = *file.Group.ID
	} else if file.Group.Name != nil && *file.Group.Name != "" {
		gid, err = lookupGID(*file.Group.Name)
		if err != nil {
			return uid, gid, err
		}
	}
	return uid, gid, nil
}

func lookupUID(username string) (int, error) {
	osUser, err := user.Lookup(username)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve UserID for username: %s", username)
	}
	klog.V(2).Infof("Retrieved UserId: %s for username: %s", osUser.Uid, username)
	uid, _ := strconv.Atoi(osUser.Uid)
	return uid, nil
}

func lookupGID(group string) (int, error) {
	osGroup, err := user.LookupGroup(group)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve GroupID for group: %v", group)
	}
	klog.V(2).Infof("Retrieved GroupID: %s for group: %s", osGroup.Gid, group)
	gid, _ := strconv.Atoi(osGroup.Gid)
	return gid, nil
}

func DecodeIgnitionFileContents(source, compression *string) ([]byte, error) {
	var contentsBytes []byte

	// To allow writing of "empty" files we'll allow source to be nil
	if source != nil {
		source, err := dataurl.DecodeString(*source)
		if err != nil {
			return []byte{}, fmt.Errorf("could not decode file content string: %w", err)
		}
		if compression != nil {
			switch *compression {
			case "":
				contentsBytes = source.Data
			case "gzip":
				reader, err := gzip.NewReader(bytes.NewReader(source.Data))
				if err != nil {
					return []byte{}, fmt.Errorf("could not create gzip reader: %w", err)
				}
				defer reader.Close()
				contentsBytes, err = io.ReadAll(reader)
				if err != nil {
					return []byte{}, fmt.Errorf("failed decompressing: %w", err)
				}
			default:
				return []byte{}, fmt.Errorf("unsupported compression type %q", *compression)
			}
		} else {
			contentsBytes = source.Data
		}
	}
	return contentsBytes, nil
}

// NewIgnFileBytes is like NewIgnFile, but accepts binary data
func NewIgnFileBytes(path string, contents []byte, mode os.FileMode) ign3types.File {
	fileMode := int(mode.Perm())
	return ign3types.File{
		Node: ign3types.Node{
			Path: path,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Mode: &fileMode,
			Contents: ign3types.Resource{
				Source:      util.StrToPtr(dataurl.EncodeBytes(contents)),
				Compression: util.StrToPtr(""),
			},
		},
	}
}
