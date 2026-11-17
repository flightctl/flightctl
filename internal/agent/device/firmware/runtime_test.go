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
	class   elf.Class
	machine elf.Machine
	order   binary.ByteOrder
	typ     elf.Type
	interp  bool
	dynamic bool
	needed  bool
	noLoad  bool
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
	}
	dataOff := ehsize + phentsize*len(progs)
	total := dataOff
	for _, p := range progs {
		total += len(p.data)
	}

	var buf bytes.Buffer
	ident := [16]byte{0x7f, 'E', 'L', 'F', byte(s.class), 1, 1}
	if s.order == binary.BigEndian {
		ident[5] = 2
	}
	buf.Write(ident[:])
	w := func(v any) { require.NoError(t, binary.Write(&buf, s.order, v)) }
	w(uint16(s.typ))
	w(uint16(s.machine))
	w(uint32(1))
	if is64 {
		w(uint64(0x401000))
		w(uint64(ehsize))
		w(uint64(0))
	} else {
		w(uint32(0x8048000))
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
		if p.typ == elf.PT_LOAD {
			offset, size = 0, uint64(total)
		}
		if is64 {
			w(uint32(p.typ))
			w(uint32(elf.PF_R | elf.PF_X))
			w(offset)
			w(uint64(0x400000) + offset)
			w(uint64(0x400000) + offset)
			w(size)
			w(size)
			w(uint64(0x1000))
		} else {
			w(uint32(p.typ))
			w(uint32(offset))
			w(uint32(0x8048000) + uint32(offset))
			w(uint32(0x8048000) + uint32(offset))
			w(uint32(size))
			w(uint32(size))
			w(uint32(elf.PF_R | elf.PF_X))
			w(uint32(0x1000))
		}
		off += len(p.data)
	}
	for _, p := range progs {
		buf.Write(p.data)
	}
	return buf.Bytes()
}

// pieWithDynamic returns a static position-independent amd64 executable whose
// dynamic segment is the second program header and ends the file.
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
			data:     pieWithDynamic(t)[:len(pieWithDynamic(t))-4],
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
