# Disable debug information package creation
%define debug_package %{nil}

# Define the Go Import Path
%global goipath github.com/flightctl/flightctl

# SELinux specifics
%global selinuxtype targeted
%define selinux_policyver 3.14.3-67

Name:           flightctl
# Version and Release are automatically updated by Packit during build
# Do not manually change these values - they will be overwritten
Version:        1.0.0
Release:        1.20251113140656860923.decoupled.builds.images.252.g5f96d2fc%{?dist}
Summary:        Flight Control service

%gometa

License:        Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND ISC AND MIT
URL:            %{gourl}

Source0:        flightctl-1.0.0.tar.gz

BuildRequires:  golang
BuildRequires:  make
BuildRequires:  git
BuildRequires:  openssl-devel
BuildRequires:  systemd-rpm-macros

Requires: openssl

%global flightctl_target flightctl.target

# --- Restart these on upgrade  ---
%global flightctl_services_restart flightctl-api.service flightctl-ui.service flightctl-worker.service flightctl-alertmanager.service flightctl-alert-exporter.service flightctl-alertmanager-proxy.service flightctl-cli-artifacts.service flightctl-periodic.service flightctl-db-migrate.service flightctl-db-wait.service


%description
# Main package is empty and not created.

# cli sub-package
%package cli
Summary: Flight Control CLI
Recommends: bash-completion
%description cli
flightctl is the CLI for controlling the Flight Control service.

# agent sub-package
%package agent
Summary: Flight Control management agent

Requires: flightctl-selinux = %{version}

%description agent
The flightctl-agent package provides the management agent for the Flight Control fleet management service.

# selinux sub-package
%package selinux
Summary: SELinux policies for the Flight Control management agent
BuildRequires: selinux-policy >= %{selinux_policyver}
BuildRequires: selinux-policy-devel >= %{selinux_policyver}
BuildRequires: container-selinux
BuildArch: noarch
Requires: selinux-policy >= %{selinux_policyver}

# For restorecon
Requires: policycoreutils
# For semanage
Requires: policycoreutils-python-utils
# For policy macros
Requires: container-selinux

%description selinux
The flightctl-selinux package provides the SELinux policy modules required by the Flight Control management agent.

# services sub-package
%package services
Summary: Flight Control services
Requires: bash
Requires: podman
Requires: python3-pyyaml
BuildRequires: systemd-rpm-macros
%{?systemd_requires}

%description services
The flightctl-services package provides installation and setup of files for running containerized Flight Control services

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
%dir /etc/grafana/provisioning/alerting
%dir /etc/grafana/provisioning/dashboards
%dir /etc/grafana/provisioning/dashboards/flightctl
%dir /etc/grafana/certs
%dir /var/lib/prometheus
%dir /var/lib/grafana
%dir /etc/flightctl
%dir /etc/flightctl/scripts
%dir /etc/flightctl/definitions


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


%pre observability
# This script runs BEFORE the files are installed onto the system.
echo "Preparing to install FlightCtl Observability Stack..."
echo "Note: Observability stack can be installed independently of other FlightCtl services."


%post observability
# This script runs AFTER the files have been installed onto the system.
echo "Running post-install actions for Flightctl Observability Stack..."

# Create necessary directories on the host if they don't already exist.
/usr/bin/mkdir -p /etc/prometheus /var/lib/prometheus
/usr/bin/mkdir -p /etc/grafana /etc/grafana/provisioning /etc/grafana/provisioning/datasources /etc/grafana/provisioning/alerting /var/lib/grafana
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

%prep
%goprep -A
%setup -q %{forgesetupargs} -n flightctl-1.0.0

%build
    # if this is a buggy version of go we need to set GOPROXY as workaround
    # see https://github.com/golang/go/issues/61928
    GOENVFILE=$(go env GOROOT)/go.env
    if [[ ! -f "${GOENVFILE}" ]]; then
        export GOPROXY='https://proxy.golang.org,direct'
    fi

    # Prefer values injected by Makefile/CI; fall back to RPM macros when unset
    SOURCE_GIT_TAG="%{?SOURCE_GIT_TAG:%{SOURCE_GIT_TAG}}%{!?SOURCE_GIT_TAG:%(./hack/current-version)}" \
    SOURCE_GIT_TREE_STATE="%{?SOURCE_GIT_TREE_STATE:%{SOURCE_GIT_TREE_STATE}}%{!?SOURCE_GIT_TREE_STATE:clean}" \
    SOURCE_GIT_COMMIT="%{?SOURCE_GIT_COMMIT:%{SOURCE_GIT_COMMIT}}%{!?SOURCE_GIT_COMMIT:%(echo %{version} | grep -o '[-~]g[0-9a-f]*' | sed 's/[-~]g//' || echo unknown)}" \
    SOURCE_GIT_TAG_NO_V="%{?SOURCE_GIT_TAG_NO_V:%{SOURCE_GIT_TAG_NO_V}}%{!?SOURCE_GIT_TAG_NO_V:%{version}}" \
    %if 0%{?rhel} == 9
        %make_build build-cli build-agent build-restore
    %else
        DISABLE_FIPS="true" %make_build build-cli build-agent build-restore
    %endif

    # SELinux modules build
    %make_build --directory packaging/selinux

%install
    mkdir -p %{buildroot}/usr/bin
    mkdir -p %{buildroot}/etc/flightctl
    cp bin/flightctl %{buildroot}/usr/bin
    cp bin/flightctl-restore %{buildroot}/usr/bin
    mkdir -p %{buildroot}/usr/lib/systemd/system
    mkdir -p %{buildroot}/usr/lib/tmpfiles.d
    mkdir -p %{buildroot}/usr/lib/flightctl/custom-info.d
    mkdir -p %{buildroot}/usr/lib/flightctl/hooks.d/{afterupdating,beforeupdating,afterrebooting,beforerebooting}
    mkdir -p %{buildroot}/usr/lib/greenboot/check/required.d
    install -m 0755 packaging/greenboot/flightctl-agent-running-check.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    cp bin/flightctl-agent %{buildroot}/usr/bin
    cp packaging/must-gather/flightctl-must-gather %{buildroot}/usr/bin
    cp packaging/hooks.d/afterupdating/00-default.yaml %{buildroot}/usr/lib/flightctl/hooks.d/afterupdating
    cp packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system
    echo "d /var/lib/flightctl 0755 root root -" > %{buildroot}/usr/lib/tmpfiles.d/flightctl.conf
    echo "# systemd-tmpfiles configuration for CentOS bootc buildinfo directories" > %{buildroot}/usr/lib/tmpfiles.d/centos-buildinfo.conf
    echo "d /var/roothome 0755 root root -" >> %{buildroot}/usr/lib/tmpfiles.d/centos-buildinfo.conf
    echo "d /var/roothome/buildinfo 0755 root root -" >> %{buildroot}/usr/lib/tmpfiles.d/centos-buildinfo.conf
    echo "d /var/roothome/buildinfo/content_manifests 0755 root root -" >> %{buildroot}/usr/lib/tmpfiles.d/centos-buildinfo.conf
    bin/flightctl completion bash > flightctl-completion.bash
    install -Dpm 0644 flightctl-completion.bash -t %{buildroot}/%{_datadir}/bash-completion/completions
    bin/flightctl completion fish > flightctl-completion.fish
    install -Dpm 0644 flightctl-completion.fish -t %{buildroot}/%{_datadir}/fish/vendor_completions.d/
    bin/flightctl completion zsh > _flightctl-completion
    install -Dpm 0644 _flightctl-completion -t %{buildroot}/%{_datadir}/zsh/site-functions/
    install -d %{buildroot}%{_datadir}/selinux/packages/%{selinuxtype}
    install -m644 packaging/selinux/*.bz2 %{buildroot}%{_datadir}/selinux/packages/%{selinuxtype}

    install -Dpm 0644 packaging/flightctl-services-install.conf %{buildroot}%{_sysconfdir}/flightctl/flightctl-services-install.conf

    # flightctl-services sub-package steps
    # Run the install script to move the quadlet files.
    #
    # The IMAGE_TAG is derived from the RPM version, which may include tildes (~)
    # for proper version sorting (e.g., 0.5.1~rc1-1). However, the tagged images
    # always use hyphens (-) instead of tildes (~). To ensure valid image tags we need
    # to transform the version string by replacing tildes with hyphens.
    CONFIG_READONLY_DIR="%{buildroot}%{_datadir}/flightctl" \
    CONFIG_WRITEABLE_DIR="%{buildroot}%{_sysconfdir}/flightctl" \
    QUADLET_FILES_OUTPUT_DIR="%{buildroot}%{_datadir}/containers/systemd" \
    SYSTEMD_UNIT_OUTPUT_DIR="%{buildroot}/usr/lib/systemd/system" \
    IMAGE_TAG=$(echo %{version} | tr '~' '-') \
    deploy/scripts/install.sh

    # Copy services must gather script
    cp packaging/must-gather/flightctl-services-must-gather %{buildroot}%{_bindir}

    # Copy sos report flightctl plugin
    mkdir -p %{buildroot}/usr/share/sosreport
    cp packaging/sosreport/sos/report/plugins/flightctl.py %{buildroot}/usr/share/sosreport

    # install observability
     # Create target directories within the build root (where files are staged for RPM)
     mkdir -p %{buildroot}/etc/flightctl/scripts
     mkdir -p %{buildroot}/etc/flightctl/telemetry-gateway
     mkdir -p %{buildroot}/etc/flightctl/definitions
     mkdir -p %{buildroot}/etc/containers/systemd
     mkdir -p %{buildroot}/etc/prometheus
     mkdir -p %{buildroot}/etc/grafana/provisioning/datasources
     mkdir -p %{buildroot}/etc/grafana/provisioning/alerting
     mkdir -p %{buildroot}/etc/grafana/provisioning/dashboards/flightctl
     mkdir -p %{buildroot}/etc/grafana/certs
     mkdir -p %{buildroot}/var/lib/prometheus
     mkdir -p %{buildroot}/var/lib/grafana # For Grafana's data
     mkdir -p %{buildroot}/opt/flightctl-observability/templates # Staging for template files processed in %%post
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

%check
    %{buildroot}%{_bindir}/flightctl-agent version


%pre selinux
%selinux_relabel_pre -s %{selinuxtype}

%post selinux
# Install SELinux module - if this fails, RPM installation will still continue
if ! semodule -s %{selinuxtype} -i %{_datadir}/selinux/packages/%{selinuxtype}/flightctl_agent.pp.bz2; then
    echo "ERROR: Failed to install flightctl SELinux policy (AST failure or compatibility issue)" >&2
    exit 1
fi

%postun selinux
if [ $1 -eq 0 ]; then
    semodule -s %{selinuxtype} -r flightctl_agent 2>/dev/null || :
fi

%posttrans selinux
%selinux_relabel_post -s %{selinuxtype}

# File listings
# No %%files section for the main package, so it won't be built

%files cli
    %{_bindir}/flightctl
    %{_bindir}/flightctl-restore
    %license LICENSE
    %{_datadir}/bash-completion/completions/flightctl-completion.bash
    %{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
    %{_datadir}/zsh/site-functions/_flightctl-completion

%files agent
    %license LICENSE
    %dir /etc/flightctl
    %{_bindir}/flightctl-agent
    %{_bindir}/flightctl-must-gather
    /usr/lib/flightctl/hooks.d/afterupdating/00-default.yaml
    /usr/lib/systemd/system/flightctl-agent.service
    /usr/lib/tmpfiles.d/flightctl.conf
    /usr/lib/tmpfiles.d/centos-buildinfo.conf
    /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    /usr/share/sosreport/flightctl.py

%post agent
# Ensure /var/lib/flightctl exists immediately for environments where systemd-tmpfiles succeeds or via fallback
# Try systemd-tmpfiles first, fall back to manual creation if it fails
/usr/bin/systemd-tmpfiles --create /usr/lib/tmpfiles.d/flightctl.conf || {
    mkdir -p /var/lib/flightctl && \
    chown root:root /var/lib/flightctl && \
    chmod 0755 /var/lib/flightctl
}

# These files prevent tmpfiles.d from managing the /var/roothome/buildinfo directory
rm -f /var/roothome/buildinfo/content_manifests/content-sets.json
rm -f /var/roothome/buildinfo/labels.json
# Remove the directories so tmpfiles.d can recreate them properly
rmdir /var/roothome/buildinfo/content_manifests 2>/dev/null || true
rmdir /var/roothome/buildinfo 2>/dev/null || true

INSTALL_DIR="/usr/lib/python$(python3 --version | sed 's/^.* \(3[.][0-9]*\).*$/\1/')/site-packages/sos/report/plugins"
mkdir -p $INSTALL_DIR
cp /usr/share/sosreport/flightctl.py $INSTALL_DIR
chmod 0644 $INSTALL_DIR/flightctl.py
rm -rf /usr/share/sosreport


%files selinux
%{_datadir}/selinux/packages/%{selinuxtype}/flightctl_agent.pp.bz2

%files services
    %defattr(0644,root,root,-)
    # Files mounted to system config
    %dir %{_sysconfdir}/flightctl
    %dir %{_sysconfdir}/flightctl/pki
    %dir %{_sysconfdir}/flightctl/flightctl-api
    %dir %{_sysconfdir}/flightctl/flightctl-ui
    %dir %{_sysconfdir}/flightctl/flightctl-cli-artifacts
    %dir %{_sysconfdir}/flightctl/flightctl-alertmanager-proxy
    %dir %{_sysconfdir}/flightctl/flightctl-pam-issuer
    %dir %{_sysconfdir}/flightctl/ssh
    %config(noreplace) %{_sysconfdir}/flightctl/service-config.yaml
    %config(noreplace) %{_sysconfdir}/flightctl/flightctl-services-install.conf
    %config(noreplace) %{_sysconfdir}/flightctl/ssh/known_hosts

    # Files mounted to data dir
    %dir %attr(0755,root,root) %{_datadir}/flightctl
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-api
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-alert-exporter
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-db
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-kv
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-alertmanager-proxy
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-ui
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-cli-artifacts
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-pam-issuer
    %{_datadir}/flightctl/flightctl-api/config.yaml.template
    %{_datadir}/flightctl/flightctl-api/env.template
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-api/init.sh
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-api/create_aap_application.sh
    %{_datadir}/flightctl/flightctl-alert-exporter/config.yaml
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-db/enable-superuser.sh
    %{_datadir}/flightctl/flightctl-kv/redis.conf
    %{_datadir}/flightctl/flightctl-ui/env.template
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-ui/init.sh
    %attr(0755,root,root) %{_datadir}/flightctl/init_utils.sh
    %{_datadir}/flightctl/flightctl-cli-artifacts/env.template
    %{_datadir}/flightctl/flightctl-cli-artifacts/nginx.conf
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-cli-artifacts/init.sh
    %{_datadir}/containers/systemd/flightctl*
    %{_datadir}/flightctl/flightctl-alertmanager/alertmanager.yml
    %{_datadir}/flightctl/flightctl-alertmanager-proxy/env.template
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-alertmanager-proxy/init.sh
    %{_datadir}/flightctl/flightctl-pam-issuer/config.yaml.template
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-pam-issuer/init.sh

    # Handle permissions for scripts setting host config
    %attr(0755,root,root) %{_datadir}/flightctl/init_host.sh
    %attr(0755,root,root) %{_datadir}/flightctl/secrets.sh
    %attr(0755,root,root) %{_datadir}/flightctl/yaml_helpers.py

    # flightctl-services pre upgrade checks
    %dir %{_libexecdir}/flightctl
    %attr(0755,root,root) %{_libexecdir}/flightctl/pre-upgrade-dry-run.sh

    # Files mounted to lib dir
    /usr/lib/systemd/system/flightctl.target

    # Files mounted to bin dir
    %attr(0755,root,root) %{_bindir}/flightctl-services-must-gather

# Optional pre-upgrade database migration dry-run
%pre services
# $1 == 1 if it's an install
# $1 == 2 if it's an upgrade
if [ "$1" -eq 2 ]; then
    IMAGE_TAG="$(echo %{version} | tr '~' '-')"
    echo "flightctl: running pre upgrade checks, target version $IMAGE_TAG"
    if [ -x "%{_libexecdir}/flightctl/pre-upgrade-dry-run.sh" ]; then
        IMAGE_TAG="$IMAGE_TAG" \
        CONFIG_PATH="%{_sysconfdir}/flightctl/flightctl-api/config.yaml" \
        "%{_libexecdir}/flightctl/pre-upgrade-dry-run.sh" "$IMAGE_TAG" "%{_sysconfdir}/flightctl/flightctl-api/config.yaml" || {
            echo "flightctl: dry-run failed; aborting upgrade." >&2
            exit 1
        }
    else
        echo "flightctl: pre-upgrade-dry-run.sh not found at %{_libexecdir}/flightctl; skipping."
    fi
fi

%post services
# On initial install: apply preset policy to enable/disable services based on system defaults
%systemd_post %{flightctl_target}

# Reload systemd to recognize new container files
/usr/bin/systemctl daemon-reload >/dev/null 2>&1 || :

cfg="%{_sysconfdir}/flightctl/flightctl-services-install.conf"

if [ "$1" -eq 1 ]; then # it's a fresh install
  %{__cat} <<EOF
[flightctl] Installed.

Start services:
  sudo systemctl start flightctl.target

Check status:
  systemctl list-units 'flightctl*' --all
EOF
fi

# Suggest enabling migration dry-run if not set
if [ -f "$cfg" ] && ! %{__grep} -q '^[[:space:]]*FLIGHTCTL_MIGRATION_DRY_RUN=1[[:space:]]*$' "$cfg"; then
  %{__cat} <<EOF
Recommendation:
  A database migration dry-run before updates is currently DISABLED.
  To enable it, edit:
    $cfg
  and set:
    FLIGHTCTL_MIGRATION_DRY_RUN=1
EOF
fi

if [ "$1" -eq 2 ]; then # it's an upgrade
  %{__cat} <<'EOF'
[flightctl] Upgraded.

Review status:
  systemctl list-units 'flightctl*' --all
EOF
fi

%preun services
# On package removal: stop and disable all services
%systemd_preun %{flightctl_target}
%systemd_preun flightctl-network.service

%postun services
# On upgrade: mark services for restart after transaction completes
%systemd_postun_with_restart %{flightctl_services_restart}
%systemd_postun %{flightctl_target}

# If contexts were managed via policy, no cleanup is needed here.

%changelog
* Thu Nov 13 2025 Super User - 1.0.0-1.20251113140656860923.decoupled.builds.images.252.g5f96d2fc
- EDM-2477: test stream10 (Ilya Skornyakov)
- EDM-2477: refactor (Ilya Skornyakov)
- EDM-2477: performance (Ilya Skornyakov)
- EDM-2477: directly stream artifacts into podman (Ilya Skornyakov)
- EDM-2477: agent - dont create /etc/flightctl (Ilya Skornyakov)
- EDM-2477: agent builds (Ilya Skornyakov)
- NO-ISSUE: speed up build (Asaf Ben Natan)
- EDM-2509: enforcing PKCE for public clients (Asaf Ben Natan)
- EDM-2234: CLI Kind/Name Autocomplete (#1871) (Ben Keith)
- NO-ISSUE: use systemd stop selinux macro (#1928) (kkyrazis)
- EDM-2294: Quadlet reset failed systemd units on remove (#1919) (kkyrazis)
- NO-ISSUE: add dependency to test vms (sserafin)
- EDM-2230: Persist CSR Until enrollment (Siddarth R)
- EDM-2329: Auto select org on login (Siddarth R)
- NO-ISSUE: add link to orgs user docs from main nav (Dakota Crowder)
- EDM-2443: Add Inline Provider for Quadlet Apps (#1913) (kkyrazis)
- EDM-2477: agent builds (Ilya Skornyakov)
- EDM-2269: Upstream flightctl services rpm version is incorrect (remove branch info) (#1914) (Gregory Shilin)
- EDM-2477: setup cluster with published artifacts (Ilya Skornyakov)
- EDM-2265: fix E2E rollout tests beforeEach in OCP (sserafin)
- EDM-2477: update compression & retention (Ilya Skornyakov)
- EDM-2346: replaced keycloak with PAM issuer (Asaf Ben Natan)
- EDM-2358: fix lint cache (Asaf Ben Natan)
- EDM-2395: Add Inline Quadlet Validation (Kyle Kyrazis)
- EDM-2294: Add Quadlet App Lifecycle Handler (#1875) (kkyrazis)
- EDM-2393: Added quadlet spec definition (#1855) (kkyrazis)
- EDM-2414: grafana starts but there are warnings and errors (Ori Amizur)
- EDM-2477: upload CI artifacts to github (Ilya Skornyakov)
- EDM-1116: Audit the use of rate limiting controls (Ori Amizur)
- EDM-2455: Add quadlet installation logic (#1881) (kkyrazis)
- EDM-2269: Upstream flightctl services rpm version is incorrect (#1897) (Gregory Shilin)
- EDM-2465: Add upstream RHEL/CS-10 targes targets (#1895) (Gregory Shilin)
- NO-ISSUE: Fix TLD in image labels (Frank A. Zdarsky)
- EDM-2459: add pprof server for runtime profiling (#1887) (Assaf Albo)
- EDM-2302: User quadlet docs (Dakota Crowder)
- EDM-2495: Add HSTS and X-Content-Type-Options headers (Avishay Traeger)
- EDM-2424: helm - preserve migration job (#1866) (Ilya)
- EDM-1124: null_method_call: Calling a method on null object (Ori Amizur)
- EDM-2250: Treat graceful HTTP shutdown correctly in metrics server (#1888) (Assaf Albo)
- NO-ISSUE: Temporarily disable backward compatibility integration tests (Avishay Traeger)
- EDM-2404: Add debug logs to snapshot restore (sserafin)
- NO-ISSUE: Remove rate limit environment variables (Avishay Traeger)
- NO-ISSUE: Add explicit enabled field for rate limiting configuration (Avishay Traeger)
- EDM-2405: Increase default rate limiting from 60/10 to 300/20 requests (Avishay Traeger)
- EDM-2378: automate flightctl edit (Eldar Weiss)
- EDM-2240: Default to YAML format when using --rendered (Celia Amador)
- EDM-2240: Fix bug lastSeen allowed for multiple devices but info not shown (Celia Amador)
- EDM-2240: Flags shown in help for 'get' commands are contextual (Celia Amador)
- EDM-2300: Services must-gather script (Dakota Crowder)
- NO-ISSUE: Use selected organization when creating new devices with agent-vm (Celia Amador)
- EDM-2739: automate new get format (Eldar Weiss)
- EDM-2233: Automation of Deny feature (Eldar Weiss)
- EDM-2168: Fix for [It] VM Agent behavior status Device status tests (Itzik Brown)
- EDM-2031: Added checking number of devices in each fleet test (Hadar Ferber)
- NO-ISSUE: add missing labels (#1861) (Ilya)
- EDM-2419: fixed meter collector to have a single label (Asaf Ben Natan)
- EDM-2412: store: return not found if checkpoint does not exist (Sam Batschelet)
- EDM-2408: Move redis config for quadlets kv service to a .conf file to prevent directory permissions issues (Dakota Crowder)
- EDM-2409: Add internal ctx key to alert exporter service (Dakota Crowder)
- EDM-2400: Fix bootc linting issue for CentOS buildinfo directories (Gal Elharar)
- EDM-2385:Redis integration test race  condition fix (amalykhi)
- EDM-1961: Enhance CLI Login error handling and Docs (Siddarth R)
- EDM-2396: Fix build source tag detection (Ben Keith)
- EDM-2387: Allow flightctl_agent_t to get status of systemd services (Ben Keith)
- NO-ISSUE: Fix commit message check for backport branches (Avishay Traeger)
- EDM-2322: Flightctl is using the Internal database when enabling the … (#1797) (Gregory Shilin)
- EDM-2354: agent: remove klog as dependency (Sam Batschelet)
- EDM-2354: device/systeminfo: improve context handling (Sam Batschelet)
- EDM-2354: packaging/greenboot: bump timeout (Sam Batschelet)
- EDM-2354: test/agent: use drop-in for system-info config (Sam Batschelet)
- EDM-2350-v2: Fix documentation to reference correct telemetery gateway export listen port (9464) for prometheus (Ori Amizur)
- NO-ISSUE: move files (JasonN3)
- EDM-2350: [documenttion] Telemetry quadlets configuration is wrong in the doc (Ori Amizur)
- EDM-2236: use require package for approve command tests (Gal Elharar)
- EDM-2351: Update doc refs from new utilty usage, accept stdin input in py yaml utility (Dakota Crowder)
- EDM-2253: Add hostname support to systeminfo and improve agent config (Gal Elharar)
- EDM-2251: remove start condition for db-migrate (#1815) (Ilya)
- NO-ISSUE: workflow disk cleanup config update (Ilya)
- EDM-2367: Separate last seen from device (Ori Amizur)
- EDM-2161: detect integration tests base (#1809) (Ilya)
- EDM-2236: Add support for space-separated approve command syntax (Gal Elharar)
- EDM-2341: flightctl-userinfo-proxy container doesn't support the Memory setting (Ori Amizur)
- EDM-1834: Agent attempts to reconcile before reboot on OS upgrade (noga-magen)
- EDM-2351: Add python utility for transforming yaml (Dakota Crowder)
- EDM-2352: improve e2e beforeEach stability (sserafin)
- EDM-1392: Add fallback for immediate /var/lib/flightctl creation (Gal Elharar)
- EDM-2214: create migration pod just once and retry without a limit (#1794) (Ilya)
- EDM-2288: remove yq dependency (Siddarth R)
- EDM-1601: Prevent Invalid Memory monitor type path fields (Gal Elharar)
- EDM-1601: Add unit tests for ResourceMonitor validation (Gal Elharar)
- EDM-1601: Prevent duplicate monitorType and invalid CPU path fields (Gal Elharar)
- EDM-1601: Add validation to prevent duplicate monitorType in fleet resources (Gal Elharar)
- NO-ISSUE: dont shutdown deployments in ACM (#1793) (Ilya)
- NO-ISSUE: fix broken link (#1796) (Ilya)
- NO-ISSUE: Fix custom-info examples in agent-vm (Celia Amador)
- NO-ISSUE: Fix git container on deploy (#1772) (Siddarth Royapally)
- EDM-2214: helm add migration wait init containers to services (#1785) (Siddarth Royapally)
- EDM-2286: Add correct selinux policy to custom-info directory (#1788) (kkyrazis)
- EDM-2259: Fix draining workloads on shutdown (#1786) (kkyrazis)
- NO-ISSUE: deploy/helm: fix doc links (Sam Batschelet)
- EMD-2207: Remove Docs and duplicate licenses from RPMs (#1781) (kkyrazis)
- Merge pull request #1779 from keitwb/rhel-build-note (Ben Keith)
- EDM-2254: Fix directory drop in reference for db/kv (Dakota Crowder)
- Add doc on how to access downstream builds (#1774) (Ben Keith)
- NO-ISSUE: Various doc fixes (Frank A. Zdarsky)
- EDM-2043: Create SSH known_hosts file during installation (#1775) (Siddarth Royapally)
- EDM-2273: device/dependency: ensure stale images are removed on version change (Sam Batschelet)
- EDM-2271: Fix TPM Activate Credential with tracing disabled (#1763) (kkyrazis)
- EDM-2255: added flightctl-restore to the rpm (Asaf Ben Natan)
- EDM-2272: device/application/podman: ensure pods are cleaned up on removal (Sam Batschelet)
- EDM-2266: Append org id param to console requests from cli and address outdated const refs (Dakota Crowder)
- EDM-1870: automation of quadlets installation in RHEL9 vm (sserafin)
- NO-ISSUE: Update repo to rpm.flightctl.io (Frank A. Zdarsky)
- EDM-2254: fixed quadlets docs, added restore version option (Asaf Ben Natan)
- EDM-1183: Encapsulate template detection logic (Gal Elharar)
- EDM-2171: Update dry run script (Siddarth R)
- EDM-2043: bypass known_hosts check for skip verification (#1602) (Siddarth Royapally)
- EDM-2036: CLI Update Flightctl completion (#1601) (Siddarth Royapally)
- EDM-2211: docs: clarify bootc image building dep (Sam Batschelet)
- EDM-2248: Replace yq dep with jq/pyyaml (Dakota Crowder)
- EDM-2246: Ensure alertmanager-proxy tag is updated from :latest when installing rpm (Dakota Crowder)
- NO-ISSUE: agent: remove stop and unnecessary locking (Sam Batschelet)
- EDM-2088: docs: revert downgrade support (Sam Batschelet)
- EDM-2232: Explicitly set device fields unset by ApplyJSONPatch (Dakota Crowder)
- NO-ISSUE: fixed race condition when stop is called before start is finished on agent (Asaf Ben Natan)
- NO-ISSUE: create clean snap and verify snapshot revert (Asaf Ben Natan)
- EDM-2224: setting awaitingReconnect annotation on ERs based on renderedVersion parameter ( new parameter) (Asaf Ben Natan)
- EDM-2169: ERs marked when restoring (Asaf Ben Natan)
- EDM-1246: Add FIPS validator (Frank A. Zdarsky)
- NO-ISSUE: Fix mismatch in RPM version calculation across builds (#1729) (#1730) (Assaf Albo)
- NO-ISSUE: agent: clarify concurrency model (Sam Batschelet)
- EDM-959: Add documentation for device observability with Telemetry Gateway and otelcol (#1695) (Assaf Albo)
- EDM-552: support for cli format (Asaf Ben Natan)
- EDM-2228: Installation fails when using an external database with sslmode verify-ca (#1716) (Gregory Shilin)
- Bump tj-actions/changed-files from 44 to 46 in /.github/workflows (dependabot[bot])
- EDM-2133: Ensure only valid orgs can be selected in CLI (noga-magen)
- NO-ISSUE: Update base image version to 9.6-1758714456 (#1715) (Assaf Albo)
- NO-ISSUE: LastSeen CLI use "never" not "none" (Avishay Traeger)
- EDM-2016: Credential Challenge documentation (#1709) (kkyrazis)
- EDM-2196: Add AAP details and update org docs (Dakota Crowder)
- NO-ISSUE: Include pkg in unit tests (#1712) (kkyrazis)
- EDM-1183: Add comprehensive unit tests for template OCI image validation (Gal Elharar)
- EDM-2138: Add support for telemetry gateway for standalone observability (Ori Amizur)
- EDM-1183: Enhance OCI image reference validation to support template parameters (Gal Elharar)
- EDM-1392: Remove test containerfiles (Gal Elharar)
- EDM-1392: Fix bootc linting issue with tmpfiles.d configuration (Gal Elharar)

* Mon Oct 27 2025 Dakota Crowder <dcrowder@redhat.com> - 1.0
- Add must-gather script for the services sub package
* Wed Oct 8 2025 Ilya Skornyakov <iskornya@redhat.com> - 0.10.0
- Add pre-upgrade database migration dry-run capability
* Tue Jul 15 2025 Sam Batschelet <sbatsche@redhat.com> - 0.9.0-2
- Improve selinux policy deps and install
* Sun Jul 6 2025 Ori Amizur <oamizur@redhat.com> - 0.9.0-1
- Add support for Flight Control standalone observability stack
* Tue Apr 15 2025 Dakota Crowder <dcrowder@redhat.com> - 0.6.0-4
- Add ability to create an AAP Oauth Application within flightctl-services sub-package
* Fri Apr 11 2025 Dakota Crowder <dcrowder@redhat.com> - 0.6.0-3
- Add versioning to container images within flightctl-services sub-package
* Thu Apr 3 2025 Ori Amizur <oamizur@redhat.com> - 0.6.0-2
- Add sos report plugin support
* Mon Mar 31 2025 Dakota Crowder <dcrowder@redhat.com> - 0.6.0-1
- Add services sub-package for installation of containerized flightctl services
* Fri Feb 7 2025 Miguel Angel Ajo <majopela@redhat.com> - 0.4.0-1
- Add selinux support for console pty access
* Mon Nov 4 2024 Miguel Angel Ajo <majopela@redhat.com> - 0.3.0-1
- Move the Release field to -1 so we avoid auto generating packages
  with -5 all the time.
* Wed Aug 21 2024 Sam Batschelet <sbatsche@redhat.com> - 0.0.1-5
- Add must-gather script to provide a simple mechanism to collect agent debug
* Wed Aug 7 2024 Sam Batschelet <sbatsche@redhat.com> - 0.0.1-4
- Add basic greenboot support for failed flightctl-agent service
* Wed Mar 13 2024 Ricardo Noriega <rnoriega@redhat.com> - 0.0.1-3
- New specfile for both CLI and agent packages
