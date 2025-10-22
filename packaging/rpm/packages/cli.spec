# cli sub-package
%package cli
Summary: Flight Control CLI

%description cli
flightctl is the CLI for controlling the Flight Control service.

%build
%{?rhel:%(if [ "%{rhel}" = "9" ]; then echo "%make_build build-cli build-restore"; else echo "DISABLE_FIPS=\"true\" %make_build build-cli build-restore"; fi)}%{!?rhel:DISABLE_FIPS="true" %make_build build-cli build-restore}

%install
install -D -m 0755 bin/flightctl %{buildroot}%{_bindir}/flightctl
install -D -m 0755 bin/flightctl-restore %{buildroot}%{_bindir}/flightctl-restore
# Generate shell completions
mkdir -p %{buildroot}%{_datadir}/bash-completion/completions
mkdir -p %{buildroot}%{_datadir}/fish/vendor_completions.d
mkdir -p %{buildroot}%{_datadir}/zsh/site-functions
%{buildroot}%{_bindir}/flightctl completion bash > %{buildroot}%{_datadir}/bash-completion/completions/flightctl-completion.bash
%{buildroot}%{_bindir}/flightctl completion fish > %{buildroot}%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
%{buildroot}%{_bindir}/flightctl completion zsh > %{buildroot}%{_datadir}/zsh/site-functions/_flightctl-completion

%files cli
    %{_bindir}/flightctl
    %{_bindir}/flightctl-restore
    %license LICENSE
    %{_datadir}/bash-completion/completions/flightctl-completion.bash
    %{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
    %{_datadir}/zsh/site-functions/_flightctl-completion
