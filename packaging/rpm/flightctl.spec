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
Version:        1.1.0~main~287~g9a051694
Release:        1.20260127220443281991.rootless.quadlets.436.g9a051694%{?dist}
Summary:        Flight Control service

%gometa

License:        Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND ISC AND MIT
URL:            %{gourl}

Source0:        flightctl-1.1.0~main~287~g9a051694.tar.gz

BuildRequires:  golang
BuildRequires:  make
BuildRequires:  git
BuildRequires:  openssl-devel
BuildRequires:  systemd-rpm-macros

Requires: openssl

%global flightctl_target flightctl.target

# --- Restart these on upgrade  ---
%global flightctl_services_restart flightctl-api.service flightctl-ui.service flightctl-worker.service flightctl-alertmanager.service flightctl-alert-exporter.service flightctl-alertmanager-proxy.service flightctl-cli-artifacts.service flightctl-periodic.service flightctl-db-migrate.service flightctl-db-wait.service flightctl-imagebuilder-api.service


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
Requires: jq
# Pin the greenboot package to 0.15.z until the following issue is resolved:
# https://github.com/fedora-iot/greenboot-rs/issues/141
Requires: greenboot >= 0.15.0
Requires: greenboot < 0.16.0

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
Summary: Telemetry Gateway for Flight Control
Requires:       flightctl-services = %{version}-%{release}
Requires:       podman
Requires:       python3-pyyaml
Requires(post): python3-pyyaml gettext
%{?systemd_requires}
Requires:       selinux-policy-targeted

%description telemetry-gateway
This package provides the Flight Control Telemetry Gateway for telemetry collection/forwarding.
It runs in a Podman container managed by systemd and can be installed
independently of core Flight Control services. Includes certificate tooling for Podman/Kubernetes.

%package observability
Summary: Complete Flight Control observability stack
Requires:       flightctl-telemetry-gateway = %{version}-%{release}
Requires:       flightctl-services = %{version}-%{release}
Requires:       /usr/sbin/semanage
Requires:       /usr/sbin/restorecon
Requires:       podman
Requires:       systemd
Requires(post): python3-pyyaml gettext
%{?systemd_requires}
Requires:       selinux-policy-targeted

%description observability
This package provides the complete Flight Control Observability Stack, including
Prometheus for metric storage, Grafana for visualization, and
Telemetry Gateway for metric collection. All components run in Podman containers
managed by systemd and can be installed independently without requiring core Flight Control
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
echo "Preparing to install Flight Control Telemetry Gateway..."
echo "Note: OpenTelemetry collector can be installed independently of other Flight Control services."


%post telemetry-gateway
# This script runs AFTER the files have been installed onto the system.
echo "Running post-install actions for Flight Control Telemetry Gateway..."

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

echo "Flight Control Telemetry Gateway installed. Service is configured but not started."
echo "To render config: sudo flightctl-render-observability"
echo "To start services: sudo systemctl start flightctl-telemetry-gateway.target"
echo "For automatic startup: sudo systemctl enable flightctl-telemetry-gateway.target"


%preun telemetry-gateway
echo "Running pre-uninstall actions for Flight Control Telemetry Gateway..."
# Stop and disable the target and services
/usr/bin/systemctl stop flightctl-telemetry-gateway.target >/dev/null 2>&1 || :
/usr/bin/systemctl disable flightctl-telemetry-gateway.target >/dev/null 2>&1 || :
/usr/bin/systemctl stop flightctl-telemetry-gateway.service >/dev/null 2>&1 || :
/usr/bin/systemctl disable flightctl-telemetry-gateway.service >/dev/null 2>&1 || :


%postun telemetry-gateway
echo "Running post-uninstall actions for Flight Control Telemetry Gateway..."
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
echo "Flight Control Telemetry Gateway uninstalled."


%pre observability
# This script runs BEFORE the files are installed onto the system.
echo "Preparing to install Flight Control Observability Stack..."
echo "Note: Observability stack can be installed independently of other Flight Control services."


%post observability
# This script runs AFTER the files have been installed onto the system.
echo "Running post-install actions for Flight Control Observability Stack..."

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
# Note: We use the basic reloader here because Flight Control services aren't running yet during installation.
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

echo "Flight Control Observability Stack services installed. Services are configured but not started."
echo "To render config: sudo flightctl-render-observability"
echo "To start services: sudo systemctl start flightctl-observability.target"
echo "For automatic startup: sudo systemctl enable flightctl-observability.target"




%preun observability
echo "Running pre-uninstall actions for Flight Control Observability Stack..."
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
echo "Running post-uninstall actions for Flight Control Observability Stack..."
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
echo "Flight Control Observability Stack uninstalled."

%prep
%goprep -A
%setup -q %{forgesetupargs} -n flightctl-1.1.0~main~287~g9a051694

%build
    # if this is a buggy version of go we need to set GOPROXY as workaround
    # see https://github.com/golang/go/issues/61928
    GOENVFILE=$(go env GOROOT)/go.env
    if [[ ! -f "${GOENVFILE}" ]]; then
        export GOPROXY='https://proxy.golang.org,direct'
    fi

    # Prefer values injected by Makefile/CI; fall back to RPM macros when unset
    SOURCE_GIT_TAG="%{?SOURCE_GIT_TAG:%{SOURCE_GIT_TAG}}%{!?SOURCE_GIT_TAG:%(echo "v%{version}" | tr '~' '-')}" \
    SOURCE_GIT_TREE_STATE="%{?SOURCE_GIT_TREE_STATE:%{SOURCE_GIT_TREE_STATE}}%{!?SOURCE_GIT_TREE_STATE:clean}" \
    SOURCE_GIT_COMMIT="%{?SOURCE_GIT_COMMIT:%{SOURCE_GIT_COMMIT}}%{!?SOURCE_GIT_COMMIT:%(
        commit=$(git rev-parse --short HEAD 2>/dev/null);
        if [ -z "$commit" ]; then
            commit=$(echo %{version} | grep -o '[-~]g[0-9a-f]*' | sed 's/[-~]g//');
        fi;
        echo "${commit:-unknown}";
    )}" \
    %if 0%{?rhel} == 9
        %make_build build-cli build-agent build-restore build-standalone
    %else
        DISABLE_FIPS="true" %make_build build-cli build-agent build-restore build-standalone
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
    mkdir -p %{buildroot}/usr/lib/greenboot/red.d
    mkdir -p %{buildroot}/usr/share/flightctl/functions
    install -m 0755 packaging/greenboot/functions.sh %{buildroot}/usr/share/flightctl/functions/greenboot.sh
    install -m 0755 packaging/greenboot/flightctl-agent-running-check.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    install -m 0755 packaging/greenboot/flightctl-agent-pre-rollback.sh %{buildroot}/usr/lib/greenboot/red.d/40_flightctl_agent_pre_rollback.sh
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
    # Use the flightctl-standalone render quadlets command to generate quadlet files with the correct image tags.
    #
    # The IMAGE_TAG is derived from the RPM version, which may include tildes (~)
    # for proper version sorting (e.g., 0.5.1~rc1-1). However, the tagged images
    # always use hyphens (-) instead of tildes (~). To ensure valid image tags we need
    # to transform the version string by replacing tildes with hyphens.
    IMAGE_TAG=$(echo %{version} | tr '~' '-')

    # Check if IMAGE_TAG matches a release version pattern (x.x.x or x.x.x-rcX).
    # Release versions match: 1.2.3 or 1.2.3-rc1
    # Development builds have additional suffixes like: 1.2.3-main-79-g54721648
    if echo "${IMAGE_TAG}" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+(-rc[0-9]+)?$'; then
        APPLY_UI_OVERRIDE="--flightctl-ui-tag-override"
    else
        APPLY_UI_OVERRIDE=""
    fi
    bin/flightctl-standalone render quadlets \
        --config deploy/podman/images.yaml \
        --flightctl-services-tag-override "${IMAGE_TAG}" \
        ${APPLY_UI_OVERRIDE} \
        --readonly-config-dir "%{buildroot}%{_datadir}/flightctl" \
        --writeable-config-dir "%{buildroot}%{_sysconfdir}/flightctl" \
        --quadlet-dir "%{buildroot}%{_datadir}/containers/systemd" \
        --systemd-dir "%{buildroot}/usr/lib/systemd/system" \
        --bin-dir "%{buildroot}/usr/bin" \
        --var-tmp-dir "%{buildroot}%{_var}/tmp"

    # Copy services must gather script
    cp packaging/must-gather/flightctl-services-must-gather %{buildroot}%{_bindir}

    # Copy generate-certificates.sh script
    mkdir -p %{buildroot}%{_datadir}/flightctl
    install -m 0755 deploy/helm/flightctl/scripts/generate-certificates.sh %{buildroot}%{_datadir}/flightctl/generate-certificates.sh

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
    # Run the installed binary from the buildroot and capture its output
    out="$("%{buildroot}%{_bindir}/flightctl-agent" version)"
    echo "$out"

    # Extract the parts after the colons
    version=$(printf '%s\n' "$out" | sed -n 's/^Agent Version:[[:space:]]*//p')
    commit=$(printf '%s\n' "$out" | sed -n 's/^Git Commit:[[:space:]]*//p')

    # Fail if either is empty
    if [ -z "$version" ]; then
        echo "ERROR: Agent Version is empty"
        exit 1
    fi

    if [ -z "$commit" ]; then
        echo "ERROR: Git Commit is empty"
        exit 1
    fi

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
    /usr/share/flightctl/functions/greenboot.sh
    /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    /usr/lib/greenboot/red.d/40_flightctl_agent_pre_rollback.sh
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

# We want a regular user to run applications with as there are several issues around system users
# and running quadlet applications.
id -u flightctl || useradd --home-dir /home/flightctl --create-home --user-group flightctl
loginctl enable-linger flightctl || :
mkdir -p /home/flightctl/{.config,.local}
chown -R flightctl:flightctl /home/flightctl/{.config,.local}

%postun agent
loginctl disable-linger flightctl || :
userdel flightctl

%files selinux
%{_datadir}/selinux/packages/%{selinuxtype}/flightctl_agent.pp.bz2

%files services
    %defattr(0644,root,root,-)
    # Files mounted to system config
    %dir %{_sysconfdir}/flightctl
    %dir %{_sysconfdir}/flightctl/pki
    %dir %{_sysconfdir}/flightctl/pki/flightctl-api
    %dir %{_sysconfdir}/flightctl/pki/flightctl-alertmanager-proxy
    %dir %{_sysconfdir}/flightctl/pki/flightctl-pam-issuer
    %dir %{_sysconfdir}/flightctl/pki/flightctl-imagebuilder-api
    %dir %{_sysconfdir}/flightctl/pki/db
    %dir %{_sysconfdir}/flightctl/flightctl-api
    %dir %{_sysconfdir}/flightctl/flightctl-ui
    %dir %{_sysconfdir}/flightctl/flightctl-cli-artifacts
    %dir %{_sysconfdir}/flightctl/flightctl-alertmanager-proxy
    %dir %{_sysconfdir}/flightctl/flightctl-pam-issuer
    %dir %{_sysconfdir}/flightctl/flightctl-db-migrate
    %dir %{_sysconfdir}/flightctl/flightctl-imagebuilder-api
    %dir %{_sysconfdir}/flightctl/flightctl-imagebuilder-worker
    %dir %{_sysconfdir}/flightctl/ssh
    %config(noreplace) %{_sysconfdir}/flightctl/service-config.yaml
    %config(noreplace) %{_sysconfdir}/flightctl/flightctl-services-install.conf
    %config(noreplace) %{_sysconfdir}/flightctl/ssh/known_hosts

    # Files mounted to data dir
    %dir %attr(0755,root,root) %{_datadir}/flightctl
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-api
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-db
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-kv
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-alertmanager-proxy
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-ui
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-cli-artifacts
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-pam-issuer
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-alert-exporter
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-periodic
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-worker
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-db-migrate
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-imagebuilder-api
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-imagebuilder-worker
    %dir %attr(0755,root,root) %{_var}/tmp/flightctl-builds
    %dir %attr(0755,root,root) %{_var}/tmp/flightctl-exports
    %{_datadir}/flightctl/flightctl-api/config.yaml.template
    %{_datadir}/flightctl/flightctl-api/env.template
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-api/init.sh
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-api/create_aap_application.sh
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
    %{_datadir}/flightctl/flightctl-pam-issuer/config.yaml.template
    %{_datadir}/flightctl/flightctl-alertmanager-proxy/config.yaml.template
    %{_datadir}/flightctl/flightctl-alert-exporter/config.yaml.template
    %{_datadir}/flightctl/flightctl-periodic/config.yaml.template
    %{_datadir}/flightctl/flightctl-worker/config.yaml.template
    %{_datadir}/flightctl/flightctl-db-migrate/config.yaml.template
    %{_datadir}/flightctl/flightctl-imagebuilder-api/config.yaml.template
    %{_datadir}/flightctl/flightctl-imagebuilder-worker/config.yaml.template

    # Handle permissions for scripts setting host config
    %attr(0755,root,root) %{_datadir}/flightctl/init_host.sh
    %attr(0755,root,root) %{_datadir}/flightctl/init_certs.sh
    %attr(0755,root,root) %{_datadir}/flightctl/secrets.sh
    %attr(0755,root,root) %{_datadir}/flightctl/yaml_helpers.py
    %attr(0755,root,root) %{_datadir}/flightctl/generate-certificates.sh

    # flightctl-services pre upgrade checks
    %dir %{_libexecdir}/flightctl
    %attr(0755,root,root) %{_libexecdir}/flightctl/pre-upgrade-dry-run.sh

    # Files mounted to lib dir
    /usr/lib/systemd/system/flightctl.target
    /usr/lib/systemd/system/flightctl-certs-init.service

    # Files mounted to bin dir
    %attr(0755,root,root) %{_bindir}/flightctl-services-must-gather
    %attr(0755,root,root) %{_bindir}/flightctl-standalone

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
* Tue Jan 27 2026 Super User - 1.1.0~main~287~g9a051694-1.20260127220443281991.rootless.quadlets.436.g9a051694
- EDM-2636: Rootless Quadlets (Ben Keith)
- EDM-2636: Fix up PodmanMonitor (Ben Keith)
- EDM-1948: Additional const usage, fix some comments and error msg (Dakota Crowder)
- EDM-1948: Move additional methods to resourcesync specific harness file (Dakota Crowder)
- EDM-1948: Fix internal port for OCP resourcesync task connecting to git server (Dakota Crowder)
- EDM-1948: Mount ssh keys to allow for authentication from remote images (Dakota Crowder)
- EDM-1948: Configure git server to handle arbitrary non-root user in OpenShift (Dakota Crowder)
- EDM-1948: Restructure test and add labels (Dakota Crowder)
- EDM-1948: Discover git server setup in OCP (Dakota Crowder)
- EDM-1948: Refactoring helpers and test structure (Dakota Crowder)
- EDM-1948: Resource Sync e2e tests (Dakota Crowder)
- EDM-1948: Enable resource sync owned fleets to be removed by setting context flag (Dakota Crowder)
- EDM-1948: Add configurable interval timing for resource sync periodic task (Dakota Crowder)
- NO-ISSUE: fix ER certificate is missing the org ID extension (Asaf Ben Natan)
- EDM-3074: Change renewal before expiration to 75% (asafbss)
- EDM-3173: Update README.md to remove rpm-ostree status command from deployment verification instructions (Gal Elharar)
- EDM-3173: Changed from SSH to virsh console (Gal Elharar)
- EDM-3173: add greenboot developer documentation (Gal Elharar)
- EDM-3171: Fix broken links (Gal Elharar)
- EDM-3171: Rename docs file (Gal Elharar)
- EDM-3171: Add backlink to greenboot journal config from agent docs (Gal Elharar)
- EDM-3171: Remove rpm-ostree and change docs location (Gal Elharar)
- EDM-3171: Add user documentation for integrating Flight Control with Greenboot for automatic rollback (Gal Elharar)
- EDM-2326: Address PR review feedback Fix log levels, remove unnecessary nil guards, handle errors in consumeLatest, and add unit tests (Gal Elharar)
- EDM-2326: Add nil check for systemdClient before rebooting and notifying systemd (Gal Elharar)
- EDM-2326: Remove ReportConnectivityStatus config flag (Gal Elharar)
- EDM-2326: Add check for permanently failed device versions before requeuing (Gal Elharar)
- EDM-2326: Move connectivity status reporting to bootstrap & Add 10s timeout to SdNotify for socket safety (Gal Elharar)
- EDM-2326: Use PrefixLogger and improve greenboot scripts (Gal Elharar)
- EDM-2326: Implement two-phase polling with stability window (Gal Elharar)
- EDM-2326: Add configurable connectivity status reporting via sd_notify (Gal Elharar)
- EDM-2326: add sd_notify STATUS for connectivity reporting (Gal Elharar)
- EDM-2326: Make checker type unexported and rename constructor (Gal Elharar)
- EDM-2326: Refactor health package to use existing systemd client patterns (Gal Elharar)
- EDM-2326: Add health subcommand for greenboot integration (Gal Elharar)
- EDM-2326: refactor healthcheck scripts with shared functions library (Gal Elharar)
- EDM-3095: SELinux with more file access (Ben Keith)
- NO-ISSUE: github/workflows: disable external link checks (Sam Batschelet)
- EDM-2893: cancel imagebuild/imageexport (Asaf Ben Natan)
- EDM-2636: Add application user config (Ben Keith)
- NO-ISSUE: support disk container format (Asaf Ben Natan)
- NO-ISSUE: delete imagebuild fixes (Asaf Ben Natan)
- NO-ISSUE: delete imagebuild deletes related exports (Asaf Ben Natan)
- EDM-2691:Fix RBAC tests (Itzik Brown)
- NO-ISSUE: Add image builder api/worker to tag override (Itzik Brown)
- EDM-2749: Update Downstream Deps (Kyle Kyrazis)
- NO-ISSUE: disable bootc automatic os updates (Asaf Ben Natan)
- EDM-3170: fix sensitive returned for repo (Asaf Ben Natan)
- NO-ISSUE: fix dirs not created for rpm (Asaf Ben Natan)
- EDM-3162: CI fix for should show events for application workload validation (Eldar Weiss)
- NO-ISSUE: use docker args (Asaf Ben Natan)
- EDM-3104: Add integration test for device management cert rotation (asafbss)
- NO-ISSUE: update docs for image building (Asaf Ben Natan)
- NO-ISSUE: fix imagebuilder CLI on openshift (Asaf Ben Natan)
- EDM-2881: PR fixes (Asaf Ben Natan)
- EDM-2881: image export logs (Asaf Ben Natan)
- EDM-2881: image builder logs (Asaf Ben Natan)
- EDM-3131: Add Helm post-renderer for release label injection (Kyle Kyrazis)
- EDM-3136: token refresh is now initialized on its own and does not cause issues where multiple instance are used (Asaf Ben Natan)
- NO-ISSUE: add user config (Asaf Ben Natan)
- EDM-3100: formalize system-info key registry to avoid silent collector misconfig (asafbss)
- EDM-2636: User-aware lifecycle handler + podman monitor (Ben Keith)
- NO-ISSUE: docs/user/references: add agent architecture.md (Sam Batschelet)
- EDM-3164: OCP E2E failure fix tc 78853 (sserafin)
- NO-ISSUE: removed imageexport dest ,tagSuffix , renamed tag -> imagetag (Asaf Ben Natan)
- EDM-2636: User aware clients + image pruner (Ben Keith)
- EDM-3100: honor system-info opt-out for runtime collectors (asafbss)
- EDM-2943: align certificate renewal timing with cert-manager semantics (asafbss)
- EDM-3100: add post-renewal status collection for management certs (asafbss)
- EDM-3099: Add metrics to observe certificate expiration, renewal attempts, and duration (asafbss)
- NO-ISSUE: download image export adjustments (Asaf Ben Natan)
- NO-ISSUE: download image export , CLI - download support, CLI - added support for listing imagebuilds with exports (Asaf Ben Natan)
- EDM-2113:Automation of Enable observability of devices and their workloads (Eldar Weiss)
- EDM-2875: lifecycle - requeue , timeout , imagebuild update (Asaf Ben Natan)
- EDM-2979: remove deprecation warnings (Ilya Skornyakov)
- EDM-2979: clients - include  Flightctl-API-Version (Ilya Skornyakov)
- EDM-3150: API Versioning docs (Ilya Skornyakov)
- EDM-2636: Add executer user options (Ben Keith)
- EDM-2636: Systemd user client (Ben Keith)
- EDM-3148: fix v7 image build in OCP (sserafin)
- EDM-3127: Add Helm OCI chart prefetching support (Kyle Kyrazis)
- Fix config for externalDB with RPM installation (Itzik Brown)
- EDM-2979: update spectral lint (Ilya Skornyakov)
- EDM-2979: prefer error over panic (Ilya Skornyakov)
- EDM-2994: unify server url concatenation across clients (Ilya Skornyakov)
- EDM-2994: refactor (Ilya Skornyakov)
- EDM-2994: improve API server routing and rate limiting (Ilya Skornyakov)
- EDM-2994: unify agent and api server structure (Ilya Skornyakov)
- EDM-2994: make sure the login client uses the correct path (Ilya Skornyakov)
- EDM-2994: agent routing (Ilya Skornyakov)
- EDM-2994: generate the api url const (Ilya Skornyakov)
- EDM-2994: remove the api version prefix from agent spec paths (Ilya Skornyakov)
- EDM-2994: bump oapi-codegen (Ilya Skornyakov)
- EDM-2994: API version negotiation via Flightctl-API-Version header (Ilya Skornyakov)
- EDM-3123: Add CLI clients for helm, kube, and CRI operations (Kyle Kyrazis)
- NO-ISSUE: repository validation when CRUD imagebuild/export (Asaf Ben Natan)
- EDM-2894: imageexport task (Asaf Ben Natan)
- EDM-2636: fileio.Writer: Add ownership handling (Ben Keith)
- EDM-3117: fix imagebuild worker deployment (Asaf Ben Natan)
- EDM-2993: remove redundant converters aggregation (Ilya Skornyakov)
- EDM-2993: Versioned transport structure (Ilya Skornyakov)
- NO-ISSUE: fix e2e (Ilya Skornyakov)
- EDM-2981: keep go template helpers as aliases (Ilya Skornyakov)
- EDM-2981: function alias refactoring (Ilya Skornyakov)
- EDM-2981: Initial conversion implementation (Ilya Skornyakov)
- NO-ISSUE: image build docs (Asaf Ben Natan)
- NO-ISSUE: remove sssd from the docs (Asaf Ben Natan)
- EDM-3074: Add initial support for management certificate rotation in agent (asafbss)
- EDM-2981: additional updated domain import (after merge) (Ilya Skornyakov)
- EDM-2981: Internal domain model (Ilya Skornyakov)
- NO-ISSUE: remove imagepipeline , added withExports parameter, fixed delete , added imagebuild selector for imageexport , fixed resourceversion and generation not set when creating (Asaf Ben Natan)
- EDM-2991: OpenAPI Endpoint Metadata codegen updates (Ilya Skornyakov)
- EDM-2306: Add greenboot rollback verification to E2E test (Gal Elharar)
- EDM-2306: Optimize greenboot rollback test timing (Gal Elharar)
- EDM-2306: add sanity label and optimize greenboot test duration (Gal Elharar)
- EDM-2306: use polling for boot ID assertion in greenboot test (Gal Elharar)
- EDM-2306: add greenboot OS rollback test (Gal Elharar)
- EDM-3067: Document image pruning (Ori Amizur)
- NO-ISSUE: Check that the test-vm has IPs before provisioning. (Itzik Brown)
- NO-ISSUE: added harness helpers to use device simulator (Samuel de la Cruz)
- NO-ISSUE: re-organize agent tests (Eldar Weiss)
- EDM-2891: using stdin for auth and validating image tag/image name (Asaf Ben Natan)
- EDM-2891: finish build (Asaf Ben Natan)
- EDM-2890: container generation logic + serverside CSR (Asaf Ben Natan)
- NO-ISSUE: docs/user/reference: add components (Sam Batschelet)
- EDM-2918: Integrate Pruning with Agent Lifecycle (Ori Amizur)
- EDM-2980: fix merge conflict (Ilya Skornyakov)
- EDM-2980: gci (Ilya Skornyakov)
- EDM-2980: structurize api by component and version (Ilya Skornyakov)
- EDM-3028: pin greenboot to 0.15.x to avoid greenboot-rs issues (Gal Elharar)
- EDM-2961: Fix console error handling to return proper HTTP status codes (Avishay Traeger)
- EDM-2917: Implement Complete Pruning Manager (Ori Amizur)
- EDM-2889: image builder worker bootstrap (Asaf Ben Natan)
- NO-ISSUE: Add documentation and checks to ensure correct version of podman for quadlets (Kyle Kyrazis)
- EDM-2967: Obtain Ingress cert on OpenShift env (rawagner)
- NO-ISSUE: Suppress error message for dummy client status (Ben Keith)
- NO-ISSUE: fix image builder api (Asaf Ben Natan)
- NO-ISSUE: fix image builder api (Asaf Ben Natan)
- EDM-2968: Reload configuration upon configuration dropin files change (#2320) (Ori Amizur)
- EDM-2887: ImageBuild with tasks API (#2314) (asafbennatan)
- EDM-2920: Add Pruning Configuration Support (#2305) (Ori Amizur)
- EDM-2969: fix helm upgrade baseline selection (#2321) (Ilya)
- NO-ISSUE: Allow overriding binary search dirs (#2273) (kkyrazis)
- EDM-2872: Image Export Api (#2308) (asafbennatan)
- NO-ISSUE: publish image builder container (#2310) (asafbennatan)
- NO-ISSUE: fix image builder helm (#2309) (asafbennatan)
- NO-ISSUE: fix quadlet go build using root (#2298) (Ilya)
- EDM-2261: Fix templating issue with inline application definitions (#2285) (kkyrazis)
- NO-ISSUE: Fix issue with application status race condition (#2274) (kkyrazis)
- EDM-2871: add image builder server apis and CLI (#2306) (asafbennatan)
- EDM-2961: Decommission fixes (#2307) (Avishay Traeger)
- EDM-2916: Extend Podman Client with Image and Artifact Management Methods (#2304) (Ori Amizur)
- NO-ISSUE: Expose embedded UI in port 9001 (#2251) (Celia Amador Gonzalez)
- NO-ISSUE: Update auth-openshift doc (#2297) (Itzik Brown)
- EDM-2943: Prepare agent certmanager for future certificate rotation (#2302) (Assaf Albo)
- NO-ISSUE: e2e testing only in merge queue (and release branches , until we activate merge queue there as well) (#2301) (asafbennatan)
- EDM-2856: execute cs10-bootc agent e2e test (#2287) (Ilya)
- NO-ISSUE: fix label override (#2202) (asafbennatan)
- NO-ISSUE: e2e test reliability using polling-based assertions with retry logic (Gregory Shilin)
- NO-ISSUE: fix repo (Asaf Ben Natan)
- EDM-2878: additional renaming and support for selecting scheme (Asaf Ben Natan)
- EDM-2878: EDM-2879 , support for OCI repositories including repotester (connectivity) implementation (Asaf Ben Natan)
- EDM-2878: EDM-2879 , support for OCI repositories including repotester (connectivity) implementation (Asaf Ben Natan)
- EDM-2911: Add skopeo to test-vm (Itzik Brown)
- NO-ISSUE: fix incorrect address injection in ocp (#2295) (Ilya)
- NO-ISSUE: Unset FLIGHTCTL_NS in deploy_e2e_extras.with_helm.sh (Itzik Brown)
- NO-ISSUE: fix tpm test + e2e debug logging toggle (#2288) (Ilya)
- EDM-2855: remove E2E git server creation (sserafin)
- EDM-2463: Decoupled agent image builds (#2139) (Ilya)
- EDM-2860: setting CreatedBySuperAdmin annotation when replace is called without existing resource (as it is when we create from CLI) (Asaf Ben Natan)
- EDM-2805: agent: improve reconciliation speed (Sam Batschelet)
- EDM-2852: Disconnected status inconsistent with lastSeen when agent stuck on /rendered long-poll (Ori Amizur)
- NO-ISSUE: proper login error when pam issuer's cookie expires (Asaf Ben Natan)
- EDM-2836: Separate UI and CLI artifacts certs (Frank A. Zdarsky)
- NO-ISSUE: Add a troubleshooting section to auth-openshift (Itzik Brown)
- NO-ISSUE: support branding for flightctl-pam-issuer (Asaf Ben Natan)
- EDM-2261: Add templating for application images (Kyle Kyrazis)
- EDM-2837: pin image versions (Frank A. Zdarsky)
- EDM-2833: Allow modification of /etc/hostname (Ben Keith)
- NO-ISSUE: Add link to configuring auth after installing service on Linux (Celia Amador)
- NO-ISSUE: fixed service config location (Asaf Ben Natan)
- NO-ISSUE: Bug fix where CLI overrides 365 day ecert default (Lily Sturmann)
- EDM-2820: Agent retries indenfinetly if invalid image is parsed by prefetch manager (#2259) (kkyrazis)
- EDM-2814: Use db-specific config, instead of service-config. Fix wait script for the new format (rawagner)
- EDM-2823: using encrypted cookie instead of serverside cache for sessions, added rate limiter (Asaf Ben Natan)
- NO-ISSUE: Added documentation on managing auth providers via the UI (Celia Amador)
- EDM-2289: added missing EOL 2 (Amir Yogev)
- EDM-2289: added missing EOL (Amir Yogev)
- EDM-2289: [Documentation] Kubernetes RBAC requirements for secretRef feature (Amir Yogev)
- EDM-2841: Add external db cert mounts (Dakota Crowder)
- EDM-2814: Use new DB config (rawagner)
- NO-ISSUE: fixed refresh token not keeping scopes for the first request (Asaf Ben Natan)
- EDM-2814: RPM of flightctl-services should have the proper database config in /etc/flightctl/service-config.yaml (#2246) (Gregory Shilin)
- EDM-2815: Documentation for external database should be updated (#2247) (Gregory Shilin)
- NO-ISSUE: hostname to be resolved in containers (Asaf Ben Natan)
- NO-ISSUE: Enable ingress from same ns too (rawagner)
- EDM-2536: separate device systemd status (Eldar Weiss)
- EDM-2799: added offline access scope to get refresh token, fixed extraction of id_token in renew case (Asaf Ben Natan)
- EDM-2794: add pam-issuer certs, set restrictive file permissions (Frank A. Zdarsky)
- EDM-1959:Safeguard flightctl config and dirs (Siddarth R)
- EDM-2562: Apply override to UI tags only for releases (#2220) (Celia Amador Gonzalez)
- EDM-2794: add missing TLS SANs, validate FQDN (Frank A. Zdarsky)
- NO-ISSUE: update certificate docs for accuracy (Lily Sturmann)
- NO-ISSUE: extend enrollment cert default validity to 1 year (Lily Sturmann)
- NO-ISSUE: Update config's server cert validity to match helm (Lily Sturmann)
- NO-ISSUE: Change enrollment cert signer name to DeviceEnrollment for clarity (Lily Sturmann)
- NO-ISSUE: Change management cert signer name to DeviceManagement for clarity (Lily Sturmann)
- EDM-2795: Ensure consumer group is recreated on Redis restart  (#2227) (Ilya)
- EDM-2800: Fix resource sync failing to update fleets with owners (Avishay Traeger)
- EDM-2736: Add note on restrictions of perUser orgAssignment (Celia Amador)
- NO-ISSUE: cosmetic fixes (Frank A. Zdarsky)
- NO-ISSUE: Update auth-aap scopes. (Itzik Brown)
- EDM-2780: EDM-2784,EDM-2785,EDM-2787,EDM-2788 - doc changes (Asaf Ben Natan)
- EDM-2159: Update Must-gather (Siddarth R)
- EDM-2756: Add Quadlet nmae validation (Siddarth R)
- EDM-2569: Change apiEndpoint to service, adjust values.yaml Remove unecessary role/binding (rawagner)
- NO-ISSUE: fixed org not extracted correctly for alert manager (Asaf Ben Natan)
- NO-ISSUE: adding fields to pam issuer template (Asaf Ben Natan)
- NO-ISSUE: fixed excessive logs (Asaf Ben Natan)
- EDM-2616: Add Policy removing Application Vols (Siddarth R)
- EDM-2569: helm: flightctl api discovery (Sam Batschelet)
- EDM-2753: Fix quadlet status reporting (#2208) (kkyrazis)
- NO-ISSUE: Add quadlet and container examples (#2200) (kkyrazis)
- EDM-2754: Add quadlet validation for defined workloads (#2213) (kkyrazis)
- NO-ISSUE: fix login form xss (Asaf Ben Natan)
- NO-ISSUE: Update istalling on linux docs (Dakota Crowder)
- EDM-2786: Use auto instead of hardcoded ssl_ecdh_curve value (Dakota Crowder)
- NO-ISSUE: default to CS9 mock when building RPMs (#2209) (Ilya)
- EDM-2781: Fix ResourceSync fleet owner setting (Avishay Traeger)
- EDM-2764: cluster role suffix based on release (Asaf Ben Natan)
- NO-ISSUE: fix label override (Asaf Ben Natan)
- EDM-2776: Correct doc on permission to approve ERs (Celia Amador)
- EDM-2696: Fix embedded quadlet app reconciliation (Kyle Kyrazis)
- EDM-2688: [CI] fix to autocompletion tests on OCP (Eldar Weiss)
- EDM-2779: add to docs (Asaf Ben Natan)
- NO-ISSUE: Fix local UI deployment in kind cluster (Celia Amador)
- EDM-2778: fix userinfo is org validated (Asaf Ben Natan)
- EDM-2778: fix validate and get orgs not passing org validation if the user (Asaf Ben Natan)
- NO-ISSUE: Consolidate http org middleware for clarity and fix issue with orgID == uuid.Nil conditional resulting in a different org than was passed being set (Dakota Crowder)
- NO-ISSUE: Fix commit parsing in rpm spec (#2187) (kkyrazis)
- EDM-2201: docs/user/reference: add certificate architecture doc (Sam Batschelet)
- EDM-2741: fix redis e2e on OCP (amalykhi)
- EDM-2744: Give access to install_var_run_t files (#2186) (Ben Keith)
- EDM-2757: Add quadlet file cross referencing (Kyle Kyrazis)
- EDM-2760: multi-auth listing AP's without org filter NO-ISSUE: prevent non super-admins from setting static role = super admin , or by receiving it as a dynamic role value (Asaf Ben Natan)
- EDM-2689: Fix systemd unit in status when spec empty (Frank A. Zdarsky)
- EDM-2761: support edit for AP EDM-2762: updating client secret does not refresh cache (Asaf Ben Natan)
- EDM-2743: helm - cleanup all temporary resources (#2163) (Ilya)
- EDM-2698: added project filter for openshift (Asaf Ben Natan)
- NO-ISSUE: api: policy: make startGraceDuration required (Sam Batschelet)
- EDM-2748: ResourceSync should not overwrite annotations (Avishay Traeger)
- EDM-1332: Suppress bootc SELinux error (Ben Keith)
- EDM-2744: SELinux - allow connections to kernel stream sockets (#2165) (Ben Keith)
- EDM-2710: re-added password flow for the default quadlets deployment (Asaf Ben Natan)
- EDM-2739: added validation for oauth2/oidc with static role assignment - only known external roles are allowed (Asaf Ben Natan)
- EDM-2731: Allow cli-artifacts to be exposed by NodePorts (Celia Amador)
- EDM-2732: cleanup flightctl-cert-generator job when succeeded (#2160) (Ilya)
- EDM-2694: fixed alert manager proxy config (Asaf Ben Natan)
- NO-ISSUE: agent: cleanup noisy logs (Sam Batschelet)
- EDM-2402: Add opt out functionality for quadlet cert generation (Dakota Crowder)
- EDM-2729: helm: fix nil ui port (Sam Batschelet)
- NO-ISSUE: Fix version discovery (Ben Keith)
- NO-ISSUE: Update current-version script (Ben Keith)
- Revert "EDM-2648: flightctl version is not showing the client version in the …" (Ben Keith)
- NO-ISSUE: add more info for openshift auth (Asaf Ben Natan)
- NO-ISSUE: wokrflow - fix api readiness script (#2137) (Ilya)
- NO-ISSUE: auto select first org (Noga Magen)
- EDM-2716: fixed installer permissions (Asaf Ben Natan)
- EDM-2454: Set device integrity status to Unsupported for non-TPM enrollments (#2133) (Andy Dalton)
- EDM-2720: Use fallback for empty agent version in audit logger (Gal Elharar)
- EDM-2628: Add additional context to version command when config file is missing (Dakota Crowder)
- EDM-2712: Mount ca bundle in api container (Dakota Crowder)
- EDM-2681/2696 Quadlet applications management fixes (#2114) (kkyrazis)
- EDM-2711: client: add repository support (Sam Batschelet)
- EDM-2723: agent/status: ensure timeout (Sam Batschelet)
- EDM-2576: When logging in using CLI the default organization for a user is not shown (Noga Magen)
- EDM-2576: When logging in using CLI the default organization for a user is not shown (Noga Magen)
- EDM-2461: Write documentation for Agent Observability & Diagnostics (Gal)
- EDM-2093: Add redis restart tests (amalykhi)
- EDM-2716: fixed installer permissions (Asaf Ben Natan)
- NO-ISSUE: dont run upgrade test after merge (#2111) (Ilya)
- EDM-2713: super admin is granted access to all orgs (Asaf Ben Natan)
- EDM-2305: Remove accidental binary (Dakota Crowder)
- EDM-2305: Add basic domain and auth type validation to config rendering (Dakota Crowder)
- EDM-2709: Address race condition in services startup, modify templating/tls default config (Dakota Crowder)
- EDM-2707: fixed introspection field validation (Asaf Ben Natan)
- NO-ISSUE: added FlakeAttempts for redis (Asaf Ben Natan)
- EDM-2677: table now shows "" for non default (Asaf Ben Natan)
- EDM-1687: cleanup leftovers, wait for api to become ready (#2106) (Ilya)
- EDM-1687: helm upgrade test (#1993) (Ilya)
- NO-ISSUE: Add INJECT_CONFIG parameter to conditionally inject config into agent-vm qcow2 image (Gal Elharar)
- EDM-2705: fix role mapping to exclude internal role names coming from auth provider , fixed auth provider cache invalidation for org assignment / role assignment (Asaf Ben Natan)
- EDM-2562: Use latest UI tag on push to branch (rawagner)
- EDM-2473: build RPMs for arbitrary system (#2081) (Ilya)
- EDM-2699: Fix setting global CA certs (Frank A. Zdarsky)
- EDM-2699: Fix missing newline in ca-bundle, causing flightctl-ui to crashloop (Frank A. Zdarsky)
- EDM-2699: Remove now unused certs PV (Frank A. Zdarsky)
- EDM-2699: Move ca-bundle to Secret (Frank A. Zdarsky)
- EDM-2699: Add common names (Frank A. Zdarsky)
- EDM-2638: adds token expiration date to docs for pam issuer , adds info about login command (Asaf Ben Natan)
- EDM-2702: fixed aap public oauth2 client (Asaf Ben Natan)
- EDM-2071: support proper oauth2 token validation types (rfc/github) (Asaf Ben Natan)
- EDM-2473: offline e2e cert injection (#2089) (Ilya)
- EDM-2648: flightctl version is not showing the client version in the latest helm chart (#2086) (Gregory Shilin)
- EDM-2692: Enable alerts in the UI quadlets deployment (Celia Amador)

* Wed Nov 26 2025 Dakota Crowder <dcrowder@redhat.com> - 1.0-1
- Adding certificate generation service
* Mon Nov 17 2025 Dakota Crowder <dcrowder@redhat.com> - 1.0-1
- Refactoring quadlet install, add standalone utils
* Wed Nov 12 2025 Ben Keith <bkeith@redhat.com> - 1.0-1
- Make observability and telemetry-gateway packages require services package
* Mon Oct 27 2025 Dakota Crowder <dcrowder@redhat.com> - 1.0-1
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
