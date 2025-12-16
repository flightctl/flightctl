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
Requires: jq

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
%setup -q %{forgesetupargs}

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
        --bin-dir "%{buildroot}/usr/bin"

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
    %dir %{_sysconfdir}/flightctl/pki/flightctl-api
    %dir %{_sysconfdir}/flightctl/pki/flightctl-alertmanager-proxy
    %dir %{_sysconfdir}/flightctl/pki/flightctl-pam-issuer
    %dir %{_sysconfdir}/flightctl/pki/db
    %dir %{_sysconfdir}/flightctl/flightctl-api
    %dir %{_sysconfdir}/flightctl/flightctl-ui
    %dir %{_sysconfdir}/flightctl/flightctl-cli-artifacts
    %dir %{_sysconfdir}/flightctl/flightctl-alertmanager-proxy
    %dir %{_sysconfdir}/flightctl/flightctl-pam-issuer
    %dir %{_sysconfdir}/flightctl/flightctl-db-migrate
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
