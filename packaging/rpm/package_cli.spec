# cli sub-package
%package cli
Summary: Flight Control CLI

%description cli
flightctl is the CLI for controlling the Flight Control service.

# CLI build commands
%global cli_build_commands %{?rhel:%(if [ "%{rhel}" = "9" ]; then echo "%make_build build-cli build-restore"; else echo "DISABLE_FIPS=\"true\" %make_build build-cli build-restore"; fi)}%{!?rhel:DISABLE_FIPS="true" %make_build build-cli build-restore}

# CLI install commands
%global cli_install_commands \
install -D -m 0755 bin/flightctl %{buildroot}%{_bindir}/flightctl; \
install -D -m 0755 bin/flightctl-restore %{buildroot}%{_bindir}/flightctl-restore; \
install -D -m 0644 ./packaging/bash-completion/flightctl-completion.bash %{buildroot}%{_datadir}/bash-completion/completions/flightctl-completion.bash; \
install -D -m 0644 ./packaging/fish-completion/flightctl-completion.fish %{buildroot}%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish; \
install -D -m 0644 ./packaging/zsh-completion/_flightctl-completion %{buildroot}%{_datadir}/zsh/site-functions/_flightctl-completion

%files cli -f licenses.list
    %{_bindir}/flightctl
    %{_bindir}/flightctl-restore
    %license LICENSE
    %{_datadir}/bash-completion/completions/flightctl-completion.bash
    %{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
    %{_datadir}/zsh/site-functions/_flightctl-completion
