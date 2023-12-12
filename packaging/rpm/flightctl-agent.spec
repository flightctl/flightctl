%define debug_package %{nil}

Name: flightctl-agent
Version: 0.0.1
Release: 1%{?dist}
Summary: Flightctl Agent

License: XXX
URL: https://github.com/flightctl/flightctl
Source0: flightctl-agent-0.0.1.tar

%description
Flightctl Agent is a component of the flightctl tool.

%prep
%setup -q

%build

%install
mkdir -p %{buildroot}/usr/bin
mkdir -p %{buildroot}/usr/lib/systemd/system
cp flightctl-agent %{buildroot}/usr/bin
cp flightctl-agent.service %{buildroot}/usr/lib/systemd/system



%files
/usr/bin/flightctl-agent
/usr/lib/systemd/system/flightctl-agent.service

%changelog
* Mon Dec 11 2023 Ricardo Noriega <rnoriega@redhat.com> - 0.0.1-1
- Initial RPM package for flightctl agent
