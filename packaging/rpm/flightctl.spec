# Disable debug information package creation
%define debug_package %{nil}

# Define the Go Import Path
%global goipath github.com/flightctl/flightctl
Version:    0.3.0
%gometa

Name:       flightctl
Release:    1%{?dist}
Summary:    Flightctl is a manager of the edge device fleets.

License:    Apache-2.0
URL:        %{gourl}
Source0:    %{gosource}
Source1:    %{archivename}-vendor.tar.bz2

BuildRequires: go-rpm-macros
BuildRequires: git
BuildRequires: compiler(go-compiler)
BuildRequires: openssl-devel

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
%setup -q -n flightctl-0.0.1

%build
# if this is a buggy version of go we need to set GOPROXY as workaround
# see https://github.com/golang/go/issues/61928
GOENVFILE=$(go env GOROOT)/go.env
if [[ ! -f "{$GOENVFILE}" ]]; then
    export GOPROXY='https://proxy.golang.org,direct'
fi
make build-cli build-agent \
SOURCE_GIT_TAG=$(git describe --tags --exclude latest) \
SOURCE_GIT_TREE_STATE=$(( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty') \
SOURCE_GIT_COMMIT=$(git rev-parse --short "HEAD^{commit}" 2>/dev/null) \
BIN_TIMESTAMP=$(date +'%Y%m%d') \
SOURCE_GIT_TAG_NO_V=$(echo ${SOURCE_GIT_TAG} | sed 's/^v//') \
MAJOR=$(echo ${SOURCE_GIT_TAG_NO_V} | awk -F'[._~-]' '{print $1}') \
MINOR=$(echo ${SOURCE_GIT_TAG_NO_V} | awk -F'[._~-]' '{print $2}') \
PATCH=$(echo ${SOURCE_GIT_TAG_NO_V} | awk -F'[._~-]' '{print $3}')

%install
install -m 0755 -vd %{buildroot}%{_bindir} \
                    %{buildroot}/usr/lib/systemd/system \
                    %{buildroot}/%{_sharedstatedir}/flightctl

install -Dm 0755 packaging/greenboot/flightctl-agent-running-check.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
cp bin/flightctl bin/flightctl-agent packaging/must-gather/flightctl-must-gather %{buildroot}%{_bindir}
cp packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system

install -m 0755 -vd %{buildroot}/usr/lib/flightctl/hooks.d/{afterupdating,beforeupdating,afterrebooting,beforerebooting}
install -m 0755 -vd %{buildroot}/usr/lib/greenboot/check/required.d
install -m 0755 -vp packaging/hooks.d/afterupdating/00-default.yaml %{buildroot}/usr/lib/flightctl/hooks.d/afterupdating

%{buildroot}%{_bindir}/flightctl completion bash > flightctl-completion.bash
install -Dpm 0644 flightctl-completion.bash -t %{buildroot}/%{_datadir}/bash-completion/completions/flightctl-completion.bash
%{buildroot}%{_bindir}/flightctl completion fish > flightctl-completion.fish
install -Dpm 0644 flightctl-completion.fish -t %{buildroot}/%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
%{buildroot}%{_bindir}/flightctl completion zsh > _flightctl-completion
install -Dpm 0644 _flightctl-completion -t %{buildroot}/%{_datadir}/zsh/site-functions/_flightctl-completion

mkdir -vp "%{buildroot}%{_docdir}/%{NAME}"

for DOC in docs examples .markdownlint-cli2.yaml README.md; do
    echo "DOC $DOC" "%{buildroot}%{_docdir}/%{NAME}/${DOC}"
    cp -vr "${DOC}" "%{buildroot}%{_docdir}/%{NAME}/${DOC}"
done

%check
%{buildroot}%{_bindir}/flightctl-agent version

# File listings
# No %files section for the main package, so it won't be built

%files cli
%{_bindir}/flightctl
%{_datadir}/bash-completion/completions/flightctl-completion.bash
%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
%{_datadir}/zsh/site-functions/_flightctl-completion
%{_docdir}/%{NAME}/*
%{_docdir}/%{NAME}/.markdownlint-cli2.yaml

%files agent
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
