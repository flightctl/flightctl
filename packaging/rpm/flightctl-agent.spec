%define debug_package %{nil}

Name: flightctl-agent
Version: 0.0.1
Release: 2%{?dist}
Summary: Flightctl Agent

License: XXX
URL: https://github.com/flightctl/flightctl
Source0: flightctl-agent-0.0.1.tar.gz

BuildRequires: golang
BuildRequires: make
BuildRequires: git
BuildRequires: openssl-devel
Requires: openssl

%description
Flightctl Agent is a component of the flightctl tool.

%prep
%setup -q -n flightctl-agent-0.0.1

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
mkdir -p %{buildroot}/usr/lib/systemd/system
cp bin/flightctl-agent %{buildroot}/usr/bin
cp packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system


%files
/usr/bin/flightctl-agent
/usr/lib/systemd/system/flightctl-agent.service

%changelog

* Wed Feb 7 2024 Miguel Angel Ajo Pelayo <majopela@redhat.com> - 0.0.1-2
- Initial RPM building via packit for the flightctl agent
* Mon Dec 11 2023 Ricardo Noriega <rnoriega@redhat.com> - 0.0.1-1
- Initial RPM package for flightctl agent
