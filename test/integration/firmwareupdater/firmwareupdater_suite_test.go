package firmwareupdater_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	catalog "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/flightctl/flightctl/test/util/sigstoretest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/unix"
)

const (
	registryImage   = "quay.io/flightctl/e2eregistry:2"
	repository      = "firmware/updater"
	otherRepository = "firmware/elsewhere"
	releaseName     = "test-updater"
	registryUser    = "firmware"
	schemaJSON      = `{"type":"object","additionalProperties":false,"properties":{}}`
	// extractableSchemaJSON gives the extractable fixture schema digests of its
	// own: the catalog requires every release of a name that shares a schema
	// digest to declare the same budget keys, and only this fixture declares
	// extraction budgets.
	extractableSchemaJSON = `{"title":"extractable","type":"object","additionalProperties":false,"properties":{}}`
)

// Fixture roles. Each role is an updater image pushed to the test registry and
// a release of that image in the approved policy.
const (
	roleValid          = "valid"
	roleUnknownKey     = "unknown-key"
	roleSchemaMismatch = "schema-mismatch"
	roleSymlink        = "symlink-manifest"
	roleUnsigned       = "unsigned"
	roleOtherIdentity  = "other-identity"
	roleManifestList   = "manifest-list"
	roleExtractable    = "extractable"
)

var roleVersions = map[string]string{
	roleValid:          "1.0.0",
	roleUnknownKey:     "1.0.1",
	roleSchemaMismatch: "1.0.2",
	roleSymlink:        "1.0.3",
	roleUnsigned:       "1.0.4",
	roleOtherIdentity:  "1.0.5",
	roleManifestList:   "1.0.6",
	roleExtractable:    "1.0.7",
}

type fixture struct {
	Version string         `json:"version"`
	Digest  string         `json:"digest"`
	Binding policy.Binding `json:"binding"`
	// Manifest holds the exact updater/manifest.json bytes inside the image.
	Manifest []byte `json:"manifest"`
}

// suiteState is provisioned once and shared with every parallel process.
type suiteState struct {
	WorkDir          string             `json:"workDir"`
	Registry         string             `json:"registry"`
	RegistryPassword string             `json:"registryPassword"`
	AuthFile         string             `json:"authFile"`
	RegistriesConf   string             `json:"registriesConf"`
	DiscoveryHome    string             `json:"discoveryHome"`
	NoDiscoveryHome  string             `json:"noDiscoveryHome"`
	ElsewhereDigest  string             `json:"elsewhereDigest"`
	Fixtures         map[string]fixture `json:"fixtures"`
}

var (
	state             suiteState
	registryContainer testcontainers.Container
)

// TestMain runs the suite as root: directly when the process is root, and
// otherwise inside podman unshare, where image mounts are permitted.
func TestMain(m *testing.M) {
	if os.Geteuid() != 0 {
		os.Exit(runInUserNamespace())
	}
	os.Exit(m.Run())
}

func runInUserNamespace() int {
	// Resolve the container API socket before entering the user namespace,
	// where the rootless socket path can no longer be derived from the uid.
	containers.ConfigureDockerHost()
	cmd := exec.Command("podman", append([]string{"unshare", "--", os.Args[0]}, os.Args[1:]...)...) //nolint:gosec // G204: re-executes this test binary with its own arguments
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "firmware updater integration tests require root or podman unshare: %v\n", err)
		return 1
	}
	return 0
}

func TestFirmwareUpdater(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Firmware Updater Integration Suite")
}

var _ = SynchronizedBeforeSuite(func(ctx SpecContext) []byte {
	work, err := os.MkdirTemp("", "firmware-updater-it-")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(removeTree, work)
	Expect(preflightImageMount(ctx, work)).To(Succeed(),
		"the firmware updater suite must mount images: run it as root or where podman unshare supports overlay storage")
	data, err := json.Marshal(provision(ctx, work))
	Expect(err).NotTo(HaveOccurred())
	return data
}, func(data []byte) {
	Expect(json.Unmarshal(data, &state)).To(Succeed())
})

var _ = SynchronizedAfterSuite(func() {}, func(ctx SpecContext) {
	if registryContainer != nil {
		Expect(registryContainer.Terminate(ctx)).To(Succeed())
	}
})

// storage describes isolated container storage for one podman environment.
type storage struct {
	conf, graphRoot string
}

func newStorage(dir string) storage {
	s := storage{conf: filepath.Join(dir, "storage.conf"), graphRoot: filepath.Join(dir, "graph")}
	run := filepath.Join(dir, "run")
	Expect(os.MkdirAll(s.graphRoot, 0o700)).To(Succeed())
	Expect(os.MkdirAll(run, 0o700)).To(Succeed())
	conf := fmt.Sprintf("[storage]\ndriver = \"overlay\"\ngraphroot = %q\nrunroot = %q\n", s.graphRoot, run)
	Expect(os.WriteFile(s.conf, []byte(conf), 0o600)).To(Succeed())
	return s
}

// podmanEnv returns the environment for podman commands using s, the test
// registries configuration and the registries.d directory beneath home.
func podmanEnv(s storage, home string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "CONTAINERS_STORAGE_CONF=") || strings.HasPrefix(kv, "CONTAINERS_REGISTRIES_CONF=")
	})
	env = append(env, "HOME="+home, "CONTAINERS_STORAGE_CONF="+s.conf)
	if state.RegistriesConf != "" {
		env = append(env, "CONTAINERS_REGISTRIES_CONF="+state.RegistriesConf)
	}
	return env
}

func runPodman(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("podman %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// removeTree unmounts anything podman left mounted beneath dir, such as the
// overlay driver's private home mount, and removes dir.
func removeTree(dir string) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	Expect(err).NotTo(HaveOccurred())
	var mounts []string
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 4 && (fields[4] == dir || strings.HasPrefix(fields[4], dir+"/")) {
			mounts = append(mounts, fields[4])
		}
	}
	slices.SortFunc(mounts, func(a, b string) int { return len(b) - len(a) })
	for _, m := range mounts {
		// Detached unmount is best-effort cleanup; a vanished mount must not
		// prevent the remaining mounts and directory from being removed.
		_ = unix.Unmount(m, unix.MNT_DETACH)
	}
	Expect(os.RemoveAll(dir)).To(Succeed())
}

// tempDir returns a per-spec directory removed after the spec, including any
// mounts left beneath it.
func tempDir() string {
	dir, err := os.MkdirTemp(state.WorkDir, "spec-")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(removeTree, dir)
	return dir
}

func buildContext(dir string, files map[string]string, links map[string]string) {
	for name, content := range files {
		path := filepath.Join(dir, name)
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
	}
	for name, target := range links {
		path := filepath.Join(dir, name)
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.Symlink(target, path)).To(Succeed())
	}
	Expect(os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM scratch\nCOPY updater /updater\n"), 0o600)).To(Succeed())
}

func preflightImageMount(ctx context.Context, work string) error {
	dir := filepath.Join(work, "preflight")
	s := newStorage(filepath.Join(dir, "storage"))
	env := podmanEnv(s, work)
	buildContext(filepath.Join(dir, "context"), map[string]string{"updater/manifest.json": `{"preflight":true}`}, nil)
	if _, err := runPodman(ctx, env, "build", "-q", "-t", "localhost/firmware-updater-preflight:1", filepath.Join(dir, "context")); err != nil {
		return err
	}
	mountPoint, err := runPodman(ctx, env, "image", "mount", "localhost/firmware-updater-preflight:1")
	if err != nil {
		return err
	}
	content, readErr := os.ReadFile(filepath.Join(mountPoint, "updater", "manifest.json"))
	// Mount options are diagnostic; read and unmount errors below are authoritative.
	options, _ := mountOptions(mountPoint)
	if _, err := runPodman(ctx, env, "image", "unmount", "localhost/firmware-updater-preflight:1"); err != nil {
		return err
	}
	if readErr != nil {
		return fmt.Errorf("read through image mount %s: %w", mountPoint, readErr)
	}
	if string(content) != `{"preflight":true}` {
		return fmt.Errorf("unexpected content read through image mount %s", mountPoint)
	}
	// Native overlay mounts images read-only; fuse-overlayfs, which podman
	// falls back to when the storage lies on overlayfs, mounts them read-write.
	if !slices.Contains(strings.Split(options, ","), "ro") {
		return fmt.Errorf("image mount %s is not read-only (mount options %q): place TMPDIR on a filesystem with native overlay support such as ext4 or xfs", mountPoint, options)
	}
	return nil
}

func provision(ctx context.Context, work string) suiteState {
	st := suiteState{WorkDir: work, Fixtures: map[string]fixture{}}
	approved, err := sigstoretest.NewKey()
	Expect(err).NotTo(HaveOccurred())
	unknown, err := sigstoretest.NewKey()
	Expect(err).NotTo(HaveOccurred())
	approvedKey, approvedPass := writeKey(work, "approved", approved)
	unknownKey, unknownPass := writeKey(work, "unknown", unknown)

	st.RegistryPassword = randomString()
	st.Registry = startRegistry(ctx, work, st.RegistryPassword)
	st.AuthFile = filepath.Join(work, "auth.json")
	auth := base64.StdEncoding.EncodeToString([]byte(registryUser + ":" + st.RegistryPassword))
	Expect(os.WriteFile(st.AuthFile, []byte(fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, st.Registry, auth)), 0o600)).To(Succeed())
	st.RegistriesConf = filepath.Join(work, "registries.conf")
	Expect(os.WriteFile(st.RegistriesConf, []byte(fmt.Sprintf("[[registry]]\nlocation = %q\ninsecure = true\n", st.Registry)), 0o600)).To(Succeed())
	st.DiscoveryHome = writeRegistriesD(filepath.Join(work, "home-discovery"), st.Registry, true)
	st.NoDiscoveryHome = writeRegistriesD(filepath.Join(work, "home-no-discovery"), st.Registry, false)
	state.RegistriesConf = st.RegistriesConf

	build := newStorage(filepath.Join(work, "build"))
	env := podmanEnv(build, st.DiscoveryHome)
	repo := st.Registry + "/" + repository
	push := func(role, target string, key, pass string) string {
		args := []string{"push", "-q", "--authfile", st.AuthFile, "--digestfile", filepath.Join(work, role+".digest")}
		if key != "" {
			args = append(args, "--sign-by-sigstore-private-key", key, "--sign-passphrase-file", pass)
		}
		_, err := runPodman(ctx, env, append(args, target)...)
		Expect(err).NotTo(HaveOccurred())
		digest, err := os.ReadFile(filepath.Join(work, role+".digest"))
		Expect(err).NotTo(HaveOccurred())
		return strings.TrimSpace(string(digest))
	}

	for role, version := range roleVersions {
		manifest := manifestFor(version, role == roleSchemaMismatch)
		files := map[string]string{"updater/payload/bin/updater": "example updater binary\n"}
		if role == roleExtractable {
			files = extractableFiles()
			manifest = extractableManifest(version, files)
		}
		var links map[string]string
		if role == roleSymlink {
			files["updater/real-manifest.json"] = string(manifest)
			links = map[string]string{"updater/manifest.json": "real-manifest.json"}
		} else {
			files["updater/manifest.json"] = string(manifest)
		}
		contextDir := filepath.Join(work, "contexts", role)
		buildContext(contextDir, files, links)
		tag := repo + ":" + version
		_, err := runPodman(ctx, env, "build", "-q", "-t", tag, contextDir)
		Expect(err).NotTo(HaveOccurred())

		var digest string
		switch role {
		case roleUnsigned:
			digest = push(role, tag, "", "")
		case roleUnknownKey:
			digest = push(role, tag, unknownKey, unknownPass)
		case roleManifestList:
			list := tag + "-list"
			_, err := runPodman(ctx, env, "manifest", "create", list)
			Expect(err).NotTo(HaveOccurred())
			_, err = runPodman(ctx, env, "manifest", "add", list, "containers-storage:"+tag)
			Expect(err).NotTo(HaveOccurred())
			digestFile := filepath.Join(work, role+".digest")
			_, err = runPodman(ctx, env, "manifest", "push", "-q", "--all", "--authfile", st.AuthFile, "--digestfile", digestFile,
				"--sign-by-sigstore-private-key", approvedKey, "--sign-passphrase-file", approvedPass, list, "docker://"+tag)
			Expect(err).NotTo(HaveOccurred())
			listDigest, err := os.ReadFile(digestFile)
			Expect(err).NotTo(HaveOccurred())
			digest = strings.TrimSpace(string(listDigest))
		case roleOtherIdentity:
			elsewhere := st.Registry + "/" + otherRepository + ":" + version
			_, err := runPodman(ctx, env, "tag", tag, elsewhere)
			Expect(err).NotTo(HaveOccurred())
			digest = push(role, elsewhere, approvedKey, approvedPass)
			st.ElsewhereDigest = digest
			for _, ref := range []string{digest, signatureTag(digest)} {
				Expect(copyManifest(ctx, st, otherRepository, repository, ref)).To(Succeed())
			}
		default:
			digest = push(role, tag, approvedKey, approvedPass)
		}
		st.Fixtures[role] = fixture{Version: version, Digest: digest, Manifest: manifest}
	}

	releases := make([]catalog.ReleaseBinding, 0, len(st.Fixtures))
	for role, f := range st.Fixtures {
		r := release(repo, f.Version, f.Digest, approved.KeyData())
		if role == roleExtractable {
			schema := catalog.SchemaDocument{Digest: policy.Hash([]byte(extractableSchemaJSON)), Bytes: extractableSchemaJSON}
			r.Schemas = catalog.SchemaBundle{Config: schema, Observation: schema, Request: schema, Result: schema, Checkpoint: schema}
			r.Extraction = &catalog.ExtractionLimits{MaxExtractedManifestBytes: 65536, MaxExtractedFiles: 16, MaxExtractedFileBytes: 1 << 20, MaxExtractedTotalBytes: 2 << 20, ExtractionScratchBytes: 3 << 20, MaxExtractionSeconds: 300}
			r.BudgetKeySet = append(r.BudgetKeySet, "maxExtractedManifestBytes", "maxExtractedFiles", "maxExtractedFileBytes", "maxExtractedTotalBytes", "extractionScratchBytes", "maxExtractionSeconds")
		}
		releases = append(releases, r)
		data, err := json.Marshal(r)
		Expect(err).NotTo(HaveOccurred())
		f.Binding = policy.Binding{Name: r.Name, Version: r.Version, DescriptorDigest: policy.Hash(data), Descriptor: data}
		st.Fixtures[role] = f
	}
	admin, err := json.Marshal(catalog.AdministratorConfig{APIVersion: "firmware-catalog/v1", Revision: 1, Releases: releases})
	Expect(err).NotTo(HaveOccurred())
	_, err = catalog.ParseAdministratorConfig(admin)
	Expect(err).NotTo(HaveOccurred(), "test releases must be valid catalog releases")
	return st
}

func writeKey(dir, name string, key *sigstoretest.Key) (string, string) {
	keyPath := filepath.Join(dir, name+".private")
	passPath := filepath.Join(dir, name+".pass")
	Expect(os.WriteFile(keyPath, key.PrivateKeyPEM, 0o600)).To(Succeed())
	Expect(os.WriteFile(passPath, key.Passphrase, 0o600)).To(Succeed())
	return keyPath, passPath
}

func randomString() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	Expect(err).NotTo(HaveOccurred())
	return hex.EncodeToString(b)
}

// writeRegistriesD creates a home directory whose user registries.d enables or
// disables sigstore attachment discovery. Its presence makes podman ignore the
// host's registries.d.
func writeRegistriesD(home, registry string, enabled bool) string {
	dir := filepath.Join(home, ".config", "containers", "registries.d")
	Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
	content := fmt.Sprintf("default-docker:\n  use-sigstore-attachments: %t\ndocker:\n  %s:\n    use-sigstore-attachments: %t\n", enabled, registry, enabled)
	Expect(os.WriteFile(filepath.Join(dir, "firmware-updater-test.yaml"), []byte(content), 0o600)).To(Succeed())
	return home
}

func startRegistry(ctx context.Context, work, password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	Expect(err).NotTo(HaveOccurred())
	htpasswd := filepath.Join(work, "htpasswd")
	Expect(os.WriteFile(htpasswd, []byte(registryUser+":"+string(hash)+"\n"), 0o600)).To(Succeed())
	registryContainer, err = containers.GenericStart(ctx, testcontainers.ContainerRequest{
		Image:        registryImage,
		ExposedPorts: []string{"5000/tcp"},
		Env: map[string]string{
			"REGISTRY_AUTH":                "htpasswd",
			"REGISTRY_AUTH_HTPASSWD_REALM": "firmware-updater-test",
			"REGISTRY_AUTH_HTPASSWD_PATH":  "/auth/htpasswd",
		},
		Files:      []testcontainers.ContainerFile{{HostFilePath: htpasswd, ContainerFilePath: "/auth/htpasswd", FileMode: 0o644}},
		WaitingFor: wait.ForHTTP("/v2/").WithPort("5000/tcp").WithBasicAuth(registryUser, password),
	}, false)
	Expect(err).NotTo(HaveOccurred(), "the test registry container must start")
	port, err := registryContainer.MappedPort(ctx, "5000/tcp")
	Expect(err).NotTo(HaveOccurred())
	return "localhost:" + port.Port()
}

func manifestFor(version string, otherSchema bool) []byte {
	digest := policy.Hash([]byte(schemaJSON))
	config := digest
	if otherSchema {
		config = policy.Hash([]byte(`{"title":"other","type":"object"}`))
	}
	m := map[string]any{
		"apiVersion":   firmware.ManifestAPIVersion,
		"name":         releaseName,
		"version":      version,
		"protocol":     1,
		"entrypoint":   "payload/bin/updater",
		"dependencies": []any{},
		"schemas":      map[string]any{"config": config, "observation": digest, "request": digest, "result": digest, "checkpoint": digest},
		"files": []any{map[string]any{
			"path":       "payload/bin/updater",
			"sha256":     "905820073c735d475b79b03ae8617e4a495027dba3c83f5df6fe6536a3d1ed8b",
			"size":       23,
			"executable": true,
		}},
	}
	data, err := json.Marshal(m)
	Expect(err).NotTo(HaveOccurred())
	return data
}

// nativeELF returns a minimal static ELF executable for the test host
// architecture (amd64 or arm64).
func nativeELF() []byte {
	machine := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		machine = elf.EM_AARCH64
	}
	var buf bytes.Buffer
	ident := [16]byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)}
	buf.Write(ident[:])
	for _, v := range []any{
		uint16(elf.ET_EXEC), uint16(machine), uint32(elf.EV_CURRENT), uint64(0x401000), uint64(64), uint64(0), uint32(0),
		uint16(64), uint16(56), uint16(1), uint16(64), uint16(0), uint16(0),
		uint32(elf.PT_LOAD), uint32(elf.PF_R | elf.PF_X), uint64(0), uint64(0x400000), uint64(0x400000), uint64(120), uint64(0x2000), uint64(0x1000),
	} {
		Expect(binary.Write(&buf, binary.LittleEndian, v)).To(Succeed())
	}
	return buf.Bytes()
}

// extractableFiles is the image content of the extractable fixture: a native
// static entrypoint and a data file.
func extractableFiles() map[string]string {
	return map[string]string{
		"updater/payload/bin/updater":     string(nativeELF()),
		"updater/payload/share/notes.txt": "updater notes\n",
	}
}

func extractableManifest(version string, files map[string]string) []byte {
	var m map[string]any
	Expect(json.Unmarshal(manifestFor(version, false), &m)).To(Succeed())
	list := []any{}
	for _, path := range []string{"payload/bin/updater", "payload/share/notes.txt"} {
		content := files["updater/"+path]
		sum := sha256.Sum256([]byte(content))
		list = append(list, map[string]any{"path": path, "sha256": hex.EncodeToString(sum[:]), "size": len(content), "executable": path == "payload/bin/updater"})
	}
	m["files"] = list
	digest := policy.Hash([]byte(extractableSchemaJSON))
	m["schemas"] = map[string]any{"config": digest, "observation": digest, "request": digest, "result": digest, "checkpoint": digest}
	data, err := json.Marshal(m)
	Expect(err).NotTo(HaveOccurred())
	return data
}

func release(repo, version, digest, keyData string) catalog.ReleaseBinding {
	publisher := catalog.PublisherPolicy{Version: 1, Type: "sigstoreSigned", KeyData: keyData, SignedIdentity: catalog.PublisherIdentity{Type: "exactRepository", DockerRepository: repo}}
	schema := catalog.SchemaDocument{Digest: policy.Hash([]byte(schemaJSON)), Bytes: schemaJSON}
	return catalog.ReleaseBinding{
		Name:          releaseName,
		Version:       version,
		PackageDigest: digest,
		Protocol:      1,
		Publisher:     catalog.PublisherBinding{Identity: "test-publisher", PolicyDigest: canonicalDigest(publisher), Policy: publisher},
		Schemas:       catalog.SchemaBundle{Config: schema, Observation: schema, Request: schema, Result: schema, Checkpoint: schema},
		Access:        &catalog.PhaseAccess{Prepare: &catalog.AccessGrant{WorkspaceWrite: true}, Apply: &catalog.AccessGrant{}, Verify: &catalog.AccessGrant{}},
		Limits: catalog.ExecutionLimits{MaxRequestBytes: 4096, MaxResultBytes: 4096, MaxStderrBytes: 4096, MaxCheckpointBytes: 4096, MaxScratchBytes: 1 << 20,
			MaxMemoryBytes: 64 << 20, MaxProcesses: 4, MaxInvocationSeconds: 60, MaxCycles: 3, MaxReboots: 3, MaxAttempts: 3},
		BudgetKeySet: []string{"maxCycles"},
	}
}

func canonicalDigest(value any) string {
	data, err := json.Marshal(value)
	Expect(err).NotTo(HaveOccurred())
	var normalized any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	Expect(dec.Decode(&normalized)).To(Succeed())
	data, err = json.Marshal(normalized)
	Expect(err).NotTo(HaveOccurred())
	return policy.Hash(data)
}

func signatureTag(digest string) string {
	return strings.Replace(digest, ":", "-", 1) + ".sig"
}

// copyManifest copies a manifest and its blobs between repositories of the test
// registry without re-signing, keeping the manifest digest unchanged.
func copyManifest(ctx context.Context, st suiteState, from, to, ref string) error {
	call := func(method, url string, body []byte, header map[string]string) (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, "http://"+st.Registry+url, bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		req.SetBasicAuth(registryUser, st.RegistryPassword)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, nil, err
		}
		// The body is fully read below, so a close error cannot alter its data.
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		return resp, data, err
	}
	resp, manifest, err := call(http.MethodGet, "/v2/"+from+"/manifests/"+ref, nil, map[string]string{
		"Accept": "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json",
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get manifest %s@%s: %s", from, ref, resp.Status)
	}
	var parsed struct {
		Config struct{ Digest string }   `json:"config"`
		Layers []struct{ Digest string } `json:"layers"`
	}
	if err := json.Unmarshal(manifest, &parsed); err != nil {
		return err
	}
	blobs := []string{parsed.Config.Digest}
	for _, l := range parsed.Layers {
		blobs = append(blobs, l.Digest)
	}
	for _, blob := range blobs {
		resp, _, err := call(http.MethodPost, "/v2/"+to+"/blobs/uploads/?mount="+blob+"&from="+from, nil, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("mount blob %s into %s: %s", blob, to, resp.Status)
		}
	}
	resp, _, err = call(http.MethodPut, "/v2/"+to+"/manifests/"+ref, manifest, map[string]string{"Content-Type": resp.Header.Get("Content-Type")})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("put manifest %s@%s: %s", to, ref, resp.Status)
	}
	return nil
}

// recordingExecuter runs commands with a fixed environment and records each
// podman invocation. For image mounts it also records the mount options seen
// in /proc/self/mountinfo while the image is mounted.
type recordingExecuter struct {
	inner executer.Executer
	env   []string

	mu          sync.Mutex
	calls       [][]string
	mountPoints map[string]string
}

func newRecordingExecuter(env []string) *recordingExecuter {
	return &recordingExecuter{inner: executer.NewCommonExecuter(), env: env, mountPoints: map[string]string{}}
}

func (r *recordingExecuter) record(command string, args []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{command}, args...))
}

func (r *recordingExecuter) subcommands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for _, call := range r.calls {
		names = append(names, subcommand(call[1:]))
	}
	return names
}

func (r *recordingExecuter) pullArgs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, call := range r.calls {
		if subcommand(call[1:]) == "pull" {
			return call[1:]
		}
	}
	return nil
}

func subcommand(args []string) string {
	if len(args) >= 2 && args[0] == "image" {
		return "image " + args[1]
	}
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func (r *recordingExecuter) CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	r.record(command, args)
	cmd := r.inner.CommandContext(ctx, command, args...)
	cmd.Env = r.env
	return cmd
}

func (r *recordingExecuter) Execute(command string, args ...string) (string, string, int) {
	return r.ExecuteWithContext(context.Background(), command, args...)
}

func (r *recordingExecuter) ExecuteWithContext(ctx context.Context, command string, args ...string) (string, string, int) {
	r.record(command, args)
	stdout, stderr, code := r.inner.ExecuteWithContextFromDir(ctx, "", command, args, r.env...)
	if command == "podman" && subcommand(args) == "image mount" && code == 0 {
		mountPoint := strings.TrimSpace(stdout)
		// Mount options are diagnostic; the command exit status is authoritative.
		options, _ := mountOptions(mountPoint)
		r.mu.Lock()
		r.mountPoints[mountPoint] = options
		r.mu.Unlock()
	}
	return stdout, stderr, code
}

func (r *recordingExecuter) ExecuteWithContextFromDir(ctx context.Context, dir, command string, args []string, env ...string) (string, string, int) {
	r.record(command, args)
	return r.inner.ExecuteWithContextFromDir(ctx, dir, command, args, append(append([]string(nil), r.env...), env...)...)
}

func (r *recordingExecuter) ExecuteWithBoundedOutputFromDir(ctx context.Context, dir, command string, args []string, limit int, env ...string) (string, string, int) {
	r.record(command, args)
	return r.inner.ExecuteWithBoundedOutputFromDir(ctx, dir, command, args, limit, append(append([]string(nil), r.env...), env...)...)
}

// mountOptions returns the per-mount options of mountPoint from
// /proc/self/mountinfo, or false when it is not mounted.
func mountOptions(mountPoint string) (string, bool) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	Expect(err).NotTo(HaveOccurred())
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 5 && fields[4] == mountPoint {
			return fields[5], true
		}
	}
	return "", false
}
