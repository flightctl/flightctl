package quadlet

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/coreos/go-systemd/v22/unit"
)

// ErrKeyNotFound is returned when a requested key is not found in a unit section.
var ErrKeyNotFound = errors.New("key not found")

// ErrSectionNotFound is returned when a requested section is not found in a unit file.
var ErrSectionNotFound = errors.New("section not found")

// Unit represents a systemd unit file with sections and key-value entries.
type Unit struct {
	sections []*unit.UnitSection
}

// NewUnitFromReader creates a new Unit by deserializing systemd unit file data from a reader.
func NewUnitFromReader(reader io.Reader) (*Unit, error) {
	u := &Unit{}
	sec, err := unit.DeserializeSections(reader)
	if err != nil {
		return nil, fmt.Errorf("deserializing sections :%w", err)
	}
	u.sections = sec
	return u, nil
}

// NewUnit creates a new Unit by deserializing systemd unit file data from a byte slice.
func NewUnit(b []byte) (*Unit, error) {
	return NewUnitFromReader(bytes.NewReader(b))
}

// Merge merges another Unit into this Unit, combining sections and entries.
func (u *Unit) Merge(o *Unit) {
	for _, section := range o.sections {
		sec := u.findSection(section.Section)
		if sec == nil {
			u.sections = append(u.sections, section)
		} else {
			sec.Entries = append(sec.Entries, section.Entries...)
		}
	}
}

// Lookup finds the last value for a given key in the specified section.
// Returns ErrSectionNotFound if the section doesn't exist, or ErrKeyNotFound if the key doesn't exist.
func (u *Unit) Lookup(section string, key string) (string, error) {
	sec := u.findSection(section)
	if sec == nil {
		return "", ErrSectionNotFound
	}
	for i := len(sec.Entries) - 1; i >= 0; i-- {
		entry := sec.Entries[i]
		if entry.Name == key {
			return entry.Value, nil
		}
	}
	return "", ErrKeyNotFound
}

// LookupAll finds all values for a given key in the specified section.
// Returns ErrSectionNotFound if the section doesn't exist.
func (u *Unit) LookupAll(section string, key string) ([]string, error) {
	sec := u.findSection(section)
	if sec == nil {
		return nil, ErrSectionNotFound
	}

	var values []string
	for _, entry := range sec.Entries {
		if entry.Name == key {
			values = append(values, entry.Value)
		}
	}
	return values, nil
}

// Write serializes the Unit back to systemd unit file format and returns the byte representation.
func (u *Unit) Write() ([]byte, error) {
	contents, err := io.ReadAll(unit.SerializeSections(u.sections))
	if err != nil {
		return nil, fmt.Errorf("serialzing sections: %w", err)
	}
	return contents, nil
}

// HasSection returns true if the Unit contains the specified section.
func (u *Unit) HasSection(section string) bool {
	return u.findSection(section) != nil
}

func (u *Unit) findSection(name string) *unit.UnitSection {
	for _, section := range u.sections {
		if section.Section == name {
			return section
		}
	}
	return nil
}
