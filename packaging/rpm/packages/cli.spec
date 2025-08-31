# cli sub-package
%package cli
Summary: Flight Control CLI

%description cli
flightctl is the CLI for controlling the Flight Control service.

%files cli -f licenses.list
  %{_bindir}/flightctl
  %license LICENSE
  %{_datadir}/bash-completion/completions/flightctl-completion.bash
  %{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
  %{_datadir}/zsh/site-functions/_flightctl-completion
