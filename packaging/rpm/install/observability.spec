# install observability
# Create target directories within the build root (where files are staged for RPM)
mkdir -p %{buildroot}/etc/flightctl/scripts
mkdir -p %{buildroot}/etc/flightctl/definitions
mkdir -p %{buildroot}/etc/containers/systemd
mkdir -p %{buildroot}/etc/prometheus
mkdir -p %{buildroot}/etc/otelcol
mkdir -p %{buildroot}/etc/grafana/provisioning/datasources
mkdir -p %{buildroot}/etc/grafana/provisioning/dashboards/flightctl
mkdir -p %{buildroot}/etc/grafana/certs
mkdir -p %{buildroot}/var/lib/prometheus
mkdir -p %{buildroot}/var/lib/grafana # For Grafana's data
mkdir -p %{buildroot}/var/lib/otelcol
mkdir -p %{buildroot}/opt/flightctl-observability/templates # Staging for template files processed in %%post
mkdir -p %{buildroot}/usr/local/bin # For the reloader script
mkdir -p %{buildroot}/usr/lib/systemd/system # For systemd units

# Copy static configuration files (those not templated)
install -m 0644 packaging/observability/prometheus.yml %{buildroot}/etc/prometheus/
install -m 0644 packaging/observability/otelcol-config.yaml %{buildroot}/etc/otelcol/

# Copy template source files to a temporary staging area for processing in %%post
install -m 0644 packaging/observability/grafana.ini.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-grafana.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-prometheus.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-otel-collector.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-userinfo-proxy.container.template %{buildroot}/opt/flightctl-observability/templates/

# Copy non-templated Grafana datasource provisioning file
install -m 0644 packaging/observability/grafana-datasources.yaml %{buildroot}/etc/grafana/provisioning/datasources/prometheus.yaml

install -m 0644 packaging/observability/grafana-dashboards.yaml %{buildroot}/etc/grafana/provisioning/dashboards/flightctl.yaml

# Copy the reloader script and its systemd units
install -m 0755 packaging/observability/render-templates.sh %{buildroot}/etc/flightctl/scripts

install -m 0755 packaging/observability/flightctl-render-observability %{buildroot}/usr/local/bin/
install -m 0644 packaging/observability/observability.defs %{buildroot}/etc/flightctl/definitions/
install -m 0644 packaging/observability/otel-collector.defs %{buildroot}/etc/flightctl/definitions/

# Copy the observability network quadlet
install -m 0644 packaging/observability/flightctl-observability.network %{buildroot}/etc/containers/systemd/

# Install systemd targets for service grouping
install -m 0644 packaging/observability/flightctl-otel-collector.target %{buildroot}/usr/lib/systemd/system/
install -m 0644 packaging/observability/flightctl-observability.target %{buildroot}/usr/lib/systemd/system/

# Copy sos report flightctl plugin
mkdir -p %{buildroot}/usr/share/sosreport
cp packaging/sosreport/sos/report/plugins/flightctl.py %{buildroot}/usr/share/sosreport
