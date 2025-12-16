package renderer

import "path/filepath"

func servicesManifest(config *RendererConfig) []InstallAction {
	return []InstallAction{
		// API service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-api/flightctl-api.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-api.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-api/flightctl-api-init.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-api-init.container"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-api/flightctl-api-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-api/"), Template: false, Mode: RegularFileMode},

		// Periodic service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-periodic/flightctl-periodic.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-periodic.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-periodic/flightctl-periodic-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-periodic/"), Template: false, Mode: RegularFileMode},

		// Worker service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-worker/flightctl-worker.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-worker.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-worker/flightctl-worker-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-worker/"), Template: false, Mode: RegularFileMode},

		// Alert Exporter service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-alert-exporter/flightctl-alert-exporter.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-alert-exporter.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-alert-exporter/flightctl-alert-exporter-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-alert-exporter/"), Template: false, Mode: RegularFileMode},

		// PAM Issuer service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-pam-issuer/flightctl-pam-issuer.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-pam-issuer.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-pam-issuer/flightctl-pam-issuer-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-pam-issuer/"), Template: false, Mode: RegularFileMode},

		// Database service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-db/flightctl-db.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-db.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-db/flightctl-db.volume", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-db.volume"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-db/flightctl-db-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-db/"), Template: false, Mode: RegularFileMode},

		// Database migration services
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-db-migrate/flightctl-db-migrate.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-db-migrate.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-db-migrate/flightctl-db-wait.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-db-wait.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-db-migrate/flightctl-db-users-init.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-db-users-init.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-db-migrate/flightctl-db-migrate-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-db-migrate/"), Template: false, Mode: RegularFileMode},

		// KV service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-kv/flightctl-kv.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-kv.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-kv/flightctl-kv.volume", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-kv.volume"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-kv/flightctl-kv-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-kv/"), Template: false, Mode: RegularFileMode},

		// Alertmanager service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-alertmanager/flightctl-alertmanager.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-alertmanager.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-alertmanager/flightctl-alertmanager.volume", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-alertmanager.volume"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-alertmanager/flightctl-alertmanager-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-alertmanager/"), Template: false, Mode: RegularFileMode},

		// Alertmanager Proxy service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-alertmanager-proxy/flightctl-alertmanager-proxy.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-alertmanager-proxy.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-alertmanager-proxy/flightctl-alertmanager-proxy-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-alertmanager-proxy/"), Template: false, Mode: RegularFileMode},

		// UI service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-ui/flightctl-ui.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-ui.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-ui/flightctl-ui-init.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-ui-init.container"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-ui/flightctl-ui-certs.volume", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-ui-certs.volume"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-ui/flightctl-ui-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-ui/"), Template: false, Mode: RegularFileMode},

		// CLI Artifacts service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-cli-artifacts/flightctl-cli-artifacts.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-cli-artifacts.container"), Template: true, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-cli-artifacts/flightctl-cli-artifacts-init.container", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-cli-artifacts-init.container"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-cli-artifacts/flightctl-cli-artifacts-certs.volume", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl-cli-artifacts-certs.volume"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyDir, Source: "deploy/podman/flightctl-cli-artifacts/flightctl-cli-artifacts-config/", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "flightctl-cli-artifacts/"), Template: false, Mode: RegularFileMode},

		// Certs init service
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl-certs-init.service", Destination: filepath.Join(config.SystemdUnitOutputDir, "flightctl-certs-init.service"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/helm/flightctl/scripts/generate-certificates.sh", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "generate-certificates.sh"), Template: false, Mode: ExecutableFileMode},

		// Shared files
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl.network", Destination: filepath.Join(config.QuadletFilesOutputDir, "flightctl.network"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/flightctl.target", Destination: filepath.Join(config.SystemdUnitOutputDir, "flightctl.target"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/podman/service-config.yaml", Destination: filepath.Join(config.WriteableConfigOutputDir, "service-config.yaml"), Template: false, Mode: RegularFileMode},

		// Helper scripts
		{Action: ActionCopyFile, Source: "deploy/scripts/init_utils.sh", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "init_utils.sh"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/scripts/init_host.sh", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "init_host.sh"), Template: false, Mode: ExecutableFileMode},
		{Action: ActionCopyFile, Source: "deploy/scripts/secrets.sh", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "secrets.sh"), Template: false, Mode: ExecutableFileMode},
		{Action: ActionCopyFile, Source: "deploy/scripts/yaml_helpers.py", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "yaml_helpers.py"), Template: false, Mode: RegularFileMode},
		{Action: ActionCopyFile, Source: "deploy/scripts/init_certs.sh", Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "init_certs.sh"), Template: false, Mode: ExecutableFileMode},

		// Standalone binary
		{Action: ActionCopyFile, Source: "bin/flightctl-standalone", Destination: filepath.Join(config.BinOutputDir, "flightctl-standalone"), Template: false, Mode: ExecutableFileMode},

		// Empty files
		{Action: ActionCreateEmptyFile, Destination: filepath.Join(config.WriteableConfigOutputDir, "ssh", "known_hosts"), Mode: RegularFileMode},

		// Empty directories
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "pki"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "pki", "flightctl-api"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "pki", "flightctl-alertmanager-proxy"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "pki", "flightctl-pam-issuer"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "pki", "db"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "flightctl-api"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "flightctl-worker"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "flightctl-periodic"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "flightctl-alert-exporter"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "flightctl-ui"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "flightctl-cli-artifacts"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "flightctl-alertmanager-proxy"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "flightctl-pam-issuer"), Mode: ExecutableFileMode},
		{Action: ActionCreateEmptyDir, Destination: filepath.Join(config.WriteableConfigOutputDir, "flightctl-db-migrate"), Mode: ExecutableFileMode},
	}
}
