# OpenTelemetry Collector sub-package
%package otel-collector
Summary: OpenTelemetry Collector for FlightCtl
Requires:       podman
Requires:       systemd
Requires:       yq
Requires(post): systemd, yq, gettext
Requires(preun):systemd
Requires(postun):systemd
Requires:       selinux-policy-targeted

%description otel-collector
This package provides the OpenTelemetry Collector for FlightCtl metric collection.
The collector runs in a Podman container managed by systemd and can be installed
and used independently without requiring core FlightCtl services to be running.

# OpenTelemetry Collector specific files
%files otel-collector
  /etc/otelcol/otelcol-config.yaml
  /opt/flightctl-observability/templates/flightctl-otel-collector.container.template

  # Shared rendering infrastructure for otel-collector
  /etc/flightctl/scripts/render-templates.sh
  /etc/flightctl/definitions/otel-collector.defs

  # Configuration management script - needed for standalone otel-collector deployment
  /usr/local/bin/flightctl-render-observability

  # Observability network quadlet
  /etc/containers/systemd/flightctl-observability.network

  # Systemd target for service grouping
  /usr/lib/systemd/system/flightctl-otel-collector.target

  # Directories owned by the otel-collector RPM
  %dir /etc/otelcol
  %dir /var/lib/otelcol
  %dir /opt/flightctl-observability/templates
  %dir /etc/flightctl
  %dir /etc/flightctl/scripts
  %dir /etc/flightctl/definitions

  # Ghost file for generated container file
  %ghost /etc/containers/systemd/flightctl-otel-collector.container

%pre otel-collector
  # This script runs BEFORE the files are installed onto the system.
  echo "Preparing to install FlightCtl OpenTelemetry Collector..."
  echo "Note: OpenTelemetry collector can be installed independently of other FlightCtl services."


%post otel-collector
  # This script runs AFTER the files have been installed onto the system.
  echo "Running post-install actions for FlightCtl OpenTelemetry Collector..."

  # Create necessary directories on the host if they don't already exist.
  /usr/bin/mkdir -p /etc/otelcol /var/lib/otelcol
  /usr/bin/mkdir -p /opt/flightctl-observability/templates
  /usr/bin/mkdir -p /etc/flightctl /etc/flightctl/scripts /etc/flightctl/definitions

  # Apply persistent SELinux contexts for volumes and configuration files.
  /usr/sbin/semanage fcontext -a -t container_file_t "/etc/otelcol/otelcol-config.yaml" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -a -t container_file_t "/var/lib/otelcol(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -a -t container_file_t "/opt/flightctl-observability/templates(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -a -t container_file_t "/usr/local/bin/flightctl-render-observability" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -a -t container_file_t "/usr/local/bin/" >/dev/null 2>&1 || :

  # Restore file contexts based on the new rules (and default rules)
  /usr/sbin/restorecon -RvF /etc/otelcol >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /var/lib/otelcol >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /opt/flightctl-observability/templates >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /usr/local/bin/flightctl-render-observability >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /usr/local/bin/ >/dev/null 2>&1 || :

  # Enable specific SELinux boolean if needed
  /usr/sbin/setsebool -P container_manage_cgroup on >/dev/null 2>&1 || :

  # Generate OpenTelemetry collector container file from template
  echo "Generating OpenTelemetry collector container configuration..."
  CONFIG_FILE="/etc/flightctl/service-config.yaml"
  TEMPLATES_DIR="/opt/flightctl-observability/templates"
  DEFINITIONS_FILE="/etc/flightctl/definitions/otel-collector.defs"

  # Source shared logic and call rendering with otel-collector specific definitions
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

  echo "FlightCtl OpenTelemetry Collector installed. Service is configured but not started."
  echo "To render config: sudo flightctl-render-observability"
  echo "To start services: sudo systemctl start flightctl-otel-collector.target"
  echo "For automatic startup: sudo systemctl enable flightctl-otel-collector.target"


%preun otel-collector
  echo "Running pre-uninstall actions for FlightCtl OpenTelemetry Collector..."
  # Stop and disable the target and services
  /usr/bin/systemctl stop flightctl-otel-collector.target >/dev/null 2>&1 || :
  /usr/bin/systemctl disable flightctl-otel-collector.target >/dev/null 2>&1 || :
  /usr/bin/systemctl stop flightctl-otel-collector.service >/dev/null 2>&1 || :
  /usr/bin/systemctl disable flightctl-otel-collector.service >/dev/null 2>&1 || :
  /usr/bin/systemctl stop flightctl-observability-network.service >/dev/null 2>&1 || :


%postun otel-collector
  echo "Running post-uninstall actions for FlightCtl OpenTelemetry Collector..."
  # Clean up Podman container
  /usr/bin/podman rm -f flightctl-otel-collector >/dev/null 2>&1 || :

  # Remove SELinux fcontext rules added by this package
  /usr/sbin/semanage fcontext -d -t container_file_t "/etc/otelcol/otelcol-config.yaml" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/var/lib/otelcol(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/opt/flightctl-observability/templates(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/usr/local/bin/flightctl-render-observability" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/usr/local/bin/" >/dev/null 2>&1 || :

  # Restore default SELinux contexts for affected directories
  /usr/sbin/restorecon -RvF /etc/otelcol >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /var/lib/otelcol >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /opt/flightctl-observability/templates >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /usr/local/bin/flightctl-render-observability >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /usr/local/bin/ >/dev/null 2>&1 || :

  /usr/bin/systemctl daemon-reload
  echo "FlightCtl OpenTelemetry Collector uninstalled."
