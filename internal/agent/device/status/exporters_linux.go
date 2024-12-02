//go:build linux

package status

import (
	"os"

	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

func newExporters(
	resourceManager resource.Manager,
	applicationManager applications.Manager,
	systemdManager systemd.Manager,
	executer executer.Executer,
	log *log.PrefixLogger,
) []Exporter {
	return []Exporter{
		newApplications(applicationManager),
		newSystemD(systemdManager),
		newSystemInfo(executer),
		newResources(log, resourceManager),
	}
}

func getBootID(bootIDPath string) (string, error) {
	bootID, err := os.ReadFile(bootIDPath)
	if err != nil {
		return "", err
	}
	return string(bootID), nil
}
