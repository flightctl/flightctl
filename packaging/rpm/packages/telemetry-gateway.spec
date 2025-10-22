# telemetry-gateway sub-package
%package telemetry-gateway
Summary: Telemetry Gateway for FlightCtl
Requires:       podman
Requires:       python3-pyyaml
Requires(post): python3-pyyaml gettext
%{?systemd_requires}
Requires:       selinux-policy-targeted

%description telemetry-gateway
This package provides the FlightCtl Telemetry Gateway for telemetry collection/forwarding.
It runs in a Podman container managed by systemd and can be installed
independently of core FlightCtl services. Includes certificate tooling for Podman/Kubernetes.

%install
mkdir -p %{buildroot}/etc/flightctl/telemetry-gateway
install -m 0644 packaging/observability/flightctl-telemetry-gateway.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-telemetry-gateway-config.yaml.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/telemetry-gateway.defs %{buildroot}/etc/flightctl/definitions/
install -m 0644 packaging/observability/flightctl-telemetry-gateway.target %{buildroot}/usr/lib/systemd/system/

%files telemetry-gateway
  # Telemetry Gateway specific files
  /opt/flightctl-observability/templates/flightctl-telemetry-gateway.container.template
  /opt/flightctl-observability/templates/flightctl-telemetry-gateway-config.yaml.template

  # Shared rendering infrastructure for telemetry-gateway
  /etc/flightctl/scripts/render-templates.sh
  /etc/flightctl/scripts/setup_telemetry_gateway_certs.sh
  /etc/flightctl/scripts/functions
  /etc/flightctl/definitions/telemetry-gateway.defs

  # Configuration management script - needed for standalone telemetry-gateway deployment
  /usr/bin/flightctl-render-observability

  # Note: Uses flightctl network from flightctl-services package

  # Systemd target for service grouping
  /usr/lib/systemd/system/flightctl-telemetry-gateway.target

  # Directories owned by the telemetry-gateway RPM
  %dir /opt/flightctl-observability/templates
  %dir /etc/flightctl
  %dir /etc/flightctl/telemetry-gateway
  %dir /etc/flightctl/scripts
  %dir /etc/flightctl/definitions

  # Ghost file for generated container file
  %ghost /etc/containers/systemd/flightctl-telemetry-gateway.container
  %ghost /etc/flightctl/telemetry-gateway/config.yaml

%pre telemetry-gateway
  # This script runs BEFORE the files are installed onto the system.
  echo "Preparing to install FlightCtl Telemetry Gateway..."
  echo "Note: OpenTelemetry collector can be installed independently of other FlightCtl services."

%post telemetry-gateway
  # This script runs AFTER the files have been installed onto the system.
  echo "Running post-install actions for FlightCtl Telemetry Gateway..."

  # Create necessary directories on the host if they don't already exist.
  /usr/bin/mkdir -p /opt/flightctl-observability/templates
  /usr/bin/mkdir -p /etc/flightctl /etc/flightctl/scripts /etc/flightctl/definitions /etc/flightctl/telemetry-gateway


  # Apply persistent SELinux contexts for volumes and configuration files.
  /usr/sbin/semanage fcontext -a -t container_file_t "/opt/flightctl-observability/templates(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -a -t container_file_t "/usr/bin/flightctl-render-observability" >/dev/null 2>&1 || :

  # Restore file contexts based on the new rules (and default rules)
  /usr/sbin/restorecon -RvF /opt/flightctl-observability/templates >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /usr/bin/flightctl-render-observability >/dev/null 2>&1 || :

  # Enable specific SELinux boolean if needed
  /usr/sbin/setsebool -P container_manage_cgroup on >/dev/null 2>&1 || :

  # Generate OpenTelemetry collector container file from template
  echo "Generating OpenTelemetry collector container configuration..."
  CONFIG_FILE="/etc/flightctl/service-config.yaml"
  TEMPLATES_DIR="/opt/flightctl-observability/templates"
  DEFINITIONS_FILE="/etc/flightctl/definitions/telemetry-gateway.defs"

  # Source shared logic and call rendering with telemetry-gateway specific definitions
  if [ -f "/etc/flightctl/scripts/render-templates.sh" ]; then
      source /etc/flightctl/scripts/render-templates.sh
      render_templates "$CONFIG_FILE" "$TEMPLATES_DIR" "$DEFINITIONS_FILE" || { echo "ERROR: OpenTelemetry collector config generation failed!"; exit 1; }
  else
      echo "ERROR: render-templates.sh not found!"
      exit 1
  fi

  # Final service management
  echo "Reloading systemd daemon..."
  /usr/bin/systemctl daemon-reload

  echo "FlightCtl Telemetry Gateway installed. Service is configured but not started."
  echo "To render config: sudo flightctl-render-observability"
  echo "To start services: sudo systemctl start flightctl-telemetry-gateway.target"
  echo "For automatic startup: sudo systemctl enable flightctl-telemetry-gateway.target"

%preun telemetry-gateway
  echo "Running pre-uninstall actions for FlightCtl Telemetry Gateway..."
  # Stop and disable the target and services
  /usr/bin/systemctl stop flightctl-telemetry-gateway.target >/dev/null 2>&1 || :
  /usr/bin/systemctl disable flightctl-telemetry-gateway.target >/dev/null 2>&1 || :
  /usr/bin/systemctl stop flightctl-telemetry-gateway.service >/dev/null 2>&1 || :
  /usr/bin/systemctl disable flightctl-telemetry-gateway.service >/dev/null 2>&1 || :

%postun telemetry-gateway
  echo "Running post-uninstall actions for FlightCtl Telemetry Gateway..."
  # Clean up Podman container
  /usr/bin/podman rm -f flightctl-telemetry-gateway >/dev/null 2>&1 || :

  # Clean up Podman secrets created by the certificate setup script
  echo "Cleaning up Podman secrets..."
  if command -v podman >/dev/null 2>&1; then
      /usr/bin/podman secret rm telemetry-gateway-tls >/dev/null 2>&1 || :
      /usr/bin/podman secret rm telemetry-gateway-tls-key >/dev/null 2>&1 || :
      /usr/bin/podman secret rm flightctl-ca-secret >/dev/null 2>&1 || :
      echo "Podman secrets cleanup completed"

  else
      echo "Podman not available, skipping cleanup"
  fi

  # Remove SELinux fcontext rules added by this package
  /usr/sbin/semanage fcontext -d -t container_file_t "/opt/flightctl-observability/templates(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/usr/bin/flightctl-render-observability" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/usr/bin/" >/dev/null 2>&1 || :

  # Restore default SELinux contexts for affected directories
  /usr/sbin/restorecon -RvF /opt/flightctl-observability/templates >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /usr/bin/flightctl-render-observability >/dev/null 2>&1 || :

  /usr/bin/systemctl daemon-reload
  echo "FlightCtl Telemetry Gateway uninstalled."
