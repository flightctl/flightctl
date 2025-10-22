# install observability

# Copy sos report flightctl plugin
mkdir -p %{buildroot}/usr/share/sosreport
cp packaging/sosreport/sos/report/plugins/flightctl.py %{buildroot}/usr/share/sosreport

# Create target directories within the build root (where files are staged for RPM)
mkdir -p %{buildroot}/etc/flightctl/scripts
mkdir -p %{buildroot}/etc/flightctl/telemetry-gateway
mkdir -p %{buildroot}/etc/flightctl/definitions
mkdir -p %{buildroot}/etc/containers/systemd
mkdir -p %{buildroot}/etc/prometheus
mkdir -p %{buildroot}/etc/grafana/provisioning/datasources
mkdir -p %{buildroot}/etc/grafana/provisioning/dashboards/flightctl
mkdir -p %{buildroot}/etc/grafana/certs
mkdir -p %{buildroot}/var/lib/prometheus
mkdir -p %{buildroot}/var/lib/grafana # For Grafana's data
mkdir -p %{buildroot}/opt/flightctl-observability/templates # Staging for template files processed in %post
mkdir -p %{buildroot}/usr/bin # For the reloader script
mkdir -p %{buildroot}/usr/lib/systemd/system # For systemd units

# Install pre-upgrade helper script to libexec
mkdir -p %{buildroot}%{_libexecdir}/flightctl
install -Dpm 0755 deploy/scripts/pre-upgrade-dry-run.sh %{buildroot}%{_libexecdir}/flightctl/pre-upgrade-dry-run.sh

# Copy static configuration files (those not templated)
install -m 0644 packaging/observability/prometheus.yml %{buildroot}/etc/prometheus/

# Copy template source files to a temporary staging area for processing in %%post
install -m 0644 packaging/observability/grafana.ini.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-grafana.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-prometheus.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-telemetry-gateway.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-telemetry-gateway-config.yaml.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-userinfo-proxy.container.template %{buildroot}/opt/flightctl-observability/templates/

# Copy non-templated Grafana datasource provisioning file
install -m 0644 packaging/observability/grafana-datasources.yaml %{buildroot}/etc/grafana/provisioning/datasources/prometheus.yaml

install -m 0644 packaging/observability/grafana-dashboards.yaml %{buildroot}/etc/grafana/provisioning/dashboards/flightctl.yaml

# Copy the reloader script and its systemd units
install -m 0755 packaging/observability/render-templates.sh %{buildroot}/etc/flightctl/scripts
install -m 0755 test/scripts/setup_telemetry_gateway_certs.sh %{buildroot}/etc/flightctl/scripts
install -m 0755 test/scripts/functions %{buildroot}/etc/flightctl/scripts

install -m 0755 packaging/observability/flightctl-render-observability %{buildroot}/usr/bin/
install -m 0644 packaging/observability/observability.defs %{buildroot}/etc/flightctl/definitions/
install -m 0644 packaging/observability/telemetry-gateway.defs %{buildroot}/etc/flightctl/definitions/

# Note: flightctl network is provided by flightctl-services package

# Install systemd targets for service grouping
install -m 0644 packaging/observability/flightctl-telemetry-gateway.target %{buildroot}/usr/lib/systemd/system/
install -m 0644 packaging/observability/flightctl-observability.target %{buildroot}/usr/lib/systemd/system/
