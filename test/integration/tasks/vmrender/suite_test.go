package vmrender_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/tasks"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	containers "github.com/flightctl/flightctl/test/harness/containers"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/testcontainers/testcontainers-go"
)

const (
	vmToQuadletImageRepo = "flightctl/vm-to-quadlet-test"
	vmToQuadletImageTag  = "latest"
)

var (
	suiteCtx      context.Context
	redisHost     string
	redisPort     uint
	redisPassword domain.SecureString
	redisCleanup  func()

	vmConverter     tasks.VmConverterFn
	vmBinaryCleanup func()
)

func TestVmRender(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "VmRender Suite")
}

// SynchronizedBeforeSuite ensures the expensive binary extraction runs only
// once (on proc 1). The resulting path is broadcast as []byte to all procs,
// which each initialise their own vmConverter, Redis connection, and tracer.
var _ = SynchronizedBeforeSuite(
	// Proc 1 only: extract the vm-to-quadlet binary from a container.
	func(ctx context.Context) []byte {
		Expect(integrationstack.EnsureRunning(ctx)).To(Succeed())
		binaryPath, cleanup, err := buildVmToQuadletBinary(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to extract vm-to-quadlet binary")
		vmBinaryCleanup = cleanup
		return []byte(binaryPath)
	},
	// All procs: receive the shared binary path; set up per-process state.
	func(ctx context.Context, binaryPathBytes []byte) {
		suiteCtx = testutil.InitSuiteTracerForGinkgo("VmRender Suite")
		Expect(integrationstack.EnsureRunning(ctx)).To(Succeed())

		var err error
		redisHost, redisPort, redisPassword, redisCleanup, err = testdb.CreateTestRedis(
			suiteCtx, flightlog.InitLogs())
		Expect(err).NotTo(HaveOccurred())

		vmConverter = tasks.NewVmConverter(string(binaryPathBytes))
	},
)

// SynchronizedAfterSuite mirrors the above: per-process cleanup first, then
// proc-1 teardown (binary container + temp dir) last.
var _ = SynchronizedAfterSuite(
	func() {
		if redisCleanup != nil {
			redisCleanup()
		}
	},
	func() {
		if vmBinaryCleanup != nil {
			vmBinaryCleanup()
		}
	},
)

// buildVmToQuadletBinary builds the vm-to-quadlet image from the
// Containerfile.vm-to-quadlet that lives alongside this test. The built image
// is kept so subsequent runs skip the build. The binary is copied out of the
// running container via CopyFileFromContainer, written to a temp directory, and
// the container is terminated. Returns the absolute binary path and a cleanup func.
func buildVmToQuadletBinary(ctx context.Context) (string, func(), error) {
	containers.ConfigureDockerHost()

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			// The Containerfile lives alongside this test file; Go tests run
			// with cwd set to the package directory.
			Context:    ".",
			Dockerfile: "Containerfile.vm-to-quadlet",
			Repo:       vmToQuadletImageRepo,
			Tag:        vmToQuadletImageTag,
			KeepImage:  true, // reuse across test runs; rebuilds only on Containerfile changes
		},
		// Keep the container alive so CopyFileFromContainer can read from it.
		Cmd: []string{"/bin/sh", "-c", "sleep infinity"},
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ProviderType:     containers.GetProviderType(),
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start vm-to-quadlet container: %w", err)
	}
	cleanup := func() {
		termCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.Terminate(termCtx)
	}

	rc, err := c.CopyFileFromContainer(ctx, "/usr/local/bin/vm-to-quadlet")
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("copy binary from container: %w", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("read binary: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "flightctl-vm-to-quadlet-*")
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	combinedCleanup := func() {
		cleanup()
		_ = os.RemoveAll(tmpDir)
	}

	binaryPath := filepath.Join(tmpDir, "vm-to-quadlet")
	if err := os.WriteFile(binaryPath, data, 0700); err != nil { //nolint:gosec // executable binary requires execute permission
		combinedCleanup()
		return "", nil, fmt.Errorf("write binary: %w", err)
	}
	return binaryPath, combinedCleanup, nil
}
