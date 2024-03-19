package status

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/image"
	"github.com/flightctl/flightctl/pkg/executer"
)

var _ Exporter = (*Bootc)(nil)

type Bootc struct {
	client *image.BootcCmd
}

func newBootc(exec executer.Executer) *Bootc {
	return &Bootc{
		client: image.NewBootcCmd(exec),
	}
}

func (b *Bootc) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	host, err := b.client.Status(ctx)
	if err != nil {
		return err
	}

	status.Versions.OsBooted = host.BootedImage()
	status.Versions.OsStaged = host.StagedImage()
	status.Versions.OsRollback = host.RollbackImage()

	return nil
}
