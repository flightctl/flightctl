package os

import (
	"context"
	"os/exec"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

// DetectMode determines the OS management mode by checking for image
// management binaries on PATH. If bootc or rpm-ostree is available, the
// mode is "image"; otherwise it is "package".
//
// lookPath should be exec.LookPath in production. Accepting it as a
// parameter enables deterministic unit testing without PATH manipulation.
//
// DetectMode and NewClient both check binary availability but via separate
// mechanisms — DetectMode uses the caller-supplied lookPath while NewClient
// uses isBinaryAvailable (os/exec.LookPath directly). Both run sequentially
// at startup in agent.go, so PATH cannot change between the two calls.
func DetectMode(lookPath func(string) (string, error)) v1beta1.OsModeType {
	if _, err := lookPath("bootc"); err == nil {
		return v1beta1.OsModeImage
	}
	if _, err := lookPath("rpm-ostree"); err == nil {
		return v1beta1.OsModeImage
	}
	return v1beta1.OsModePackage
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
		log.Warnf("OS not managed by any supported OS manager. Using dummy client.")
		return newDummyClient(log)
	}
}

func isBinaryAvailable(binaryName string) bool {
	_, err := exec.LookPath(binaryName)
	return err == nil
}

func newBootcClient(log *log.PrefixLogger, exec executer.Executer) *bootc {
	return &bootc{
		client: client.NewBootc(log, exec),
	}
}

type bootc struct {
	client client.Bootc
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
		client: client.NewRPMOSTree(exec),
	}
}

type rpmOSTree struct {
	client *client.RPMOSTree
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

func newDummyClient(log *log.PrefixLogger) *dummy {
	return &dummy{
		log: log,
	}
}

// dummy client for unsupported OS
type dummy struct {
	log *log.PrefixLogger
}

func (d *dummy) Status(ctx context.Context) (*Status, error) {
	return &Status{container.BootcHost{}}, nil
}

func (d *dummy) Switch(ctx context.Context, image string) error {
	d.log.Debugf("Ignoring switch to image %s from dummy client for unsupported OS", image)
	return nil
}

func (d *dummy) Rollback(ctx context.Context) error {
	d.log.Debugf("Ignoring rollback and reboot from dummy client for unsupported OS")
	return nil
}

func (d *dummy) Apply(ctx context.Context) error {
	d.log.Debugf("Ignoring apply from dummy client for unsupported OS")
	return nil
}
