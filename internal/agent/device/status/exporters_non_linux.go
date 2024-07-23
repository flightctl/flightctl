//go:build !linux
// +build !linux

package status

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

func newExporters(
	resourceManager resource.Manager,
	executer executer.Executer,
	log *log.PrefixLogger,
) []Exporter {
	return []Exporter{
		newSystemD(executer),
		newContainer(executer),
		newSystemInfo(executer),
		newUnsupportedExporter(log, "resources"),
	}
}

func getBootID(_ string) (string, error) {
	return "", nil
}

func newUnsupportedExporter(log *log.PrefixLogger, name string) Exporter {
	log.Warnf("Status exporter %q is not supported on this platform", name)
	return &unsupportedExporter{}
}

type unsupportedExporter struct {
}

func (u *unsupportedExporter) Export(context.Context, *v1alpha1.DeviceStatus) error {
	return nil
}

func (u *unsupportedExporter) SetProperties(*v1alpha1.RenderedDeviceSpec) {
}
