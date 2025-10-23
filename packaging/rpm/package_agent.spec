# agent sub-package
%package agent
Summary: Flight Control management agent
Requires: flightctl-selinux = %{version}

%description agent
The flightctl-agent package provides the management agent for the Flight Control fleet management service.

# Agent build commands
%global agent_build_commands %{?rhel:%(if [ "%{rhel}" = "9" ]; then echo "%make_build build-agent"; else echo "DISABLE_FIPS=\"true\" %make_build build-agent"; fi)}%{!?rhel:DISABLE_FIPS="true" %make_build build-agent}

# Agent install commands
%global agent_install_commands \
install -D -m 0755 bin/flightctl-agent %{buildroot}%{_bindir}/flightctl-agent; \
install -D -m 0755 hack/flightctl-must-gather %{buildroot}%{_bindir}/flightctl-must-gather; \
install -D -m 0644 packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system/flightctl-agent.service; \
install -D -m 0644 packaging/tmpfiles/flightctl.conf %{buildroot}/usr/lib/tmpfiles.d/flightctl.conf; \
install -D -m 0644 packaging/greenboot/20_check_flightctl_agent.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh; \
install -D -m 0644 packaging/flightctl-hooks/00-default.yaml %{buildroot}/usr/lib/flightctl/hooks.d/afterupdating/00-default.yaml; \
install -D -m 0644 packaging/sosreport/flightctl.py %{buildroot}/usr/share/sosreport/flightctl.py

%files agent -f licenses.list
    %license LICENSE
    %dir /etc/flightctl
    %{_bindir}/flightctl-agent
    %{_bindir}/flightctl-must-gather
    /usr/lib/flightctl/hooks.d/afterupdating/00-default.yaml
    /usr/lib/systemd/system/flightctl-agent.service
    /usr/lib/tmpfiles.d/flightctl.conf
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

  INSTALL_DIR="/usr/lib/python$(python3 --version | sed 's/^.* \(3[.][0-9]*\).*$/\1/')/site-packages/sos/report/plugins"
  mkdir -p $INSTALL_DIR
  cp /usr/share/sosreport/flightctl.py $INSTALL_DIR
  chmod 0644 $INSTALL_DIR/flightctl.py
  rm -rf /usr/share/sosreport
