# Build configuration flags: by default enable all packages
# To disable use: --without cli
%bcond_without cli
%bcond_without agent
%bcond_without selinux
%bcond_without services
%bcond_without otel_collector
%bcond_without observability

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
# No %%files section for the main package, so it won't be built

%if %{with cli}
  %include packages/cli.spec
%endif

%if %{with agent}
  %include packages/agent.spec
%endif

%if %{with selinux}
  %include packages/selinux.spec
%endif

%if %{with services}
  %include packages/services.spec
%endif

%if %{with otel_collector}
  %include packages/otel-collector.spec
%endif

%if %{with observability}
  %include packages/observability.spec
%endif

%prep
  %goprep -A
  %setup -q %{forgesetupargs}

%check
  %include tests/tests.spec

%build
  %if %{with cli} || %{with agent}
    %include build/build.spec
  %endif
  %if %{with selinux}
    %include build/selinux.spec
  %endif

%install
  %if %{with cli} || %{with agent}
    %include install/flightctl.spec
  %endif
  %if %{with selinux}
    %include install/selinux.spec
  %endif
  %include install/licenses.spec
  %if %{with services}
    %include install/services.spec
  %endif
  %if %{with observability}
    %include install/observability.spec
  %endif

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
