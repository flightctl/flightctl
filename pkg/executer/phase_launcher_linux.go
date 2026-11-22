//go:build linux

package executer

import (
	"crypto/sha256"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/flightctl/flightctl/pkg/phasebootstrap"
	"golang.org/x/sys/unix"
)

const (
	phaseLauncherRootPath     = "/.flightctl-phase-launcher"
	phaseLauncherRelativePath = ".flightctl-phase-launcher"
	maxPhaseLauncherBytes     = 64 << 20
)

// PhaseLauncherCommand selects the private namespace-proof trampoline.
const PhaseLauncherCommand = phasebootstrap.Command

// CurrentPhaseNamespaceIdentity records the current process's namespace IDs.
func CurrentPhaseNamespaceIdentity() (PhaseNamespaceIdentity, error) {
	identity, err := phasebootstrap.CurrentIdentity()
	if err != nil {
		return PhaseNamespaceIdentity{}, fmt.Errorf("%w: %w", ErrPhaseUnsupported, err)
	}
	return identity, nil
}

// CurrentHostPhaseNamespaceIdentity reads the system-manager baseline at PID 1.
func CurrentHostPhaseNamespaceIdentity() (PhaseNamespaceIdentity, error) {
	identity, err := phasebootstrap.InitIdentity()
	if err != nil {
		return PhaseNamespaceIdentity{}, fmt.Errorf("%w: %w", ErrPhaseUnsupported, err)
	}
	return identity, nil
}

func phaseNamespaceLauncherArgs(identity PhaseNamespaceIdentity) []string {
	return phasebootstrap.Args(identity)
}

// RunPhaseLauncher proves the namespaces of its own process, then replaces it
// with the updater executable while preserving the updater's argv.
func RunPhaseLauncher(args []string) error {
	return phasebootstrap.Run(args)
}

func validatePhaseNamespaceIsolation(actual, expected PhaseNamespaceIdentity) error {
	return phasebootstrap.ValidateIsolation(actual, expected)
}

func validateStaticPhaseLauncher(file *os.File) error {
	binary, err := elf.NewFile(file)
	if err != nil {
		return err
	}
	defer binary.Close()
	for _, program := range binary.Progs {
		if program.Type == elf.PT_INTERP {
			return errors.New("dynamic interpreter is not available inside the phase root")
		}
	}
	return nil
}

func validatePhaseLauncher(path string) error {
	if err := validatePhaseRoot(filepath.Dir(path)); err != nil {
		return fmt.Errorf("phase launcher parent chain is not trusted: %w", err)
	}
	file, info, err := openTrustedPhaseLauncher(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() <= 0 || info.Size() > maxPhaseLauncherBytes {
		return errors.New("phase launcher exceeds its fixed staging bound")
	}
	return validateStaticPhaseLauncher(file)
}

func openTrustedPhaseLauncher(path string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open trusted phase launcher: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("inspect phase launcher: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 ||
		info.Mode().Perm()&0o022 != 0 || stat.Uid != 0 {
		_ = file.Close()
		return nil, nil, errors.New("phase launcher must be a root-owned executable that is not group- or world-writable")
	}
	return file, info, nil
}

func stagePhaseLauncher(root, source string) (string, error) {
	if err := validatePhaseRoot(root); err != nil {
		return "", err
	}
	if err := validatePhaseRoot(filepath.Dir(source)); err != nil {
		return "", fmt.Errorf("phase launcher parent chain is not trusted: %w", err)
	}
	input, before, err := openTrustedPhaseLauncher(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	if before.Size() <= 0 || before.Size() > maxPhaseLauncherBytes {
		return "", errors.New("phase launcher exceeds its fixed staging bound")
	}
	if err := validateStaticPhaseLauncher(input); err != nil {
		return "", fmt.Errorf("phase launcher must be a static executable: %w", err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open phase root for launcher staging: %w", err)
	}
	defer unix.Close(rootFD)
	targetFD, err := unix.Openat(rootFD, phaseLauncherRelativePath,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o555)
	if err != nil {
		return "", fmt.Errorf("create staged phase launcher: %w", err)
	}
	targetPath := filepath.Join(root, phaseLauncherRelativePath)
	staged := os.NewFile(uintptr(targetFD), targetPath)
	cleanup := func(cause error) (string, error) {
		_ = staged.Close()
		_ = unix.Unlinkat(rootFD, phaseLauncherRelativePath, 0)
		_ = unix.Fsync(rootFD)
		return "", cause
	}
	hash := sha256.New()
	written, err := io.CopyN(io.MultiWriter(staged, hash), input, before.Size())
	if err != nil || written != before.Size() {
		return cleanup(fmt.Errorf("copy phase launcher: %w", errors.Join(err, io.ErrUnexpectedEOF)))
	}
	after, err := input.Stat()
	if err != nil || !samePhaseLauncherFile(before, after) {
		return cleanup(errors.New("phase launcher changed while it was staged"))
	}
	if err := staged.Chmod(0o555); err != nil {
		return cleanup(err)
	}
	if err := copyPhaseLauncherSELinuxLabel(input, staged); err != nil {
		return cleanup(err)
	}
	if err := staged.Sync(); err != nil {
		return cleanup(err)
	}
	if err := staged.Close(); err != nil {
		return cleanup(err)
	}
	checkFD, err := unix.Openat(rootFD, phaseLauncherRelativePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return cleanup(err)
	}
	check := os.NewFile(uintptr(checkFD), targetPath)
	defer check.Close()
	checkHash := sha256.New()
	if _, err := io.Copy(checkHash, check); err != nil {
		return cleanup(err)
	}
	if !equalPhaseDigest(hash.Sum(nil), checkHash.Sum(nil)) {
		return cleanup(errors.New("staged phase launcher did not match the trusted source bytes"))
	}
	if _, err := check.Seek(0, io.SeekStart); err != nil {
		return cleanup(err)
	}
	if err := validateStaticPhaseLauncher(check); err != nil {
		return cleanup(fmt.Errorf("staged phase launcher is invalid: %w", err))
	}
	if err := unix.Fsync(rootFD); err != nil {
		return cleanup(fmt.Errorf("sync staged phase launcher: %w", err))
	}
	return targetPath, nil
}

func copyPhaseLauncherSELinuxLabel(source, target *os.File) error {
	size, err := unix.Fgetxattr(int(source.Fd()), "security.selinux", nil)
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read phase launcher SELinux label: %w", err)
	}
	label := make([]byte, size)
	read, err := unix.Fgetxattr(int(source.Fd()), "security.selinux", label)
	if err != nil {
		return fmt.Errorf("read phase launcher SELinux label: %w", err)
	}
	if read != size {
		return errors.New("phase launcher SELinux label changed while staging")
	}
	if err := unix.Fsetxattr(int(target.Fd()), "security.selinux", label, 0); err != nil {
		return fmt.Errorf("preserve phase launcher SELinux label: %w", err)
	}
	return nil
}

func samePhaseLauncherFile(a, b os.FileInfo) bool {
	left, leftOK := a.Sys().(*syscall.Stat_t)
	right, rightOK := b.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && a.Size() == b.Size() && a.ModTime() == b.ModTime() &&
		left.Dev == right.Dev && left.Ino == right.Ino && left.Uid == right.Uid &&
		a.Mode().Perm() == b.Mode().Perm()
}

func equalPhaseDigest(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}

func removeStagedPhaseLauncher(root, stagedPath string) error {
	if stagedPath == "" {
		return nil
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	if err := unix.Unlinkat(rootFD, phaseLauncherRelativePath, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return unix.Fsync(rootFD)
}
