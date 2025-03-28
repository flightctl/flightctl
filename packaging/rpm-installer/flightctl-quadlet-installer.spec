Name:           flightctl-quadlet-installer
Version:        0.5.0
Release:        1%{?dist}
Summary:        Flight Control Quadlet Installer

License:        Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND ISC AND MIT

Source0:        1%{?dist}

BuildArch:      noarch
BuildRequires:  make
Requires:       bash

%description
The flightctl-quadlet-installer package provides quadlet files and setup for running Flight Control services

%prep
%setup -q

%build
# No compilation needed for this package

%install
# Create the target directory
mkdir -p %{buildroot}/etc/flightctl/

# Copy files into the build root
cp -r deploy/podman %{buildroot}/etc/flightctl/templates
cp deploy/scripts/installer.sh %{buildroot}/etc/flightctl/installer.sh
cp deploy/scripts/shared.sh %{buildroot}/etc/flightctl/shared.sh

%files
%defattr(0755,root,root,-)
/etc/flightctl/templates
/etc/flightctl/installer.sh
/etc/flightctl/shared.sh

%changelog
* Wed Mar 26 2025 Dakota Crowder <dcrowder@redhat.com> - 0.0.1
- Initial spec definition
