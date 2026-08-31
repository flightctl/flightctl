package infra

import (
	"fmt"
	"strings"
)

// DeltaWorkerRegistryRemapFiles returns registries.conf.d snippets that rewrite
// quay.io/flightctl image refs to registryURL (host:port).
func DeltaWorkerRegistryRemapFiles(registryURL string) (remap, insecure string) {
	host := registryURL
	if i := strings.LastIndex(registryURL, ":"); i >= 0 {
		host = registryURL[:i]
	}
	remap = fmt.Sprintf(`[[registry]]
prefix = "quay.io/flightctl/flightctl-device"
location = "%s/flightctl/flightctl-device"

[[registry]]
prefix = "quay.io/flightctl/sleep-app"
location = "%s/flightctl/sleep-app"

[[registry]]
prefix = "quay.io/flightctl/dummy-volume"
location = "%s/flightctl/dummy-volume"

[[registry]]
prefix = "quay.io/flightctl-private"
location = "%s:5002/flightctl"

[[registry]]
prefix = "quay.io/flightctl-tests"
location = "%s/flightctl-tests"
`, registryURL, registryURL, registryURL, host, registryURL)
	insecure = fmt.Sprintf(`[[registry]]
location = "%s"
insecure = true
`, registryURL)
	return remap, insecure
}
