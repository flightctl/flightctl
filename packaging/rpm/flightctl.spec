%define debug_package %{nil}

Name: flightctl
# PLEASE READ BEFORE CHANGING the Version: field
# https://docs.fedoraproject.org/en-US/packaging-guidelines/Versioning/
# TL;DR With upstream releases 0.4.0, 0.4.1, 0.5.0-rc1, 0.5.0-rc2, 0.5.0, the two "release candidates" should use 0.5.0~rc1 and 0.5.0~rc2 in the Version: field
Version: 0.3.0
Release: 1%{?dist}
Summary: Flightctl CLI

License: XXX
URL: https://github.com/flightctl/flightctl
Source0: flightctl-0.3.0.tar.gz

BuildRequires: golang
BuildRequires: make
BuildRequires: git
BuildRequires: openssl-devel
Requires: openssl

%description
Flightctl is a command line interface for managing edge device fleets.


%package agent
Summary: Flightctl Agent
%description agent
Flightctl Agent is a component of the flightctl tool.

%prep
%setup -q -n flightctl-0.0.1

%define source_git_tag $(git describe --tags --exclude latest)
%define source_git_commit $(git rev-parse --short "HEAD^{commit}" 2>/dev/null)

%build

# if this is a buggy version of go we need to set GOPROXY as workaround
# see https://github.com/golang/go/issues/61928
GOENVFILE=$(go env GOROOT)/go.env
if [[ ! -f "{$GOENVFILE}" ]]; then
    export GOPROXY='https://proxy.golang.org,direct'
fi
SOURCE_GIT_TAG=%{source_git_tag} SOURCE_GIT_COMMIT=%{source_git_commit} make build

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
install -Dpm 0644 flightctl-completion.bash -t %{buildroot}/%{_datadir}/bash-completion/completions/flightctl-completion.bash
bin/flightctl completion fish > flightctl-completion.fish
install -Dpm 0644 flightctl-completion.fish -t %{buildroot}/%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
bin/flightctl completion zsh > _flightctl-completion
install -Dpm 0644 _flightctl-completion -t %{buildroot}/%{_datadir}/zsh/site-functions/_flightctl-completion

%files
/usr/bin/flightctl
%{_datadir}/bash-completion/completions/flightctl-completion.bash
%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
%{_datadir}/zsh/site-functions/_flightctl-completion

%files agent
/usr/bin/flightctl-agent
/usr/bin/flightctl-must-gather
/usr/lib/flightctl/hooks.d/afterupdating/00-default.yaml
/usr/lib/systemd/system/flightctl-agent.service
%{_sharedstatedir}/flightctl
/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh

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
