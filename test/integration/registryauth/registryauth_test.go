package registryauth_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/registryconfig"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/pkg/userutil"
	"github.com/flightctl/flightctl/test/harness/containers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"
)

const (
	registryUser    = "prefetch"
	registrySecret  = "fixture-private-password"
	applicationRepo = "private/container"
	quadletRepo     = "private/quadlet"
	volumeRepo      = "private/volume"
	ordinaryFile    = "/etc/registry-auth-test"
	dataDir         = "/var/lib/flightctl"
)

func TestMain(m *testing.M) {
	if os.Geteuid() == 0 {
		os.Exit(m.Run())
	}
	containers.ConfigureDockerHost()
	cmd := exec.Command("podman", append([]string{"unshare", "--", os.Args[0]}, os.Args[1:]...)...) //nolint:gosec // reexecute this test binary and its arguments
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var status *exec.ExitError
		if errors.As(err, &status) {
			os.Exit(status.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "registry auth integration tests require root or podman unshare: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestRegistryAuth(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Registry Auth Integration Suite")
}

type appCollector struct {
	collect func(context.Context, *v1beta1.DeviceSpec) (*dependency.OCICollection, error)
}

func (c *appCollector) CollectOCITargets(ctx context.Context, _, desired *v1beta1.DeviceSpec, _ ...dependency.OCICollectOpt) (*dependency.OCICollection, error) {
	return c.collect(ctx, desired)
}

var _ = Describe("Container registry credentials", Serial, func() {
	It("When private workloads are prefetched it should authenticate with staged auth before applying configuration", func(ctx SpecContext) {
		work := GinkgoT().TempDir()
		registry, endpoint := startRegistry(ctx, work)
		configurePodman(work, endpoint)
		exec := executer.NewCommonExecuter()
		publishFixtures(ctx, exec, work, endpoint)
		setStorage(work, "device")

		root := filepath.Join(work, "device-root")
		_, _, home, err := userutil.LookupExecutionUser(v1beta1.CurrentProcessUsername)
		Expect(err).NotTo(HaveOccurred())
		for _, dir := range []string{home, dataDir, os.TempDir()} {
			Expect(os.MkdirAll(filepath.Join(root, dir), 0o700)).To(Succeed())
		}
		factory := fileio.NewReadWriterFactory(root)
		rw, err := factory(v1beta1.CurrentProcessUsername)
		Expect(err).NotTo(HaveOccurred())
		logger := log.NewPrefixLogger("registry-auth-it")
		var agentLogs bytes.Buffer
		logger.SetOutput(&agentLogs)
		DeferCleanup(func() { Expect(agentLogs.String()).NotTo(ContainSubstring(registrySecret)) })
		manager := registryconfig.NewManager(factory, dataDir)
		DeferCleanup(manager.Cleanup)
		resolver := dependency.NewPullConfigResolver(logger, factory, dependency.WithGeneratedConfigSource(manager))
		DeferCleanup(resolver.Cleanup)
		backoff := poll.Config{BaseDelay: 20 * time.Millisecond, Factor: 1, MaxSteps: 1}
		// The clients execute on the host; the simulated device root is for native state.
		hostFactory := fileio.NewReadWriterFactory("")
		podmanFactory := client.NewPodmanFactory(logger, backoff, hostFactory)
		skopeoFactory := client.NewSkopeoFactory(logger, hostFactory)
		prefetch := dependency.NewPrefetchManager(logger, podmanFactory, skopeoFactory, nil, rw, util.Duration(25*time.Second), resource.NewManager(logger, nil), backoff)
		workerCtx, cancel := context.WithCancel(ctx)
		workerDone := make(chan struct{})
		go func() {
			defer close(workerDone)
			prefetch.Run(workerCtx)
		}()
		DeferCleanup(func() {
			prefetch.Cleanup()
			cancel()
			Eventually(workerDone).Should(BeClosed())
		})
		collector := &appCollector{collect: func(ctx context.Context, desired *v1beta1.DeviceSpec) (*dependency.OCICollection, error) {
			return provider.CollectOCITargets(ctx, logger, podmanFactory, nil, factory, desired, provider.WithPullConfigResolver(resolver))
		}}
		current, desired := &v1beta1.DeviceSpec{}, renderedSpec(endpoint)
		live := filepath.Join(home, consts.ContainersAuthRelativePath)

		resolver.BeforeUpdate(desired)
		Expect(manager.Prepare(ctx, desired)).To(Succeed())
		stage, ok := manager.StagedPath(live)
		Expect(ok).To(BeTrue())
		Expect(stage).To(BeAnExistingFile())
		prefetch.RegisterOCICollector(collector)
		Eventually(func() error {
			err := prefetch.BeforeUpdate(ctx, current, desired)
			if err != nil && !errors.Is(err, deviceerrors.ErrPrefetchNotReady) {
				return StopTrying("prefetch failed").Wrap(err)
			}
			return err
		}, 45*time.Second, 100*time.Millisecond).Should(Succeed())
		Expect(rw.PathFor(live)).NotTo(BeAnExistingFile())
		Expect(rw.PathFor(ordinaryFile)).NotTo(BeAnExistingFile())

		registryLogs := readRegistryLogs(ctx, registry)
		for _, repo := range []string{applicationRepo, quadletRepo, volumeRepo} {
			Expect(strings.Split(registryLogs, "\n")).To(ContainElement(SatisfyAll(
				ContainSubstring(`msg="authorized request"`), ContainSubstring("http.request.method=GET"), ContainSubstring("/v2/"+repo+"/manifests/"),
			)))
		}
		Expect(registryLogs).NotTo(ContainSubstring(registrySecret))

		Expect(manager.Commit(ctx, desired)).To(Succeed())
		Expect(config.NewController(rw, dataDir, logger).Sync(ctx, current, desired)).To(Succeed())
		info, err := os.Stat(rw.PathFor(live))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(rw.PathFor(ordinaryFile)).To(BeAnExistingFile())
		Expect(stage).NotTo(BeAnExistingFile())

		// A fresh store and an empty desired resource collection exercise the pull failure path.
		prefetch.Cleanup()
		setStorage(work, "unauthenticated")
		Expect(manager.Commit(ctx, &v1beta1.DeviceSpec{})).To(Succeed())
		desired.ConfigResources = nil
		resolver.BeforeUpdate(desired)
		Expect(manager.Prepare(ctx, desired)).To(Succeed())
		prefetch.RegisterOCICollector(collector)
		var pullErr error
		Eventually(func() bool {
			pullErr = prefetch.BeforeUpdate(ctx, current, desired)
			return pullErr != nil && !errors.Is(pullErr, deviceerrors.ErrPrefetchNotReady)
		}, 45*time.Second, 100*time.Millisecond).Should(BeTrue())
		Expect(errors.Is(pullErr, deviceerrors.ErrAuthenticationFailed)).To(BeTrue(), "expected an authentication failure: %v", pullErr)
		Expect(pullErr.Error()).NotTo(ContainSubstring(registrySecret))
	})

	It("When the pull uses a named account it should read only that account's staged auth", func(ctx SpecContext) {
		const uid, gid = 1, 1
		const home = "/srv/workload"
		GinkgoT().Setenv("TMPDIR", "/tmp")
		root := GinkgoT().TempDir()
		Expect(os.Chmod(root, 0o711)).To(Succeed())
		Expect(os.Mkdir(filepath.Join(root, "tmp"), 0o700)).To(Succeed())
		Expect(os.Chmod(filepath.Join(root, "tmp"), 0o711)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, home), 0o700)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, dataDir), 0o700)).To(Succeed())
		Expect(os.Chown(filepath.Join(root, home), uid, gid)).To(Succeed())
		factory := fileio.NewReadWriterFactory(root)
		manager := registryconfig.NewManager(factory, dataDir, registryconfig.WithAccountLookup(func(name v1beta1.Username) (registryconfig.Account, error) {
			Expect(name).To(Equal(v1beta1.Username("workload")))
			return registryconfig.Account{UID: uid, GID: gid, HomeDir: home}, nil
		}))
		DeferCleanup(manager.Cleanup)
		resource := authResource("quay.io:5000", lo.ToPtr(v1beta1.Username("workload")))
		desired := &v1beta1.DeviceSpec{ConfigResources: &[]v1beta1.ConfigResource{resource}}
		live := filepath.Join(home, consts.ContainersAuthRelativePath)
		Expect(manager.Prepare(ctx, desired)).To(Succeed())
		stage, ok := manager.StagedPath(live)
		Expect(ok).To(BeTrue())
		parent, err := os.Stat(filepath.Dir(stage))
		Expect(err).NotTo(HaveOccurred())
		Expect(parent.Sys().(*syscall.Stat_t).Uid).To(BeZero())
		file, err := os.Stat(stage)
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Sys().(*syscall.Stat_t).Uid).To(BeEquivalentTo(uid))
		Expect(file.Sys().(*syscall.Stat_t).Gid).To(BeEquivalentTo(gid))
		Expect(file.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		canReadAs := func(uid, gid uint32) error {
			cmd := exec.CommandContext(ctx, "sh", "-c", `test ! -w "${1%/*}" && head -c 1 "$1" >/dev/null`, "sh", stage) //nolint:gosec // fixed shell expression and a separate path argument
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, NoSetGroups: true}}
			return cmd.Run()
		}
		Expect(canReadAs(uid, gid)).To(Succeed())
		Expect(canReadAs(uid+1, gid+1)).To(BeAssignableToTypeOf(&exec.ExitError{}))
	})
})

func renderedSpec(endpoint string) *v1beta1.DeviceSpec {
	image := func(repo string) string { return endpoint + "/" + repo + ":latest" }
	var volume v1beta1.ApplicationVolume
	volume.Name = "data"
	Expect(volume.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{Image: v1beta1.ImageVolumeSource{Reference: image(volumeRepo)}})).To(Succeed())
	container := v1beta1.ContainerApplication{Name: lo.ToPtr("container"), AppType: v1beta1.AppTypeContainer, Volumes: &[]v1beta1.ApplicationVolume{volume}}
	Expect(container.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image(applicationRepo)})).To(Succeed())
	quadlet := v1beta1.QuadletApplication{Name: lo.ToPtr("quadlet"), AppType: v1beta1.AppTypeQuadlet}
	Expect(quadlet.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{Inline: []v1beta1.ApplicationContent{{
		Path: "quadlet.container", Content: lo.ToPtr("[Container]\nImage=" + image(quadletRepo) + "\n"),
	}}})).To(Succeed())
	var a, b v1beta1.ApplicationProviderSpec
	Expect(a.FromContainerApplication(container)).To(Succeed())
	Expect(b.FromQuadletApplication(quadlet)).To(Succeed())

	registry := authResource(endpoint, nil)
	files, err := config.FilesToProviderSpec([]v1beta1.FileSpec{{Path: ordinaryFile, Content: "after approval"}})
	Expect(err).NotTo(HaveOccurred())
	return &v1beta1.DeviceSpec{Config: files, Applications: &[]v1beta1.ApplicationProviderSpec{a, b}, ConfigResources: &[]v1beta1.ConfigResource{registry}}
}

func authResource(endpoint string, user *v1beta1.Username) v1beta1.ConfigResource {
	var authentication v1beta1.OciAuth
	Expect(authentication.FromDockerAuth(v1beta1.DockerAuth{AuthType: v1beta1.Docker, Username: registryUser, Password: registrySecret})).To(Succeed())
	var registry v1beta1.ConfigResource
	Expect(registry.FromRegistryConfigResource(v1beta1.RegistryConfigResource{
		Name: "registry:" + endpoint, Registry: endpoint, Type: v1beta1.Registry, Scheme: v1beta1.RegistrySchemeHTTP, SkipServerVerification: lo.ToPtr(false),
		Access: []v1beta1.RegistryConfigAccess{{Scope: lo.ToPtr("private"), Authentication: &authentication, Consumers: []v1beta1.RegistryConfigConsumer{{Type: v1beta1.Container, User: user}}}},
	})).To(Succeed())
	return registry
}

func startRegistry(ctx context.Context, dir string) (testcontainers.Container, string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(registrySecret), bcrypt.DefaultCost)
	Expect(err).NotTo(HaveOccurred())
	passwd := filepath.Join(dir, "htpasswd")
	Expect(os.WriteFile(passwd, []byte(registryUser+":"+string(hash)+"\n"), 0o600)).To(Succeed())
	registry, err := containers.GenericStart(ctx, testcontainers.ContainerRequest{
		Image:        "quay.io/flightctl/e2eregistry:2",
		ExposedPorts: []string{"5000/tcp"},
		Env: map[string]string{
			"REGISTRY_AUTH":                "htpasswd",
			"REGISTRY_AUTH_HTPASSWD_REALM": "registry-auth-test",
			"REGISTRY_AUTH_HTPASSWD_PATH":  "/auth/htpasswd",
		},
		Files:      []testcontainers.ContainerFile{{HostFilePath: passwd, ContainerFilePath: "/auth/htpasswd", FileMode: 0o644}},
		WaitingFor: wait.ForHTTP("/v2/").WithPort("5000/tcp").WithBasicAuth(registryUser, registrySecret),
	}, false)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(registry.Terminate)
	port, err := registry.MappedPort(ctx, "5000/tcp")
	Expect(err).NotTo(HaveOccurred())
	return registry, "localhost:" + port.Port()
}

func configurePodman(dir, endpoint string) {
	path := filepath.Join(dir, "registries.conf")
	Expect(os.WriteFile(path, fmt.Appendf(nil, "[[registry]]\nlocation = %q\ninsecure = true\n", endpoint), 0o600)).To(Succeed())
	GinkgoT().Setenv("CONTAINERS_REGISTRIES_CONF", path)
	// Explicitly isolate default discovery as well as the image store.
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		path := filepath.Join(dir, strings.ToLower(name))
		Expect(os.MkdirAll(path, 0o700)).To(Succeed())
		GinkgoT().Setenv(name, path)
	}
	GinkgoT().Setenv("REGISTRY_AUTH_FILE", "")
}

func setStorage(dir, name string) {
	path := filepath.Join(dir, name+".conf")
	storage := fmt.Appendf(nil, "[storage]\ndriver = \"vfs\"\ngraphroot = %q\nrunroot = %q\n", filepath.Join(dir, name+"-graph"), filepath.Join(dir, name+"-run"))
	Expect(os.WriteFile(path, storage, 0o600)).To(Succeed())
	GinkgoT().Setenv("CONTAINERS_STORAGE_CONF", path)
}

func publishFixtures(ctx context.Context, exec executer.Executer, dir, endpoint string) {
	setStorage(dir, "publisher")
	fixture := filepath.Join(dir, "fixture")
	Expect(os.WriteFile(fixture, []byte("test payload"), 0o600)).To(Succeed())
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	Expect(tw.WriteHeader(&tar.Header{Name: "fixture", Mode: 0o644, Size: 12})).To(Succeed())
	_, err := tw.Write([]byte("test payload"))
	Expect(err).NotTo(HaveOccurred())
	Expect(tw.Close()).To(Succeed())
	archivePath := filepath.Join(dir, "rootfs.tar")
	Expect(os.WriteFile(archivePath, archive.Bytes(), 0o600)).To(Succeed())
	encoded := base64.StdEncoding.EncodeToString([]byte(registryUser + ":" + registrySecret))
	data, err := json.Marshal(map[string]any{"auths": map[string]map[string]string{endpoint: {"auth": encoded}}})
	Expect(err).NotTo(HaveOccurred())
	auth := filepath.Join(dir, "publisher-auth.json")
	Expect(os.WriteFile(auth, data, 0o600)).To(Succeed())
	for _, repo := range []string{applicationRepo, quadletRepo} {
		ref := endpoint + "/" + repo + ":latest"
		runPodman(ctx, exec, "import", archivePath, ref)
		runPodman(ctx, exec, "push", "--authfile", auth, ref)
	}
	volume := endpoint + "/" + volumeRepo + ":latest"
	runPodman(ctx, exec, "artifact", "add", volume, fixture)
	runPodman(ctx, exec, "artifact", "push", "--authfile", auth, volume)
}

func runPodman(ctx context.Context, exec executer.Executer, args ...string) {
	_, stderr, code := exec.ExecuteWithContext(ctx, "podman", args...)
	Expect(code).To(BeZero(), "podman %s: %s", args[0], stderr)
}

func readRegistryLogs(ctx context.Context, registry testcontainers.Container) string {
	logs, err := registry.Logs(ctx)
	Expect(err).NotTo(HaveOccurred())
	defer logs.Close()
	data, err := io.ReadAll(logs)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}
