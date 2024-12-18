# Disable debug information package creation
%define debug_package %{nil}

# Build mode:
# - upstream
# - downstream [default]
%{expand: %%define build_mode %(echo ${BUILD_MODE:-downstream})}
%if "%{build_mode}" != "downstream" && "%{build_mode}" != "upstream"
        %error BUILD MODE must be downstream or upstream
%endif

# Define the Go Import Path
%global goipath github.com/flightctl/flightctl

Version:        0.3.0
%gometa

Name:           flightctl
Release:        1%{?dist}
Summary:        Flightctl is a manager of the edge device fleets.

License:        Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND ISC AND MIT
URL:            %{gourl}

Source0:        flightctl-0.3.0.tar.gz
%if "%{build_mode}" == "upstream"
    Source1:        %{archivename}-vendor.tar.bz2
%endif

BuildRequires:  git
BuildRequires:  openssl-devel
%if "%{build_mode}" == "upstream"
BuildRequires:  go-rpm-macros
BuildRequires:  compiler(go-compiler)
BuildRequires:  libvirt-devel
%else
BuildRequires: golang
BuildRequires: make
%endif
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
%description agent
Flightctl Agent is a component of the flightctl tool.

%prep
%goprep -A
%if "%{build_mode}" == "upstream"
%setup -q -T -D -a1 %{forgesetupargs}
%else
%setup -q %{forgesetupargs}
%endif

%build
    %if "%{build_mode}" == "upstream"
        SOURCE_GIT_TAG=$(git describe --tags --exclude latest)
        SOURCE_GIT_TREE_STATE=$(( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty')
        SOURCE_GIT_COMMIT=$(git rev-parse --short "HEAD^{commit}" 2>/dev/null)
        BIN_TIMESTAMP=$(date +'%Y%m%d')
        SOURCE_GIT_TAG_NO_V=$(echo ${SOURCE_GIT_TAG} | sed 's/^v//')
        MAJOR=$(echo ${SOURCE_GIT_TAG_NO_V} | awk -F'[._~-]' '{print $1}')
        MINOR=$(echo ${SOURCE_GIT_TAG_NO_V} | awk -F'[._~-]' '{print $2}')
        PATCH=$(echo ${SOURCE_GIT_TAG_NO_V} | awk -F'[._~-]' '{print $3}')

        export GO_LDFLAGS="-X %{goipath}/pkg/version.majorFromGit=${MAJOR} -X %{goipath}/pkg/version.minorFromGit=${MINOR} -X %{goipath}/pkg/version.patchFromGit=${PATCH} -X %{goipath}/pkg/version.versionFromGit=${SOURCE_GIT_TAG} -X %{goipath}/pkg/version.commitFromGit=${SOURCE_GIT_COMMIT} -X %{goipath}/pkg/version.gitTreeState=${SOURCE_GIT_TREE_STATE} -X %{goipath}/pkg/version.buildDate=${BIN_TIMESTAMP}"

        # LDFLAGS is deprecrated in Fedora, but appears to be the variable still used in RHEL
        %if "%{dist_vendor}" != "Fedora"
            export LDFLAGS="${GO_LDFLAGS}"
        %endif

        for cmd in cmd/* ; do
          %gobuild -o %{gobuilddir}/bin/$(basename $cmd) %{goipath}/$cmd
        done
    %else
        # if this is a buggy version of go we need to set GOPROXY as workaround
        # see https://github.com/golang/go/issues/61928
        GOENVFILE=$(go env GOROOT)/go.env
        if [[ ! -f "{$GOENVFILE}" ]]; then
            export GOPROXY='https://proxy.golang.org,direct'
        fi
        make build-cli build-agent
    %endif

%install
    %if "%{build_mode}" == "upstream"
         install -m 0755 -vd                     %{buildroot}%{_bindir}
         install -m 0755 -vp %{gobuilddir}/bin/* %{buildroot}%{_bindir}/


         %{buildroot}%{_bindir}/flightctl completion bash > flightctl-completion.bash
         install -Dpm 0644 flightctl-completion.bash %{buildroot}/%{_datadir}/bash-completion/completions/flightctl-completion.bash
         %{buildroot}%{_bindir}/flightctl completion fish > flightctl-completion.fish
         install -Dpm 0644 flightctl-completion.fish %{buildroot}/%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
         %{buildroot}%{_bindir}/flightctl completion zsh > _flightctl-completion
         install -Dpm 0644 _flightctl-completion %{buildroot}/%{_datadir}/zsh/site-functions/_flightctl-completion

         rm -f licenses.list

         find -type f -name LICENSE -or -name License | while read LICENSE_FILE; do
             install -Dv -m0644 "${LICENSE_FILE}" "%{buildroot}%{_datadir}/licenses/%{NAME}/${LICENSE_FILE}"
             echo "%{_datadir}/licenses/%{NAME}/${LICENSE_FILE}" >> licenses.list
         done

         cat licenses.list

         mkdir -vp "%{buildroot}%{_docdir}/%{NAME}"

         for DOC in docs examples .markdownlint-cli2.yaml README.md; do
             cp -vr "${DOC}" "%{buildroot}%{_docdir}/%{NAME}/${DOC}"
         done

         mkdir -vp %{buildroot}/%{_sharedstatedir}/flightctl \
                   %{buildroot}/usr/lib/systemd/system

         install -Dm 0755 packaging/greenboot/flightctl-agent-running-check.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
         cp packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system
         cp packaging/must-gather/flightctl-must-gather %{buildroot}/%{_bindir}
    %else
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
        install -Dpm 0644 flightctl-completion.bash -t %{buildroot}/%{_datadir}/bash-completion/completions/flightctl-completion.bash
        bin/flightctl completion fish > flightctl-completion.fish
        install -Dpm 0644 flightctl-completion.fish -t %{buildroot}/%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
        bin/flightctl completion zsh > _flightctl-completion
        install -Dpm 0644 _flightctl-completion -t %{buildroot}/%{_datadir}/zsh/site-functions/_flightctl-completion

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
    %endif

%check
    %if "%{build_mode}" == "upstream"
        export GOPATH="%{_builddir}/%{name}-%{version}/_build"
        cd "_build/src/github.com/flightctl/%{name}"
        for d in $(go list %{?exp} ./... | grep -v 'cmd\|scripts\|internal/tpm\|e2e/agent\|e2e/cli\|integration/agent\|integration/store\|device/console\|integration/tasks' ); do
            go test %{?exp} ${d}
        done
    %else
        %{buildroot}%{_bindir}/flightctl-agent version
    %endif

# File listings
# No %files section for the main package, so it won't be built

%files cli -f licenses.list
    %{_bindir}/flightctl
    %license LICENSE
    %if "%{build_mode}" == "upstream"
        %license vendor/modules.txt
    %endif
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
