%define debug_package %{nil}

Name: flightctl
Version: 0.0.1
Release: 5%{?dist}
Summary: Flightctl CLI

License: XXX
URL: https://github.com/flightctl/flightctl
Source0: flightctl-0.0.1.tar.gz

BuildRequires: golang
BuildRequires: make
BuildRequires: git
BuildRequires: openssl-devel
Requires: openssl

%description
Flightctl is a command line interface for managing edge device fleets.


%package agent
Summary: Flightctl Agent
%description agent
Flightctl Agent is a component of the flightctl tool.

%prep
%setup -q -n flightctl-0.0.1

%build

# if this is a buggy version of go we need to set GOPROXY as workaround
# see https://github.com/golang/go/issues/61928
GOENVFILE=$(go env GOROOT)/go.env
if [[ ! -f "{$GOENVFILE}" ]]; then
    export GOPROXY='https://proxy.golang.org,direct'
fi
make build

%install
mkdir -p %{buildroot}/usr/bin
cp bin/flightctl %{buildroot}/usr/bin
mkdir -p %{buildroot}/usr/lib/systemd/system
mkdir -p %{buildroot}/%{_sharedstatedir}/flightctl
mkdir -p %{buildroot}/usr/lib/greenboot/check/required.d
install -m 0755 packaging/greenboot/flightctl-agent-running-check.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
cp bin/flightctl-agent %{buildroot}/usr/bin
cp packaging/must-gather/flightctl-must-gather %{buildroot}/usr/bin
cp packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system

%files
/usr/bin/flightctl

%files agent
/usr/bin/flightctl-agent
/usr/bin/flightctl-must-gather
/usr/lib/systemd/system/flightctl-agent.service
%{_sharedstatedir}/flightctl
/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh

%changelog
* Wed Aug 21 2024 Sam Batschelet <sbatsche@redhat.com> - 0.0.1-5
- Add must-gather script to provide a simple mechanism to collect agent debug
* Wed Aug 7 2024 Sam Batschelet <sbatsche@redhat.com> - 0.0.1-4
- Add basic greenboot support for failed flightctl-agent service 
* Wed Mar 13 2024 Ricardo Noriega <rnoriega@redhat.com> - 0.0.1-3
- New specfile for both CLI and agent packages
