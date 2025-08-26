# Disable debug information package creation
%define debug_package %{nil}

# Define the Go Import Path
%global goipath github.com/flightctl/flightctl

# SELinux specifics
%global selinuxtype targeted
%define selinux_policyver 3.14.3-67

Name:           flightctl
Version:        0.6.0
Release:        1%{?dist}
Summary:        Flight Control service

%gometa

License:        Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND ISC AND MIT
URL:            %{gourl}

Source0:        1%{?dist}

BuildRequires:  golang
BuildRequires:  make
BuildRequires:  git
BuildRequires:  openssl-devel

Requires: openssl

# Skip description for the main package since it won't be created
%description
# Main package is empty and not created.

# cli sub-package
%package cli
Summary: Flight Control CLI
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

%description services
The flightctl-services package provides installation and setup of files for running containerized Flight Control services

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

%package observability
Summary: Complete FlightCtl observability stack
Requires:       flightctl-otel-collector = %{version}-%{release}
Requires:       /usr/sbin/semanage
Requires:       /usr/sbin/restorecon
Requires:       podman
Requires:       systemd
Requires(post): systemd, yq, gettext
Requires(preun):systemd
Requires(postun):systemd
Requires:       selinux-policy-targeted

%description observability
This package provides the complete FlightCtl Observability Stack, including
Prometheus for metric storage, Grafana for visualization, and
OpenTelemetry Collector for metric collection. All components run in Podman containers
managed by systemd and can be installed independently without requiring core FlightCtl
services to be running. This package automatically includes the flightctl-otel-collector package.

%files otel-collector
# OpenTelemetry Collector specific files
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

# The files that will be generated in %post must be listed as %ghost files.
%ghost /etc/grafana/grafana.ini
%ghost /etc/containers/systemd/flightctl-grafana.container
%ghost /etc/containers/systemd/flightctl-prometheus.container
%ghost /etc/containers/systemd/flightctl-userinfo-proxy.container

# Configuration management script
/usr/local/bin/flightctl-render-observability

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
/usr/bin/mkdir -p /usr/local/bin /usr/lib/systemd/system
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
/usr/sbin/semanage fcontext -a -t container_file_t "/usr/local/bin/flightctl-render-observability" >/dev/null 2>&1 || :
/usr/sbin/semanage fcontext -a -t container_file_t "/usr/local/bin/" >/dev/null 2>&1 || :

# Restore file contexts based on the new rules (and default rules)
/usr/sbin/restorecon -RvF /etc/prometheus >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /var/lib/prometheus >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /etc/grafana >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /var/lib/grafana >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /etc/grafana/certs >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /opt/flightctl-observability/templates >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /usr/local/bin/flightctl-render-observability >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /usr/local/bin/ >/dev/null 2>&1 || :

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

# Remove SELinux fcontext rules added by this package
/usr/sbin/semanage fcontext -d -t container_file_t "/etc/grafana(/.*)?" >/dev/null 2>&1 || :
/usr/sbin/semanage fcontext -d -t container_file_t "/var/lib/grafana(/.*)?" >/dev/null 2>&1 || :
/usr/sbin/semanage fcontext -d -t container_file_t "/etc/grafana/certs(/.*)?" >/dev/null 2>&1 || :

/usr/sbin/semanage fcontext -d -t container_file_t "/etc/prometheus/prometheus.yml" >/dev/null 2>&1 || :
/usr/sbin/semanage fcontext -d -t container_file_t "/var/lib/prometheus(/.*)?" >/dev/null 2>&1 || :

/usr/sbin/semanage fcontext -d -t container_file_t "/opt/flightctl-observability/templates(/.*)?" >/dev/null 2>&1 || :
/usr/sbin/semanage fcontext -d -t container_file_t "/usr/local/bin/flightctl-render-observability" >/dev/null 2>&1 || :
/usr/sbin/semanage fcontext -d -t container_file_t "/usr/local/bin/" >/dev/null 2>&1 || :


# Restore default SELinux contexts for affected directories
/usr/sbin/restorecon -RvF /etc/grafana >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /var/lib/grafana >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /etc/grafana/certs >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /etc/prometheus >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /var/lib/prometheus >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /opt/flightctl-observability/templates >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /usr/local/bin/flightctl-render-observability >/dev/null 2>&1 || :
/usr/sbin/restorecon -RvF /usr/local/bin/ >/dev/null 2>&1 || :


/usr/bin/systemctl daemon-reload
echo "Flightctl Observability Stack uninstalled."

%prep
%goprep -A
%setup -q %{forgesetupargs}

%build
    # if this is a buggy version of go we need to set GOPROXY as workaround
    # see https://github.com/golang/go/issues/61928
    GOENVFILE=$(go env GOROOT)/go.env
    if [[ ! -f "${GOENVFILE}" ]]; then
        export GOPROXY='https://proxy.golang.org,direct'
    fi

    SOURCE_GIT_TAG=$(echo %{version} | tr '~' '-') \
    SOURCE_GIT_TREE_STATE=clean \
    SOURCE_GIT_COMMIT=$(echo %{version} | awk -F'[-~]g' '{print $2}') \
    SOURCE_GIT_TAG_NO_V=%{version} \
    %if 0%{?rhel} == 9
        %make_build build-cli build-agent
    %else
        DISABLE_FIPS="true" %make_build build-cli build-agent
    %endif

    # SELinux modules build
    %make_build --directory packaging/selinux

%install
    mkdir -p %{buildroot}/usr/bin
    mkdir -p %{buildroot}/etc/flightctl
    cp bin/flightctl %{buildroot}/usr/bin
    mkdir -p %{buildroot}/usr/lib/systemd/system
    mkdir -p %{buildroot}/%{_sharedstatedir}/flightctl
    mkdir -p %{buildroot}/usr/lib/flightctl/custom-info.d
    mkdir -p %{buildroot}/usr/lib/flightctl/hooks.d/{afterupdating,beforeupdating,afterrebooting,beforerebooting}
    mkdir -p %{buildroot}/usr/lib/greenboot/check/required.d
    install -m 0755 packaging/greenboot/flightctl-agent-running-check.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    cp bin/flightctl-agent %{buildroot}/usr/bin
    cp packaging/must-gather/flightctl-must-gather %{buildroot}/usr/bin
    cp packaging/hooks.d/afterupdating/00-default.yaml %{buildroot}/usr/lib/flightctl/hooks.d/afterupdating
    cp packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system
    bin/flightctl completion bash > flightctl-completion.bash
    install -Dpm 0644 flightctl-completion.bash -t %{buildroot}/%{_datadir}/bash-completion/completions
    bin/flightctl completion fish > flightctl-completion.fish
    install -Dpm 0644 flightctl-completion.fish -t %{buildroot}/%{_datadir}/fish/vendor_completions.d/
    bin/flightctl completion zsh > _flightctl-completion
    install -Dpm 0644 _flightctl-completion -t %{buildroot}/%{_datadir}/zsh/site-functions/
    install -d %{buildroot}%{_datadir}/selinux/packages/%{selinuxtype}
    install -m644 packaging/selinux/*.bz2 %{buildroot}%{_datadir}/selinux/packages/%{selinuxtype}

    rm -f licenses.list

    find . -type f -name LICENSE -or -name License | while read LICENSE_FILE; do
        echo "%{_datadir}/licenses/%{NAME}/${LICENSE_FILE}" >> licenses.list
    done
    mkdir -vp "%{buildroot}%{_datadir}/licenses/%{NAME}"
    cp LICENSE "%{buildroot}%{_datadir}/licenses/%{NAME}"

    mkdir -vp "%{buildroot}%{_docdir}/%{NAME}"

    for DOC in docs examples .markdownlint-cli2.yaml README.md; do
        cp -vr "${DOC}" "%{buildroot}%{_docdir}/%{NAME}/${DOC}"
    done

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

    # Copy sos report flightctl plugin
    mkdir -p %{buildroot}/usr/share/sosreport
    cp packaging/sosreport/sos/report/plugins/flightctl.py %{buildroot}/usr/share/sosreport

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
     mkdir -p %{buildroot}/opt/flightctl-observability/templates # Staging for template files processed in %post
     mkdir -p %{buildroot}/usr/local/bin # For the reloader script
     mkdir -p %{buildroot}/usr/lib/systemd/system # For systemd units

     # Copy static configuration files (those not templated)
     install -m 0644 packaging/observability/prometheus.yml %{buildroot}/etc/prometheus/
     install -m 0644 packaging/observability/otelcol-config.yaml %{buildroot}/etc/otelcol/

     # Copy template source files to a temporary staging area for processing in %post
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
# No %files section for the main package, so it won't be built

%files cli -f licenses.list
    %{_bindir}/flightctl
    %license LICENSE
    %{_datadir}/bash-completion/completions/flightctl-completion.bash
    %{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
    %{_datadir}/zsh/site-functions/_flightctl-completion

%files agent -f licenses.list
    %license LICENSE
    %dir /etc/flightctl
    %{_bindir}/flightctl-agent
    %{_bindir}/flightctl-must-gather
    /usr/lib/flightctl/hooks.d/afterupdating/00-default.yaml
    /usr/lib/systemd/system/flightctl-agent.service
    %{_sharedstatedir}/flightctl
    /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    %{_docdir}/%{NAME}/*
    %{_docdir}/%{NAME}/.markdownlint-cli2.yaml
    /usr/share/sosreport/flightctl.py

%post agent
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
    %config(noreplace) %{_sysconfdir}/flightctl/service-config.yaml

    # Files mounted to data dir
    %dir %attr(0444,root,root) %{_datadir}/flightctl
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-api
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-alert-exporter
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-db
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-db-migrate
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-db-migrate/migration-setup.sh
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-kv
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-ui
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-cli-artifacts
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

    # Handle permissions for scripts setting host config
    %attr(0755,root,root) %{_datadir}/flightctl/init_host.sh
    %attr(0755,root,root) %{_datadir}/flightctl/secrets.sh

    # Files mounted to lib dir
    /usr/lib/systemd/system/flightctl.target
    /usr/lib/systemd/system/flightctl-db-migrate.service

%changelog
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
