package os

import (
	"context"
	"errors"
	"os/exec"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// ModeBootc is the management mode for bootc-managed systems.
	ModeBootc = "bootc"
	// ModeRPMOSTree is the management mode for rpm-ostree-managed systems.
	ModeRPMOSTree = "rpm-ostree"
	// ModePackage is the management mode for package-managed systems.
	ModePackage = "package-mode"
)

// ErrOSUpdateNotSupported is returned when an OS image operation is
// attempted on a package-mode device that does not support OS updates.
var ErrOSUpdateNotSupported = errors.New("OS updates are not supported on package-mode devices")

func NewClient(log *log.PrefixLogger, exec executer.Executer, reader fileio.Reader) Client {
	switch {
	case isBinaryAvailable("bootc"):
		log.Infof("OS managed by bootc client")
		return newBootcClient(log, exec, reader)
	case isBinaryAvailable("rpm-ostree"):
		log.Infof("OS managed by rpm-ostree client")
		return newRpmOSTreeClient(exec, reader)
	default:
		log.Infof("OS managed by package-mode client")
		return newPackageModeClient(log, reader)
	}
}

func isBinaryAvailable(binaryName string) bool {
	_, err := exec.LookPath(binaryName)
	return err == nil
}

func newBootcClient(log *log.PrefixLogger, exec executer.Executer, reader fileio.Reader) *bootc {
	return &bootc{
		client: client.NewBootc(log, exec),
		reader: reader,
	}
}

type bootc struct {
	client client.Bootc
	reader fileio.Reader
}

func (b *bootc) Mode() string {
	return ModeBootc
}

func (b *bootc) Status(ctx context.Context) (*Status, error) {
	host, err := b.client.Status(ctx)
	if err != nil {
		return nil, err
	}

	osName, osVersion := readOSInfo(b.reader)
	return &Status{
		BootcHost:      *host,
		ManagementMode: b.Mode(),
		OSName:         osName,
		OSVersion:      osVersion,
	}, nil
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

func newRpmOSTreeClient(exec executer.Executer, reader fileio.Reader) *rpmOSTree {
	return &rpmOSTree{
		client: client.NewRPMOSTree(exec),
		reader: reader,
	}
}

type rpmOSTree struct {
	client *client.RPMOSTree
	reader fileio.Reader
}

func (r *rpmOSTree) Mode() string {
	return ModeRPMOSTree
}

func (r *rpmOSTree) Status(ctx context.Context) (*Status, error) {
	host, err := r.client.Status(ctx)
	if err != nil {
		return nil, err
	}

	osName, osVersion := readOSInfo(r.reader)
	return &Status{
		BootcHost:      *host,
		ManagementMode: r.Mode(),
		OSName:         osName,
		OSVersion:      osVersion,
	}, nil
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

func newPackageModeClient(log *log.PrefixLogger, reader fileio.Reader) *packageMode {
	return &packageMode{
		log:    log,
		reader: reader,
	}
}

// packageMode client for systems without bootc or rpm-ostree.
type packageMode struct {
	log    *log.PrefixLogger
	reader fileio.Reader
}

func (p *packageMode) Mode() string {
	return ModePackage
}

func (p *packageMode) Status(ctx context.Context) (*Status, error) {
	osName, osVersion := readOSInfo(p.reader)
	return &Status{
		ManagementMode: p.Mode(),
		OSName:         osName,
		OSVersion:      osVersion,
	}, nil
}

func (p *packageMode) Switch(ctx context.Context, image string) error {
	return ErrOSUpdateNotSupported
}

func (p *packageMode) Rollback(ctx context.Context) error {
	return ErrOSUpdateNotSupported
}

func (p *packageMode) Apply(ctx context.Context) error {
	return ErrOSUpdateNotSupported
}

// readOSInfo reads OS name and version from /etc/os-release.
// Returns empty strings on failure (non-fatal for image-mode clients).
func readOSInfo(reader fileio.Reader) (name, version string) {
	osInfo, err := ParseOSRelease(reader)
	if err != nil {
		return "", ""
	}
	return osInfo["NAME"], osInfo["VERSION_ID"]
}
