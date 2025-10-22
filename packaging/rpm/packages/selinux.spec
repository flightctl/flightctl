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

%build
%make_build --directory packaging/selinux

%install
install -d %{buildroot}%{_datadir}/selinux/packages/%{selinuxtype}
install -m644 packaging/selinux/*.bz2 %{buildroot}%{_datadir}/selinux/packages/%{selinuxtype}

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

%files selinux
  %{_datadir}/selinux/packages/%{selinuxtype}/flightctl_agent.pp.bz2
