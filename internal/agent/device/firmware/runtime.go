package firmware

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// maxDynamicSegmentBytes bounds the dynamic segment read from an untrusted
// executable.
const maxDynamicSegmentBytes = 1 << 20

var errNotNativeExecutable = errors.New("updater executable is not a static native binary")

type nativeTarget struct {
	class   elf.Class
	machine elf.Machine
	order   binary.ByteOrder
}

var nativeTargets = map[string]nativeTarget{
	"amd64":   {elf.ELFCLASS64, elf.EM_X86_64, binary.LittleEndian},
	"arm64":   {elf.ELFCLASS64, elf.EM_AARCH64, binary.LittleEndian},
	"arm":     {elf.ELFCLASS32, elf.EM_ARM, binary.LittleEndian},
	"386":     {elf.ELFCLASS32, elf.EM_386, binary.LittleEndian},
	"ppc64le": {elf.ELFCLASS64, elf.EM_PPC64, binary.LittleEndian},
	"riscv64": {elf.ELFCLASS64, elf.EM_RISCV, binary.LittleEndian},
	"s390x":   {elf.ELFCLASS64, elf.EM_S390, binary.BigEndian},
}

// CheckNativeExecutable verifies that r holds a Linux (System V or Linux OS
// ABI) ELF executable for goarch whose entry point lies in the file-backed
// portion of an executable load segment and that runs without a program
// interpreter or shared libraries.
// Static and static position-independent executables are accepted.
func CheckNativeExecutable(r io.ReaderAt, goarch string) error {
	target, ok := nativeTargets[goarch]
	if !ok {
		return fmt.Errorf("%w: unsupported device architecture %q", errNotNativeExecutable, goarch)
	}
	f, err := elf.NewFile(r)
	if err != nil {
		return fmt.Errorf("%w: not a valid ELF file: %w", errNotNativeExecutable, err)
	}
	defer f.Close()
	if f.Class != target.class || f.Machine != target.machine || f.ByteOrder != target.order {
		return fmt.Errorf("%w: built for architecture %s/%s, device needs %s/%s", errNotNativeExecutable, f.Machine, f.Class, target.machine, target.class)
	}
	if f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		return fmt.Errorf("%w: built for OS ABI %s, device needs %s or %s", errNotNativeExecutable, f.OSABI, elf.ELFOSABI_NONE, elf.ELFOSABI_LINUX)
	}
	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return fmt.Errorf("%w: ELF type %s is not an executable", errNotNativeExecutable, f.Type)
	}
	var loadable, entryExecutable, dynamicSeen bool
	for _, p := range f.Progs {
		switch p.Type {
		case elf.PT_LOAD:
			loadable = true
			entryExecutable = entryExecutable || fileBackedEntry(r, p, f.Entry)
		case elf.PT_INTERP:
			return fmt.Errorf("%w: requires a program interpreter", errNotNativeExecutable)
		case elf.PT_DYNAMIC:
			if dynamicSeen {
				return fmt.Errorf("%w: multiple dynamic segments", errNotNativeExecutable)
			}
			dynamicSeen = true
			needs, err := needsSharedLibraries(f, p)
			if err != nil {
				return fmt.Errorf("%w: unreadable dynamic segment: %w", errNotNativeExecutable, err)
			}
			if needs {
				return fmt.Errorf("%w: requires shared libraries", errNotNativeExecutable)
			}
		}
	}
	if !loadable {
		return fmt.Errorf("%w: has no loadable segment", errNotNativeExecutable)
	}
	if !entryExecutable {
		return fmt.Errorf("%w: entry point %#x is not in an executable load segment", errNotNativeExecutable, f.Entry)
	}
	return nil
}

// fileBackedEntry checks both the virtual entry address and whether the
// segment's declared file range exists.
func fileBackedEntry(r io.ReaderAt, p *elf.Prog, entry uint64) bool {
	if p.Flags&elf.PF_X == 0 || entry < p.Vaddr || entry-p.Vaddr >= p.Filesz || p.Filesz > p.Memsz {
		return false
	}
	if p.Off > math.MaxInt64 || p.Filesz-1 > math.MaxInt64-p.Off {
		return false
	}
	var last [1]byte
	_, err := r.ReadAt(last[:], int64(p.Off+p.Filesz-1))
	return err == nil
}

// needsSharedLibraries reports whether the dynamic segment lists a DT_NEEDED
// entry. It reads the segment directly so that stripped section headers
// cannot hide the dependency.
func needsSharedLibraries(f *elf.File, p *elf.Prog) (bool, error) {
	if p.Filesz > maxDynamicSegmentBytes {
		return false, fmt.Errorf("dynamic segment of %d bytes is too large", p.Filesz)
	}
	data := make([]byte, p.Filesz)
	if _, err := io.ReadFull(p.Open(), data); err != nil {
		return false, err
	}
	entry := 16
	if f.Class == elf.ELFCLASS32 {
		entry = 8
	}
	for off := 0; off+entry <= len(data); off += entry {
		var tag elf.DynTag
		if entry == 16 {
			tag = elf.DynTag(int64(f.ByteOrder.Uint64(data[off:])))
		} else {
			tag = elf.DynTag(int32(f.ByteOrder.Uint32(data[off:])))
		}
		switch tag {
		case elf.DT_NULL:
			return false, nil
		case elf.DT_NEEDED:
			return true, nil
		}
	}
	return false, nil
}
