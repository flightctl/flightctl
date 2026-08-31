package os

import (
	"context"
	"fmt"
	stdexec "os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const versionCmdTimeout = 10 * time.Second

func collectBootcVersion(ctx context.Context, lookPath func(string) (string, error), bootcVersion func(context.Context) (string, error)) string {
	if _, err := lookPath("bootc"); err != nil {
		return ""
	}
	return versionString(ctx, bootcVersion)
}

func collectOCIDelta(ctx context.Context, lookPath func(string) (string, error), ociDeltaVersion func(context.Context) (string, error)) (string, bool) {
	if _, err := lookPath("oci-delta"); err != nil {
		return "", false
	}
	return versionString(ctx, ociDeltaVersion), true
}

func versionString(ctx context.Context, fn func(context.Context) (string, error)) string {
	versionCtx, cancel := context.WithTimeout(ctx, versionCmdTimeout)
	defer cancel()
	out, err := fn(versionCtx)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(line)
}

func versionCmd(exec executer.Executer, name string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		stdout, stderr, exitCode := exec.ExecuteWithContext(ctx, name, "--version")
		if exitCode != 0 {
			return "", fmt.Errorf("%s --version: %s", name, stderr)
		}
		return stdout, nil
	}
}

func NewClient(log *log.PrefixLogger, exec executer.Executer) Client {
	switch {
	case isBinaryAvailable("bootc"):
		log.Infof("OS managed by bootc client")
		return newBootcClient(log, exec)
	case isBinaryAvailable("rpm-ostree"):
		log.Infof("OS managed by rpm-ostree client")
		return newRpmOSTreeClient(exec)
	default:
		log.Infof("package-mode / no image manager; using no-op OS client")
		return newDummyClient(log, exec)
	}
}

func isBinaryAvailable(binaryName string) bool {
	_, err := stdexec.LookPath(binaryName)
	return err == nil
}

func newBootcClient(log *log.PrefixLogger, exec executer.Executer) *bootc {
	return &bootc{
		client:          client.NewBootc(log, exec),
		lookPath:        stdexec.LookPath,
		bootcVersion:    versionCmd(exec, "bootc"),
		ociDeltaVersion: versionCmd(exec, "oci-delta"),
	}
}

type bootc struct {
	client          client.Bootc
	lookPath        func(string) (string, error)
	bootcVersion    func(context.Context) (string, error)
	ociDeltaVersion func(context.Context) (string, error)
}

func (b *bootc) Capabilities(ctx context.Context) Capabilities {
	ociVer, eligible := collectOCIDelta(ctx, b.lookPath, b.ociDeltaVersion)
	return Capabilities{
		OsMode:          v1beta1.OsModeImage,
		DeltaEligible:   eligible,
		BootcVersion:    collectBootcVersion(ctx, b.lookPath, b.bootcVersion),
		OCIDeltaVersion: ociVer,
	}
}

func (b *bootc) Status(ctx context.Context) (*Status, error) {
	status, err := b.client.Status(ctx)
	if err != nil {
		return nil, err
	}
	return &Status{*status}, nil
}

func (b *bootc) Switch(ctx context.Context, image string) error {
	return b.client.Switch(ctx, image)
}

func (b *bootc) Rollback(ctx context.Context) error {
	return b.client.Rollback(ctx)
}

func (b *bootc) Apply(ctx context.Context) error {
	return b.client.Apply(ctx)
}

func newRpmOSTreeClient(exec executer.Executer) *rpmOSTree {
	return &rpmOSTree{
		client:          client.NewRPMOSTree(exec),
		lookPath:        stdexec.LookPath,
		ociDeltaVersion: versionCmd(exec, "oci-delta"),
	}
}

type rpmOSTree struct {
	client          *client.RPMOSTree
	lookPath        func(string) (string, error)
	ociDeltaVersion func(context.Context) (string, error)
}

func (r *rpmOSTree) Status(ctx context.Context) (*Status, error) {
	status, err := r.client.Status(ctx)
	if err != nil {
		return nil, err
	}
	return &Status{*status}, nil
}

func (r *rpmOSTree) Switch(ctx context.Context, image string) error {
	return r.client.Switch(ctx, image)
}

func (r *rpmOSTree) Rollback(ctx context.Context) error {
	return r.client.Rollback(ctx)
}

func (r *rpmOSTree) Apply(ctx context.Context) error {
	return r.client.Apply(ctx)
}

func (r *rpmOSTree) Capabilities(ctx context.Context) Capabilities {
	ociVer, eligible := collectOCIDelta(ctx, r.lookPath, r.ociDeltaVersion)
	return Capabilities{
		OsMode:          v1beta1.OsModeImage,
		DeltaEligible:   eligible,
		OCIDeltaVersion: ociVer,
	}
}

func newDummyClient(log *log.PrefixLogger, exec executer.Executer) *dummy {
	return &dummy{
		log:             log,
		lookPath:        stdexec.LookPath,
		ociDeltaVersion: versionCmd(exec, "oci-delta"),
	}
}

// dummy client for package-mode (no image manager)
type dummy struct {
	log             *log.PrefixLogger
	lookPath        func(string) (string, error)
	ociDeltaVersion func(context.Context) (string, error)
}

func (d *dummy) Status(ctx context.Context) (*Status, error) {
	return &Status{container.BootcHost{}}, nil
}

func (d *dummy) Switch(ctx context.Context, image string) error {
	d.log.Debugf("Ignoring switch to image %s from dummy client for package-mode", image)
	return nil
}

func (d *dummy) Rollback(ctx context.Context) error {
	d.log.Debugf("Ignoring rollback and reboot from dummy client for package-mode")
	return nil
}

func (d *dummy) Apply(ctx context.Context) error {
	d.log.Debugf("Ignoring apply from dummy client for package-mode")
	return nil
}

func (d *dummy) Capabilities(ctx context.Context) Capabilities {
	ociVer, eligible := collectOCIDelta(ctx, d.lookPath, d.ociDeltaVersion)
	return Capabilities{
		OsMode:          v1beta1.OsModePackage,
		DeltaEligible:   eligible,
		OCIDeltaVersion: ociVer,
	}
}
