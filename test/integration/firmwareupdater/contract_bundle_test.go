package firmwareupdater_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	catalog "github.com/flightctl/flightctl/internal/service/updatercatalog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	contractRepository = "firmware/updater-contract-fixture"
	contractVersion    = "0.1.0"
)

var contractSchemaSlots = []string{"config", "observation", "request", "result", "checkpoint"}

// foreignArch is the supported platform that does not match this host.
func foreignArch() string {
	if runtime.GOARCH == "arm64" {
		return "amd64"
	}
	return "arm64"
}

type contractInputs struct {
	Name         string                     `json:"name"`
	Version      string                     `json:"version"`
	Protocol     int                        `json:"protocol"`
	Dependencies []policy.RuntimeDependency `json:"dependencies"`
	Schemas      policy.SchemaBundle        `json:"schemas"`
}

type contractReportFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
}

type contractReport struct {
	Name           string               `json:"name"`
	Version        string               `json:"version"`
	Platform       string               `json:"platform"`
	SourceRevision string               `json:"sourceRevision"`
	ManifestDigest string               `json:"manifestDigest"`
	Schemas        map[string]string    `json:"schemas"`
	Files          []contractReportFile `json:"files"`
}

// contractBuild is the output of one packaging/images/firmware-updater/build.sh run.
type contractBuild struct {
	arch   string
	out    string
	tag    string
	digest string
	inputs contractInputs
	report contractReport
	env    []string
}

func readJSONFile(path string, value any) {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	Expect(json.Unmarshal(data, value)).To(Succeed())
}

func buildContract(ctx context.Context, arch, dir string) contractBuild {
	s := newStorage(filepath.Join(dir, "storage"))
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "CONTAINERS_STORAGE_CONF=") })
	env = append(env, "CONTAINERS_STORAGE_CONF="+s.conf)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	Expect(err).NotTo(HaveOccurred())
	out := filepath.Join(dir, "out")
	cmd := exec.CommandContext(ctx, filepath.Join(root, "packaging", "images", "firmware-updater", "build.sh"), "--arch", arch, "--output", out) //nolint:gosec // G204: the repository build script with test-chosen arguments
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "build.sh --arch %s: %s", arch, output)
	b := contractBuild{arch: arch, out: out, tag: "localhost/updater-contract-fixture:" + contractVersion + "-linux-" + arch, env: podmanEnv(s, state.DiscoveryHome)}
	digest, err := os.ReadFile(filepath.Join(out, "image.digest"))
	Expect(err).NotTo(HaveOccurred())
	b.digest = strings.TrimSpace(string(digest))
	readJSONFile(filepath.Join(out, "descriptor-inputs.json"), &b.inputs)
	readJSONFile(filepath.Join(out, "report.json"), &b.report)
	return b
}

// pushContract pushes the build to repository, signed with the suite's test
// key named key when key is set, and returns the pushed image digest.
func pushContract(ctx context.Context, b contractBuild, image, repository, tag, key string) string {
	digestFile := filepath.Join(filepath.Dir(b.out), strings.ReplaceAll(repository, "/", "-")+"-"+tag+".digest")
	args := []string{"push", "-q", "--format", "oci", "--compression-format", "gzip", "--force-compression", "--authfile", state.AuthFile, "--digestfile", digestFile}
	if key != "" {
		args = append(args, "--sign-by-sigstore-private-key", filepath.Join(state.WorkDir, key+".private"), "--sign-passphrase-file", filepath.Join(state.WorkDir, key+".pass"))
	}
	_, err := runPodman(ctx, b.env, append(args, image, "docker://"+state.Registry+"/"+repository+":"+tag)...)
	Expect(err).NotTo(HaveOccurred())
	digest, err := os.ReadFile(digestFile)
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(string(digest))
}

func copyContractTree(from, to string) {
	Expect(filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})).To(Succeed())
}

// buildMalformedContract builds an image of b's context whose request schema
// gained one byte, while its manifest still lists the original file.
func buildMalformedContract(ctx context.Context, b contractBuild) string {
	contextDir := filepath.Join(filepath.Dir(b.out), "malformed-context")
	copyContractTree(filepath.Join(b.out, "context"), contextDir)
	schema := filepath.Join(contextDir, "updater", "payload", "schemas", "request.schema.json")
	data, err := os.ReadFile(schema)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(schema, append(data, ' '), 0o600)).To(Succeed())
	Expect(os.Chmod(schema, 0o644)).To(Succeed())
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	Expect(err).NotTo(HaveOccurred())
	tag := "localhost/updater-contract-fixture:malformed"
	_, err = runPodman(ctx, b.env, "build", "-q", "--no-cache", "--pull=never", "--format", "oci", "--platform", "linux/"+b.arch,
		"--file", filepath.Join(root, "packaging", "images", "firmware-updater", "Containerfile"), "--tag", tag, contextDir)
	Expect(err).NotTo(HaveOccurred())
	return pushContract(ctx, b, tag, contractRepository, contractVersion+"-malformed", "approved")
}

func approvedPublisherKey() string {
	d, err := policy.DecodeDescriptor(state.Fixtures[roleValid].Binding)
	Expect(err).NotTo(HaveOccurred())
	return d.Publisher.Policy.KeyData
}

func contractRelease(b contractBuild, version, repository, digest, keyData string) catalog.ReleaseBinding {
	publisher := catalog.PublisherPolicy{Version: 1, Type: "sigstoreSigned", KeyData: keyData,
		SignedIdentity: catalog.PublisherIdentity{Type: "exactRepository", DockerRepository: state.Registry + "/" + repository}}
	document := func(d policy.SchemaDocument) catalog.SchemaDocument {
		return catalog.SchemaDocument{Digest: d.Digest, Bytes: d.Bytes}
	}
	return catalog.ReleaseBinding{
		Name: b.inputs.Name, Version: version, PackageDigest: digest, Protocol: b.inputs.Protocol,
		Publisher: catalog.PublisherBinding{Identity: "test-publisher", PolicyDigest: canonicalDigest(publisher), Policy: publisher},
		Schemas: catalog.SchemaBundle{
			Config: document(b.inputs.Schemas.Config), Observation: document(b.inputs.Schemas.Observation), Request: document(b.inputs.Schemas.Request),
			Result: document(b.inputs.Schemas.Result), Checkpoint: document(b.inputs.Schemas.Checkpoint),
		},
		Dependencies: []catalog.RuntimeDependency{},
		Access:       &catalog.PhaseAccess{Prepare: &catalog.AccessGrant{}, Apply: &catalog.AccessGrant{}, Verify: &catalog.AccessGrant{}},
		Limits: catalog.ExecutionLimits{MaxRequestBytes: 65536, MaxResultBytes: 65536, MaxStderrBytes: 4096, MaxCheckpointBytes: 4096, MaxScratchBytes: 1 << 20,
			MaxMemoryBytes: 64 << 20, MaxProcesses: 4, MaxInvocationSeconds: 60, MaxCycles: 3, MaxReboots: 1, MaxAttempts: 1},
		Extraction: &catalog.ExtractionLimits{MaxExtractedManifestBytes: 65536, MaxExtractedFiles: 16, MaxExtractedFileBytes: 8 << 20,
			MaxExtractedTotalBytes: 16 << 20, ExtractionScratchBytes: 17 << 20, MaxExtractionSeconds: 300},
		BudgetKeySet: []string{"maxCycles", "maxExtractedManifestBytes", "maxExtractedFiles", "maxExtractedFileBytes", "maxExtractedTotalBytes", "extractionScratchBytes", "maxExtractionSeconds"},
	}
}

func contractBindings(releases ...catalog.ReleaseBinding) map[string]policy.Binding {
	admin, err := json.Marshal(catalog.AdministratorConfig{APIVersion: "firmware-catalog/v1", Revision: 1, Releases: releases})
	Expect(err).NotTo(HaveOccurred())
	_, err = catalog.ParseAdministratorConfig(admin)
	Expect(err).NotTo(HaveOccurred(), "contract releases must be valid catalog releases")
	bindings := map[string]policy.Binding{}
	for _, r := range releases {
		data, err := json.Marshal(r)
		Expect(err).NotTo(HaveOccurred())
		bindings[r.Name+"/"+r.Version] = policy.Binding{Name: r.Name, Version: r.Version, DescriptorDigest: policy.Hash(data), Descriptor: data}
	}
	return bindings
}

func requireNoContractRecord(e *fetchEnv, b policy.Binding) {
	_, err := os.Stat(e.recordPathFor(b.DescriptorDigest))
	Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue(), "no verified record may exist")
}

func requireNoPublishedVersion(x *extractEnv, pkg *firmware.VerifiedPackage) {
	_, err := os.Stat(x.versionDir(pkg))
	Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue(), "no updater version may be published")
}

type ociMember struct {
	mode    int64
	uid     int
	gid     int
	modTime time.Time
	digest  string
	dir     bool
}

// inspectOCILayout reads an OCI image layout without running anything and
// returns its image config and the members of its only layer.
func inspectOCILayout(layout string) (map[string]any, map[string]ociMember) {
	blob := func(digest string) []byte {
		data, err := os.ReadFile(filepath.Join(layout, "blobs", strings.Replace(digest, ":", string(filepath.Separator), 1)))
		Expect(err).NotTo(HaveOccurred())
		Expect(policy.Hash(data)).To(Equal(digest))
		return data
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	readJSONFile(filepath.Join(layout, "index.json"), &index)
	Expect(index.Manifests).To(HaveLen(1))
	var manifest struct {
		MediaType string `json:"mediaType"`
		Config    struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest    string `json:"digest"`
			MediaType string `json:"mediaType"`
		} `json:"layers"`
	}
	Expect(json.Unmarshal(blob(index.Manifests[0].Digest), &manifest)).To(Succeed())
	Expect(manifest.MediaType).To(Equal("application/vnd.oci.image.manifest.v1+json"))
	Expect(manifest.Layers).To(HaveLen(1))
	Expect(manifest.Layers[0].MediaType).To(Equal("application/vnd.oci.image.layer.v1.tar+gzip"))
	var config map[string]any
	Expect(json.Unmarshal(blob(manifest.Config.Digest), &config)).To(Succeed())
	gz, err := gzip.NewReader(bytes.NewReader(blob(manifest.Layers[0].Digest)))
	Expect(err).NotTo(HaveOccurred())
	members := map[string]ociMember{}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		Expect(err).NotTo(HaveOccurred())
		data, err := io.ReadAll(reader)
		Expect(err).NotTo(HaveOccurred())
		sum := sha256.Sum256(data)
		Expect(header.PAXRecords).To(BeEmpty(), "%s must not carry extended attributes", header.Name)
		members[strings.TrimSuffix(header.Name, "/")] = ociMember{mode: header.Mode, uid: header.Uid, gid: header.Gid, modTime: header.ModTime.UTC(),
			digest: hex.EncodeToString(sum[:]), dir: header.Typeflag == tar.TypeDir}
	}
	return config, members
}

var _ = Describe("Native updater contract bundle", Ordered, func() {
	var (
		builds   map[string]contractBuild
		repeats  map[string]contractBuild
		digests  map[string]string
		bindings map[string]policy.Binding
	)

	BeforeAll(func(ctx SpecContext) {
		requireNativeHost()
		work := tempDir()
		builds, repeats = map[string]contractBuild{}, map[string]contractBuild{}
		for _, arch := range []string{"amd64", "arm64"} {
			builds[arch] = buildContract(ctx, arch, filepath.Join(work, arch+"-1"))
			repeats[arch] = buildContract(ctx, arch, filepath.Join(work, arch+"-2"))
		}
		native, foreign := builds[runtime.GOARCH], builds[foreignArch()]
		digests = map[string]string{
			native.arch:      pushContract(ctx, native, native.tag, contractRepository, contractVersion+"-linux-"+native.arch, "approved"),
			foreign.arch:     pushContract(ctx, foreign, foreign.tag, contractRepository, contractVersion+"-linux-"+foreign.arch, "approved"),
			"unsigned":       pushContract(ctx, native, native.tag, "firmware/contract-unsigned", contractVersion, ""),
			"unknown-signer": pushContract(ctx, native, native.tag, "firmware/contract-unknown-signer", contractVersion, "unknown"),
		}
		for _, ref := range []string{digests[native.arch], signatureTag(digests[native.arch])} {
			Expect(copyManifest(ctx, state, contractRepository, "firmware/contract-elsewhere", ref)).To(Succeed())
		}
		digests["malformed"] = buildMalformedContract(ctx, native)
		key := approvedPublisherKey()
		bindings = contractBindings(
			contractRelease(native, contractVersion, contractRepository, digests[native.arch], key),
			contractRelease(foreign, contractVersion, contractRepository, digests[foreign.arch], key),
			contractRelease(native, contractVersion+"-unsigned", "firmware/contract-unsigned", digests["unsigned"], key),
			contractRelease(native, contractVersion+"-unknown-signer", "firmware/contract-unknown-signer", digests["unknown-signer"], key),
			contractRelease(native, contractVersion+"-elsewhere", "firmware/contract-elsewhere", digests[native.arch], key),
		)
		for prefix, digest := range map[string]string{"malformed:": digests["malformed"], "wrong-digest:": digests[foreign.arch]} {
			for k, v := range contractBindings(contractRelease(native, contractVersion, contractRepository, digest, key)) {
				bindings[prefix+k] = v
			}
		}
	})

	It("When each platform is built twice from one commit it should produce the same image and publish that digest", func() {
		for _, arch := range []string{"amd64", "arm64"} {
			Expect(repeats[arch].digest).To(Equal(builds[arch].digest), "two clean %s builds must produce one OCI image digest", arch)
			Expect(repeats[arch].report).To(Equal(builds[arch].report))
			Expect(repeats[arch].inputs).To(Equal(builds[arch].inputs))
			Expect(digests[arch]).To(Equal(builds[arch].digest), "the published %s digest must equal the local build", arch)
		}
		Expect(builds["amd64"].report.ManifestDigest).NotTo(Equal(builds["arm64"].report.ManifestDigest))
	})

	It("When the image is inspected without running it it should carry its build identity, no command and the exact payload", func() {
		for _, arch := range []string{"amd64", "arm64"} {
			b := builds[arch]
			config, members := inspectOCILayout(filepath.Join(b.out, "oci"))
			Expect(config["os"]).To(Equal("linux"))
			Expect(config["architecture"]).To(Equal(arch))
			runtimeConfig, _ := config["config"].(map[string]any)
			Expect(runtimeConfig["Entrypoint"]).To(BeNil(), "the image has no entrypoint")
			Expect(runtimeConfig["Cmd"]).To(BeNil(), "the image has no command")
			created := config["created"].(string)
			Expect(runtimeConfig["Labels"]).To(Equal(map[string]any{
				"org.opencontainers.image.source":   "https://github.com/flightctl/flightctl",
				"org.opencontainers.image.revision": b.report.SourceRevision,
				"org.opencontainers.image.version":  contractVersion,
				"org.opencontainers.image.created":  created,
				"io.flightctl.updater.platform":     "linux/" + arch,
			}))
			Expect(b.report.SourceRevision).To(MatchRegexp(`^[0-9a-f]{40}$`))
			createdAt, err := time.Parse(time.RFC3339, created)
			Expect(err).NotTo(HaveOccurred())

			want := map[string]bool{"updater": true, "updater/payload": true, "updater/payload/bin": true, "updater/payload/schemas": true}
			expected := maps.Clone(want)
			expected["updater/manifest.json"] = true
			for path, member := range members {
				Expect(member.uid).To(BeZero(), path)
				Expect(member.gid).To(BeZero(), path)
				Expect(member.modTime).To(Equal(createdAt), "%s carries the source commit time", path)
				if member.dir {
					Expect(want).To(HaveKey(path))
					Expect(member.mode&0o7777).To(Equal(int64(0o755)), path)
				}
			}
			manifest, ok := members["updater/manifest.json"]
			Expect(ok).To(BeTrue())
			Expect(manifest.mode & 0o7777).To(Equal(int64(0o644)))
			Expect("sha256:" + manifest.digest).To(Equal(b.report.ManifestDigest))
			for _, f := range b.report.Files {
				expected["updater/"+f.Path] = true
				member, ok := members["updater/"+f.Path]
				Expect(ok).To(BeTrue(), f.Path)
				Expect(member.dir).To(BeFalse(), f.Path)
				Expect(member.digest).To(Equal(f.SHA256), f.Path)
				wantMode := int64(0o644)
				if f.Mode == "0755" {
					wantMode = 0o755
				}
				Expect(member.mode&0o7777).To(Equal(wantMode), f.Path)
			}
			Expect(slices.Sorted(maps.Keys(members))).To(Equal(slices.Sorted(maps.Keys(expected))), "the layer holds only the listed files, their directories and the manifest")
		}
	})

	It("When the built image is fetched by digest it should verify the publisher and extract exactly the packaged bytes", func(ctx SpecContext) {
		b := builds[runtime.GOARCH]
		binding := bindings[b.inputs.Name+"/"+contractVersion]
		x := newExtractEnv(newFetchEnvWithBindings(state.DiscoveryHome, state.AuthFile, []policy.Binding{binding}))
		reference := state.Registry + "/" + contractRepository + "@" + digests[b.arch]
		pkg, err := x.fetchRelease(ctx, b.inputs.Name, contractVersion, reference)
		Expect(err).NotTo(HaveOccurred())
		Expect(pkg.PackageDigest).To(Equal(b.digest))
		Expect(pkg.ManifestDigest).To(Equal(b.report.ManifestDigest))
		Expect(pkg.Reference).To(Equal(reference))
		x.requireNoSignaturePolicy()

		containers := x.containers(ctx)
		payload, err := x.extractor.Extract(ctx, pkg)
		Expect(err).NotTo(HaveOccurred())
		Expect(payload.Entrypoint).To(Equal("payload/bin/updater"))
		Expect(payload.Files).To(HaveLen(len(b.report.Files)))
		Expect(x.containers(ctx)).To(Equal(containers), "extraction must not create containers")
		x.requireNoMountLeft()

		manifest, err := os.ReadFile(x.versionDir(pkg, "manifest.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(policy.Hash(manifest)).To(Equal(pkg.ManifestDigest))
		for _, f := range b.report.Files {
			data, err := os.ReadFile(x.versionDir(pkg, filepath.FromSlash(f.Path)))
			Expect(err).NotTo(HaveOccurred())
			sum := sha256.Sum256(data)
			Expect(hex.EncodeToString(sum[:])).To(Equal(f.SHA256), f.Path)
			Expect(int64(len(data))).To(Equal(f.Size), f.Path)
		}
		documents := map[string]policy.SchemaDocument{
			"config": b.inputs.Schemas.Config, "observation": b.inputs.Schemas.Observation, "request": b.inputs.Schemas.Request,
			"result": b.inputs.Schemas.Result, "checkpoint": b.inputs.Schemas.Checkpoint,
		}
		for _, slot := range contractSchemaSlots {
			data, err := os.ReadFile(x.versionDir(pkg, "payload", "schemas", slot+".schema.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal(documents[slot].Bytes), "the extracted %s schema is the descriptor's exact bytes", slot)
			Expect(policy.Hash(data)).To(Equal(documents[slot].Digest))
		}
		evidence, err := x.retention.Evidence(pkg.PackageDigest)
		Expect(err).NotTo(HaveOccurred())
		AddReportEntry("extraction-selinux-mode", string(evidence.Published.Mode))
		AddReportEntry("extraction-version-dir", x.versionDir(pkg))
	})

	It("When the image for another architecture is verified it should refuse extraction without publishing a version", func(ctx SpecContext) {
		b := builds[foreignArch()]
		x := newExtractEnv(newFetchEnvWithBindings(state.DiscoveryHome, state.AuthFile, []policy.Binding{bindings[b.inputs.Name+"/"+contractVersion]}))
		pkg, err := x.fetchRelease(ctx, b.inputs.Name, contractVersion, state.Registry+"/"+contractRepository+"@"+digests[b.arch])
		Expect(err).NotTo(HaveOccurred(), "signature, digest and manifest binding do not depend on the host architecture")
		containers := x.containers(ctx)
		_, err = x.extractor.Extract(ctx, pkg)
		Expect(err).To(MatchError(ContainSubstring("static native binary")))
		Expect(x.containers(ctx)).To(Equal(containers))
		x.requireNoMountLeft()
		requireNoPublishedVersion(x, pkg)
	})

	DescribeTable("When the publisher or digest binding is wrong it should fail closed without verified material",
		func(ctx SpecContext, prefix, version, repository, digestKey, message string) {
			native := builds[runtime.GOARCH]
			name := native.inputs.Name
			binding := bindings[prefix+name+"/"+version]
			if digestKey == "native" || digestKey == "foreign" {
				digestKey = map[string]string{"native": native.arch, "foreign": foreignArch()}[digestKey]
			}
			if message == "" {
				message = fmt.Sprintf("manifest identity %q/%q differs", builds[foreignArch()].inputs.Name, contractVersion)
			}
			e := newFetchEnvWithBindings(state.DiscoveryHome, state.AuthFile, []policy.Binding{binding})
			pkg, err := e.fetchRelease(ctx, name, version, state.Registry+"/"+repository+"@"+digests[digestKey])
			Expect(pkg).To(BeNil())
			requireFetchError(err, firmware.ReasonUpdaterUnavailable, false)
			Expect(err.Error()).To(ContainSubstring(message))
			requireNoContractRecord(e, binding)
			e.requireNoSignaturePolicy()
			e.requireNoMountLeft()
		},
		Entry("unsigned image", "", contractVersion+"-unsigned", "firmware/contract-unsigned", "unsigned", "publisher signature verification failed"),
		Entry("image signed by an unapproved key", "", contractVersion+"-unknown-signer", "firmware/contract-unknown-signer", "unknown-signer", "publisher signature verification failed"),
		Entry("signature made for another repository", "", contractVersion+"-elsewhere", "firmware/contract-elsewhere", "native", "publisher signature verification failed"),
		Entry("digest of another signed image", "wrong-digest:", contractVersion, contractRepository, "foreign", ""),
	)

	It("When a signed image's payload differs from its manifest it should refuse extraction and publish nothing", func(ctx SpecContext) {
		b := builds[runtime.GOARCH]
		x := newExtractEnv(newFetchEnvWithBindings(state.DiscoveryHome, state.AuthFile, []policy.Binding{bindings["malformed:"+b.inputs.Name+"/"+contractVersion]}))
		pkg, err := x.fetchRelease(ctx, b.inputs.Name, contractVersion, state.Registry+"/"+contractRepository+"@"+digests["malformed"])
		Expect(err).NotTo(HaveOccurred(), "the manifest still binds the approved identity")
		containers := x.containers(ctx)
		_, err = x.extractor.Extract(ctx, pkg)
		Expect(err).To(MatchError(ContainSubstring("payload/schemas/request.schema.json")))
		Expect(x.containers(ctx)).To(Equal(containers))
		x.requireNoMountLeft()
		requireNoPublishedVersion(x, pkg)
	})
})
