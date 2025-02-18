# Disable debug information package creation
%define debug_package %{nil}

# Define the Go Import Path
%global goipath github.com/flightctl/flightctl

# SELinux specifics
%global selinuxtype targeted
%define selinux_policyver 3.14.3-67
%define agent_relabel_files() \
    semanage fcontext -a -t flightctl_agent_exec_t "/usr/bin/flightctl-agent" ; \
    restorecon -v /usr/bin/flightctl-agent

Name:           flightctl
Version:        0.4.0
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

# cli sub-package
%package cli
Summary: Flight Control CLI
%description cli
flightctl is the CLI for controlling the Flight Control service.

# agent sub-package
%package agent
Summary: Flight Control management agent

Requires: flightctl-selinux = %{version}
Requires: bootc

%description agent
The flightctl-agent package provides the management agent for the Flight Control fleet management service.


%package selinux
Summary: SELinux policies for the Flight Control management agent
BuildRequires: selinux-policy >= %{selinux_policyver}
BuildRequires: selinux-policy-devel >= %{selinux_policyver}
BuildArch: noarch
Requires: flightctl-agent = %{version}
Requires: selinux-policy >= %{selinux_policyver}

%description selinux
The flightctl-selinux package provides the SELinux policy modules required by the Flight Control management agent.


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

    SOURCE_GIT_TAG=%{version} \
    SOURCE_GIT_TREE_STATE=clean \
    SOURCE_GIT_COMMIT=$(echo %{version} | awk -F'~g' '{print $2}') \
    SOURCE_GIT_TAG_NO_V=%{version} \
    make build-cli build-agent

    # SELinux modules build
    make --directory packaging/selinux

%install
    mkdir -p %{buildroot}/usr/bin
    cp bin/flightctl %{buildroot}/usr/bin
    mkdir -p %{buildroot}/usr/lib/systemd/system
    mkdir -p %{buildroot}/%{_sharedstatedir}/flightctl
    mkdir -p %{buildroot}/usr/lib/flightctl/hooks.d/{afterupdating,beforeupdating,afterrebooting,beforerebooting}
    mkdir -p %{buildroot}/usr/lib/greenboot/check/required.d
    install -m 0755 packaging/greenboot/flightctl-agent-running-check.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    cp bin/flightctl-agent %{buildroot}/usr/bin
    cp packaging/must-gather/flightctl-must-gather %{buildroot}/usr/bin
    cp packaging/hooks.d/afterupdating/00-default.yaml %{buildroot}/usr/lib/flightctl/hooks.d/afterupdating
    cp packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system
    bin/flightctl completion bash > flightctl-completion.bash
    install -Dpm 0644 flightctl-completion.bash -t %{buildroot}/%{_datadir}/bash-completion/completions
    bin/flightctl completion fish > flightctl-completion.fish
    install -Dpm 0644 flightctl-completion.fish -t %{buildroot}/%{_datadir}/fish/vendor_completions.d/
    bin/flightctl completion zsh > _flightctl-completion
    install -Dpm 0644 _flightctl-completion -t %{buildroot}/%{_datadir}/zsh/site-functions/
    install -d %{buildroot}%{_datadir}/selinux/packages/%{selinuxtype}
    install -m644 packaging/selinux/*.bz2 %{buildroot}%{_datadir}/selinux/packages/%{selinuxtype}

    rm -f licenses.list

    find . -type f -name LICENSE -or -name License | while read LICENSE_FILE; do
        echo "%{_datadir}/licenses/%{NAME}/${LICENSE_FILE}" >> licenses.list
    done
    mkdir -vp "%{buildroot}%{_datadir}/licenses/%{NAME}"
    cp LICENSE "%{buildroot}%{_datadir}/licenses/%{NAME}"

    mkdir -vp "%{buildroot}%{_docdir}/%{NAME}"

    for DOC in docs examples .markdownlint-cli2.yaml README.md; do
        cp -vr "${DOC}" "%{buildroot}%{_docdir}/%{NAME}/${DOC}"
    done

%check
    %{buildroot}%{_bindir}/flightctl-agent version


%pre selinux
%selinux_relabel_pre -s %{selinuxtype}

%post selinux

%selinux_modules_install -s %{selinuxtype} %{_datadir}/selinux/packages/%{selinuxtype}/flightctl_agent.pp.bz2
%agent_relabel_files

%postun selinux

if [ $1 -eq 0 ]; then
    %selinux_modules_uninstall -s %{selinuxtype} flightctl_agent
fi

%posttrans selinux

%selinux_relabel_post -s %{selinuxtype}

# File listings
# No %files section for the main package, so it won't be built

%files cli -f licenses.list
    %{_bindir}/flightctl
    %license LICENSE
    %{_datadir}/bash-completion/completions/flightctl-completion.bash
    %{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
    %{_datadir}/zsh/site-functions/_flightctl-completion

%files agent -f licenses.list
    %license LICENSE
    %{_bindir}/flightctl-agent
    %{_bindir}/flightctl-must-gather
    /usr/lib/flightctl/hooks.d/afterupdating/00-default.yaml
    /usr/lib/systemd/system/flightctl-agent.service
    %{_sharedstatedir}/flightctl
    /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    %{_docdir}/%{NAME}/*
    %{_docdir}/%{NAME}/.markdownlint-cli2.yaml

%files selinux
%{_datadir}/selinux/packages/%{selinuxtype}/flightctl_agent.pp.bz2

%changelog

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
