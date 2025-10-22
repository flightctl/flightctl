# observability sub-package
%package observability
Summary: Complete FlightCtl observability stack
Requires:       flightctl-telemetry-gateway = %{version}-%{release}
Requires:       /usr/sbin/semanage
Requires:       /usr/sbin/restorecon
Requires:       podman
Requires:       systemd
Requires(post): python3-pyyaml gettext
%{?systemd_requires}
Requires:       selinux-policy-targeted

%description observability
This package provides the complete FlightCtl Observability Stack, including
Prometheus for metric storage, Grafana for visualization, and
Telemetry Gateway for metric collection. All components run in Podman containers
managed by systemd and can be installed independently without requiring core FlightCtl
services to be running. This package automatically includes the flightctl-telemetry-gateway package.

%install
mkdir -p %{buildroot}/usr/share/sosreport
cp packaging/sosreport/sos/report/plugins/flightctl.py %{buildroot}/usr/share/sosreport
mkdir -p %{buildroot}/etc/flightctl/scripts
mkdir -p %{buildroot}/etc/flightctl/definitions
mkdir -p %{buildroot}/etc/containers/systemd
mkdir -p %{buildroot}/etc/prometheus
mkdir -p %{buildroot}/etc/grafana/provisioning/datasources
mkdir -p %{buildroot}/etc/grafana/provisioning/dashboards/flightctl
mkdir -p %{buildroot}/etc/grafana/certs
mkdir -p %{buildroot}/var/lib/prometheus
mkdir -p %{buildroot}/var/lib/grafana
mkdir -p %{buildroot}/opt/flightctl-observability/templates
mkdir -p %{buildroot}/usr/bin
mkdir -p %{buildroot}/usr/lib/systemd/system
mkdir -p %{buildroot}%{_libexecdir}/flightctl
install -Dpm 0755 deploy/scripts/pre-upgrade-dry-run.sh %{buildroot}%{_libexecdir}/flightctl/pre-upgrade-dry-run.sh
install -m 0644 packaging/observability/prometheus.yml %{buildroot}/etc/prometheus/
install -m 0644 packaging/observability/grafana.ini.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-grafana.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-prometheus.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-userinfo-proxy.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/grafana-datasources.yaml %{buildroot}/etc/grafana/provisioning/datasources/prometheus.yaml
install -m 0644 packaging/observability/grafana-dashboards.yaml %{buildroot}/etc/grafana/provisioning/dashboards/flightctl.yaml
install -m 0755 packaging/observability/render-templates.sh %{buildroot}/etc/flightctl/scripts
install -m 0755 test/scripts/setup_telemetry_gateway_certs.sh %{buildroot}/etc/flightctl/scripts
install -m 0755 test/scripts/functions %{buildroot}/etc/flightctl/scripts
install -m 0755 packaging/observability/flightctl-render-observability %{buildroot}/usr/bin/
install -m 0644 packaging/observability/observability.defs %{buildroot}/etc/flightctl/definitions/
install -m 0644 packaging/observability/flightctl-observability.target %{buildroot}/usr/lib/systemd/system/

%files observability
  # Static configuration files (Prometheus and Grafana only)
  /etc/prometheus/prometheus.yml

  /etc/flightctl/scripts/render-templates.sh
  /etc/flightctl/definitions/observability.defs

  # Template source files (Prometheus, Grafana, and UserInfo Proxy)
  /opt/flightctl-observability/templates/grafana.ini.template
  /opt/flightctl-observability/templates/flightctl-grafana.container.template
  /opt/flightctl-observability/templates/flightctl-prometheus.container.template
  /opt/flightctl-observability/templates/flightctl-userinfo-proxy.container.template

  /etc/grafana/provisioning/datasources/prometheus.yaml

  /etc/grafana/provisioning/dashboards/flightctl.yaml

  # The files that will be generated in %%post must be listed as %%ghost files.
  %ghost /etc/grafana/grafana.ini
  %ghost /etc/containers/systemd/flightctl-grafana.container
  %ghost /etc/containers/systemd/flightctl-prometheus.container
  %ghost /etc/containers/systemd/flightctl-userinfo-proxy.container

  # Configuration management script
  /usr/bin/flightctl-render-observability

  # Systemd target for full observability stack
  /usr/lib/systemd/system/flightctl-observability.target

  # Directories owned by the observability RPM (Prometheus and Grafana only)
  %dir /etc/prometheus
  %dir /etc/grafana
  %dir /etc/grafana/provisioning
  %dir /etc/grafana/provisioning/datasources
  %dir /etc/grafana/provisioning/dashboards
  %dir /etc/grafana/provisioning/dashboards/flightctl
  %dir /etc/grafana/certs
  %dir /var/lib/prometheus
  %dir /var/lib/grafana
  %dir /etc/flightctl
  %dir /etc/flightctl/scripts
  %dir /etc/flightctl/definitions

%pre observability
  # This script runs BEFORE the files are installed onto the system.
  echo "Preparing to install FlightCtl Observability Stack..."
  echo "Note: Observability stack can be installed independently of other FlightCtl services."

%post observability
  # This script runs AFTER the files have been installed onto the system.
  echo "Running post-install actions for Flightctl Observability Stack..."

  # Create necessary directories on the host if they don't already exist.
  /usr/bin/mkdir -p /etc/prometheus /var/lib/prometheus
  /usr/bin/mkdir -p /etc/grafana /etc/grafana/provisioning /etc/grafana/provisioning/datasources /var/lib/grafana
  /usr/bin/mkdir -p /etc/grafana/provisioning/dashboards /etc/grafana/provisioning/dashboards/flightctl
  /usr/bin/mkdir -p /etc/grafana/certs
  /usr/bin/mkdir -p /etc/flightctl /opt/flightctl-observability/templates
  /usr/bin/mkdir -p /usr/bin /usr/lib/systemd/system
  /usr/bin/mkdir -p /etc/flightctl/scripts
  /usr/bin/mkdir -p /etc/flightctl/definitions

  chown 65534:65534 /var/lib/prometheus
  chown 472:472 /var/lib/grafana

  # Apply persistent SELinux contexts for volumes and configuration files.
  /usr/sbin/semanage fcontext -a -t container_file_t "/etc/prometheus/prometheus.yml" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -a -t container_file_t "/var/lib/prometheus(/.*)?" >/dev/null 2>&1 || :

  /usr/sbin/semanage fcontext -a -t container_file_t "/etc/grafana(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -a -t container_file_t "/var/lib/grafana(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -a -t container_file_t "/etc/grafana/certs(/.*)?" >/dev/null 2>&1 || :

  /usr/sbin/semanage fcontext -a -t container_file_t "/opt/flightctl-observability/templates(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -a -t container_file_t "/usr/bin/flightctl-render-observability" >/dev/null 2>&1 || :

  # Restore file contexts based on the new rules (and default rules)
  /usr/sbin/restorecon -RvF /etc/prometheus >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /var/lib/prometheus >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /etc/grafana >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /var/lib/grafana >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /etc/grafana/certs >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /opt/flightctl-observability/templates >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /usr/bin/flightctl-render-observability >/dev/null 2>&1 || :

  # Enable specific SELinux boolean if needed
  /usr/sbin/setsebool -P container_manage_cgroup on >/dev/null 2>&1 || :

  # --- Process Configuration Templates (Initial Generation) ---
  # Call the basic config reloader script once during installation to generate initial config files.
  # Note: We use the basic reloader here because FlightCtl services aren't running yet during installation.
  echo "Generating initial configuration files..."
  CONFIG_FILE="/etc/flightctl/service-config.yaml"
  TEMPLATES_DIR="/opt/flightctl-observability/templates"
  DEFINITIONS_FILE="/etc/flightctl/definitions/observability.defs"

  # Source shared logic and call rendering without restarting services
  if [ -f "/etc/flightctl/scripts/render-templates.sh" ]; then
      source /etc/flightctl/scripts/render-templates.sh
      render_templates "$CONFIG_FILE" "$TEMPLATES_DIR" "$DEFINITIONS_FILE" || { echo "ERROR: Initial config generation failed!"; exit 1; }
  else
      echo "ERROR: render-templates.sh not found!"
      exit 1
  fi

  # --- Final service management ---
  echo "Reloading systemd daemon..."
  /usr/bin/systemctl daemon-reload

  echo "Flightctl Observability Stack services installed. Services are configured but not started."
  echo "To render config: sudo flightctl-render-observability"
  echo "To start services: sudo systemctl start flightctl-observability.target"
  echo "For automatic startup: sudo systemctl enable flightctl-observability.target"

%preun observability
  echo "Running pre-uninstall actions for Flightctl Observability Stack..."
  # Stop and disable the target and all services
  /usr/bin/systemctl stop flightctl-observability.target >/dev/null 2>&1 || :
  /usr/bin/systemctl disable flightctl-observability.target >/dev/null 2>&1 || :
  /usr/bin/systemctl stop flightctl-grafana.service >/dev/null 2>&1 || :
  /usr/bin/systemctl disable flightctl-grafana.service >/dev/null 2>&1 || :
  /usr/bin/systemctl stop flightctl-userinfo-proxy.service >/dev/null 2>&1 || :
  /usr/bin/systemctl disable flightctl-userinfo-proxy.service >/dev/null 2>&1 || :
  /usr/bin/systemctl stop flightctl-prometheus.service >/dev/null 2>&1 || :
  /usr/bin/systemctl disable flightctl-prometheus.service >/dev/null 2>&1 || :

%postun observability
  echo "Running post-uninstall actions for Flightctl Observability Stack..."
  # Clean up Podman containers associated with the services
  /usr/bin/podman rm -f flightctl-grafana >/dev/null 2>&1 || :
  /usr/bin/podman rm -f flightctl-userinfo-proxy >/dev/null 2>&1 || :
  /usr/bin/podman rm -f flightctl-prometheus >/dev/null 2>&1 || :

  # Note: Podman secrets are managed by the telemetry-gateway package
  # and will be cleaned up when that package is uninstalled

  # Remove SELinux fcontext rules added by this package
  /usr/sbin/semanage fcontext -d -t container_file_t "/etc/grafana(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/var/lib/grafana(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/etc/grafana/certs(/.*)?" >/dev/null 2>&1 || :

  /usr/sbin/semanage fcontext -d -t container_file_t "/etc/prometheus/prometheus.yml" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/var/lib/prometheus(/.*)?" >/dev/null 2>&1 || :

  /usr/sbin/semanage fcontext -d -t container_file_t "/opt/flightctl-observability/templates(/.*)?" >/dev/null 2>&1 || :
  /usr/sbin/semanage fcontext -d -t container_file_t "/usr/bin/flightctl-render-observability" >/dev/null 2>&1 || :

  # Restore default SELinux contexts for affected directories
  /usr/sbin/restorecon -RvF /etc/grafana >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /var/lib/grafana >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /etc/grafana/certs >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /etc/prometheus >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /var/lib/prometheus >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /opt/flightctl-observability/templates >/dev/null 2>&1 || :
  /usr/sbin/restorecon -RvF /usr/bin/flightctl-render-observability >/dev/null 2>&1 || :

  /usr/bin/systemctl daemon-reload
  echo "Flightctl Observability Stack uninstalled."
