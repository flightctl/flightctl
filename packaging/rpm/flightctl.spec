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

%description
# Main package is empty and not created.

# File listings
# No %%files section for the main package, so it won't be built

# cli sub-package
%package cli
Summary: Flight Control CLI
%description cli
flightctl is the CLI for controlling the Flight Control service.

%files cli -f licenses.list
    %{_bindir}/flightctl
    %{_bindir}/flightctl-restore
    %license LICENSE
    %{_datadir}/bash-completion/completions/flightctl-completion.bash
    %{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
    %{_datadir}/zsh/site-functions/_flightctl-completion

# %include packaging/rpm/packages/main.spec
# %include packaging/rpm/packages/cli.spec
# %include packaging/rpm/packages/agent.spec
# %include packaging/rpm/packages/selinux.spec
# %include packaging/rpm/packages/telemetry-gateway.spec
# %include packaging/rpm/packages/services.spec
# %include packaging/rpm/packages/observability.spec

%prep
%goprep -A
%setup -q %{forgesetupargs}

%build
echo "Testing absolute path include:"
ls -la /builddir/build/BUILD/flightctl-1.0.0_main_147_g36fb8aba-build/flightctl-1.0.0~main~147~g36fb8aba/packaging/rpm/packages/main.spec

echo "Testing source subdirectory:"
basename $(pwd)
ls -la ../$(basename $(pwd))/packaging/rpm/packages/main.spec

# %include packaging/rpm/build/build.spec

%install
# %include packaging/rpm/install/licences.spec
# %include packaging/rpm/install/flightctl.spec
# %include packaging/rpm/install/selinux.spec
# %include packaging/rpm/install/services.spec
# %include packaging/rpm/install/observability.spec

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

