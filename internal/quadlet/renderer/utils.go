package renderer

const (
	// Default installation paths
	DefaultQuadletDir         = "/usr/share/containers/systemd"
	DefaultReadOnlyConfigDir  = "/usr/share/flightctl"
	DefaultWriteableConfigDir = "/etc/flightctl"
	DefaultSystemdUnitDir     = "/usr/lib/systemd/system"
	DefaultBinDir             = "/usr/bin"

	// Systemd target and network names
	SystemdTargetName = "flightctl.target"
	PodmanNetworkName = "flightctl"
)

// KnownVolumes lists volume names created by the standalone installation
var KnownVolumes = []string{
	"flightctl-db",
	"flightctl-kv",
	"flightctl-alertmanager",
	"flightctl-ui-certs",
	"flightctl-cli-artifacts-certs",
	"flightctl-pam-issuer-etc",
}

// KnownSecrets lists secret names created by the standalone installation
var KnownSecrets = []string{
	"flightctl-postgresql-password",
	"flightctl-postgresql-master-password",
	"flightctl-postgresql-user-password",
	"flightctl-postgresql-migrator-password",
	"flightctl-kv-password",
}
