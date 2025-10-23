# Build configuration flags: by default enable all packages
# To disable use: --without services
%bcond_without services
%bcond_without observability

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

%{expand:%(cat packaging/rpm/packages/main.spec)}
%{expand:%(cat packaging/rpm/packages/licences.spec)}
%{expand:%(cat packaging/rpm/packages/cli.spec)}
%{expand:%(cat packaging/rpm/packages/agent.spec)}
%{expand:%(cat packaging/rpm/packages/selinux.spec)}
%{expand:%(cat packaging/rpm/packages/telemetry-gateway.spec)}
%{expand:%(cat packaging/rpm/packages/services.spec)}
%{expand:%(cat packaging/rpm/packages/observability.spec)}

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
  SOURCE_GIT_COMMIT="%{?SOURCE_GIT_COMMIT:%{SOURCE_GIT_COMMIT}}%{!?SOURCE_GIT_COMMIT:%(echo %{version} | grep -o '[-~]g[0-9a-f]*' | sed 's/[-~]g//' || echo unknown)}" \
  SOURCE_GIT_TAG_NO_V="%{?SOURCE_GIT_TAG_NO_V:%{SOURCE_GIT_TAG_NO_V}}%{!?SOURCE_GIT_TAG_NO_V:%{version}}" \

  # Execute modular build commands
  %{cli_build_commands}
  %{agent_build_commands}
  %{selinux_build_commands}

%install
  # Execute modular install commands
  %{licences_install_commands}
  %{cli_install_commands}
  %{agent_install_commands}
  %{selinux_install_commands}
  %{services_install_commands}
  %{observability_install_commands}
  %{telemetry_gateway_install_commands}

%check
  %{buildroot}%{_bindir}/flightctl-agent version

%changelog
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

