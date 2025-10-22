mkdir -p %{buildroot}/etc/flightctl/telemetry-gateway

install -m 0644 packaging/observability/flightctl-telemetry-gateway.container.template %{buildroot}/opt/flightctl-observability/templates/
install -m 0644 packaging/observability/flightctl-telemetry-gateway-config.yaml.template %{buildroot}/opt/flightctl-observability/templates/

install -m 0644 packaging/observability/telemetry-gateway.defs %{buildroot}/etc/flightctl/definitions/

install -m 0644 packaging/observability/flightctl-telemetry-gateway.target %{buildroot}/usr/lib/systemd/system/
