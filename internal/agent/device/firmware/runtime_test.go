package firmware_test

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	"github.com/stretchr/testify/require"
)

type elfSpec struct {
	class            elf.Class
	machine          elf.Machine
	order            binary.ByteOrder
	typ              elf.Type
	interp           bool
	dynamic          bool
	duplicateDynamic bool
	needed           bool
	noLoad           bool
	osabi            elf.OSABI
	// entry, loadFlags, loadVaddr and loadMemsz override the defaults, under
	// which the single load segment is readable and executable and contains
	// the entry point.
	entry     uint64
	loadFlags elf.ProgFlag
	loadVaddr uint64
	loadMemsz uint64
}

func amd64Static() elfSpec {
	return elfSpec{class: elf.ELFCLASS64, machine: elf.EM_X86_64, order: binary.LittleEndian, typ: elf.ET_EXEC}
}

// buildELF writes a minimal ELF image with program headers only.
func buildELF(t *testing.T, s elfSpec) []byte {
	t.Helper()
	is64 := s.class == elf.ELFCLASS64
	ehsize, phentsize := 52, 32
	if is64 {
		ehsize, phentsize = 64, 56
	}
	type prog struct {
		typ  elf.ProgType
		data []byte
	}
	var progs []prog
	if !s.noLoad {
		progs = append(progs, prog{typ: elf.PT_LOAD})
	}
	if s.interp {
		progs = append(progs, prog{typ: elf.PT_INTERP, data: []byte("/lib/ld-linux.so.2\x00")})
	}
	if s.dynamic || s.needed {
		var dyn bytes.Buffer
		put := func(tag elf.DynTag, val uint64) {
			if is64 {
				require.NoError(t, binary.Write(&dyn, s.order, int64(tag)))
				require.NoError(t, binary.Write(&dyn, s.order, val))
			} else {
				require.NoError(t, binary.Write(&dyn, s.order, int32(tag)))
				require.NoError(t, binary.Write(&dyn, s.order, uint32(val)))
			}
		}
		put(elf.DT_FLAGS_1, uint64(elf.DF_1_PIE))
		if s.needed {
			put(elf.DT_NEEDED, 1)
		}
		put(elf.DT_NULL, 0)
		progs = append(progs, prog{typ: elf.PT_DYNAMIC, data: dyn.Bytes()})
		if s.duplicateDynamic {
			progs = append(progs, progs[len(progs)-1])
		}
	}
	dataOff := ehsize + phentsize*len(progs)
	total := dataOff + 4
	for _, p := range progs {
		total += len(p.data)
	}

	var buf bytes.Buffer
	ident := [16]byte{0x7f, 'E', 'L', 'F', byte(s.class), 1, 1, byte(s.osabi)}
	if s.order == binary.BigEndian {
		ident[5] = 2
	}
	buf.Write(ident[:])
	w := func(v any) { require.NoError(t, binary.Write(&buf, s.order, v)) }
	w(uint16(s.typ))
	w(uint16(s.machine))
	w(uint32(1))
	base := uint64(0x400000)
	if !is64 {
		base = 0x8048000
	}
	entry := base + uint64(total-4)
	if s.entry != 0 {
		entry = s.entry
	}
	if is64 {
		w(entry)
		w(uint64(ehsize))
		w(uint64(0))
	} else {
		w(uint32(entry))
		w(uint32(ehsize))
		w(uint32(0))
	}
	w(uint32(0))
	w(uint16(ehsize))
	w(uint16(phentsize))
	w(uint16(len(progs)))
	if is64 {
		w(uint16(64))
	} else {
		w(uint16(40))
	}
	w(uint16(0))
	w(uint16(0))

	off := dataOff
	for _, p := range progs {
		offset, size := uint64(off), uint64(len(p.data))
		flags, vaddr, memsz := elf.PF_R|elf.PF_X, base+offset, size
		if p.typ == elf.PT_LOAD {
			offset, size, vaddr, memsz = 0, uint64(total), base, max(uint64(total), 0x2000)
			if s.loadFlags != 0 {
				flags = s.loadFlags
			}
			if s.loadVaddr != 0 {
				vaddr = s.loadVaddr
			}
			if s.loadMemsz != 0 {
				memsz = s.loadMemsz
			}
		}
		if is64 {
			w(uint32(p.typ))
			w(uint32(flags))
			w(offset)
			w(vaddr)
			w(vaddr)
			w(size)
			w(memsz)
			w(uint64(0x1000))
		} else {
			w(uint32(p.typ))
			w(uint32(offset))
			w(uint32(vaddr))
			w(uint32(vaddr))
			w(uint32(size))
			w(uint32(memsz))
			w(uint32(flags))
			w(uint32(0x1000))
		}
		off += len(p.data)
	}
	for _, p := range progs {
		buf.Write(p.data)
	}
	buf.Write([]byte{0xc3, 0, 0, 0})
	return buf.Bytes()
}

// pieWithDynamic returns a static position-independent amd64 executable whose
// dynamic segment is the second program header and precedes the entry bytes.
func pieWithDynamic(t *testing.T) []byte {
	s := amd64Static()
	s.typ = elf.ET_DYN
	s.dynamic = true
	return buildELF(t, s)
}

// withDynamicFileSize rewrites the file size of the dynamic program header of
// a pieWithDynamic image.
func withDynamicFileSize(t *testing.T, data []byte, size uint64) []byte {
	t.Helper()
	const ehsize, phentsize, fileszOffset = 64, 56, 32
	out := append([]byte(nil), data...)
	at := ehsize + phentsize + fileszOffset
	require.Equal(t, uint32(elf.PT_DYNAMIC), binary.LittleEndian.Uint32(out[ehsize+phentsize:]))
	binary.LittleEndian.PutUint64(out[at:], size)
	return out
}

func TestCheckNativeExecutable(t *testing.T) {
	valid := buildELF(t, amd64Static())
	testCases := []struct {
		name     string
		data     []byte
		goarch   string
		wantErr  bool
		contains string
	}{
		{name: "When the executable is a static binary for the architecture it should accept it", data: valid, goarch: "amd64"},
		{name: "When the entry point is in zero-fill it should reject it", data: buildELF(t, func() elfSpec { s := amd64Static(); s.entry = 0x401000; return s }()), goarch: "amd64", wantErr: true, contains: "entry point"},
		{name: "When the load segment is truncated it should reject it", data: valid[:len(valid)-1], goarch: "amd64", wantErr: true, contains: "entry point"},
		{name: "When the executable has multiple dynamic segments it should reject it", data: buildELF(t, func() elfSpec { s := amd64Static(); s.dynamic = true; s.duplicateDynamic = true; return s }()), goarch: "amd64", wantErr: true, contains: "multiple dynamic segments"},
		{
			name:   "When the executable is a static position-independent binary it should accept it",
			data:   buildELF(t, func() elfSpec { s := amd64Static(); s.typ = elf.ET_DYN; s.dynamic = true; return s }()),
			goarch: "amd64",
		},
		{
			name:   "When the executable is a static 32-bit ARM binary it should accept it for arm",
			data:   buildELF(t, elfSpec{class: elf.ELFCLASS32, machine: elf.EM_ARM, order: binary.LittleEndian, typ: elf.ET_EXEC}),
			goarch: "arm",
		},
		{
			name:   "When the executable is a static big-endian s390x binary it should accept it for s390x",
			data:   buildELF(t, elfSpec{class: elf.ELFCLASS64, machine: elf.EM_S390, order: binary.BigEndian, typ: elf.ET_EXEC}),
			goarch: "s390x",
		},
		{
			name:     "When the executable needs a program interpreter it should reject it",
			data:     buildELF(t, func() elfSpec { s := amd64Static(); s.interp = true; return s }()),
			goarch:   "amd64",
			wantErr:  true,
			contains: "interpreter",
		},
		{
			name:     "When the executable needs shared libraries it should reject it",
			data:     buildELF(t, func() elfSpec { s := amd64Static(); s.typ = elf.ET_DYN; s.needed = true; return s }()),
			goarch:   "amd64",
			wantErr:  true,
			contains: "shared libraries",
		},
		{
			name:     "When the executable is built for another machine it should reject it",
			data:     buildELF(t, elfSpec{class: elf.ELFCLASS64, machine: elf.EM_AARCH64, order: binary.LittleEndian, typ: elf.ET_EXEC}),
			goarch:   "amd64",
			wantErr:  true,
			contains: "architecture",
		},
		{
			name:     "When the executable has the wrong class it should reject it",
			data:     buildELF(t, elfSpec{class: elf.ELFCLASS32, machine: elf.EM_X86_64, order: binary.LittleEndian, typ: elf.ET_EXEC}),
			goarch:   "amd64",
			wantErr:  true,
			contains: "architecture",
		},
		{
			name:     "When the file is a relocatable object it should reject it",
			data:     buildELF(t, func() elfSpec { s := amd64Static(); s.typ = elf.ET_REL; return s }()),
			goarch:   "amd64",
			wantErr:  true,
			contains: "not an executable",
		},
		{
			name:     "When the executable has no loadable segment it should reject it",
			data:     buildELF(t, func() elfSpec { s := amd64Static(); s.noLoad = true; return s }()),
			goarch:   "amd64",
			wantErr:  true,
			contains: "loadable",
		},
		{
			name:   "When a 32-bit ARM executable is static and position independent it should accept it",
			data:   buildELF(t, elfSpec{class: elf.ELFCLASS32, machine: elf.EM_ARM, order: binary.LittleEndian, typ: elf.ET_DYN, dynamic: true}),
			goarch: "arm",
		},
		{
			name:     "When a 32-bit ARM executable needs shared libraries it should reject it",
			data:     buildELF(t, elfSpec{class: elf.ELFCLASS32, machine: elf.EM_ARM, order: binary.LittleEndian, typ: elf.ET_DYN, needed: true}),
			goarch:   "arm",
			wantErr:  true,
			contains: "shared libraries",
		},
		{
			name:     "When the dynamic segment is cut short it should reject it",
			data:     pieWithDynamic(t)[:len(pieWithDynamic(t))-8],
			goarch:   "amd64",
			wantErr:  true,
			contains: "unreadable dynamic segment",
		},
		{
			name:     "When the dynamic segment claims more bytes than the bound it should reject it without reading it",
			data:     withDynamicFileSize(t, pieWithDynamic(t), 2<<20),
			goarch:   "amd64",
			wantErr:  true,
			contains: "too large",
		},
		{
			name:   "When the executable declares the Linux OS ABI it should accept it",
			data:   buildELF(t, func() elfSpec { s := amd64Static(); s.osabi = elf.ELFOSABI_LINUX; return s }()),
			goarch: "amd64",
		},
		{
			name:     "When the executable declares a foreign OS ABI it should reject it",
			data:     buildELF(t, func() elfSpec { s := amd64Static(); s.osabi = elf.ELFOSABI_FREEBSD; return s }()),
			goarch:   "amd64",
			wantErr:  true,
			contains: "OS ABI",
		},
		{
			name:     "When a 32-bit ARM executable declares a foreign OS ABI it should reject it",
			data:     buildELF(t, elfSpec{class: elf.ELFCLASS32, machine: elf.EM_ARM, order: binary.LittleEndian, typ: elf.ET_EXEC, osabi: elf.ELFOSABI_NETBSD}),
			goarch:   "arm",
			wantErr:  true,
			contains: "OS ABI",
		},
		{
			name:     "When the entry point lies in a load segment that is not executable it should reject it",
			data:     buildELF(t, func() elfSpec { s := amd64Static(); s.loadFlags = elf.PF_R; return s }()),
			goarch:   "amd64",
			wantErr:  true,
			contains: "entry point",
		},
		{
			name:     "When the entry point lies outside every load segment it should reject it",
			data:     buildELF(t, func() elfSpec { s := amd64Static(); s.entry = 0x900000; return s }()),
			goarch:   "amd64",
			wantErr:  true,
			contains: "entry point",
		},
		{
			name:     "When the entry point is just past the end of the executable load segment it should reject it",
			data:     buildELF(t, func() elfSpec { s := amd64Static(); s.entry = 0x400000 + 0x2000; return s }()),
			goarch:   "amd64",
			wantErr:  true,
			contains: "entry point",
		},
		{
			name:   "When the entry point is the last byte of the executable load segment it should accept it",
			data:   buildELF(t, func() elfSpec { s := amd64Static(); s.entry = 0x400000 + uint64(len(valid)) - 1; return s }()),
			goarch: "amd64",
		},
		{
			name: "When an executable load segment wraps around the address space it should reject an entry point below its start",
			data: buildELF(t, func() elfSpec {
				s := amd64Static()
				s.loadVaddr, s.loadMemsz, s.entry = 0xFFFFFFFFFFFFF000, 0x2000, 0x10
				return s
			}()),
			goarch:   "amd64",
			wantErr:  true,
			contains: "entry point",
		},
		{name: "When the ELF file is truncated it should reject it", data: valid[:40], goarch: "amd64", wantErr: true, contains: "ELF"},
		{name: "When the file is a script it should reject it", data: []byte("#!/bin/sh\necho hi\n"), goarch: "amd64", wantErr: true, contains: "ELF"},
		{name: "When the file is empty it should reject it", data: nil, goarch: "amd64", wantErr: true, contains: "ELF"},
		{name: "When the architecture is not supported it should reject it", data: valid, goarch: "mips", wantErr: true, contains: "architecture"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := firmware.CheckNativeExecutable(bytes.NewReader(tc.data), tc.goarch)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, tc.contains)
		})
	}
}
