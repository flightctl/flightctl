# Disable debug information package creation
%define debug_package %{nil}

# Define the Go Import Path
%global goipath github.com/flightctl/flightctl

Name:           flightctl
Version:        0.3.0
Release:        1%{?dist}
Summary:        Flightctl is a manager of the edge device fleets.

%gometa

License:        Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND ISC AND MIT
URL:            %{gourl}

Source0:        flightctl-0.3.0.tar.gz

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
Summary: Flightctl CLI
%description cli
Flightctl is a command line interface for managing edge device fleets.

# agent sub-package
%package agent
Summary: Flightctl Agent

Requires: bootc

%description agent
Flightctl Agent is a component of the flightctl tool.

%prep
%goprep -A
%setup -q %{forgesetupargs}

%build
    # if this is a buggy version of go we need to set GOPROXY as workaround
    # see https://github.com/golang/go/issues/61928
    GOENVFILE=$(go env GOROOT)/go.env
    if [[ ! -f "{$GOENVFILE}" ]]; then
        export GOPROXY='https://proxy.golang.org,direct'
    fi
    SOURCE_GIT_TAG=%{release} SOURCE_GIT_TREE_STATE=clean SOURCE_GIT_COMMIT=%{release} SOURCE_GIT_TAG_NO_V=%{version} make build-cli build-agent

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

    rm -f licenses.list

    find -type f -name LICENSE -or -name License | while read LICENSE_FILE; do
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

# File listings
# No %files section for the main package, so it won't be built

%files cli -f licenses.list
    %{_bindir}/flightctl
    %license LICENSE
    %{_datadir}/bash-completion/completions/flightctl-completion.bash
    %{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
    %{_datadir}/zsh/site-functions/_flightctl-completion
    %{_docdir}/%{NAME}/*
    %{_docdir}/%{NAME}/.markdownlint-cli2.yaml

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


%changelog
* Mon Nov 4 2024 Miguel Angel Ajo <majopela@redhat.com> - 0.3.0-1
- Move the Release field to -1 so we avoid auto generating packages
  with -5 all the time.
* Wed Aug 21 2024 Sam Batschelet <sbatsche@redhat.com> - 0.0.1-5
- Add must-gather script to provide a simple mechanism to collect agent debug
* Wed Aug 7 2024 Sam Batschelet <sbatsche@redhat.com> - 0.0.1-4
- Add basic greenboot support for failed flightctl-agent service
* Wed Mar 13 2024 Ricardo Noriega <rnoriega@redhat.com> - 0.0.1-3
- New specfile for both CLI and agent packages
