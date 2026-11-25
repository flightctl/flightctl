//go:build !linux

package phasebootstrap

import "errors"

const Command = "--internal-phase-launcher"

type NamespaceID struct {
	Device uint64
	Inode  uint64
}

type Identity struct {
	PID     NamespaceID
	Network NamespaceID
	IPC     NamespaceID
}

func CurrentIdentity() (Identity, error) {
	return Identity{}, errors.New("Linux namespaces are unavailable")
}
func InitIdentity() (Identity, error) {
	return Identity{}, errors.New("Linux namespaces are unavailable")
}
func Args(Identity) []string { return nil }
func Run([]string) error     { return errors.New("Linux namespaces are unavailable") }
func ValidateIsolation(Identity, Identity) error {
	return errors.New("Linux namespaces are unavailable")
}
