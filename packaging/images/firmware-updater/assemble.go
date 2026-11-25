// Command firmware-updater stages the delivery tree of the updater contract
// fixture: the native executable, the five exact schema files and the closed
// updater manifest that binds them. It also writes the release descriptor
// inputs, which carry the same schema bytes, and a digest report.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
)

const (
	entrypoint     = "payload/bin/updater"
	executableMode = fs.FileMode(0o755)
	fileMode       = fs.FileMode(0o644)
	dirMode        = fs.FileMode(0o755)
)

var (
	schemaSlots = []string{"config", "observation", "request", "result", "checkpoint"}
	platforms   = map[string]bool{"amd64": true, "arm64": true}
	namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

type buildInput struct {
	binary   []byte
	schemas  map[string][]byte
	name     string
	version  string
	arch     string
	revision string
}

// descriptorInputs holds the release descriptor fields that come from the
// build; the publisher, package digest, access and limits are added when a
// release is registered.
type descriptorInputs struct {
	Name         string                     `json:"name"`
	Version      string                     `json:"version"`
	Protocol     int                        `json:"protocol"`
	Dependencies []policy.RuntimeDependency `json:"dependencies"`
	Schemas      policy.SchemaBundle        `json:"schemas"`
}

type reportFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
}

type report struct {
	Name           string            `json:"name"`
	Version        string            `json:"version"`
	Platform       string            `json:"platform"`
	SourceRevision string            `json:"sourceRevision"`
	ManifestDigest string            `json:"manifestDigest"`
	Schemas        map[string]string `json:"schemas"`
	Files          []reportFile      `json:"files"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "firmware-updater: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("firmware-updater", flag.ContinueOnError)
	binaryPath := flags.String("binary", "", "native updater executable")
	schemaDir := flags.String("schemas", "", "directory holding <slot>.schema.json for each schema slot")
	name := flags.String("name", "", "release name")
	version := flags.String("version", "", "release version")
	arch := flags.String("arch", "", "target architecture: amd64 or arm64")
	revision := flags.String("revision", "", "full source commit")
	contextDir := flags.String("context", "", "empty directory that receives the staged image context")
	descriptorPath := flags.String("descriptor", "", "output file for the release descriptor inputs")
	reportPath := flags.String("report", "", "output file for the digest report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	for _, value := range []string{*binaryPath, *schemaDir, *name, *version, *arch, *revision, *contextDir, *descriptorPath, *reportPath} {
		if value == "" || flags.NArg() != 0 {
			return errors.New("every flag is required and no positional arguments are accepted")
		}
	}
	input, err := readInput(*binaryPath, *schemaDir, *name, *version, *arch, *revision)
	if err != nil {
		return err
	}
	manifest, err := assembleManifest(input)
	if err != nil {
		return err
	}
	if err := stage(*contextDir, input, manifest); err != nil {
		return fmt.Errorf("stage context: %w", err)
	}
	inputs := newDescriptorInputs(input)
	if err := verifyStaged(*contextDir, inputs, input.arch); err != nil {
		return fmt.Errorf("staged context failed verification: %w", err)
	}
	if err := writeJSON(*descriptorPath, inputs); err != nil {
		return err
	}
	return writeJSON(*reportPath, newReport(input, manifest))
}

func readInput(binaryPath, schemaDir, name, version, arch, revision string) (buildInput, error) {
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return buildInput{}, fmt.Errorf("read executable: %w", err)
	}
	schemas := make(map[string][]byte, len(schemaSlots))
	for _, slot := range schemaSlots {
		data, err := os.ReadFile(filepath.Join(schemaDir, slot+".schema.json"))
		if err != nil {
			return buildInput{}, fmt.Errorf("read %s.schema.json: %w", slot, err)
		}
		schemas[slot] = data
	}
	return buildInput{binary: binary, schemas: schemas, name: name, version: version, arch: arch, revision: revision}, nil
}

func bareHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func digest(data []byte) string {
	return "sha256:" + bareHash(data)
}

func schemaPath(slot string) string {
	return "payload/schemas/" + slot + ".schema.json"
}

func (in buildInput) validate() error {
	if !namePattern.MatchString(in.name) {
		return fmt.Errorf("invalid release name %q", in.name)
	}
	if !namePattern.MatchString(in.version) {
		return fmt.Errorf("invalid release version %q", in.version)
	}
	if !platforms[in.arch] {
		return fmt.Errorf("unsupported platform linux/%s", in.arch)
	}
	if err := firmware.CheckNativeExecutable(bytes.NewReader(in.binary), in.arch); err != nil {
		return err
	}
	for slot := range in.schemas {
		if !slices.Contains(schemaSlots, slot) {
			return fmt.Errorf("unexpected schema slot %q", slot)
		}
	}
	for _, slot := range schemaSlots {
		if data, ok := in.schemas[slot]; !ok || !utf8.Valid(data) || !json.Valid(data) {
			return fmt.Errorf("%s schema must be present and contain valid UTF-8 JSON", slot)
		}
	}
	return nil
}

// manifestFiles lists every payload file once, sorted by path.
func manifestFiles(in buildInput) []firmware.ManifestFile {
	files := []firmware.ManifestFile{{Path: entrypoint, SHA256: bareHash(in.binary), Size: int64(len(in.binary)), Executable: true}}
	for _, slot := range schemaSlots {
		data := in.schemas[slot]
		files = append(files, firmware.ManifestFile{Path: schemaPath(slot), SHA256: bareHash(data), Size: int64(len(data))})
	}
	slices.SortFunc(files, func(a, b firmware.ManifestFile) int { return strings.Compare(a.Path, b.Path) })
	return files
}

// assembleManifest returns the closed updater manifest for input. The same
// input always yields the same bytes.
func assembleManifest(input buildInput) ([]byte, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	m := firmware.Manifest{
		APIVersion:   firmware.ManifestAPIVersion,
		Name:         input.name,
		Version:      input.version,
		Protocol:     firmware.SupportedProtocol,
		Entrypoint:   entrypoint,
		Dependencies: []policy.RuntimeDependency{},
		Schemas: firmware.ManifestSchemas{
			Config:      digest(input.schemas["config"]),
			Observation: digest(input.schemas["observation"]),
			Request:     digest(input.schemas["request"]),
			Result:      digest(input.schemas["result"]),
			Checkpoint:  digest(input.schemas["checkpoint"]),
		},
		Files: manifestFiles(input),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	data = append(data, '\n')
	if _, err := firmware.ParseManifest(data); err != nil {
		return nil, fmt.Errorf("assembled manifest is invalid: %w", err)
	}
	return data, nil
}

func newDescriptorInputs(input buildInput) descriptorInputs {
	document := func(slot string) policy.SchemaDocument {
		data := input.schemas[slot]
		return policy.SchemaDocument{Digest: digest(data), Bytes: string(data)}
	}
	return descriptorInputs{
		Name:         input.name,
		Version:      input.version,
		Protocol:     firmware.SupportedProtocol,
		Dependencies: []policy.RuntimeDependency{},
		Schemas: policy.SchemaBundle{
			Config: document("config"), Observation: document("observation"), Request: document("request"),
			Result: document("result"), Checkpoint: document("checkpoint"),
		},
	}
}

type stagedFile struct {
	data []byte
	mode fs.FileMode
}

// stage writes the image context into dir, which must be empty or absent. Each
// directory and file gets an explicit mode instead of inheriting the umask or
// a parent's setgid bit.
func stage(dir string, input buildInput, manifest []byte) error {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("context directory %s is not empty", dir)
	}
	files := map[string]stagedFile{"manifest.json": {manifest, fileMode}, entrypoint: {input.binary, executableMode}}
	for _, slot := range schemaSlots {
		files[schemaPath(slot)] = stagedFile{input.schemas[slot], fileMode}
	}
	for _, rel := range []string{"", "updater", "updater/payload", "updater/payload/bin", "updater/payload/schemas"} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(path, dirMode); err != nil {
			return err
		}
		if err := os.Chmod(path, dirMode); err != nil {
			return err
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(dir, "updater", filepath.FromSlash(rel))
		if err := os.WriteFile(path, files[rel].data, 0o600); err != nil {
			return err
		}
		if err := os.Chmod(path, files[rel].mode); err != nil {
			return err
		}
	}
	return nil
}

func checkMode(rel string, info fs.FileInfo, want fs.FileMode) error {
	if info.Mode() != want {
		return fmt.Errorf("%s has mode %s, want %s", rel, info.Mode(), want)
	}
	return nil
}

func slotDocument(bundle policy.SchemaBundle, slot string) policy.SchemaDocument {
	return map[string]policy.SchemaDocument{
		"config": bundle.Config, "observation": bundle.Observation, "request": bundle.Request,
		"result": bundle.Result, "checkpoint": bundle.Checkpoint,
	}[slot]
}

func manifestSchema(schemas firmware.ManifestSchemas, slot string) string {
	return map[string]string{
		"config": schemas.Config, "observation": schemas.Observation, "request": schemas.Request,
		"result": schemas.Result, "checkpoint": schemas.Checkpoint,
	}[slot]
}

// verifyStaged checks a staged context against its manifest and the release
// descriptor inputs: the consumer's manifest and binding checks, an exact file
// list with sizes, hashes and modes, a static native entrypoint for arch, and
// each schema slot's file equal to the descriptor bytes for that slot.
func verifyStaged(dir string, inputs descriptorInputs, arch string) error {
	root := filepath.Join(dir, "updater")
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return fmt.Errorf("read staged manifest: %w", err)
	}
	manifest, err := firmware.ParseManifest(data)
	if err != nil {
		return err
	}
	descriptor := &policy.ReleaseDescriptor{
		Name: inputs.Name, Version: inputs.Version, Protocol: inputs.Protocol, Dependencies: inputs.Dependencies, Schemas: inputs.Schemas,
	}
	if err := firmware.VerifyManifestBinding(manifest, descriptor); err != nil {
		return err
	}
	listed := make(map[string]firmware.ManifestFile, len(manifest.Files))
	directories := map[string]bool{}
	for _, f := range manifest.Files {
		listed[f.Path] = f
		for dir := path.Dir(f.Path); dir != "."; dir = path.Dir(dir) {
			directories[dir] = true
		}
	}
	seen := make(map[string]bool, len(listed))
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case rel == ".":
			return checkMode(rel, info, fs.ModeDir|dirMode)
		case rel == "manifest.json":
			return checkMode(rel, info, fileMode)
		case info.IsDir() && directories[rel]:
			return checkMode(rel, info, fs.ModeDir|dirMode)
		case info.IsDir():
			return fmt.Errorf("unexpected directory %s", rel)
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular file", rel)
		}
		return verifyListedFile(path, rel, info, listed, seen, arch)
	})
	if err != nil {
		return err
	}
	for _, f := range manifest.Files {
		if !seen[f.Path] {
			return fmt.Errorf("listed file %s is missing", f.Path)
		}
	}
	for _, slot := range schemaSlots {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(schemaPath(slot))))
		if err != nil {
			return fmt.Errorf("read %s schema file: %w", slot, err)
		}
		if string(content) != slotDocument(inputs.Schemas, slot).Bytes {
			return fmt.Errorf("%s schema file differs from the descriptor bytes", slot)
		}
		if digest(content) != manifestSchema(manifest.Schemas, slot) {
			return fmt.Errorf("%s schema file differs from the manifest schema digest", slot)
		}
	}
	return nil
}

func verifyListedFile(path, rel string, info fs.FileInfo, listed map[string]firmware.ManifestFile, seen map[string]bool, arch string) error {
	f, ok := listed[rel]
	if !ok {
		return fmt.Errorf("%s is not listed in the manifest", rel)
	}
	seen[rel] = true
	want := fileMode
	if f.Executable {
		want = executableMode
	}
	if err := checkMode(rel, info, want); err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if int64(len(content)) != f.Size || bareHash(content) != f.SHA256 {
		return fmt.Errorf("%s differs from its manifest entry", rel)
	}
	if f.Executable {
		return firmware.CheckNativeExecutable(bytes.NewReader(content), arch)
	}
	return nil
}

func newReport(input buildInput, manifest []byte) report {
	files := manifestFiles(input)
	out := make([]reportFile, 0, len(files))
	for _, f := range files {
		mode := "0644"
		if f.Executable {
			mode = "0755"
		}
		out = append(out, reportFile{Path: f.Path, SHA256: f.SHA256, Size: f.Size, Mode: mode})
	}
	schemas := make(map[string]string, len(schemaSlots))
	for _, slot := range schemaSlots {
		schemas[slot] = digest(input.schemas[slot])
	}
	return report{
		Name: input.name, Version: input.version, Platform: "linux/" + input.arch, SourceRevision: input.revision,
		ManifestDigest: digest(manifest), Schemas: schemas, Files: out,
	}
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, fileMode)
}
