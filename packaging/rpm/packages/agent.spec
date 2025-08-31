# agent sub-package
%package agent
Summary: Flight Control management agent

Requires: flightctl-selinux = %{version}

%description agent
The flightctl-agent package provides the management agent for the Flight Control fleet management service.

%files agent -f licenses.list
    %license LICENSE
    %dir /etc/flightctl
    %{_bindir}/flightctl-agent
    %{_bindir}/flightctl-must-gather
    /usr/lib/flightctl/hooks.d/afterupdating/00-default.yaml
    /usr/lib/systemd/system/flightctl-agent.service
    %{_sharedstatedir}/flightctl
    /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    %{_docdir}/%{NAME}/*
    %{_docdir}/%{NAME}/.markdownlint-cli2.yaml
    /usr/share/sosreport/flightctl.py

%post agent
  INSTALL_DIR="/usr/lib/python$(python3 --version | sed 's/^.* \(3[.][0-9]*\).*$/\1/')/site-packages/sos/report/plugins"
  mkdir -p $INSTALL_DIR
  cp /usr/share/sosreport/flightctl.py $INSTALL_DIR
  chmod 0644 $INSTALL_DIR/flightctl.py
  rm -rf /usr/share/sosreport
