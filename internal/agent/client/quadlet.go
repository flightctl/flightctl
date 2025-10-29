package client

const (
	QuadletProjectLabelKey = "io.flightctl.quadlet.project"

	QuadletContainerExtension = ".container"
	QuadletVolumeExtension    = ".volume"
	QuadletNetworkExtension   = ".network"
	QuadletImageExtension     = ".image"
	QuadletPodExtension       = ".pod"

	QuadletContainerGroup = "Container"
	QuadletVolumeGroup    = "Volume"
	QuadletNetworkGroup   = "Network"
	QuadletImageGroup     = "Image"
	QuadletPodGroup       = "Pod"

	QuadletKeyLabel           = "Label"
	QuadletKeyImage           = "Image"
	QuadletKeyVolume          = "Volume"
	QuadletKeyEnvironmentFile = "EnvironmentFile"
	QuadletKeyNetwork         = "Network"
	QuadletKeyPod             = "Pod"
	QuadletKeyMount           = "Mount"
)

// QuadletSections maps Podman quadlet file extensions to their corresponding systemd unit section names.
// This mapping is used to identify valid quadlet files and determine which section name to use when
// creating or updating quadlet unit files.
// For more details on quadlet format, see: https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html
var QuadletSections = map[string]string{
	QuadletContainerExtension: QuadletContainerGroup,
	QuadletVolumeExtension:    QuadletVolumeGroup,
	QuadletNetworkExtension:   QuadletNetworkGroup,
	QuadletImageExtension:     QuadletImageGroup,
	QuadletPodExtension:       QuadletPodGroup,
}
