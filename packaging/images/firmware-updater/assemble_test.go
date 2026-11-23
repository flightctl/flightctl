package main

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	catalog "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/stretchr/testify/require"
)

var fixtureSchemaDir = filepath.Join("..", "..", "..", "test", "fixtures", "firmware-updater", "schemas")

var expectedFiles = []string{
	"payload/bin/updater",
	"payload/schemas/checkpoint.schema.json",
	"payload/schemas/config.schema.json",
	"payload/schemas/observation.schema.json",
	"payload/schemas/request.schema.json",
	"payload/schemas/result.schema.json",
}

func bareDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func machineFor(arch string) elf.Machine {
	if arch == "arm64" {
		return elf.EM_AARCH64
	}
	return elf.EM_X86_64
}

// syntheticELF returns a minimal ELF64 little-endian executable whose entry
// point lies in a file-backed executable load segment, optionally with a
// program interpreter or a DT_NEEDED shared-library dependency.
func syntheticELF(t *testing.T, machine elf.Machine, interp, needed bool) []byte {
	t.Helper()
	type segment struct {
		typ   elf.ProgType
		flags elf.ProgFlag
		data  []byte
	}
	segments := []segment{{typ: elf.PT_LOAD, flags: elf.PF_R | elf.PF_X}}
	if interp {
		segments = append(segments, segment{typ: elf.PT_INTERP, flags: elf.PF_R, data: []byte("/lib64/ld-linux-x86-64.so.2\x00")})
	}
	if needed {
		dynamic := make([]byte, 32)
		binary.LittleEndian.PutUint64(dynamic, uint64(elf.DT_NEEDED))
		segments = append(segments, segment{typ: elf.PT_DYNAMIC, flags: elf.PF_R, data: dynamic})
	}
	const base = 0x400000
	headerSize := 64 + 56*len(segments)
	var body bytes.Buffer
	body.Write([]byte{0xc3, 0, 0, 0})
	offsets := make([]int, len(segments))
	for i := 1; i < len(segments); i++ {
		offsets[i] = headerSize + body.Len()
		body.Write(segments[i].data)
	}
	total := headerSize + body.Len()

	var buf bytes.Buffer
	ident := [16]byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)}
	buf.Write(ident[:])
	write := func(values ...any) {
		for _, v := range values {
			require.NoError(t, binary.Write(&buf, binary.LittleEndian, v))
		}
	}
	write(uint16(elf.ET_EXEC), uint16(machine), uint32(elf.EV_CURRENT), uint64(base+headerSize), uint64(64), uint64(0), uint32(0),
		uint16(64), uint16(56), uint16(len(segments)), uint16(64), uint16(0), uint16(0))
	for i, s := range segments {
		offset, size, vaddr, memory := uint64(offsets[i]), uint64(len(s.data)), uint64(base+offsets[i]), uint64(len(s.data))
		if s.typ == elf.PT_LOAD {
			offset, size, vaddr, memory = 0, uint64(total), base, 0x2000
		}
		write(uint32(s.typ), uint32(s.flags), offset, vaddr, vaddr, size, memory, uint64(0x1000))
	}
	buf.Write(body.Bytes())
	return buf.Bytes()
}

func fixtureInput(t *testing.T, arch string) buildInput {
	t.Helper()
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "updater")
	require.NoError(t, os.WriteFile(binaryPath, syntheticELF(t, machineFor(arch), false, false), 0o600))
	input, err := readInput(binaryPath, fixtureSchemaDir, "updater-contract-fixture-"+arch, "0.1.0", arch, strings.Repeat("a", 40))
	require.NoError(t, err)
	return input
}

func sourceSchema(t *testing.T, slot string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureSchemaDir, slot+".schema.json"))
	require.NoError(t, err)
	return data
}

func schemaDocumentFor(bundle *policy.SchemaBundle, slot string) *policy.SchemaDocument {
	switch slot {
	case "config":
		return &bundle.Config
	case "observation":
		return &bundle.Observation
	case "request":
		return &bundle.Request
	case "result":
		return &bundle.Result
	default:
		return &bundle.Checkpoint
	}
}

func manifestSlot(schemas firmware.ManifestSchemas, slot string) string {
	return map[string]string{
		"config": schemas.Config, "observation": schemas.Observation, "request": schemas.Request,
		"result": schemas.Result, "checkpoint": schemas.Checkpoint,
	}[slot]
}

func descriptorFor(inputs descriptorInputs) *policy.ReleaseDescriptor {
	return &policy.ReleaseDescriptor{Name: inputs.Name, Version: inputs.Version, Protocol: inputs.Protocol, Dependencies: inputs.Dependencies, Schemas: inputs.Schemas}
}

func TestAssembleManifestBindsExactPayloadAndSchemaBytes(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run("When the "+arch+" payload is assembled it should list every file once with exact identities", func(t *testing.T) {
			input := fixtureInput(t, arch)
			data, err := assembleManifest(input)
			require.NoError(t, err)
			again, err := assembleManifest(input)
			require.NoError(t, err)
			require.Equal(t, data, again, "identical inputs must produce identical manifest bytes")

			manifest, err := firmware.ParseManifest(data)
			require.NoError(t, err)
			require.Equal(t, firmware.ManifestAPIVersion, manifest.APIVersion)
			require.Equal(t, "updater-contract-fixture-"+arch, manifest.Name)
			require.Equal(t, "0.1.0", manifest.Version)
			require.Equal(t, firmware.SupportedProtocol, manifest.Protocol)
			require.Equal(t, "payload/bin/updater", manifest.Entrypoint)
			require.Empty(t, manifest.Dependencies)
			require.NotContains(t, string(data), strings.Repeat("a", 40), "the source revision stays out of the inner manifest")

			paths := make([]string, 0, len(manifest.Files))
			for _, f := range manifest.Files {
				paths = append(paths, f.Path)
			}
			require.Equal(t, expectedFiles, paths, "files are listed in sorted order without directories or a self entry")
			require.Equal(t, firmware.ManifestFile{Path: "payload/bin/updater", SHA256: bareDigest(input.binary), Size: int64(len(input.binary)), Executable: true}, manifest.Files[0])

			inputs := newDescriptorInputs(input)
			require.NoError(t, firmware.VerifyManifestBinding(manifest, descriptorFor(inputs)))
			for _, slot := range schemaSlots {
				source := sourceSchema(t, slot)
				doc := schemaDocumentFor(&inputs.Schemas, slot)
				require.Equal(t, string(source), doc.Bytes, "descriptor bytes must be the exact %s source bytes", slot)
				require.Equal(t, "sha256:"+bareDigest(source), doc.Digest)
				require.Equal(t, doc.Digest, manifestSlot(manifest.Schemas, slot))
				i := slices.IndexFunc(manifest.Files, func(f firmware.ManifestFile) bool { return f.Path == "payload/schemas/"+slot+".schema.json" })
				require.GreaterOrEqual(t, i, 0)
				require.Equal(t, firmware.ManifestFile{Path: manifest.Files[i].Path, SHA256: bareDigest(source), Size: int64(len(source)), Executable: false}, manifest.Files[i])
				require.Equal(t, strings.TrimPrefix(doc.Digest, "sha256:"), manifest.Files[i].SHA256, "slot digest and file hash name the same bytes")
			}
		})
	}
}

func TestDescriptorInputsPassTheUpdaterCatalog(t *testing.T) {
	inputs := newDescriptorInputs(fixtureInput(t, "amd64"))
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	release := &cfg.Releases[0]
	release.Name, release.Version, release.Protocol = inputs.Name, inputs.Version, inputs.Protocol
	for _, slot := range schemaSlots {
		doc := schemaDocumentFor(&inputs.Schemas, slot)
		target := map[string]*catalog.SchemaDocument{
			"config": &release.Schemas.Config, "observation": &release.Schemas.Observation, "request": &release.Schemas.Request,
			"result": &release.Schemas.Result, "checkpoint": &release.Schemas.Checkpoint,
		}[slot]
		*target = catalog.SchemaDocument{Digest: doc.Digest, Bytes: doc.Bytes}
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	_, err = catalog.ParseAdministratorConfig(data)
	require.NoError(t, err, "When the descriptor inputs carry the closed fixture schemas the catalog should accept them")
}

func TestAssembleManifestRejectsUnsupportedInputs(t *testing.T) {
	testCases := []struct {
		name    string
		arch    string
		modify  func(t *testing.T, input *buildInput)
		wantErr string
	}{
		{name: "When the executable targets another architecture it should refuse it", arch: "amd64",
			modify:  func(t *testing.T, in *buildInput) { in.binary = syntheticELF(t, elf.EM_AARCH64, false, false) },
			wantErr: "static native binary"},
		{name: "When the executable needs a program interpreter it should refuse it", arch: "amd64",
			modify:  func(t *testing.T, in *buildInput) { in.binary = syntheticELF(t, elf.EM_X86_64, true, false) },
			wantErr: "program interpreter"},
		{name: "When the executable needs shared libraries it should refuse it", arch: "arm64",
			modify:  func(t *testing.T, in *buildInput) { in.binary = syntheticELF(t, elf.EM_AARCH64, false, true) },
			wantErr: "shared libraries"},
		{name: "When the executable is not ELF it should refuse it", arch: "amd64",
			modify: func(_ *testing.T, in *buildInput) { in.binary = []byte("#!/bin/sh\nexit 0\n") }, wantErr: "static native binary"},
		{name: "When the platform is unsupported it should refuse it", arch: "amd64",
			modify: func(_ *testing.T, in *buildInput) { in.arch = "riscv64" }, wantErr: "unsupported platform"},
		{name: "When a schema slot is missing it should refuse it", arch: "amd64",
			modify: func(_ *testing.T, in *buildInput) { delete(in.schemas, "checkpoint") }, wantErr: "checkpoint schema"},
		{name: "When an extra schema slot is supplied it should refuse it", arch: "amd64",
			modify: func(_ *testing.T, in *buildInput) { in.schemas["extra"] = []byte(`{"type":"object"}`) }, wantErr: "unexpected schema"},
		{name: "When a schema is not JSON it should refuse it", arch: "amd64",
			modify: func(_ *testing.T, in *buildInput) { in.schemas["result"] = []byte("{") }, wantErr: "result schema"},
		{name: "When the release name is malformed it should refuse it", arch: "amd64",
			modify: func(_ *testing.T, in *buildInput) { in.name = "../fixture" }, wantErr: "release name"},
		{name: "When the release version is empty it should refuse it", arch: "amd64",
			modify: func(_ *testing.T, in *buildInput) { in.version = "" }, wantErr: "release version"},
		{name: "When a schema is not valid UTF-8 it should refuse it", arch: "amd64",
			modify: func(_ *testing.T, in *buildInput) { in.schemas["config"] = []byte("{\"title\":\"\xe9\"}") }, wantErr: "config schema"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			input := fixtureInput(t, tc.arch)
			tc.modify(t, &input)
			_, err := assembleManifest(input)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func stageFixture(t *testing.T, arch string) (string, buildInput, descriptorInputs) {
	t.Helper()
	input := fixtureInput(t, arch)
	manifest, err := assembleManifest(input)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "context")
	require.NoError(t, stage(dir, input, manifest))
	return dir, input, newDescriptorInputs(input)
}

func rewriteManifest(t *testing.T, dir string, edit func(*firmware.Manifest)) {
	t.Helper()
	path := filepath.Join(dir, "updater", "manifest.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var m firmware.Manifest
	require.NoError(t, json.Unmarshal(data, &m))
	edit(&m)
	data, err = json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, os.Chmod(path, 0o644))
}

func setFile(m *firmware.Manifest, path string, data []byte) {
	i := slices.IndexFunc(m.Files, func(f firmware.ManifestFile) bool { return f.Path == path })
	m.Files[i].SHA256, m.Files[i].Size = bareDigest(data), int64(len(data))
}

func TestStageWritesTheDeliveryTreeWithExplicitModes(t *testing.T) {
	input := fixtureInput(t, "amd64")
	manifest, err := assembleManifest(input)
	require.NoError(t, err)
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, fs.ModeSetgid|0o755))
	info, err := os.Stat(parent)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSetgid, "the parent directory must propagate setgid for this check")
	dir := filepath.Join(parent, "context")
	previous := syscall.Umask(0o077)
	err = stage(dir, input, manifest)
	syscall.Umask(previous)
	require.NoError(t, err, "When the parent is setgid and the umask is restrictive it should still stage explicit modes")
	inputs := newDescriptorInputs(input)
	require.NoError(t, verifyStaged(dir, inputs, "amd64"))
	modes := map[string]fs.FileMode{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		info, err := d.Info()
		require.NoError(t, err)
		rel, err := filepath.Rel(dir, path)
		require.NoError(t, err)
		modes[rel] = info.Mode()
		return nil
	}))
	want := map[string]fs.FileMode{
		".": fs.ModeDir | 0o755, "updater": fs.ModeDir | 0o755, "updater/manifest.json": 0o644,
		"updater/payload": fs.ModeDir | 0o755, "updater/payload/bin": fs.ModeDir | 0o755, "updater/payload/schemas": fs.ModeDir | 0o755,
		"updater/payload/bin/updater": 0o755,
	}
	for _, slot := range schemaSlots {
		want["updater/payload/schemas/"+slot+".schema.json"] = 0o644
	}
	require.Equal(t, want, modes)
	staged, err := os.ReadFile(filepath.Join(dir, "updater", "payload", "bin", "updater"))
	require.NoError(t, err)
	require.Equal(t, input.binary, staged)
	require.Error(t, stage(dir, input, nil), "When the context directory is not empty it should refuse to stage")
}

func TestVerifyStagedRejectsMismatchedTrees(t *testing.T) {
	schemaPath := func(dir, slot string) string {
		return filepath.Join(dir, "updater", "payload", "schemas", slot+".schema.json")
	}
	testCases := []struct {
		name    string
		modify  func(t *testing.T, dir string, inputs *descriptorInputs)
		wantErr string
	}{
		{name: "When a schema file gains only whitespace it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				data, err := os.ReadFile(schemaPath(dir, "config"))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(schemaPath(dir, "config"), append(data, ' '), 0o600))
				require.NoError(t, os.Chmod(schemaPath(dir, "config"), 0o644))
			}, wantErr: "differs from its manifest entry"},
		{name: "When a listed file is missing it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				require.NoError(t, os.Remove(schemaPath(dir, "result")))
			},
			wantErr: "missing"},
		{name: "When an unlisted file is present it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				extra := filepath.Join(dir, "updater", "payload", "notes.txt")
				require.NoError(t, os.WriteFile(extra, []byte("notes"), 0o600))
				require.NoError(t, os.Chmod(extra, 0o644))
			}, wantErr: "not listed"},
		{name: "When the executable loses its executable mode it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				require.NoError(t, os.Chmod(filepath.Join(dir, "updater", "payload", "bin", "updater"), 0o644))
			}, wantErr: "mode"},
		{name: "When a schema file is executable it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				require.NoError(t, os.Chmod(schemaPath(dir, "request"), 0o755))
			},
			wantErr: "mode"},
		{name: "When a payload entry is a symbolic link it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				require.NoError(t, os.Remove(schemaPath(dir, "observation")))
				require.NoError(t, os.Symlink("config.schema.json", schemaPath(dir, "observation")))
			}, wantErr: "regular file"},
		{name: "When the executable targets another architecture with a matching manifest entry it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				other := syntheticELF(t, elf.EM_AARCH64, false, false)
				path := filepath.Join(dir, "updater", "payload", "bin", "updater")
				require.NoError(t, os.WriteFile(path, other, 0o600))
				require.NoError(t, os.Chmod(path, 0o755))
				rewriteManifest(t, dir, func(m *firmware.Manifest) { setFile(m, "payload/bin/updater", other) })
			}, wantErr: "static native binary"},
		{name: "When the manifest declares a runtime dependency it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				rewriteManifest(t, dir, func(m *firmware.Manifest) {
					m.Dependencies = []policy.RuntimeDependency{{Name: "openssl", Constraint: ">=3"}}
				})
			}, wantErr: "runtime dependencies"},
		{name: "When the descriptor declares a runtime dependency it should refuse the tree",
			modify: func(_ *testing.T, _ string, inputs *descriptorInputs) {
				inputs.Dependencies = []policy.RuntimeDependency{{Name: "openssl", Constraint: ">=3"}}
			}, wantErr: "dependency-free"},
		{name: "When a schema file and its file hash agree but the slot names other bytes it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				changed := []byte(`{"type":"object","additionalProperties":false,"properties":{}}` + "\n")
				require.NoError(t, os.WriteFile(schemaPath(dir, "checkpoint"), changed, 0o600))
				require.NoError(t, os.Chmod(schemaPath(dir, "checkpoint"), 0o644))
				rewriteManifest(t, dir, func(m *firmware.Manifest) { setFile(m, "payload/schemas/checkpoint.schema.json", changed) })
			}, wantErr: "checkpoint schema file differs from the descriptor bytes"},
		{name: "When the descriptor names other schema bytes than the packaged slot it should refuse the tree",
			modify: func(_ *testing.T, _ string, inputs *descriptorInputs) {
				changed := `{"type":"object","additionalProperties":false,"properties":{}}`
				inputs.Schemas.Observation = policy.SchemaDocument{Digest: "sha256:" + bareDigest([]byte(changed)), Bytes: changed}
			}, wantErr: "observation schema digest differs"},
		{name: "When an unlisted empty directory is present it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				empty := filepath.Join(dir, "updater", "payload", "empty")
				require.NoError(t, os.Mkdir(empty, 0o700))
				require.NoError(t, os.Chmod(empty, 0o755))
			}, wantErr: "unexpected directory"},
		{name: "When a payload directory is setgid it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				require.NoError(t, os.Chmod(filepath.Join(dir, "updater", "payload", "bin"), fs.ModeSetgid|0o755))
			}, wantErr: "mode"},
		{name: "When the manifest is not world-readable it should refuse the tree",
			modify: func(t *testing.T, dir string, _ *descriptorInputs) {
				require.NoError(t, os.Chmod(filepath.Join(dir, "updater", "manifest.json"), 0o600))
			}, wantErr: "mode"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, inputs := stageFixture(t, "amd64")
			require.NoError(t, verifyStaged(dir, inputs, "amd64"))
			tc.modify(t, dir, &inputs)
			require.ErrorContains(t, verifyStaged(dir, inputs, "amd64"), tc.wantErr)
		})
	}
}

func TestReportRecordsSeparateIdentities(t *testing.T) {
	input := fixtureInput(t, "arm64")
	manifest, err := assembleManifest(input)
	require.NoError(t, err)
	got := newReport(input, manifest)
	require.Equal(t, "linux/arm64", got.Platform)
	require.Equal(t, strings.Repeat("a", 40), got.SourceRevision)
	require.Equal(t, "sha256:"+bareDigest(manifest), got.ManifestDigest)
	require.Len(t, got.Files, len(expectedFiles))
	require.Equal(t, reportFile{Path: "payload/bin/updater", SHA256: bareDigest(input.binary), Size: int64(len(input.binary)), Mode: "0755"}, got.Files[0])
	for _, slot := range schemaSlots {
		require.Equal(t, "sha256:"+bareDigest(sourceSchema(t, slot)), got.Schemas[slot])
	}
	data, err := json.Marshal(got)
	require.NoError(t, err)
	again, err := json.Marshal(newReport(input, manifest))
	require.NoError(t, err)
	require.Equal(t, data, again)
}

func TestReadInputRequiresEverySchemaFile(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "updater")
	require.NoError(t, os.WriteFile(binaryPath, syntheticELF(t, elf.EM_X86_64, false, false), 0o600))
	schemas := filepath.Join(dir, "schemas")
	require.NoError(t, os.Mkdir(schemas, 0o700))
	for _, slot := range schemaSlots[:4] {
		require.NoError(t, os.WriteFile(filepath.Join(schemas, slot+".schema.json"), sourceSchema(t, slot), 0o600))
	}
	_, err := readInput(binaryPath, schemas, "updater-contract-fixture-amd64", "0.1.0", "amd64", "HEAD")
	require.ErrorContains(t, err, "checkpoint.schema.json")
}
