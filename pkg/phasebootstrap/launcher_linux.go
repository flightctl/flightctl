//go:build linux

// Package phasebootstrap contains the small, statically linked trampoline used
// to prove a phase's namespaces immediately before it replaces itself with the
// updater executable.
package phasebootstrap

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// Command is the private command-line marker understood by the launcher.
const Command = "--internal-phase-launcher"

// NamespaceID identifies one namespace by its procfs device and inode.
type NamespaceID struct {
	Device uint64
	Inode  uint64
}

// Identity records a process's PID, network, and IPC namespaces.
type Identity struct {
	PID     NamespaceID
	Network NamespaceID
	IPC     NamespaceID
}

// CurrentIdentity reads namespace identities for the calling process.
func CurrentIdentity() (Identity, error) {
	return identityAt("/proc/self/ns")
}

// InitIdentity reads the namespaces of PID 1, the system manager's namespace
// baseline for the supported host profile.
func InitIdentity() (Identity, error) {
	return identityAt("/proc/1/ns")
}

func identityAt(root string) (Identity, error) {
	var identity Identity
	for _, entry := range []struct {
		name   string
		target *NamespaceID
	}{
		{name: "pid", target: &identity.PID},
		{name: "net", target: &identity.Network},
		{name: "ipc", target: &identity.IPC},
	} {
		var stat syscall.Stat_t
		if err := syscall.Stat(root+"/"+entry.name, &stat); err != nil {
			return Identity{}, fmt.Errorf("inspect %s namespace: %w", entry.name, err)
		}
		entry.target.Device = stat.Dev
		entry.target.Inode = stat.Ino
		if entry.target.Inode == 0 {
			return Identity{}, fmt.Errorf("%s namespace has no identity", entry.name)
		}
	}
	return identity, nil
}

// Args returns the trampoline's fixed identity arguments.
func Args(identity Identity) []string {
	return []string{
		Command,
		strconv.FormatUint(identity.PID.Device, 10), strconv.FormatUint(identity.PID.Inode, 10),
		strconv.FormatUint(identity.Network.Device, 10), strconv.FormatUint(identity.Network.Inode, 10),
		strconv.FormatUint(identity.IPC.Device, 10), strconv.FormatUint(identity.IPC.Inode, 10),
	}
}

// Run proves that all required namespaces differ from the host baseline, then
// replaces the launcher process with the updater while preserving its argv.
func Run(args []string) error {
	if len(args) < 9 || args[0] != Command || args[7] != "--" || args[8] == "" || args[8][0] != '/' {
		return errors.New("invalid phase launcher invocation")
	}
	if os.Geteuid() != 65534 || os.Getegid() != 65534 {
		return errors.New("phase launcher is not running as the restricted service user")
	}
	expected, err := parseIdentity(args[1:7])
	if err != nil {
		return err
	}
	actual, err := CurrentIdentity()
	if err != nil {
		return err
	}
	if err := ValidateIsolation(actual, expected); err != nil {
		return err
	}
	argv := append([]string{args[8]}, args[9:]...)
	// #nosec G204 -- the executable comes from the verified manifest; the remaining arguments are agent-generated protocol arguments.
	if err := syscall.Exec(args[8], argv, nil); err != nil {
		return fmt.Errorf("exec pinned phase entrypoint: %w", err)
	}
	return nil
}

func parseIdentity(values []string) (Identity, error) {
	var identity Identity
	for i, target := range []*NamespaceID{&identity.PID, &identity.Network, &identity.IPC} {
		device, err := strconv.ParseUint(values[i*2], 10, 64)
		if err != nil {
			return Identity{}, errors.New("invalid phase launcher namespace identity")
		}
		inode, err := strconv.ParseUint(values[i*2+1], 10, 64)
		if err != nil || inode == 0 {
			return Identity{}, errors.New("invalid phase launcher namespace identity")
		}
		target.Device, target.Inode = device, inode
	}
	return identity, nil
}

// ValidateIsolation fails if the process shares any required host namespace.
func ValidateIsolation(actual, expected Identity) error {
	if actual.PID == expected.PID || actual.Network == expected.Network || actual.IPC == expected.IPC {
		return errors.New("required phase namespace isolation is not active")
	}
	return nil
}
